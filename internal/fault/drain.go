package fault

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrBlocked means the API server refused an eviction, usually because a
// PodDisruptionBudget does not allow it. The experiment fails with a clear
// message instead of retrying: a drain that the workload's own budget forbids
// is a finding, not noise.
var ErrBlocked = errors.New("eviction blocked")

// EvictTimeout bounds one eviction request. A variable so tests can shorten it.
var EvictTimeout = 5 * time.Second

// Annotations Tremor puts on a node it has cordoned.
const (
	AnnotationNodeHeldBy       = AnnotationHeldBy
	AnnotationWasUnschedulable = "tremor.dev/was-unschedulable"
)

// NodeDrain cordons a node and evicts the target pods running on it, like node
// maintenance or a node failure, then uncordons the node when the run ends.
type NodeDrain struct{}

// Inject cordons the node, then evicts the target pods. Both steps are
// idempotent. A node cordoned by another experiment is refused.
func (NodeDrain) Inject(ctx context.Context, c client.Client, s Spec) error {
	if s.Node == "" {
		return fmt.Errorf("NodeDrain has no node to drain")
	}
	var node corev1.Node
	if err := c.Get(ctx, types.NamespacedName{Name: s.Node}, &node); err != nil {
		return fmt.Errorf("reading node %s: %w", s.Node, err)
	}
	if holder, held := node.Annotations[AnnotationNodeHeldBy]; held {
		if holder != s.Holder {
			return fmt.Errorf("node %s is already cordoned by experiment %s", s.Node, holder)
		}
	} else {
		before := node.DeepCopy()
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		node.Annotations[AnnotationNodeHeldBy] = s.Holder
		node.Annotations[AnnotationWasUnschedulable] = strconv.FormatBool(node.Spec.Unschedulable)
		node.Spec.Unschedulable = true
		if err := c.Patch(ctx, &node, client.MergeFrom(before)); err != nil {
			return fmt.Errorf("cordoning node %s: %w", s.Node, err)
		}
	}

	zero := int64(0)
	for _, t := range s.Targets {
		var pod corev1.Pod
		err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: t.Name}, &pod)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading pod %s/%s: %w", s.Namespace, t.Name, err)
		}
		if string(pod.UID) != t.UID || !pod.DeletionTimestamp.IsZero() {
			continue // replaced, or already on its way out
		}
		ev := &policyv1.Eviction{
			ObjectMeta:    metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
			DeleteOptions: &metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &pod.UID}},
		}
		// client-go retries a 429 answer for as long as the server's Retry-After
		// hint says (10 s, up to 10 times), which would hold the controller's
		// only worker for minutes. Bound each eviction and treat running out of
		// time as the refusal it is.
		evictCtx, cancel := context.WithTimeout(ctx, EvictTimeout)
		err = c.SubResource("eviction").Create(evictCtx, &pod, ev)
		timedOut := evictCtx.Err() != nil && ctx.Err() == nil
		cancel()
		switch {
		case err == nil, apierrors.IsNotFound(err), apierrors.IsConflict(err):
		case apierrors.IsTooManyRequests(err), err != nil && timedOut:
			return fmt.Errorf("%w: pod %s/%s cannot be evicted right now (a PodDisruptionBudget?): %v", ErrBlocked, s.Namespace, t.Name, err)
		default:
			return fmt.Errorf("evicting pod %s/%s: %w", s.Namespace, t.Name, err)
		}
	}
	return nil
}

// Revert uncordons the node, restoring its original schedulability. It does
// nothing if the node is gone or was cordoned by a different experiment.
func (NodeDrain) Revert(ctx context.Context, c client.Client, s Spec) error {
	if s.Node == "" {
		return nil
	}
	var node corev1.Node
	err := c.Get(ctx, types.NamespacedName{Name: s.Node}, &node)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading node %s: %w", s.Node, err)
	}
	if holder, held := node.Annotations[AnnotationNodeHeldBy]; !held || holder != s.Holder {
		return nil
	}
	before := node.DeepCopy()
	node.Spec.Unschedulable = node.Annotations[AnnotationWasUnschedulable] == "true"
	delete(node.Annotations, AnnotationNodeHeldBy)
	delete(node.Annotations, AnnotationWasUnschedulable)
	if err := c.Patch(ctx, &node, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("uncordoning node %s: %w", s.Node, err)
	}
	return nil
}
