// Package prober measures what users experience: it probes an HTTP endpoint
// on a fixed interval and turns the pass/fail series into a recovery time.
//
// The analysis functions are pure and use only the standard library.
package prober

import "time"

// Result is one probe outcome.
type Result struct {
	At time.Time
	OK bool
}

// Series is a chronological list of probe results.
type Series []Result

func trailingPasses(s Series) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i].OK; i-- {
		n++
	}
	return n
}

// Healthy reports whether the latest threshold probes all passed.
func Healthy(s Series, threshold int) bool {
	if threshold < 1 {
		threshold = 1
	}
	return trailingPasses(s) >= threshold
}

// Analysis summarises the probe series since a fault was injected.
type Analysis struct {
	// Stable is true once the series ends in an unbroken run of at least
	// threshold passes.
	Stable bool
	// StableAt is the time of the threshold-th pass of that final run: the
	// moment users could be considered served again.
	StableAt time.Time
	// Impacted is true if any probe failed in the window.
	Impacted bool
	// Successes and Failures count probes from injection up to StableAt
	// (or all probes so far when not yet stable).
	Successes int
	Failures  int
}

// Availability returns the share of successful probes in the window, or 1 when
// no probes ran.
func (a Analysis) Availability() float64 {
	total := a.Successes + a.Failures
	if total == 0 {
		return 1
	}
	return float64(a.Successes) / float64(total)
}

// Analyze looks at probes taken at or after since. A flapping service does not
// count as recovered: only the final unbroken run of passes does.
func Analyze(s Series, since time.Time, threshold int) Analysis {
	if threshold < 1 {
		threshold = 1
	}
	var window Series
	for _, r := range s {
		if !r.At.Before(since) {
			window = append(window, r)
		}
	}

	var a Analysis
	counted := window
	if streak := trailingPasses(window); streak >= threshold {
		idx := len(window) - streak + threshold - 1
		a.Stable = true
		a.StableAt = window[idx].At
		counted = window[:idx+1]
	}
	for _, r := range counted {
		if r.OK {
			a.Successes++
		} else {
			a.Failures++
		}
	}
	a.Impacted = a.Failures > 0
	return a
}
