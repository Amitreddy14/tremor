package controller

import (
	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/engine"
	"github.com/Amitreddy14/tremor/internal/prober"
)

// recordMetrics publishes metrics after a status update has been persisted.
// It runs only on success, so a retried reconcile never double counts.
func (r *ChaosExperimentReconciler) recordMetrics(exp *tremorv1.ChaosExperiment, from, to engine.Phase,
	injected bool, series prober.Series, probing bool) {
	m := r.Metrics
	if m == nil {
		return
	}
	ns, name, faultType := exp.Namespace, exp.Name, string(exp.Spec.Fault.Type)

	m.SetPhase(ns, name, string(to))
	if injected {
		m.FaultInjected(faultType)
	}
	if probing && !engine.IsTerminal(to) && len(series) > 0 {
		m.SetSteadyState(ns, name, series[len(series)-1].OK)
	}

	if from == to || !engine.IsTerminal(to) {
		return
	}
	m.ClearSteadyState(ns, name)
	m.ExperimentFinished(faultType, resultLabel(exp, to))

	if to == engine.PhaseCompleted && exp.Status.PodRecoveryMillis != nil {
		podSeconds := float64(*exp.Status.PodRecoveryMillis) / 1000
		var userSeconds, availability *float64
		if exp.Status.UserRecoveryMillis != nil {
			v := float64(*exp.Status.UserRecoveryMillis) / 1000
			userSeconds = &v
			if total := exp.Status.ProbeSuccesses + exp.Status.ProbeFailures; total > 0 {
				a := float64(exp.Status.ProbeSuccesses) / float64(total)
				availability = &a
			}
		}
		m.RecordRecovery(ns, name, faultType, podSeconds, userSeconds, availability)
	}
}

func resultLabel(exp *tremorv1.ChaosExperiment, to engine.Phase) string {
	switch to {
	case engine.PhaseCompleted:
		if exp.Spec.Safety.DryRun {
			return "dry_run"
		}
		return "completed"
	case engine.PhaseAborted:
		return "aborted"
	default:
		return "failed"
	}
}
