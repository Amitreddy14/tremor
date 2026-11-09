package fault

import (
	"context"
	"fmt"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DependencyOutage takes a dependency (for example a database) down by scaling
// its Deployment or StatefulSet to zero, then restores the original replicas.
type DependencyOutage struct{}

// Inject scales the workload to zero and records the original replica count and
// the owning experiment as annotations, in one update. Repeating Inject for the
// same experiment is a no-op; a workload held by another experiment is refused.
func (DependencyOutage) Inject(ctx context.Context, c client.Client, s Spec) error {
	if s.Workload.IsZero() {
		return fmt.Errorf("DependencyOutage has no workload to take down")
	}
	w, err := loadScalable(ctx, c, s.Namespace, s.Workload)
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.Workload, err)
	}

	ann := w.obj.GetAnnotations()
	if holder, held := ann[AnnotationHeldBy]; held {
		if holder == s.Holder {
			return nil
		}
		return fmt.Errorf("%s is already held down by experiment %s", s.Workload, holder)
	}
	if ann == nil {
		ann = map[string]string{}
	}
	ann[AnnotationHeldBy] = s.Holder
	ann[AnnotationOriginalReplicas] = strconv.Itoa(int(w.replicas()))
	w.obj.SetAnnotations(ann)
	w.setReplicas(0)
	if err := c.Update(ctx, w.obj); err != nil {
		return fmt.Errorf("scaling %s to zero: %w", s.Workload, err)
	}
	return nil
}

// Revert restores the original replica count and removes the markers. It does
// nothing if the workload is gone, was never taken down, or is held by a
// different experiment.
func (DependencyOutage) Revert(ctx context.Context, c client.Client, s Spec) error {
	if s.Workload.IsZero() {
		return nil
	}
	w, err := loadScalable(ctx, c, s.Namespace, s.Workload)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.Workload, err)
	}

	ann := w.obj.GetAnnotations()
	if holder, held := ann[AnnotationHeldBy]; !held || holder != s.Holder {
		return nil
	}
	original, err := strconv.Atoi(ann[AnnotationOriginalReplicas])
	if err != nil || original < 1 {
		original = 1
	}
	delete(ann, AnnotationHeldBy)
	delete(ann, AnnotationOriginalReplicas)
	w.obj.SetAnnotations(ann)
	w.setReplicas(int32(original))
	if err := c.Update(ctx, w.obj); err != nil {
		return fmt.Errorf("restoring %s to %d replicas: %w", s.Workload, original, err)
	}
	return nil
}

// Done reports whether the outage has lasted its duration.
func (DependencyOutage) Done(_ context.Context, _ client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	return now.Sub(injectedAt) >= s.Duration, nil
}
