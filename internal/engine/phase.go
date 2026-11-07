// Package engine holds Tremor's pure decision logic: the experiment state
// machine and the safety rules that decide how many targets a fault may touch.
//
// It deliberately depends on the standard library only, so every rule can be
// unit tested without a cluster. The controller translates Kubernetes objects
// into the plain values used here and applies the resulting decisions.
package engine

import "time"

// Phase is the lifecycle phase of an experiment. The string values match the
// api/v1alpha1 Phase constants.
type Phase string

const (
	PhasePending       Phase = "Pending"
	PhaseBaselineCheck Phase = "BaselineCheck"
	PhaseInjecting     Phase = "Injecting"
	PhaseRecovering    Phase = "Recovering"
	PhaseCompleted     Phase = "Completed"
	PhaseFailed        Phase = "Failed"
	PhaseAborted       Phase = "Aborted"
)

// IsTerminal reports whether no further transitions happen from p.
func IsTerminal(p Phase) bool {
	return p == PhaseCompleted || p == PhaseFailed || p == PhaseAborted
}

// Action is a side effect the controller must perform for a decision.
type Action int

const (
	ActionNone Action = iota
	// ActionPlan chooses the pods to disturb and records them in status.
	ActionPlan
	// ActionDryRun plans like ActionPlan but never injects.
	ActionDryRun
	// ActionInject applies the fault to the planned targets.
	ActionInject
)

// Observation is a snapshot of the target workload plus experiment timestamps.
type Observation struct {
	Now        time.Time
	StartedAt  time.Time // when the experiment was first reconciled
	InjectedAt time.Time // zero until the fault has been injected

	TotalPods       int
	ReadyPods       int
	TerminatingPods int

	// BaselineReady is the ready pod count recorded before injection.
	BaselineReady int
	// AffectedStillPresent is true while any pod targeted by the fault
	// (matched by UID) still exists in the cluster.
	AffectedStillPresent bool
}

// Config carries the per-experiment knobs the state machine needs.
type Config struct {
	RecoveryTimeout time.Duration
	BaselineTimeout time.Duration
	Abort           bool
	DryRun          bool
}

// Decision is the outcome of one evaluation of the state machine.
type Decision struct {
	Next         Phase
	Action       Action
	Message      string
	RequeueAfter time.Duration
	// RecoveryTime is set when Next is Completed after a real injection.
	RecoveryTime time.Duration
}
