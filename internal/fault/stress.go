package fault

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Amitreddy14/tremor/internal/fault/stress"
)

// StartGrace is how long a stress or netem container may take to start (image
// pull included) before the experiment fails. A variable so tests can shorten it.
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
	no, yes, user := false, true, stressUser
	return injectEphemeral(ctx, c, s, corev1.EphemeralContainerCommon{
		Name:            stress.ContainerName(s.Holder),
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
	})
}

// stressDone is true once every stress container has terminated.
func stressDone(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error) {
	return ephemeralDone(ctx, c, s, stress.ContainerName(s.Holder), "stress", injectedAt, now, nil)
}

func (CPUStress) EndedAt(ctx context.Context, c client.Client, s Spec) (time.Time, bool) {
	return ephemeralEndedAt(ctx, c, s, stress.ContainerName(s.Holder))
}

func (MemoryStress) EndedAt(ctx context.Context, c client.Client, s Spec) (time.Time, bool) {
	return ephemeralEndedAt(ctx, c, s, stress.ContainerName(s.Holder))
}
