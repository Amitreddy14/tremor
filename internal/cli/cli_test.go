package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Amitreddy14/tremor/internal/runstore"
)

var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func i64(v int64) *int64 { return &v }

func fixture() *runstore.MemoryStore {
	m := &MemoryStoreAlias{}
	ctx := context.Background()
	inj := now.Add(-2 * time.Hour)
	_ = m.Save(ctx, runstore.Run{
		ID: "aaaa1111-0000", Namespace: "demo", Name: "kill-shop", FaultType: "PodKill", Result: "completed",
		Selector: map[string]string{"app": "shop", "tier": "web"}, SteadyStateURL: "http://shop/healthz",
		StartedAt: now.Add(-2 * time.Hour), InjectedAt: &inj,
		PodRecoveryMillis: i64(4200), UserRecoveryMillis: i64(3000), UserImpacted: true,
		ProbeSuccesses: 8, ProbeFailures: 2, AffectedPods: []string{"shop-1"}, BaselineReadyPods: 3,
	})
	_ = m.Save(ctx, runstore.Run{
		ID: "aaaa2222-0000", Namespace: "demo", Name: "kill-shop", FaultType: "PodKill", Result: "failed",
		Message: "boom", StartedAt: now.Add(-3 * time.Hour),
	})
	_ = m.Save(ctx, runstore.Run{
		ID: "bbbb3333-0000", Namespace: "demo", Name: "old", FaultType: "PodKill", Result: "completed",
		StartedAt: now.Add(-30 * 24 * time.Hour), PodRecoveryMillis: i64(9000),
	})
	return m.MemoryStore
}

// MemoryStoreAlias keeps the fixture helper readable.
type MemoryStoreAlias struct{ *runstore.MemoryStore }

func (m *MemoryStoreAlias) Save(ctx context.Context, r runstore.Run) error {
	if m.MemoryStore == nil {
		m.MemoryStore = &runstore.MemoryStore{}
	}
	return m.MemoryStore.Save(ctx, r)
}

func run(t *testing.T, store runstore.Store, env map[string]string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	closed := false
	d := Deps{
		Open: func(ctx context.Context, uri string) (runstore.Store, func(), error) {
			if strings.Contains(uri, "bad") {
				return nil, nil, errors.New("connection refused")
			}
			return store, func() { closed = true }, nil
		},
		Now:    func() time.Time { return now },
		Getenv: func(k string) string { return env[k] },
		Stdout: &so, Stderr: &se,
	}
	code = Run(args, d)
	if store != nil && code == 0 && len(args) > 0 && args[0] != "help" && !closed && !strings.Contains(strings.Join(args, " "), "-h") {
		t.Errorf("store was not closed")
	}
	return code, so.String(), se.String()
}

var uri = []string{"--mongo-uri", "mongodb://x"}

func with(cmd string, rest ...string) []string {
	return append(append([]string{cmd}, uri...), rest...)
}

func TestNoArgsAndHelp(t *testing.T) {
	if code, out, _ := run(t, nil, nil); code != 2 || !strings.Contains(out, "Usage") {
		t.Errorf("no args: code=%d out=%q", code, out)
	}
	if code, out, _ := run(t, nil, nil, "help"); code != 0 || !strings.Contains(out, "history") {
		t.Errorf("help: code=%d", code)
	}
	if code, _, errOut := run(t, nil, nil, "frobnicate"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown: code=%d err=%q", code, errOut)
	}
}

func TestMissingURI(t *testing.T) {
	code, _, errOut := run(t, fixture(), nil, "history")
	if code != 1 || !strings.Contains(errOut, "TREMOR_MONGO_URI") {
		t.Errorf("code=%d err=%q, want a URI hint", code, errOut)
	}
}

func TestURIFromEnv(t *testing.T) {
	code, out, errOut := run(t, fixture(), map[string]string{EnvMongoURI: "mongodb://env"}, "history")
	if code != 0 || !strings.Contains(out, "kill-shop") {
		t.Errorf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestConnectError(t *testing.T) {
	code, _, errOut := run(t, nil, nil, "history", "--mongo-uri", "mongodb://bad")
	if code != 1 || !strings.Contains(errOut, "connection refused") {
		t.Errorf("code=%d err=%q", code, errOut)
	}
}

func TestHistoryTable(t *testing.T) {
	code, out, errOut := run(t, fixture(), nil, with("history")...)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	for _, want := range []string{"ID", "aaaa1111", "demo/kill-shop", "PodKill", "completed", "4.2s", "3.0s", "yes", "failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Newest first: aaaa1111 (2h ago) before aaaa2222 (3h ago).
	if strings.Index(out, "aaaa1111") > strings.Index(out, "aaaa2222") {
		t.Errorf("runs not newest first:\n%s", out)
	}
}

func TestHistoryFilters(t *testing.T) {
	_, out, _ := run(t, fixture(), nil, with("history", "--result", "failed")...)
	if strings.Contains(out, "aaaa1111") || !strings.Contains(out, "aaaa2222") {
		t.Errorf("--result filter wrong:\n%s", out)
	}
	_, out, _ = run(t, fixture(), nil, with("history", "--since", "7d")...)
	if strings.Contains(out, "bbbb3333") {
		t.Errorf("--since 7d should drop the 30-day-old run:\n%s", out)
	}
	_, out, _ = run(t, fixture(), nil, with("history", "--limit", "1")...)
	if strings.Contains(out, "aaaa2222") {
		t.Errorf("--limit 1 should keep only the newest:\n%s", out)
	}
	_, out, _ = run(t, fixture(), nil, with("history", "--namespace", "nope")...)
	if !strings.Contains(out, "no runs found") {
		t.Errorf("empty result message missing: %q", out)
	}
}

func TestHistoryJSON(t *testing.T) {
	_, out, _ := run(t, fixture(), nil, with("history", "--output", "json", "--limit", "1")...)
	var runs []runstore.Run
	if err := json.Unmarshal([]byte(out), &runs); err != nil || len(runs) != 1 || runs[0].ID != "aaaa1111-0000" {
		t.Errorf("json output wrong: %v %s", err, out)
	}
	_, out, _ = run(t, fixture(), nil, with("history", "--namespace", "nope", "--output", "json")...)
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty json should be [], got %q", out)
	}
}

func TestHistoryBadFlags(t *testing.T) {
	for _, args := range [][]string{
		with("history", "--output", "xml"),
		with("history", "--limit", "-1"),
		with("history", "--since", "banana"),
		with("history", "--no-such-flag"),
	} {
		if code, _, _ := run(t, fixture(), nil, args...); code != 1 {
			t.Errorf("args %v: code=%d, want 1", args, code)
		}
	}
}

func TestShow(t *testing.T) {
	code, out, errOut := run(t, fixture(), nil, with("show", "aaaa1111-0000")...)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	for _, want := range []string{"demo/kill-shop", "app=shop,tier=web", "4.2s", "3.0s", "8 passed, 2 failed", "80.0%", "shop-1", "http://shop/healthz"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestShowByPrefixAndFlagOrder(t *testing.T) {
	if code, out, _ := run(t, fixture(), nil, "show", "aaaa1", "--mongo-uri", "mongodb://x"); code != 0 || !strings.Contains(out, "aaaa1111-0000") {
		t.Errorf("prefix lookup failed: code=%d out=%q", code, out)
	}
	if code, out, _ := run(t, fixture(), nil, with("show", "aaaa1111-0000", "--output", "json")...); code != 0 || !strings.Contains(out, `"id": "aaaa1111-0000"`) {
		t.Errorf("json show failed: %q", out)
	}
}

func TestShowErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{with("show"), "needs a run ID"},
		{with("show", "zzzz"), "not found"},
		{with("show", "aaaa"), "ambiguous"},
		{with("show", "aaaa1111-0000", "--output", "xml"), "invalid --output"},
	}
	for _, c := range cases {
		code, _, errOut := run(t, fixture(), nil, c.args...)
		if code != 1 || !strings.Contains(errOut, c.want) {
			t.Errorf("args %v: code=%d err=%q, want %q", c.args, code, errOut, c.want)
		}
	}
}

func TestShowSustainedFault(t *testing.T) {
	m := &runstore.MemoryStore{}
	inj := now.Add(-10 * time.Minute)
	rel := inj.Add(45 * time.Second)
	_ = m.Save(context.Background(), runstore.Run{
		ID: "cccc4444-0000", Namespace: "demo", Name: "mongo-outage", FaultType: "DependencyOutage", Result: "completed",
		SteadyStateURL: "http://shop/orders", StartedAt: inj, InjectedAt: &inj, RevertedAt: &rel,
		PodRecoveryMillis: i64(6000), UserRecoveryMillis: i64(8000), UserImpacted: true, ProbeSuccesses: 20, ProbeFailures: 45,
	})
	_, out, _ := run(t, m, nil, with("show", "cccc4444")...)
	for _, want := range []string{"Released", "Fault active for", "45.0s", "6.0s (from release)", "8.0s (from release)", "30.8%"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// One-shot runs say nothing about release.
	_, out, _ = run(t, fixture(), nil, with("show", "aaaa1111-0000")...)
	if strings.Contains(out, "from release") || strings.Contains(out, "Released") {
		t.Errorf("PodKill run should not mention release:\n%s", out)
	}
}

func TestShowNoProbe(t *testing.T) {
	_, out, _ := run(t, fixture(), nil, with("show", "aaaa2222-0000")...)
	if !strings.Contains(out, "no steady-state probe") || !strings.Contains(out, "boom") {
		t.Errorf("no-probe run rendered wrong:\n%s", out)
	}
}

func TestReport(t *testing.T) {
	code, out, errOut := run(t, fixture(), nil, with("report")...)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	// Default window is 7d, so the 30-day-old run is excluded: 2 runs.
	if !strings.Contains(out, "2 run(s)") || !strings.Contains(out, "PodKill") {
		t.Errorf("report text wrong:\n%s", out)
	}
	_, out, _ = run(t, fixture(), nil, with("report", "--since", "all", "--format", "markdown")...)
	if !strings.Contains(out, "3 run(s)") || !strings.Contains(out, "| PodKill |") {
		t.Errorf("markdown report wrong:\n%s", out)
	}
	_, out, _ = run(t, fixture(), nil, with("report", "--format", "json")...)
	var s struct{ Total int }
	if err := json.Unmarshal([]byte(out), &s); err != nil || s.Total != 2 {
		t.Errorf("json report wrong: %v %s", err, out)
	}
	if code, _, errOut := run(t, fixture(), nil, with("report", "--format", "pdf")...); code != 1 || !strings.Contains(errOut, "invalid --format") {
		t.Errorf("bad format: code=%d err=%q", code, errOut)
	}
}

func TestSubcommandHelp(t *testing.T) {
	if code, _, errOut := run(t, fixture(), nil, "history", "-h"); code != 0 || !strings.Contains(errOut, "-mongo-uri") {
		t.Errorf("history -h: code=%d err=%q", code, errOut)
	}
}
