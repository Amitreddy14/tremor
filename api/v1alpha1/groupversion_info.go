// Package v1alpha1 contains the tremor.dev/v1alpha1 API types.
// +kubebuilder:object:generate=true
// +groupName=tremor.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group and version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "tremor.dev", Version: "v1alpha1"}

	// SchemeBuilder adds the Go types to a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme registers the types with a runtime.Scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
