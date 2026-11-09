package controller

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/engine"
	"github.com/Amitreddy14/tremor/internal/runstore"
)

const (
	historyWriteTimeout  = 10 * time.Second
	historyRetryInterval = 15 * time.Second
)

// recordRun saves a finished experiment to the history store exactly once.
//
// The store write is an upsert keyed by the experiment UID, so a crash between
// the write and the status update only causes a harmless second write. If the
// store is down, the experiment stays unrecorded and is retried; experiments
// finished before history was enabled are recorded on the first reconcile
// after it is.
func (r *ChaosExperimentReconciler) recordRun(ctx context.Context, exp *tremorv1.ChaosExperiment, phase engine.Phase) (ctrl.Result, error) {
	if r.History == nil || exp.Status.RunRecorded {
		return ctrl.Result{}, nil
	}

	wctx, cancel := context.WithTimeout(ctx, historyWriteTimeout)
	defer cancel()
	if err := r.History.Save(wctx, buildRun(exp, phase, r.now(), r.ClusterName, r.OperatorVersion)); err != nil {
		r.Metrics.RunStoreWrite("error")
		log.FromContext(ctx).Error(err, "saving run to history store; will retry", "experiment", exp.Namespace+"/"+exp.Name)
		return ctrl.Result{RequeueAfter: historyRetryInterval}, nil
	}

	exp.Status.RunRecorded = true
	if err := r.Status().Update(ctx, exp); err != nil {
		return ctrl.Result{}, err
	}
	r.Metrics.RunStoreWrite("ok")
	return ctrl.Result{}, nil
}

// buildRun converts a finished experiment into its history document.
func buildRun(exp *tremorv1.ChaosExperiment, phase engine.Phase, now time.Time, cluster, version string) runstore.Run {
	st := exp.Status
	run := runstore.Run{
		ID:                 string(exp.UID),
		Namespace:          exp.Namespace,
		Name:               exp.Name,
		Cluster:            cluster,
		OperatorVersion:    version,
		FaultType:          string(exp.Spec.Fault.Type),
		Result:             resultLabel(exp, phase),
		Message:            st.Message,
		Selector:           exp.Spec.Target.Selector,
		StartedAt:          exp.CreationTimestamp.UTC(),
		BaselineReadyPods:  int(st.BaselineReadyPods),
		PodRecoveryMillis:  st.PodRecoveryMillis,
		UserRecoveryMillis: st.UserRecoveryMillis,
		UserImpacted:       st.UserImpacted,
		ProbeSuccesses:     int(st.ProbeSuccesses),
		ProbeFailures:      int(st.ProbeFailures),
		RecordedAt:         now.UTC(),
	}
	if st.StartedAt != nil {
		run.StartedAt = st.StartedAt.UTC()
	}
	if st.InjectedAt != nil {
		t := st.InjectedAt.UTC()
		run.InjectedAt = &t
	}
	if st.RecoveredAt != nil {
		t := st.RecoveredAt.UTC()
		run.RecoveredAt = &t
	}
	if exp.Spec.SteadyState != nil {
		run.SteadyStateURL = exp.Spec.SteadyState.HTTP.URL
	}
	for _, p := range st.AffectedPods {
		run.AffectedPods = append(run.AffectedPods, p.Name)
	}
	return run
}
