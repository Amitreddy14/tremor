// Package controller reconciles ChaosExperiment resources.
package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/engine"
	"github.com/Amitreddy14/tremor/internal/fault"
	"github.com/Amitreddy14/tremor/internal/metrics"
	"github.com/Amitreddy14/tremor/internal/prober"
	"github.com/Amitreddy14/tremor/internal/runstore"
)

const (
	finalizerName          = "tremor.dev/finalizer"
	defaultRecoveryTimeout = 120 * time.Second
	defaultBaselineTimeout = 30 * time.Second
)

// ChaosExperimentReconciler drives experiments through the engine state machine.
type ChaosExperimentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Now returns the current time; tests may replace it.
	Now func() time.Time
	// Rand picks fault targets; tests may seed it.
	Rand *rand.Rand
	// Prober runs HTTP steady-state probes; probing is disabled when nil.
	Prober *prober.Manager
	// Metrics records Prometheus metrics; metrics are skipped when nil.
	Metrics *metrics.Recorder
	// History stores finished runs. Recording is skipped when nil; the
	// experiment resource stays the source of truth either way.
	History runstore.Store
	// ClusterName labels recorded runs; useful when several clusters share one
	// history database.
	ClusterName string
	// OperatorVersion labels recorded runs.
	OperatorVersion string
	// StressImage runs CPU and memory stress; fault.DefaultStressImage if empty.
	StressImage string
	// NetemImage runs network faults; fault.DefaultNetemImage if empty.
	NetemImage string
}

// +kubebuilder:rbac:groups=tremor.dev,resources=chaosexperiments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=tremor.dev,resources=chaosexperiments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tremor.dev,resources=chaosexperiments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/ephemeralcontainers,verbs=update;patch
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile advances one experiment by one step.
func (r *ChaosExperimentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	var exp tremorv1.ChaosExperiment
	if err := r.Get(ctx, req.NamespacedName, &exp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !exp.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &exp)
	}
	if !controllerutil.ContainsFinalizer(&exp, finalizerName) {
		controllerutil.AddFinalizer(&exp, finalizerName)
		if err := r.Update(ctx, &exp); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	phase := engine.Phase(exp.Status.Phase)
	if engine.IsTerminal(phase) {
		r.stopProbe(&exp)
		return r.recordRun(ctx, &exp, phase)
	}

	injector, ok := r.injector(&exp)
	if !ok {
		return r.fail(ctx, &exp, fmt.Sprintf("unsupported fault type %q", exp.Spec.Fault.Type))
	}
	if err := engine.ValidateSelector(exp.Spec.Target.Selector); err != nil {
		return r.fail(ctx, &exp, err.Error())
	}
	fp, err := engine.NormalizeFault(faultParams(&exp))
	if err != nil {
		return r.fail(ctx, &exp, err.Error())
	}
	sustained := engine.IsSustained(fp.Type)

	now := r.now()
	if exp.Status.StartedAt == nil {
		t := metav1.NewMicroTime(now)
		exp.Status.StartedAt = &t
	}

	// Steady-state probing starts with the experiment so the baseline is
	// verified and every probe between injection and recovery is captured.
	probeCfg, threshold, probing := probeConfig(&exp)
	probing = probing && r.Prober != nil
	var series prober.Series
	if probing {
		r.Prober.Ensure(string(exp.UID), probeCfg)
		series = r.Prober.Snapshot(string(exp.UID))
	}

	pods, err := r.listPods(ctx, &exp)
	if err != nil {
		return ctrl.Result{}, err
	}
	obs := observe(&exp, pods, now, series, probing, threshold)
	obs.Sustained = sustained
	if !engine.RemovesPods(fp.Type) {
		// Stress leaves the target pods in place, so "affected pods gone" is
		// not part of recovery for it.
		obs.AffectedStillPresent = false
	}
	spec := faultSpec(&exp, fp)

	if phase == engine.PhaseHolding {
		if h, ok := injector.(fault.Sustained); ok {
			done, derr := h.Done(ctx, r.Client, spec, obs.InjectedAt, now)
			if derr != nil {
				return r.failAfterFault(ctx, &exp, injector, spec, derr.Error())
			}
			obs.FaultDone = done
		}
	}
	d := engine.Decide(phase, obs, configFrom(&exp))

	switch d.Action {
	case engine.ActionPlan, engine.ActionDryRun:
		affected, workload, node, err := r.plan(ctx, &exp, pods, fp)
		if err != nil {
			return r.fail(ctx, &exp, err.Error())
		}
		exp.Status.AffectedPods = affected
		exp.Status.BaselineReadyPods = int32(obs.ReadyPods)
		if workload != nil {
			exp.Status.Workload = workload
		}
		exp.Status.Node = node
		if d.Action == engine.ActionDryRun {
			d.Message = fmt.Sprintf("dry run: would disturb %d pod(s): %s", len(affected), podNames(affected))
			if workload != nil {
				d.Message += fmt.Sprintf(" by taking down %s/%s", workload.Kind, workload.Name)
			}
			if node != "" {
				d.Message += fmt.Sprintf(" by draining node %s", node)
			}
		}

	case engine.ActionInject:
		if err := injector.Inject(ctx, r.Client, spec); err != nil {
			if errors.Is(err, fault.ErrBlocked) {
				// Refused by the cluster (a PodDisruptionBudget, say): release
				// what was done so far and report it as the result.
				return r.failAfterFault(ctx, &exp, injector, spec, err.Error())
			}
			// Injection is idempotent, so returning the error retries safely.
			return ctrl.Result{}, err
		}
		t := metav1.NewMicroTime(r.now())
		exp.Status.InjectedAt = &t

	case engine.ActionRevert:
		// Also idempotent: if the status update below fails, the next
		// reconcile releases the fault again.
		if err := injector.Revert(ctx, r.Client, spec); err != nil {
			return ctrl.Result{}, err
		}
		t := metav1.NewMicroTime(r.now())
		exp.Status.RevertedAt = &t
	}

	// Never leave a fault behind. A sustained fault is released when the run
	// ends badly (failure or abort) while it is active; a node drain keeps its
	// node cordoned until the run is over, however it ends. Release is
	// idempotent, so it is safe even if the fault was only partly injected.
	injecting := phase == engine.PhaseInjecting || phase == engine.PhaseHolding || phase == engine.PhaseRecovering
	if engine.NeedsRelease(fp.Type) && injecting && exp.Status.RevertedAt == nil && engine.IsTerminal(d.Next) {
		if err := injector.Revert(ctx, r.Client, spec); err != nil {
			return ctrl.Result{}, err
		}
		if exp.Status.InjectedAt != nil {
			t := metav1.NewMicroTime(r.now())
			exp.Status.RevertedAt = &t
		}
	}

	// Record pod-level recovery the first time pods are seen recovered, even
	// if the steady-state probe is still catching up.
	if !d.PodsRecoveredAt.IsZero() && exp.Status.PodRecoveryMillis == nil {
		at := metav1.NewMicroTime(d.PodsRecoveredAt)
		ms := d.RecoveryTime.Milliseconds()
		exp.Status.PodsRecoveredAt = &at
		exp.Status.PodRecoveryMillis = &ms
	}

	if d.Next == engine.PhaseCompleted && phase == engine.PhaseRecovering {
		t := metav1.NewMicroTime(now)
		exp.Status.RecoveredAt = &t
		if probing && exp.Status.InjectedAt != nil {
			a := prober.Analyze(series, exp.Status.InjectedAt.Time, threshold)
			// For sustained faults users are "recovered" relative to the
			// release of the fault, not its start (ADR 0003).
			clockStart := exp.Status.InjectedAt.Time
			if sustained && exp.Status.RevertedAt != nil {
				clockStart = exp.Status.RevertedAt.Time
			}
			var userMs int64
			if a.Impacted {
				userMs = a.StableAt.Sub(clockStart).Milliseconds()
				if userMs < 0 {
					userMs = 0
				}
			}
			exp.Status.UserRecoveryMillis = &userMs
			exp.Status.UserImpacted = a.Impacted
			exp.Status.ProbeSuccesses = int32(a.Successes)
			exp.Status.ProbeFailures = int32(a.Failures)
		}
	}

	exp.Status.Phase = tremorv1.Phase(d.Next)
	exp.Status.Message = d.Message
	exp.Status.ObservedGeneration = exp.Generation
	if err := r.Status().Update(ctx, &exp); err != nil {
		return ctrl.Result{}, err
	}
	if engine.IsTerminal(d.Next) {
		r.stopProbe(&exp)
	}
	r.recordMetrics(&exp, phase, d.Next, d.Action == engine.ActionInject, series, probing)

	if d.Next != phase {
		lg.Info("phase changed", "experiment", req.NamespacedName, "from", phase, "to", d.Next, "message", d.Message)
	}
	if engine.IsTerminal(d.Next) && r.History != nil {
		// Come straight back through the terminal path to record the run.
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{RequeueAfter: d.RequeueAfter}, nil
}

func (r *ChaosExperimentReconciler) stopProbe(exp *tremorv1.ChaosExperiment) {
	if r.Prober != nil {
		r.Prober.Stop(string(exp.UID))
	}
}

// finalize reverts any injected fault, then releases the resource.
func (r *ChaosExperimentReconciler) finalize(ctx context.Context, exp *tremorv1.ChaosExperiment) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(exp, finalizerName) {
		return ctrl.Result{}, nil
	}
	if injector, ok := r.injector(exp); ok {
		// Best effort on parameters: Revert only needs the workload and owner,
		// which come from status, so an invalid spec must not block deletion.
		fp, _ := engine.NormalizeFault(faultParams(exp))
		if err := injector.Revert(ctx, r.Client, faultSpec(exp, fp)); err != nil {
			return ctrl.Result{}, err
		}
	}
	r.stopProbe(exp)
	r.Metrics.Forget(exp.Namespace, exp.Name)
	controllerutil.RemoveFinalizer(exp, finalizerName)
	return ctrl.Result{}, r.Update(ctx, exp)
}

// fail moves the experiment to Failed with a message.
func (r *ChaosExperimentReconciler) fail(ctx context.Context, exp *tremorv1.ChaosExperiment, msg string) (ctrl.Result, error) {
	from := engine.Phase(exp.Status.Phase)
	exp.Status.Phase = tremorv1.PhaseFailed
	exp.Status.Message = msg
	exp.Status.ObservedGeneration = exp.Generation
	err := r.Status().Update(ctx, exp)
	if err == nil {
		r.stopProbe(exp)
		r.recordMetrics(exp, from, engine.PhaseFailed, false, nil, false)
	}
	return ctrl.Result{}, err
}

// failAfterFault releases a sustained fault, then fails the experiment.
func (r *ChaosExperimentReconciler) failAfterFault(ctx context.Context, exp *tremorv1.ChaosExperiment,
	injector fault.Injector, spec fault.Spec, msg string) (ctrl.Result, error) {
	if err := injector.Revert(ctx, r.Client, spec); err != nil {
		return ctrl.Result{}, err
	}
	t := metav1.NewMicroTime(r.now())
	exp.Status.RevertedAt = &t
	return r.fail(ctx, exp, msg)
}

func (r *ChaosExperimentReconciler) injector(exp *tremorv1.ChaosExperiment) (fault.Injector, bool) {
	return fault.For(string(exp.Spec.Fault.Type), fault.Options{StressImage: r.StressImage, NetemImage: r.NetemImage})
}

func (r *ChaosExperimentReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ChaosExperimentReconciler) listPods(ctx context.Context, exp *tremorv1.ChaosExperiment) ([]corev1.Pod, error) {
	var list corev1.PodList
	err := r.List(ctx, &list,
		client.InNamespace(exp.Namespace),
		client.MatchingLabels(exp.Spec.Target.Selector))
	if err != nil {
		return nil, err
	}
	pods := make([]corev1.Pod, 0, len(list.Items))
	for _, p := range list.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		pods = append(pods, p)
	}
	return pods, nil
}

// plan applies the safety rules and picks the pods to disturb. For a
// DependencyOutage it selects every pod and also resolves the workload that
// will be taken down; for a NodeDrain it picks the node and selects the pods
// running on it.
func (r *ChaosExperimentReconciler) plan(ctx context.Context, exp *tremorv1.ChaosExperiment,
	pods []corev1.Pod, fp engine.FaultParams) ([]tremorv1.AffectedPod, *tremorv1.WorkloadRef, string, error) {
	var ready []corev1.Pod
	for i := range pods {
		if isReady(&pods[i]) && pods[i].DeletionTimestamp.IsZero() {
			ready = append(ready, pods[i])
		}
	}
	if len(ready) == 0 {
		return nil, nil, "", engine.ErrNoSafeTarget
	}

	if fp.Type == engine.FaultDependencyOutage {
		wl, err := fault.ResolveWorkload(ctx, r.Client, exp.Namespace, ready)
		if err != nil {
			return nil, nil, "", err
		}
		affected := make([]tremorv1.AffectedPod, 0, len(ready))
		for i := range ready {
			affected = append(affected, tremorv1.AffectedPod{Name: ready[i].Name, UID: string(ready[i].UID)})
		}
		return affected, &tremorv1.WorkloadRef{Kind: wl.Kind, Name: wl.Name}, "", nil
	}

	policy := engine.SafetyPolicy{
		MaxAffectedPercent: int(exp.Spec.Safety.MaxAffectedPercent),
		AllowTotalOutage:   exp.Spec.Safety.AllowTotalOutage,
	}

	if fp.Type == engine.FaultNodeDrain {
		return planDrain(pods, policy)
	}

	n, err := engine.KillCount(len(pods), fp.Count, policy)
	if err != nil {
		return nil, nil, "", err
	}

	uids := map[string]string{}
	names := make([]string, 0, len(ready))
	for i := range ready {
		names = append(names, ready[i].Name)
		uids[ready[i].Name] = string(ready[i].UID)
	}

	rnd := r.Rand
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	picked := engine.Pick(names, n, rnd)

	affected := make([]tremorv1.AffectedPod, 0, len(picked))
	for _, name := range picked {
		affected = append(affected, tremorv1.AffectedPod{Name: name, UID: uids[name]})
	}
	if len(affected) == 0 {
		return nil, nil, "", engine.ErrNoSafeTarget
	}
	return affected, nil, "", nil
}

// planDrain picks the node to drain and selects the workload's pods on it.
// Pods without a node (not yet scheduled) are never targets.
func planDrain(pods []corev1.Pod, policy engine.SafetyPolicy) ([]tremorv1.AffectedPod, *tremorv1.WorkloadRef, string, error) {
	count := map[string]int{}
	for i := range pods {
		if pods[i].DeletionTimestamp.IsZero() && pods[i].Spec.NodeName != "" {
			count[pods[i].Spec.NodeName]++
		}
	}
	loads := make([]engine.NodeLoad, 0, len(count))
	for node, n := range count {
		loads = append(loads, engine.NodeLoad{Node: node, Pods: n})
	}
	node, err := engine.PickDrainNode(loads, len(pods), policy)
	if err != nil {
		return nil, nil, "", err
	}
	var affected []tremorv1.AffectedPod
	for i := range pods {
		if pods[i].Spec.NodeName == node && pods[i].DeletionTimestamp.IsZero() {
			affected = append(affected, tremorv1.AffectedPod{Name: pods[i].Name, UID: string(pods[i].UID)})
		}
	}
	return affected, nil, node, nil
}

// SetupWithManager registers the controller and a pod watch so experiments
// react to readiness changes immediately instead of waiting for the next poll.
func (r *ChaosExperimentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&tremorv1.ChaosExperiment{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToExperiments)).
		Named("chaosexperiment").
		Complete(r)
}

// podToExperiments maps a pod event to the active experiments selecting it.
func (r *ChaosExperimentReconciler) podToExperiments(ctx context.Context, obj client.Object) []reconcile.Request {
	var list tremorv1.ChaosExperimentList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		e := &list.Items[i]
		if len(e.Spec.Target.Selector) == 0 || engine.IsTerminal(engine.Phase(e.Status.Phase)) {
			continue
		}
		if labels.SelectorFromSet(labels.Set(e.Spec.Target.Selector)).Matches(labels.Set(obj.GetLabels())) {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Namespace, Name: e.Name}})
		}
	}
	return reqs
}
