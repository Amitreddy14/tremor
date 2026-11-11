package fault

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Amitreddy14/tremor/internal/fault/netem"
)

// DefaultNetemImage carries tc and ip (see Dockerfile.netem). BusyBox's tc has
// no netem support, so this fault needs its own small image.
const DefaultNetemImage = "tremor-netem:dev"

// NetworkLatency delays the traffic leaving the target pods by adding an
// ephemeral container that installs a netem qdisc for the experiment's duration.
type NetworkLatency struct{ Image string }

// NetworkLoss drops a share of the traffic leaving the target pods the same way.
type NetworkLoss struct{ Image string }

// Inject adds the netem container to every target pod.
func (f NetworkLatency) Inject(ctx context.Context, c client.Client, s Spec) error {
	return injectNetem(ctx, c, s, f.Image, netem.LatencyScript(s.Latency, s.Jitter, s.Duration))
}

// Inject adds the netem container to every target pod.
func (f NetworkLoss) Inject(ctx context.Context, c client.Client, s Spec) error {
	return injectNetem(ctx, c, s, f.Image, netem.LossScript(s.LossPercent, s.Duration))
}

// Revert removes the fault early, which matters when an experiment fails or is
// aborted while the fault is active. A fault that ended by itself needs nothing.
func (f NetworkLatency) Revert(ctx context.Context, c client.Client, s Spec) error {
	return revertNetem(ctx, c, s, f.Image)
}

// Revert removes the fault early, as for NetworkLatency.
func (f NetworkLoss) Revert(ctx context.Context, c client.Client, s Spec) error {
	return revertNetem(ctx, c, s, f.Image)
}

// Done reports whether the netem containers have finished. A container that
// exited with netem.ExitApplyFailed means the fault never took effect.
func (NetworkLatency) Done(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	return netemDone(ctx, c, s, injectedAt, now)
}

// Done reports whether the netem containers have finished.
func (NetworkLoss) Done(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	return netemDone(ctx, c, s, injectedAt, now)
}

// netemContainer builds a container that runs a script with NET_ADMIN, the only
// capability tc needs. It runs as root because the capability is useless to an
// unprivileged user in a container.
func netemContainer(name, image, script string) corev1.EphemeralContainerCommon {
	no := false
	var root int64
	return corev1.EphemeralContainerCommon{
		Name:            name,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-c", script},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &no,
			RunAsNonRoot:             &no,
			RunAsUser:                &root,
			Capabilities: &corev1.Capabilities{
				Add:  []corev1.Capability{"NET_ADMIN"},
				Drop: []corev1.Capability{"ALL"},
			},
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
}

func injectNetem(ctx context.Context, c client.Client, s Spec, image, script string) error {
	return injectEphemeral(ctx, c, s, netemContainer(netem.ContainerName(s.Holder), image, script))
}

// revertNetem starts a cleanup container on each target whose netem container
// is still active. The apply script notices the removed qdisc and exits.
func revertNetem(ctx context.Context, c client.Client, s Spec, image string) error {
	name := netem.ContainerName(s.Holder)
	for _, t := range s.Targets {
		var pod corev1.Pod
		err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: t.Name}, &pod)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("reading pod %s/%s: %w", s.Namespace, t.Name, err)
		}
		if string(pod.UID) != t.UID || !hasEphemeral(&pod, name) {
			continue
		}
		if st := ephemeralStatus(&pod, name); st != nil && st.State.Terminated != nil {
			continue // already gone, and it removed the qdisc itself
		}
		cleanup := netemContainer(netem.CleanupName(s.Holder), image, netem.CleanupScript())
		if err := injectEphemeral(ctx, c, Spec{Namespace: s.Namespace, Targets: []Target{t}, Holder: s.Holder}, cleanup); err != nil {
			return err
		}
	}
	return nil
}

func netemDone(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	return ephemeralDone(ctx, c, s, netem.ContainerName(s.Holder), "netem", injectedAt, now,
		func(pod string, t *corev1.ContainerStateTerminated) error {
			if t.ExitCode == netem.ExitApplyFailed {
				return fmt.Errorf("tc could not apply the network fault on pod %s (the node kernel needs the sch_netem module): %s", pod, t.Message)
			}
			return nil
		})
}

func (NetworkLatency) EndedAt(ctx context.Context, c client.Client, s Spec) (time.Time, bool) {
	return ephemeralEndedAt(ctx, c, s, netem.ContainerName(s.Holder))
}

func (NetworkLoss) EndedAt(ctx context.Context, c client.Client, s Spec) (time.Time, bool) {
	return ephemeralEndedAt(ctx, c, s, netem.ContainerName(s.Holder))
}
