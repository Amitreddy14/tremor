package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FaultType names a kind of failure Tremor can inject.
// +kubebuilder:validation:Enum=PodKill
type FaultType string

const (
	// FaultPodKill force-deletes pods of the target workload.
	FaultPodKill FaultType = "PodKill"
)

// Phase is the lifecycle phase of an experiment.
type Phase string

const (
	PhasePending       Phase = "Pending"
	PhaseBaselineCheck Phase = "BaselineCheck"
	PhaseInjecting     Phase = "Injecting"
	PhaseRecovering    Phase = "Recovering"
	PhaseCompleted     Phase = "Completed"
	PhaseFailed        Phase = "Failed"
	PhaseAborted       Phase = "Aborted"
)

// AbortAnnotation stops a running experiment when set to "true".
const AbortAnnotation = "tremor.dev/abort"

// TargetSpec selects the pods an experiment disturbs (same namespace).
type TargetSpec struct {
	// Selector is a set of labels; pods must carry all of them.
	// +kubebuilder:validation:MinProperties=1
	Selector map[string]string `json:"selector"`
}

// FaultParams holds fault-specific parameters.
type FaultParams struct {
	// Count is how many pods to disturb (default 1), subject to safety limits.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Count int32 `json:"count,omitempty"`
}

// FaultSpec describes the failure to inject.
type FaultSpec struct {
	Type FaultType `json:"type"`
	// +optional
	Params FaultParams `json:"params,omitempty"`
}

// SafetySpec bounds the blast radius of an experiment.
type SafetySpec struct {
	// DryRun plans the experiment and reports targets without injecting.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
	// MaxAffectedPercent caps the share of target pods affected.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=50
	// +optional
	MaxAffectedPercent int32 `json:"maxAffectedPercent,omitempty"`
	// AllowTotalOutage lifts the rule that always spares at least one pod.
	// +optional
	AllowTotalOutage bool `json:"allowTotalOutage,omitempty"`
}

// ChaosExperimentSpec defines the desired experiment.
type ChaosExperimentSpec struct {
	Target TargetSpec `json:"target"`
	Fault  FaultSpec  `json:"fault"`
	// +optional
	Safety SafetySpec `json:"safety,omitempty"`
	// RecoveryTimeout is how long to wait for pods to recover (default 120s).
	// +optional
	RecoveryTimeout *metav1.Duration `json:"recoveryTimeout,omitempty"`
}

// AffectedPod identifies a pod chosen as a fault target. The UID guards
// against acting on a replacement pod that reuses the same name.
type AffectedPod struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// ChaosExperimentStatus is the observed state of an experiment.
type ChaosExperimentStatus struct {
	Phase   Phase  `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`

	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	StartedAt   *metav1.MicroTime `json:"startedAt,omitempty"`
	InjectedAt  *metav1.MicroTime `json:"injectedAt,omitempty"`
	RecoveredAt *metav1.MicroTime `json:"recoveredAt,omitempty"`

	// BaselineReadyPods is the ready pod count before injection.
	BaselineReadyPods int32 `json:"baselineReadyPods,omitempty"`
	// AffectedPods are the pods chosen as fault targets.
	AffectedPods []AffectedPod `json:"affectedPods,omitempty"`
	// PodRecoveryMillis is the time from injection until all pods were Ready again.
	PodRecoveryMillis *int64 `json:"podRecoveryMillis,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cx
// +kubebuilder:printcolumn:name="Fault",type=string,JSONPath=`.spec.fault.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Recovery(ms)",type=integer,JSONPath=`.status.podRecoveryMillis`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ChaosExperiment is one controlled failure and its measured outcome.
type ChaosExperiment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChaosExperimentSpec   `json:"spec,omitempty"`
	Status ChaosExperimentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChaosExperimentList contains a list of ChaosExperiment.
type ChaosExperimentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChaosExperiment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChaosExperiment{}, &ChaosExperimentList{})
}
