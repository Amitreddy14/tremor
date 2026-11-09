// Package report turns run history into the numbers people quote: how long
// recovery takes (median and p95), how often users noticed, and availability.
package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Amitreddy14/tremor/internal/runstore"
)

// Percentiles summarises recovery times in milliseconds (nearest-rank).
type Percentiles struct {
	N   int   `json:"n"`
	Min int64 `json:"minMillis"`
	P50 int64 `json:"p50Millis"`
	P95 int64 `json:"p95Millis"`
	Max int64 `json:"maxMillis"`
}

// Group aggregates the runs of one fault type.
type Group struct {
	FaultType string `json:"faultType"`
	Runs      int    `json:"runs"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	Aborted   int    `json:"aborted"`
	DryRun    int    `json:"dryRun"`
	// ImpactedRuns counts completed runs where users saw a failure.
	ImpactedRuns int `json:"impactedRuns"`
	// Pod and User only include completed runs.
	Pod  Percentiles `json:"podRecovery"`
	User Percentiles `json:"userRecovery"`
	// AvgAvailability is the mean availability over completed runs that had a
	// steady-state probe; HasAvailability is false when there were none.
	AvgAvailability float64 `json:"avgAvailability"`
	HasAvailability bool    `json:"hasAvailability"`
}

// Summary is a full report.
type Summary struct {
	Total  int       `json:"total"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Groups []Group   `json:"groups"`
}

// Summarize aggregates runs by fault type.
func Summarize(runs []runstore.Run) Summary {
	s := Summary{Total: len(runs)}
	type acc struct {
		g          Group
		pod, user  []int64
		availSum   float64
		availCount int
	}
	byType := map[string]*acc{}

	for _, r := range runs {
		if s.From.IsZero() || r.StartedAt.Before(s.From) {
			s.From = r.StartedAt
		}
		if r.StartedAt.After(s.To) {
			s.To = r.StartedAt
		}
		a := byType[r.FaultType]
		if a == nil {
			a = &acc{g: Group{FaultType: r.FaultType}}
			byType[r.FaultType] = a
		}
		a.g.Runs++
		switch r.Result {
		case "completed":
			a.g.Completed++
			if r.PodRecoveryMillis != nil {
				a.pod = append(a.pod, *r.PodRecoveryMillis)
			}
			if r.UserRecoveryMillis != nil {
				a.user = append(a.user, *r.UserRecoveryMillis)
			}
			if r.UserImpacted {
				a.g.ImpactedRuns++
			}
			if av, ok := r.Availability(); ok {
				a.availSum += av
				a.availCount++
			}
		case "failed":
			a.g.Failed++
		case "aborted":
			a.g.Aborted++
		case "dry_run":
			a.g.DryRun++
		}
	}

	for _, a := range byType {
		a.g.Pod = percentiles(a.pod)
		a.g.User = percentiles(a.user)
		if a.availCount > 0 {
			a.g.AvgAvailability = a.availSum / float64(a.availCount)
			a.g.HasAvailability = true
		}
		s.Groups = append(s.Groups, a.g)
	}
	sort.Slice(s.Groups, func(i, j int) bool { return s.Groups[i].FaultType < s.Groups[j].FaultType })
	return s
}

func percentiles(v []int64) Percentiles {
	if len(v) == 0 {
		return Percentiles{}
	}
	sorted := append([]int64(nil), v...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return Percentiles{
		N:   len(sorted),
		Min: sorted[0],
		P50: nearestRank(sorted, 50),
		P95: nearestRank(sorted, 95),
		Max: sorted[len(sorted)-1],
	}
}

// nearestRank returns the p-th percentile of an ascending slice.
func nearestRank(sorted []int64, p float64) int64 {
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// FormatMillis renders milliseconds for people: "850ms", "4.2s", "2m05s".
func FormatMillis(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		d := time.Duration(ms) * time.Millisecond
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func recovery(p Percentiles) (p50, p95 string) {
	if p.N == 0 {
		return "-", "-"
	}
	return FormatMillis(p.P50), FormatMillis(p.P95)
}

func availability(g Group) string {
	if !g.HasAvailability {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", g.AvgAvailability*100)
}

func impact(g Group) string {
	if g.Completed == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", g.ImpactedRuns, g.Completed)
}

// Text renders the summary as an aligned plain-text table.
func (s Summary) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d run(s)%s\n\n", s.Total, s.window())
	if len(s.Groups) == 0 {
		return b.String()
	}
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "FAULT\tRUNS\tOK\tFAILED\tPOD p50\tPOD p95\tUSER p50\tUSER p95\tIMPACTED\tAVAILABILITY")
	for _, g := range s.Groups {
		pp50, pp95 := recovery(g.Pod)
		up50, up95 := recovery(g.User)
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			g.FaultType, g.Runs, g.Completed, g.Failed, pp50, pp95, up50, up95, impact(g), availability(g))
	}
	_ = w.Flush()
	return b.String()
}

// Markdown renders the summary as a Markdown table, ready for a README.
func (s Summary) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d run(s)%s\n\n", s.Total, s.window())
	if len(s.Groups) == 0 {
		return b.String()
	}
	b.WriteString("| Fault | Runs | Completed | Failed | Pod p50 | Pod p95 | User p50 | User p95 | Users impacted | Availability |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, g := range s.Groups {
		pp50, pp95 := recovery(g.Pod)
		up50, up95 := recovery(g.User)
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s | %s | %s | %s | %s | %s |\n",
			g.FaultType, g.Runs, g.Completed, g.Failed, pp50, pp95, up50, up95, impact(g), availability(g))
	}
	return b.String()
}

func (s Summary) window() string {
	if s.Total == 0 {
		return ""
	}
	return fmt.Sprintf(", %s to %s", s.From.UTC().Format("2006-01-02 15:04"), s.To.UTC().Format("2006-01-02 15:04"))
}

// ParseWindow turns "7d", "24h", "90m" or "all" into a start time relative to
// now. "all" (or empty) returns the zero time, meaning no lower bound.
func ParseWindow(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &days); err != nil || days < 1 {
			return time.Time{}, fmt.Errorf("invalid window %q: use forms like 7d, 24h, 90m or all", s)
		}
		return now.Add(-time.Duration(days) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Time{}, fmt.Errorf("invalid window %q: use forms like 7d, 24h, 90m or all", s)
	}
	return now.Add(-d), nil
}
