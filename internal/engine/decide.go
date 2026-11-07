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
//	                 |                           |
//	                 +--------> Failed <---------+      (any non-terminal -> Aborted)
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
		healthy := o.TotalPods > 0 && o.ReadyPods == o.TotalPods && o.TerminatingPods == 0
		if !healthy {
			if o.Now.Sub(o.StartedAt) >= c.BaselineTimeout {
				return Decision{
					Next:    PhaseFailed,
					Message: fmt.Sprintf("baseline not healthy: %d/%d pods ready", o.ReadyPods, o.TotalPods),
				}
			}
			return Decision{
				Next:         PhaseBaselineCheck,
				Message:      fmt.Sprintf("waiting for healthy baseline: %d/%d pods ready", o.ReadyPods, o.TotalPods),
				RequeueAfter: baselineRetry,
			}
		}
		if c.DryRun {
			return Decision{Next: PhaseCompleted, Action: ActionDryRun, Message: "dry run: no fault injected"}
		}
		return Decision{Next: PhaseInjecting, Action: ActionPlan, Message: "baseline healthy, targets chosen"}

	case PhaseInjecting:
		return Decision{Next: PhaseRecovering, Action: ActionInject, Message: "fault injected, waiting for recovery", RequeueAfter: recoveryPoll}

	case PhaseRecovering:
		elapsed := o.Now.Sub(o.InjectedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		recovered := !o.AffectedStillPresent &&
			o.TerminatingPods == 0 &&
			o.ReadyPods == o.TotalPods &&
			o.ReadyPods >= o.BaselineReady
		if recovered {
			return Decision{Next: PhaseCompleted, Message: "recovered", RecoveryTime: elapsed}
		}
		if elapsed >= c.RecoveryTimeout {
			return Decision{
				Next:    PhaseFailed,
				Message: fmt.Sprintf("not recovered after %s: %d/%d pods ready, baseline %d", c.RecoveryTimeout, o.ReadyPods, o.TotalPods, o.BaselineReady),
			}
		}
		return Decision{Next: PhaseRecovering, Message: "waiting for recovery", RequeueAfter: recoveryPoll}
	}

	return Decision{Next: PhaseFailed, Message: fmt.Sprintf("unknown phase %q", phase)}
}
