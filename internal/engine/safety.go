package engine

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
)

// ErrNoSafeTarget means the safety rules leave nothing that may be disturbed.
var ErrNoSafeTarget = errors.New("no pod can be disturbed within the safety limits")

const defaultMaxAffectedPercent = 50

// SafetyPolicy limits how much of a workload one experiment may touch.
type SafetyPolicy struct {
	// MaxAffectedPercent caps the share of pods affected (1-100, default 50).
	MaxAffectedPercent int
	// AllowTotalOutage lifts the "always spare one pod" rule.
	AllowTotalOutage bool
}

// KillCount returns how many of total pods may be disturbed when the user asked
// for requested (values below 1 mean 1).
func KillCount(total, requested int, p SafetyPolicy) (int, error) {
	if total < 1 {
		return 0, fmt.Errorf("%w: workload has no pods", ErrNoSafeTarget)
	}
	if requested < 1 {
		requested = 1
	}
	pct := p.MaxAffectedPercent
	if pct < 1 {
		pct = defaultMaxAffectedPercent
	}
	if pct > 100 {
		pct = 100
	}

	limit := total * pct / 100
	if p.AllowTotalOutage {
		if limit < 1 {
			limit = 1
		}
	} else if limit > total-1 {
		limit = total - 1
	}

	n := requested
	if n > limit {
		n = limit
	}
	if n < 1 {
		return 0, fmt.Errorf("%w: %d pod(s), max %d%%, sparing one pod (set allowTotalOutage to override)", ErrNoSafeTarget, total, pct)
	}
	return n, nil
}

// Pick chooses n names at random (reproducible with a seeded r). The input
// slice is not modified.
func Pick(names []string, n int, r *rand.Rand) []string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	r.Shuffle(len(sorted), func(i, j int) { sorted[i], sorted[j] = sorted[j], sorted[i] })
	if n > len(sorted) {
		n = len(sorted)
	}
	if n < 0 {
		n = 0
	}
	return sorted[:n]
}

// ValidateSelector rejects empty selectors, which would match every pod.
func ValidateSelector(sel map[string]string) error {
	if len(sel) == 0 {
		return errors.New("target.selector must contain at least one label")
	}
	return nil
}
