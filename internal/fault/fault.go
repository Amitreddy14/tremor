// Package fault implements the failure injectors behind ChaosExperiment.
package fault

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultStressImage runs the stress scripts. BusyBox is small and has every
// tool the scripts need (sh, yes, sleep, kill, dd).
const DefaultStressImage = "busybox:1.36"

// Target is a pod chosen for disturbance. UID protects against acting on a
// different pod that later reuses the same name.
type Target struct {
	Name string
	UID  string
}

// Spec is everything an injector needs to know about one experiment.
type Spec struct {
	Namespace string
	Targets   []Target
	// Workload is the workload a DependencyOutage scales down.
	Workload Workload
	// Holder identifies the experiment (its UID). It marks ownership of a
	// scaled-down workload and names stress containers, which makes Inject and
	// Revert idempotent.
	Holder string

	Duration   time.Duration
	CPUWorkers int
	MemoryMiB  int
}

// Injector applies and reverts one fault type. Inject and Revert must be
// idempotent: the controller may call them again after a crash or conflict.
type Injector interface {
	Inject(ctx context.Context, c client.Client, s Spec) error
	Revert(ctx context.Context, c client.Client, s Spec) error
}

// Sustained is implemented by faults that stay active for a duration.
type Sustained interface {
	Injector
	// Done reports whether the fault has run its course and may be released.
	// An error means the fault cannot work (for example a stress container that
	// never started) and the experiment should fail.
	Done(ctx context.Context, c client.Client, s Spec, injectedAt, now time.Time) (bool, error)
}

// Options configures injectors that need operator-level settings.
type Options struct {
	// StressImage runs CPU and memory stress; DefaultStressImage when empty.
	StressImage string
}

// For returns the injector registered for a fault type.
func For(faultType string, o Options) (Injector, bool) {
	image := o.StressImage
	if image == "" {
		image = DefaultStressImage
	}
	switch faultType {
	case "PodKill":
		return PodKill{}, true
	case "DependencyOutage":
		return DependencyOutage{}, true
	case "CPUStress":
		return CPUStress{Image: image}, true
	case "MemoryStress":
		return MemoryStress{Image: image}, true
	default:
		return nil, false
	}
}
