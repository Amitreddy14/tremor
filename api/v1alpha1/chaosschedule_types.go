package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScheduleLabel is set on every experiment a ChaosSchedule creates and holds
// the schedule's name.
const ScheduleLabel = "tremor.dev/schedule"

// ConcurrencyPolicy says what a schedule does when its previous experiment is
// still running at the next run time.
// +kubebuilder:validation:Enum=Forbid;Allow
type ConcurrencyPolicy string

const (
	// ConcurrencyForbid skips the new run while the previous one is active.
	ConcurrencyForbid ConcurrencyPolicy = "Forbid"
	// ConcurrencyAllow starts the new run regardless.
	ConcurrencyAllow ConcurrencyPolicy = "Allow"
)

// ChaosExperimentTemplate is the experiment a schedule stamps out.
type ChaosExperimentTemplate struct {
	// Labels are added to each created experiment.
	// +optional
	Labels map[string]string   `json:"labels,omitempty"`
	Spec   ChaosExperimentSpec `json:"spec"`
}

// ChaosScheduleSpec defines when experiments run.
type ChaosScheduleSpec struct {
	// Schedule is a five-field cron expression in UTC, or @hourly, @daily,
	// @weekly, @monthly, @yearly.
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`
	// Suspend stops new runs without deleting the schedule.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
	// ConcurrencyPolicy defaults to Forbid.
	// +kubebuilder:default=Forbid
	// +optional
	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`
	// StartingDeadlineSeconds is how late a run may start, for example after
	// the operator was down. Later runs are skipped (default 300).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=300
	// +optional
	StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`
	// SuccessfulRunsHistoryLimit is how many finished experiments to keep
	// (default 3).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	// +optional
	SuccessfulRunsHistoryLimit *int32 `json:"successfulRunsHistoryLimit,omitempty"`
	// FailedRunsHistoryLimit is how many failed or aborted experiments to keep
	// (default 3).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	// +optional
	FailedRunsHistoryLimit *int32 `json:"failedRunsHistoryLimit,omitempty"`
	// Template is the experiment to run.
	Template ChaosExperimentTemplate `json:"template"`
}

// ChaosScheduleStatus is the observed state of a schedule.
type ChaosScheduleStatus struct {
	// LastScheduleTime is the run time of the latest experiment created.
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	// NextScheduleTime is the next planned run (empty when suspended).
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`
	// LastExperiment is the name of the latest experiment created.
	LastExperiment string `json:"lastExperiment,omitempty"`
	// Active lists the schedule's experiments that have not finished.
	Active []string `json:"active,omitempty"`
	// Message explains the latest decision, for example a skipped run.
	Message string `json:"message,omitempty"`
	// ObservedGeneration is the spec generation last handled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cxs
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspend",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Last",type=date,JSONPath=`.status.lastScheduleTime`
// +kubebuilder:printcolumn:name="Next",type=date,JSONPath=`.status.nextScheduleTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ChaosSchedule runs a ChaosExperiment on a cron schedule.
type ChaosSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChaosScheduleSpec   `json:"spec,omitempty"`
	Status ChaosScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChaosScheduleList contains a list of ChaosSchedule.
type ChaosScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChaosSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ChaosSchedule{}, &ChaosScheduleList{})
}
