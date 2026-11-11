package engine

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"strings"
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

// NodeLoad is how many of a workload's pods run on one node.
type NodeLoad struct {
	Node string
	Pods int
}

// PickDrainNode chooses the node to drain: among the nodes whose pods may all
// be evicted within the safety limits (the same limits KillCount applies to a
// single pod), the one hosting the most pods, then the lowest name. total is
// the workload's pod count. Nodes with no pods, or no known name (pods not yet
// scheduled), are never chosen.
func PickDrainNode(loads []NodeLoad, total int, p SafetyPolicy) (string, error) {
	best := NodeLoad{}
	for _, l := range loads {
		if l.Node == "" || l.Pods < 1 {
			continue
		}
		n, err := KillCount(total, l.Pods, p)
		if err != nil || n < l.Pods {
			continue // draining this node would exceed the limits
		}
		if best.Node == "" || l.Pods > best.Pods || (l.Pods == best.Pods && l.Node < best.Node) {
			best = l
		}
	}
	if best.Node == "" {
		return "", fmt.Errorf("%w: no node can be drained without exceeding the limits (%d pod(s), max %d%%, sparing one pod unless allowTotalOutage)",
			ErrNoSafeTarget, total, effectivePercent(p))
	}
	return best.Node, nil
}

func effectivePercent(p SafetyPolicy) int {
	switch {
	case p.MaxAffectedPercent < 1:
		return defaultMaxAffectedPercent
	case p.MaxAffectedPercent > 100:
		return 100
	}
	return p.MaxAffectedPercent
}

// NamespaceAllowed reports whether experiments may target ns. An empty
// allowlist permits every namespace; entries are exact names.
func NamespaceAllowed(ns string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == ns {
			return true
		}
	}
	return false
}

// SelectorsOverlap reports whether two label selectors could match the same
// pod: they can unless some shared key demands different values. Selectors in
// different namespaces never overlap, which the caller checks.
func SelectorsOverlap(a, b map[string]string) bool {
	for k, v := range a {
		if w, ok := b[k]; ok && w != v {
			return false
		}
	}
	return true
}

// ValidateProbeURL decides whether the operator may send steady-state probes
// to rawURL. Probes run from the operator's pod, so an unchecked URL would let
// anyone who can create an experiment make the operator call arbitrary hosts.
//
// By default only in-cluster service names are allowed (name.<ns>.svc or
// name.<ns>.svc.cluster.local) whose namespace is the experiment's own
// namespace. allowedHosts widens this: each entry is an exact host name or a
// "*.suffix" wildcard. IP literals are only accepted when listed exactly.
func ValidateProbeURL(rawURL, namespace string, allowedHosts []string) error {
	scheme, rest, ok := strings.Cut(rawURL, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		return fmt.Errorf("probe url %q must start with http:// or https://", rawURL)
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if strings.Contains(rest, "@") {
		return errors.New("probe url must not contain credentials")
	}
	host := rest
	if h, _, err := net.SplitHostPort(rest); err == nil {
		host = h
	} else if strings.HasPrefix(rest, "[") {
		host = strings.Trim(rest, "[]")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return errors.New("probe url has no host")
	}

	for _, a := range allowedHosts {
		a = strings.ToLower(a)
		if a == host || (strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:])) {
			return nil
		}
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("probe host %s is an IP address; list it in --allowed-probe-hosts to allow it", host)
	}
	parts := strings.Split(host, ".")
	// name.<ns>.svc  or  name.<ns>.svc.cluster.local
	if len(parts) >= 3 && parts[2] == "svc" && (len(parts) == 3 || strings.Join(parts[3:], ".") == "cluster.local") {
		if parts[1] == namespace {
			return nil
		}
		return fmt.Errorf("probe host %s is in namespace %q, not the experiment's namespace %q", host, parts[1], namespace)
	}
	return fmt.Errorf("probe host %s is not an in-cluster service of namespace %q; list it in --allowed-probe-hosts to allow it", host, namespace)
}
