package prober

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func series(pattern string) Series {
	var s Series
	for i, c := range pattern {
		s = append(s, Result{At: t0.Add(time.Duration(i) * time.Second), OK: c == 'P'})
	}
	return s
}

func TestHealthy(t *testing.T) {
	tests := []struct {
		pattern   string
		threshold int
		want      bool
	}{
		{"PPP", 3, true},
		{"FPP", 3, false},
		{"PPFPP", 3, false},
		{"FPPP", 3, true},
		{"", 3, false},
		{"P", 0, true},
	}
	for _, tt := range tests {
		if got := Healthy(series(tt.pattern), tt.threshold); got != tt.want {
			t.Errorf("Healthy(%q,%d) = %v, want %v", tt.pattern, tt.threshold, got, tt.want)
		}
	}
}

func TestAnalyze(t *testing.T) {
	// Injection happens at index 2 (t0+2s). Probes before it are ignored.
	since := t0.Add(2 * time.Second)

	tests := []struct {
		name         string
		pattern      string
		wantStable   bool
		wantImpacted bool
		wantStableAt time.Duration // offset from t0
		wantSucc     int
		wantFail     int
	}{
		{"no impact", "PPPPPPP", true, false, 4 * time.Second, 3, 0},
		{"clean recovery", "PPFFFPPPP", true, true, 7 * time.Second, 3, 3},
		{"flapping resets", "PPFPPFPPPP", true, true, 8 * time.Second, 5, 2},
		{"not stable yet", "PPFFPP", false, true, 0, 2, 2},
		{"still failing", "PPFFF", false, true, 0, 0, 3},
		{"no probes yet", "PP", false, false, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Analyze(series(tt.pattern), since, 3)
			if a.Stable != tt.wantStable || a.Impacted != tt.wantImpacted {
				t.Fatalf("stable=%v impacted=%v, want %v %v", a.Stable, a.Impacted, tt.wantStable, tt.wantImpacted)
			}
			if tt.wantStable && !a.StableAt.Equal(t0.Add(tt.wantStableAt)) {
				t.Errorf("StableAt = %v, want t0+%v", a.StableAt.Sub(t0), tt.wantStableAt)
			}
			if a.Successes != tt.wantSucc || a.Failures != tt.wantFail {
				t.Errorf("counts = %d/%d, want %d/%d", a.Successes, a.Failures, tt.wantSucc, tt.wantFail)
			}
		})
	}
}

func TestAvailability(t *testing.T) {
	if got := (Analysis{Successes: 3, Failures: 1}).Availability(); got != 0.75 {
		t.Errorf("Availability = %v, want 0.75", got)
	}
	if got := (Analysis{}).Availability(); got != 1 {
		t.Errorf("empty Availability = %v, want 1", got)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestManagerRecordsPassAndFail(t *testing.T) {
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewManager()
	defer m.Stop("k")
	m.Ensure("k", Config{URL: srv.URL, Interval: 20 * time.Millisecond})
	m.Ensure("k", Config{URL: "http://ignored.invalid", Interval: time.Hour}) // first config wins

	waitFor(t, "passing probes", func() bool { return Healthy(m.Snapshot("k"), 3) })

	failing.Store(true)
	waitFor(t, "a failing probe", func() bool { return !Healthy(m.Snapshot("k"), 1) })

	failing.Store(false)
	waitFor(t, "recovery", func() bool { return Healthy(m.Snapshot("k"), 3) })

	a := Analyze(m.Snapshot("k"), time.Time{}, 3)
	if !a.Impacted || !a.Stable {
		t.Fatalf("expected impacted and stable, got %+v", a)
	}
}

func TestManagerStopEndsProbing(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	m := NewManager()
	m.Ensure("k", Config{URL: srv.URL, Interval: 10 * time.Millisecond})
	waitFor(t, "first probes", func() bool { return hits.Load() >= 3 })

	m.Stop("k")
	if m.Snapshot("k") != nil {
		t.Fatal("snapshot should be empty after Stop")
	}
	time.Sleep(50 * time.Millisecond) // let any in-flight probe drain
	after := hits.Load()
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != after {
		t.Fatalf("probing continued after Stop: %d -> %d", after, hits.Load())
	}
}

func TestHTTPProbeStatusMatching(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx := context.Background()
	if HTTPProbe(ctx, Config{URL: srv.URL, ExpectStatus: 200}) {
		t.Error("204 must not pass when 200 is expected")
	}
	if !HTTPProbe(ctx, Config{URL: srv.URL, ExpectStatus: 204}) {
		t.Error("204 must pass when 204 is expected")
	}
	if HTTPProbe(ctx, Config{URL: "http://127.0.0.1:1", ExpectStatus: 200}) {
		t.Error("unreachable endpoint must fail")
	}
}
