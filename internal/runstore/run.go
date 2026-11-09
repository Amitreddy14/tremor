// Package runstore defines the history of finished experiment runs and how it
// is queried. Documents are keyed by the experiment's UID, so saving the same
// run twice is harmless.
package runstore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get when no run matches.
var ErrNotFound = errors.New("run not found")

// Run is one finished experiment, as stored in history.
type Run struct {
	ID              string `bson:"_id" json:"id"` // experiment UID
	Namespace       string `bson:"namespace" json:"namespace"`
	Name            string `bson:"name" json:"name"`
	Cluster         string `bson:"cluster" json:"cluster"`
	OperatorVersion string `bson:"operator_version" json:"operatorVersion"`

	FaultType string            `bson:"fault_type" json:"faultType"`
	Result    string            `bson:"result" json:"result"` // completed, failed, aborted, dry_run
	Message   string            `bson:"message" json:"message"`
	Selector  map[string]string `bson:"selector" json:"selector"`
	// SteadyStateURL is empty when the experiment had no steady-state probe.
	SteadyStateURL string `bson:"steady_state_url,omitempty" json:"steadyStateUrl,omitempty"`

	StartedAt   time.Time  `bson:"started_at" json:"startedAt"`
	InjectedAt  *time.Time `bson:"injected_at,omitempty" json:"injectedAt,omitempty"`
	RecoveredAt *time.Time `bson:"recovered_at,omitempty" json:"recoveredAt,omitempty"`

	BaselineReadyPods  int      `bson:"baseline_ready_pods" json:"baselineReadyPods"`
	AffectedPods       []string `bson:"affected_pods,omitempty" json:"affectedPods,omitempty"`
	PodRecoveryMillis  *int64   `bson:"pod_recovery_millis,omitempty" json:"podRecoveryMillis,omitempty"`
	UserRecoveryMillis *int64   `bson:"user_recovery_millis,omitempty" json:"userRecoveryMillis,omitempty"`
	UserImpacted       bool     `bson:"user_impacted" json:"userImpacted"`
	ProbeSuccesses     int      `bson:"probe_successes" json:"probeSuccesses"`
	ProbeFailures      int      `bson:"probe_failures" json:"probeFailures"`

	RecordedAt time.Time `bson:"recorded_at" json:"recordedAt"`
}

// Availability is the share of passing probes, and false when no probes ran.
func (r Run) Availability() (float64, bool) {
	total := r.ProbeSuccesses + r.ProbeFailures
	if total == 0 {
		return 0, false
	}
	return float64(r.ProbeSuccesses) / float64(total), true
}

// Filter narrows a history query. Zero fields match everything.
type Filter struct {
	Namespace string
	Name      string
	FaultType string
	Result    string
	// Since keeps runs started at or after this time.
	Since time.Time
	// Limit caps the result size; 0 means no limit.
	Limit int
}

// Store persists run history.
type Store interface {
	// Save inserts or replaces the run with the same ID.
	Save(ctx context.Context, r Run) error
	// List returns matching runs, newest first.
	List(ctx context.Context, f Filter) ([]Run, error)
	// Get returns the run with the given ID, or ErrNotFound.
	Get(ctx context.Context, id string) (Run, error)
}
