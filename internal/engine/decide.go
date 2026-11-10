package engine

import (
	"fmt"
	"time"
)

const (
	baselineRetry = 2 * time.Second
	recoveryPoll  = time.Second
)

// Decide evaluates one step of the experiment state machine.
//
//	Pending -> BaselineCheck -> Injecting -> Recovering -> Completed
//	                 |             |   ^          |
//	                 |             v   |          |
//	                 |           Holding          |        (sustained faults only)
//	                 +--------> Failed <----------+      (any non-terminal -> Aborted)
//
// One-shot faults (PodKill) go straight from Injecting to Recovering. Sustained
// faults wait in Holding until the fault has run its course, are then released,
// and the recovery clock starts at the release.
func Decide(phase Phase, o Observation, c Config) Decision {
	if IsTerminal(phase) {
		return Decision{Next: phase}
	}
	if c.Abort {
		return Decision{Next: PhaseAborted, Message: "aborted by annotation"}
	}

	switch phase {
	case "", PhasePending:
		return Decision{Next: PhaseBaselineCheck, Message: "checking baseline health"}

	case PhaseBaselineCheck:
		podsHealthy := o.TotalPods > 0 && o.ReadyPods == o.TotalPods && o.TerminatingPods == 0
		healthy := podsHealthy && (!o.ProbeEnabled || o.ProbeHealthy)
		if !healthy {
			state := fmt.Sprintf("%d/%d pods ready", o.ReadyPods, o.TotalPods)
			if o.ProbeEnabled {
				state += fmt.Sprintf(", steady-state probe healthy=%t", o.ProbeHealthy)
			}
			if o.Now.Sub(o.StartedAt) >= c.BaselineTimeout {
				return Decision{Next: PhaseFailed, Message: "baseline not healthy: " + state}
			}
			return Decision{
				Next:         PhaseBaselineCheck,
				Message:      "waiting for healthy baseline: " + state,
				RequeueAfter: baselineRetry,
			}
		}
		if c.DryRun {
			return Decision{Next: PhaseCompleted, Action: ActionDryRun, Message: "dry run: no fault injected"}
		}
		return Decision{Next: PhaseInjecting, Action: ActionPlan, Message: "baseline healthy, targets chosen"}

	case PhaseInjecting:
		if o.Sustained {
			return Decision{Next: PhaseHolding, Action: ActionInject, Message: "fault injected, holding until it has run its course", RequeueAfter: recoveryPoll}
		}
		return Decision{Next: PhaseRecovering, Action: ActionInject, Message: "fault injected, waiting for recovery", RequeueAfter: recoveryPoll}

	case PhaseHolding:
		if o.FaultDone {
			return Decision{Next: PhaseRecovering, Action: ActionRevert, Message: "fault released, waiting for recovery", RequeueAfter: recoveryPoll}
		}
		return Decision{Next: PhaseHolding, Message: "fault active, holding until it has run its course", RequeueAfter: recoveryPoll}

	case PhaseRecovering:
		// Recovery is timed from the release of a sustained fault, otherwise
		// from injection, so a long outage does not inflate the recovery time.
		// A node drain is released only when the run ends, which says nothing
		// about when recovery began, so only sustained faults count.
		start := o.InjectedAt
		if o.Sustained && !o.RevertedAt.IsZero() {
			start = o.RevertedAt
		}
		elapsed := o.Now.Sub(start)
		if elapsed < 0 {
			elapsed = 0
		}
		podsOK := !o.AffectedStillPresent &&
			o.TerminatingPods == 0 &&
			o.ReadyPods == o.TotalPods &&
			o.ReadyPods >= o.BaselineReady

		var podsAt time.Time
		var podTime time.Duration
		if podsOK {
			podsAt = o.PodsRecoveredAt
			if podsAt.IsZero() {
				podsAt = o.Now
			}
			if podTime = podsAt.Sub(start); podTime < 0 {
				podTime = 0
			}
		}
		probeOK := !o.ProbeEnabled || o.ProbeStable

		if podsOK && probeOK {
			return Decision{Next: PhaseCompleted, Message: "recovered", RecoveryTime: podTime, PodsRecoveredAt: podsAt}
		}
		if elapsed >= c.RecoveryTimeout {
			msg := fmt.Sprintf("not recovered after %s: %d/%d pods ready, baseline %d", c.RecoveryTimeout, o.ReadyPods, o.TotalPods, o.BaselineReady)
			if o.ProbeEnabled {
				msg += fmt.Sprintf(", steady-state stable=%t", o.ProbeStable)
			}
			return Decision{Next: PhaseFailed, Message: msg, RecoveryTime: podTime, PodsRecoveredAt: podsAt}
		}
		msg := "waiting for pods to recover"
		if podsOK {
			msg = "pods recovered, waiting for steady state"
		}
		return Decision{Next: PhaseRecovering, Message: msg, RequeueAfter: recoveryPoll, RecoveryTime: podTime, PodsRecoveredAt: podsAt}
	}

	return Decision{Next: PhaseFailed, Message: fmt.Sprintf("unknown phase %q", phase)}
}
