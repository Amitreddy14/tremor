package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/engine"
	"github.com/Amitreddy14/tremor/internal/schedule"
)

const (
	defaultStartingDeadline = 300 * time.Second
	defaultHistoryLimit     = 3
)

// ChaosScheduleReconciler creates ChaosExperiments on a cron schedule.
type ChaosScheduleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Now returns the current time; tests replace it.
	Now func() time.Time
}

func (r *ChaosScheduleReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// +kubebuilder:rbac:groups=tremor.dev,resources=chaosschedules,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=tremor.dev,resources=chaosschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tremor.dev,resources=chaosexperiments,verbs=get;list;watch;create;delete

// Reconcile plans the next run, creates an experiment when one is due, and
// prunes finished experiments beyond the history limits.
func (r *ChaosScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var s tremorv1.ChaosSchedule
	if err := r.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !s.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil // owned experiments are garbage collected
	}

	var owned tremorv1.ChaosExperimentList
	if err := r.List(ctx, &owned, client.InNamespace(s.Namespace),
		client.MatchingLabels{tremorv1.ScheduleLabel: s.Name}); err != nil {
		return ctrl.Result{}, err
	}
	active, finished, failed := classify(owned.Items)

	before := s.Status.DeepCopy()
	s.Status.ObservedGeneration = s.Generation
	s.Status.Active = active

	now := r.now()
	result := ctrl.Result{}

	cron, err := schedule.Parse(s.Spec.Schedule)
	switch {
	case err != nil:
		s.Status.Message = "invalid schedule: " + err.Error()
		s.Status.NextScheduleTime = nil
	case s.Spec.Suspend:
		s.Status.Message = "suspended"
		s.Status.NextScheduleTime = nil
	default:
		if err := r.runDue(ctx, &s, cron, now, len(active)); err != nil {
			return ctrl.Result{}, err
		}
		next := cron.Next(now)
		if next.IsZero() {
			s.Status.NextScheduleTime = nil
		} else {
			t := metav1.NewTime(next)
			s.Status.NextScheduleTime = &t
			result.RequeueAfter = next.Sub(now) + 500*time.Millisecond
		}
	}

	if err := r.prune(ctx, &s, finished, failed); err != nil {
		logger.Error(err, "pruning old experiments")
	}

	if !statusEqual(before, &s.Status) {
		if err := r.Status().Update(ctx, &s); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	return result, nil
}

// runDue creates the experiment for the latest due run, if any. Every due run,
// created or skipped, advances LastScheduleTime so it is never judged twice.
func (r *ChaosScheduleReconciler) runDue(ctx context.Context, s *tremorv1.ChaosSchedule, cron schedule.Cron, now time.Time, activeCount int) error {
	since := s.CreationTimestamp.Time
	if s.Status.LastScheduleTime != nil {
		since = s.Status.LastScheduleTime.Time
	}
	due, missed, ok := cron.Due(since, now)
	if !ok {
		return nil
	}
	t := metav1.NewTime(due)
	s.Status.LastScheduleTime = &t

	deadline := defaultStartingDeadline
	if s.Spec.StartingDeadlineSeconds != nil {
		deadline = time.Duration(*s.Spec.StartingDeadlineSeconds) * time.Second
	}
	switch {
	case now.Sub(due) > deadline:
		s.Status.Message = fmt.Sprintf("skipped run at %s: %s past the %s starting deadline", due.Format(time.RFC3339), now.Sub(due).Round(time.Second), deadline)
		return nil
	case s.Spec.ConcurrencyPolicy != tremorv1.ConcurrencyAllow && activeCount > 0:
		s.Status.Message = fmt.Sprintf("skipped run at %s: previous experiment still active (concurrencyPolicy Forbid)", due.Format(time.RFC3339))
		return nil
	}

	exp := r.newExperiment(s, due)
	if err := controllerutil.SetControllerReference(s, exp, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, exp); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	s.Status.LastExperiment = exp.Name
	s.Status.Message = fmt.Sprintf("started %s", exp.Name)
	if missed > 1 {
		s.Status.Message += fmt.Sprintf(" (%d earlier run(s) were missed)", missed-1)
	}
	return nil
}

// newExperiment stamps out the experiment for the run at the given time. The
// name embeds the run time, so a repeated attempt for the same run collides
// instead of creating a duplicate.
func (r *ChaosScheduleReconciler) newExperiment(s *tremorv1.ChaosSchedule, at time.Time) *tremorv1.ChaosExperiment {
	name := s.Name
	if len(name) > 200 {
		name = name[:200]
	}
	labels := map[string]string{}
	for k, v := range s.Spec.Template.Labels {
		labels[k] = v
	}
	labels[tremorv1.ScheduleLabel] = s.Name
	return &tremorv1.ChaosExperiment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", name, at.Unix()),
			Namespace: s.Namespace,
			Labels:    labels,
		},
		Spec: *s.Spec.Template.Spec.DeepCopy(),
	}
}

// prune deletes the oldest finished experiments beyond the history limits.
func (r *ChaosScheduleReconciler) prune(ctx context.Context, s *tremorv1.ChaosSchedule, finished, failed []tremorv1.ChaosExperiment) error {
	for _, g := range []struct {
		items []tremorv1.ChaosExperiment
		limit *int32
	}{{finished, s.Spec.SuccessfulRunsHistoryLimit}, {failed, s.Spec.FailedRunsHistoryLimit}} {
		keep := defaultHistoryLimit
		if g.limit != nil {
			keep = int(*g.limit)
		}
		if len(g.items) <= keep {
			continue
		}
		sort.Slice(g.items, func(i, j int) bool {
			return g.items[i].CreationTimestamp.Before(&g.items[j].CreationTimestamp)
		})
		for i := range g.items[:len(g.items)-keep] {
			if err := r.Delete(ctx, &g.items[i]); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

// classify splits experiments into active names, completed ones and failed or
// aborted ones.
func classify(items []tremorv1.ChaosExperiment) (active []string, completed, failed []tremorv1.ChaosExperiment) {
	for _, e := range items {
		p := engine.Phase(e.Status.Phase)
		switch {
		case !e.DeletionTimestamp.IsZero():
			// being deleted: ignore
		case !engine.IsTerminal(p):
			active = append(active, e.Name)
		case p == engine.PhaseCompleted:
			completed = append(completed, e)
		default:
			failed = append(failed, e)
		}
	}
	sort.Strings(active)
	return active, completed, failed
}

func statusEqual(a, b *tremorv1.ChaosScheduleStatus) bool {
	eq := func(x, y *metav1.Time) bool {
		if x == nil || y == nil {
			return x == y
		}
		return x.Time.Equal(y.Time)
	}
	if a.Message != b.Message || a.LastExperiment != b.LastExperiment || a.ObservedGeneration != b.ObservedGeneration ||
		!eq(a.LastScheduleTime, b.LastScheduleTime) || !eq(a.NextScheduleTime, b.NextScheduleTime) ||
		len(a.Active) != len(b.Active) {
		return false
	}
	for i := range a.Active {
		if a.Active[i] != b.Active[i] {
			return false
		}
	}
	return true
}

// SetupWithManager registers the controller; owned experiments trigger a
// reconcile when they change, so finished runs are pruned promptly.
func (r *ChaosScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&tremorv1.ChaosSchedule{}).
		Owns(&tremorv1.ChaosExperiment{}).
		Named("chaosschedule").
		Complete(r)
}
