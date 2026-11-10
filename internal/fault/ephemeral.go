package fault

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// injectEphemeral adds the container to every target pod that still exists
// with the targeted UID and does not have a container of that name yet, which
// makes it idempotent. Pods cannot lose an ephemeral container, so a container
// added here lives until the pod does.
func injectEphemeral(ctx context.Context, c client.Client, s Spec, ec corev1.EphemeralContainerCommon) error {
	for _, t := range s.Targets {
		var pod corev1.Pod
		err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: t.Name}, &pod)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading pod %s/%s: %w", s.Namespace, t.Name, err)
		}
		if string(pod.UID) != t.UID || hasEphemeral(&pod, ec.Name) {
			continue // replaced by another pod, or already injected
		}
		pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, corev1.EphemeralContainer{EphemeralContainerCommon: ec})
		if err := c.SubResource("ephemeralcontainers").Update(ctx, &pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("adding container %s to pod %s/%s: %w", ec.Name, s.Namespace, t.Name, err)
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

// ephemeralDone is true once the named container has terminated on every
// target pod. A target pod that no longer exists counts as done. A container
// that has not started within StartGrace is an error: without it the fault
// would never have happened and the run would report a meaningless success.
//
// kind names the fault for messages. exitErr, when set, turns a terminated
// state into an error (for example the exit code of a failed tc).
func ephemeralDone(ctx context.Context, c client.Client, s Spec, name, kind string, injectedAt, now time.Time,
	exitErr func(pod string, t *corev1.ContainerStateTerminated) error) (bool, error) {
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
			if exitErr != nil {
				if err := exitErr(t.Name, st.State.Terminated); err != nil {
					return false, err
				}
			}
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
				return false, fmt.Errorf("%s container did not start on pod %s within %s (%s)", kind, t.Name, StartGrace, reason)
			}
			allDone = false
		}
	}
	return allDone, nil
}
