package fault

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Amitreddy14/tremor/internal/fault/stress"
)

// StartGrace is how long a stress container may take to start (image pull
// included) before the experiment fails. A variable so tests can shorten it.
var StartGrace = 2 * time.Minute

// stressUser is "nobody": stress needs no privileges.
const stressUser int64 = 65534

// CPUStress burns CPU inside the target pods by adding an ephemeral container
// that runs busy loops for the experiment's duration.
type CPUStress struct{ Image string }

// MemoryStress holds memory inside the target pods the same way.
type MemoryStress struct{ Image string }

// Inject adds the stress container to every target pod.
func (f CPUStress) Inject(ctx context.Context, c client.Client, s Spec) error {
	return injectStress(ctx, c, s, f.Image, stress.CPUScript(s.CPUWorkers, s.Duration))
}

// Inject adds the stress container to every target pod.
func (f MemoryStress) Inject(ctx context.Context, c client.Client, s Spec) error {
	return injectStress(ctx, c, s, f.Image, stress.MemoryScript(s.MemoryMiB, s.Duration))
}

// Revert is a no-op: an ephemeral container cannot be removed from a pod, but
// the stress script ends by itself when its duration is over.
func (CPUStress) Revert(context.Context, client.Client, Spec) error { return nil }

// Revert is a no-op, as for CPUStress.
func (MemoryStress) Revert(context.Context, client.Client, Spec) error { return nil }

// Done reports whether the stress containers have finished.
func (CPUStress) Done(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	return stressDone(ctx, c, s, injectedAt, now)
}

// Done reports whether the stress containers have finished.
func (MemoryStress) Done(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	return stressDone(ctx, c, s, injectedAt, now)
}

func injectStress(ctx context.Context, c client.Client, s Spec, image, script string) error {
	name := stress.ContainerName(s.Holder)
	for _, t := range s.Targets {
		var pod corev1.Pod
		err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: t.Name}, &pod)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading pod %s/%s: %w", s.Namespace, t.Name, err)
		}
		if string(pod.UID) != t.UID || hasEphemeral(&pod, name) {
			continue // replaced by another pod, or already injected
		}

		no, yes, user := false, true, stressUser
		pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, corev1.EphemeralContainer{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{
				Name:            name,
				Image:           image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sh", "-c", script},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no,
					RunAsNonRoot:             &yes,
					RunAsUser:                &user,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
			},
		})
		if err := c.SubResource("ephemeralcontainers").Update(ctx, &pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("adding stress container to pod %s/%s: %w", s.Namespace, t.Name, err)
		}
	}
	return nil
}

func hasEphemeral(pod *corev1.Pod, name string) bool {
	for _, e := range pod.Spec.EphemeralContainers {
		if e.Name == name {
			return true
		}
	}
	return false
}

func ephemeralStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	for i := range pod.Status.EphemeralContainerStatuses {
		if pod.Status.EphemeralContainerStatuses[i].Name == name {
			return &pod.Status.EphemeralContainerStatuses[i]
		}
	}
	return nil
}

// stressDone is true once every stress container has terminated. A target pod
// that no longer exists counts as done. A container that has not started within
// StartGrace is an error: without it the "stress" would never have happened and
// the run would report a meaningless success.
func stressDone(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	name := stress.ContainerName(s.Holder)
	sinceInject := now.Sub(injectedAt)
	allDone := true

	for _, t := range s.Targets {
		var pod corev1.Pod
		err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: t.Name}, &pod)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("reading pod %s/%s: %w", s.Namespace, t.Name, err)
		}
		if string(pod.UID) != t.UID {
			continue
		}

		st := ephemeralStatus(&pod, name)
		switch {
		case st != nil && st.State.Terminated != nil:
			// finished (an OOM kill of a memory stress also lands here)
		case st != nil && st.State.Running != nil:
			if sinceInject > s.Duration+StartGrace {
				continue // far past its duration; do not hang the experiment
			}
			allDone = false
		default:
			if sinceInject > StartGrace {
				reason := "no status reported"
				if st != nil && st.State.Waiting != nil {
					reason = st.State.Waiting.Reason
					if st.State.Waiting.Message != "" {
						reason += ": " + st.State.Waiting.Message
					}
				}
				return false, fmt.Errorf("stress container did not start on pod %s within %s (%s)", t.Name, StartGrace, reason)
			}
			allDone = false
		}
	}
	return allDone, nil
}
