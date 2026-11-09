package fault

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodKill force-deletes target pods (grace period 0), simulating a crash.
type PodKill struct{}

// Inject deletes each target pod. A pod that is already gone, or whose UID no
// longer matches, counts as done, which keeps retries safe.
func (PodKill) Inject(ctx context.Context, c client.Client, s Spec) error {
	namespace := s.Namespace
	for _, t := range s.Targets {
		uid := types.UID(t.UID)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: t.Name, Namespace: namespace}}
		err := c.Delete(ctx, pod, client.GracePeriodSeconds(0), client.Preconditions{UID: &uid})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return fmt.Errorf("deleting pod %s/%s: %w", namespace, t.Name, err)
		}
	}
	return nil
}

// Revert is a no-op: a killed pod is recreated by its owner, not by Tremor.
func (PodKill) Revert(context.Context, client.Client, Spec) error { return nil }
