package fault

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Annotations Tremor puts on a workload it has scaled to zero. They are written
// in the same update that sets replicas to 0, so the original replica count is
// never lost, and they make Revert safe to repeat.
const (
	AnnotationHeldBy           = "tremor.dev/held-by"
	AnnotationOriginalReplicas = "tremor.dev/original-replicas"
)

// Supported workload kinds.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
)

// Workload names a Deployment or StatefulSet.
type Workload struct {
	Kind string
	Name string
}

func (w Workload) String() string { return w.Kind + "/" + w.Name }

// IsZero reports whether no workload is set.
func (w Workload) IsZero() bool { return w.Name == "" }

// ResolveWorkload finds the single Deployment or StatefulSet that owns all of
// the given pods. Pods of a bare ReplicaSet, Job, DaemonSet or no owner at all
// are rejected: scaling those to zero is not a meaningful outage.
func ResolveWorkload(ctx context.Context, c client.Client, namespace string, pods []corev1.Pod) (Workload, error) {
	if len(pods) == 0 {
		return Workload{}, fmt.Errorf("no pods to resolve a workload from")
	}
	var found Workload
	for i := range pods {
		w, err := ownerWorkload(ctx, c, namespace, &pods[i])
		if err != nil {
			return Workload{}, err
		}
		if found.IsZero() {
			found = w
		} else if found != w {
			return Workload{}, fmt.Errorf("target.selector matches pods of more than one workload (%s and %s); narrow the selector", found, w)
		}
	}
	return found, nil
}

func ownerWorkload(ctx context.Context, c client.Client, namespace string, pod *corev1.Pod) (Workload, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return Workload{}, fmt.Errorf("pod %s has no controller owner, so there is no workload to take down", pod.Name)
	}
	switch owner.Kind {
	case KindStatefulSet:
		return Workload{Kind: KindStatefulSet, Name: owner.Name}, nil
	case "ReplicaSet":
		var rs appsv1.ReplicaSet
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: owner.Name}, &rs); err != nil {
			return Workload{}, fmt.Errorf("reading ReplicaSet %s of pod %s: %w", owner.Name, pod.Name, err)
		}
		if dep := metav1.GetControllerOf(&rs); dep != nil && dep.Kind == KindDeployment {
			return Workload{Kind: KindDeployment, Name: dep.Name}, nil
		}
		return Workload{}, fmt.Errorf("ReplicaSet %s is not owned by a Deployment", owner.Name)
	default:
		return Workload{}, fmt.Errorf("pod %s is owned by a %s; only Deployments and StatefulSets are supported", pod.Name, owner.Kind)
	}
}

// scalable gives uniform access to the replica count of either workload kind.
type scalable struct {
	obj         client.Object
	replicas    func() int32
	setReplicas func(int32)
}

func replicasOrOne(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

func loadScalable(ctx context.Context, c client.Client, namespace string, w Workload) (scalable, error) {
	key := types.NamespacedName{Namespace: namespace, Name: w.Name}
	switch w.Kind {
	case KindDeployment:
		d := &appsv1.Deployment{}
		if err := c.Get(ctx, key, d); err != nil {
			return scalable{}, err
		}
		return scalable{
			obj:         d,
			replicas:    func() int32 { return replicasOrOne(d.Spec.Replicas) },
			setReplicas: func(n int32) { d.Spec.Replicas = &n },
		}, nil
	case KindStatefulSet:
		s := &appsv1.StatefulSet{}
		if err := c.Get(ctx, key, s); err != nil {
			return scalable{}, err
		}
		return scalable{
			obj:         s,
			replicas:    func() int32 { return replicasOrOne(s.Spec.Replicas) },
			setReplicas: func(n int32) { s.Spec.Replicas = &n },
		}, nil
	}
	return scalable{}, fmt.Errorf("unsupported workload kind %q", w.Kind)
}
