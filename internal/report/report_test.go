package report

import (
	"strings"
	"testing"
	"time"

	"github.com/Amitreddy14/tremor/internal/runstore"
)

func i64(v int64) *int64 { return &v }

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func completed(fault string, podMs, userMs int64, impacted bool, ok, failed int, offset time.Duration) runstore.Run {
	return runstore.Run{
		FaultType: fault, Result: "completed", StartedAt: t0.Add(offset),
		PodRecoveryMillis: i64(podMs), UserRecoveryMillis: i64(userMs), UserImpacted: impacted,
		ProbeSuccesses: ok, ProbeFailures: failed,
	}
}

func TestSummarize(t *testing.T) {
	runs := []runstore.Run{
		completed("PodKill", 4000, 0, false, 10, 0, 0),
		completed("PodKill", 6000, 3000, true, 8, 2, time.Hour),
		completed("PodKill", 10000, 7000, true, 5, 5, 2*time.Hour),
		{FaultType: "PodKill", Result: "failed", StartedAt: t0.Add(3 * time.Hour)},
		{FaultType: "PodKill", Result: "dry_run", StartedAt: t0.Add(4 * time.Hour)},
		completed("DependencyOutage", 30000, 45000, true, 2, 8, 5*time.Hour),
	}
	s := Summarize(runs)

	if s.Total != 6 || !s.From.Equal(t0) || !s.To.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("summary header wrong: %+v", s)
	}
	if len(s.Groups) != 2 || s.Groups[0].FaultType != "DependencyOutage" || s.Groups[1].FaultType != "PodKill" {
		t.Fatalf("groups = %+v, want sorted DependencyOutage then PodKill", s.Groups)
	}

	pk := s.Groups[1]
	if pk.Runs != 5 || pk.Completed != 3 || pk.Failed != 1 || pk.DryRun != 1 || pk.ImpactedRuns != 2 {
		t.Errorf("PodKill counts wrong: %+v", pk)
	}
	if pk.Pod.N != 3 || pk.Pod.Min != 4000 || pk.Pod.P50 != 6000 || pk.Pod.P95 != 10000 || pk.Pod.Max != 10000 {
		t.Errorf("PodKill pod percentiles wrong: %+v", pk.Pod)
	}
	if pk.User.P50 != 3000 || pk.User.Max != 7000 {
		t.Errorf("PodKill user percentiles wrong: %+v", pk.User)
	}
	// Availability: (1.0 + 0.8 + 0.5) / 3
	if !pk.HasAvailability || pk.AvgAvailability < 0.766 || pk.AvgAvailability > 0.767 {
		t.Errorf("availability = %v (%v), want about 0.7667", pk.AvgAvailability, pk.HasAvailability)
	}
}

func TestNoProbeMeansNoUserOrAvailability(t *testing.T) {
	s := Summarize([]runstore.Run{{
		FaultType: "PodKill", Result: "completed", StartedAt: t0, PodRecoveryMillis: i64(2000),
	}})
	g := s.Groups[0]
	if g.User.N != 0 || g.HasAvailability {
		t.Errorf("expected no user stats or availability, got %+v", g)
	}
	if !strings.Contains(s.Text(), "-") {
		t.Errorf("text should show dashes for missing values:\n%s", s.Text())
	}
}

func TestPercentilesNearestRank(t *testing.T) {
	var v []int64
	for i := int64(1); i <= 20; i++ {
		v = append(v, i*100)
	}
	p := percentiles(v)
	if p.P50 != 1000 || p.P95 != 1900 || p.Min != 100 || p.Max != 2000 {
		t.Errorf("percentiles = %+v", p)
	}
	if (percentiles(nil) != Percentiles{}) {
		t.Error("empty input should give zero Percentiles")
	}
	if p := percentiles([]int64{500}); p.P50 != 500 || p.P95 != 500 {
		t.Errorf("single value percentiles = %+v", p)
	}
}

func TestFormatMillis(t *testing.T) {
	tests := map[int64]string{0: "0ms", 850: "850ms", 999: "999ms", 1000: "1.0s", 4200: "4.2s", 59900: "59.9s", 60000: "1m00s", 125000: "2m05s"}
	for ms, want := range tests {
		if got := FormatMillis(ms); got != want {
			t.Errorf("FormatMillis(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestRenderers(t *testing.T) {
	s := Summarize([]runstore.Run{completed("PodKill", 4200, 3000, true, 8, 2, 0)})

	text := s.Text()
	for _, want := range []string{"1 run(s)", "FAULT", "PodKill", "4.2s", "3.0s", "1/1", "80.0%"} {
		if !strings.Contains(text, want) {
			t.Errorf("Text() missing %q:\n%s", want, text)
		}
	}
	md := s.Markdown()
	for _, want := range []string{"| Fault |", "| PodKill |", "80.0%"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown() missing %q:\n%s", want, md)
		}
	}
	if got := Summarize(nil).Text(); !strings.HasPrefix(got, "0 run(s)") {
		t.Errorf("empty summary text = %q", got)
	}
}

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"7d", now.Add(-7 * 24 * time.Hour), false},
		{"24h", now.Add(-24 * time.Hour), false},
		{"90m", now.Add(-90 * time.Minute), false},
		{"all", time.Time{}, false},
		{"", time.Time{}, false},
		{"0d", time.Time{}, true},
		{"abc", time.Time{}, true},
		{"-5h", time.Time{}, true},
	}
	for _, tt := range tests {
		got, err := ParseWindow(tt.in, now)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseWindow(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && !got.Equal(tt.want) {
			t.Errorf("ParseWindow(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
