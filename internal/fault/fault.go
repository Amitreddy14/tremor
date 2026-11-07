// Package fault implements the failure injectors behind ChaosExperiment.
package fault

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Target is a pod chosen for disturbance. UID protects against acting on a
// different pod that later reuses the same name.
type Target struct {
	Name string
	UID  string
}

// Injector applies and reverts one fault type. Inject and Revert must be
// idempotent: the controller may call them again after a crash or conflict.
type Injector interface {
	Inject(ctx context.Context, c client.Client, namespace string, targets []Target) error
	Revert(ctx context.Context, c client.Client, namespace string, targets []Target) error
}

// For returns the injector registered for a fault type.
func For(faultType string) (Injector, bool) {
	switch faultType {
	case "PodKill":
		return PodKill{}, true
	default:
		return nil, false
	}
}
