package prober

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// Config describes one HTTP steady-state probe.
type Config struct {
	URL          string
	ExpectStatus int
	Interval     time.Duration
	Timeout      time.Duration
}

func (c Config) normalised() Config {
	if c.Interval <= 0 {
		c.Interval = time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = c.Interval
	}
	if c.ExpectStatus == 0 {
		c.ExpectStatus = http.StatusOK
	}
	return c
}

// ProbeFunc performs a single probe and reports whether it passed.
type ProbeFunc func(ctx context.Context, cfg Config) bool

var httpClient = &http.Client{
	// A fresh connection per probe so every probe exercises service routing,
	// as a new user request would.
	Transport: &http.Transport{DisableKeepAlives: true},
}

// HTTPProbe issues a GET and passes when the status matches ExpectStatus.
func HTTPProbe(ctx context.Context, cfg Config) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode == cfg.ExpectStatus
}

const maxResults = 3600 // about an hour at the default 1 s interval

// Manager runs one background prober per key (an experiment UID) and keeps its
// results in memory. If the operator restarts mid-run, history restarts too.
type Manager struct {
	mu    sync.Mutex
	runs  map[string]*run
	probe ProbeFunc
	now   func() time.Time
}

type run struct {
	cancel  context.CancelFunc
	mu      sync.Mutex
	results Series
}

// NewManager returns a Manager that probes over real HTTP.
func NewManager() *Manager {
	return &Manager{runs: map[string]*run{}, probe: HTTPProbe, now: time.Now}
}

// Ensure starts probing for key if it is not already running. The first Config
// wins; later calls with the same key are no-ops.
func (m *Manager) Ensure(key string, cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[key]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{cancel: cancel}
	m.runs[key] = r
	go m.loop(ctx, r, cfg.normalised())
}

func (m *Manager) loop(ctx context.Context, r *run, cfg Config) {
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	m.sample(ctx, r, cfg)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sample(ctx, r, cfg)
		}
	}
}

func (m *Manager) sample(ctx context.Context, r *run, cfg Config) {
	pctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	ok := m.probe(pctx, cfg)
	if ctx.Err() != nil {
		return // stopped while probing; do not record
	}
	res := Result{At: m.now(), OK: ok}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
	if len(r.results) > maxResults {
		r.results = append(Series(nil), r.results[len(r.results)-maxResults:]...)
	}
}

// Snapshot returns a copy of the results recorded so far for key.
func (m *Manager) Snapshot(key string) Series {
	m.mu.Lock()
	r := m.runs[key]
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(Series(nil), r.results...)
}

// Stop ends probing for key and discards its results.
func (m *Manager) Stop(key string) {
	m.mu.Lock()
	r := m.runs[key]
	delete(m.runs, key)
	m.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}
