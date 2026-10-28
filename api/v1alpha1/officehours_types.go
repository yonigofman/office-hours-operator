/*
Copyright 2026 Yoni Gofman.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ExcludeAnnotation = "scheduling.yonigofman.github.io/exclude"
const ConditionReady = "Ready"

// +kubebuilder:validation:Enum=Sunday;Monday;Tuesday;Wednesday;Thursday;Friday;Saturday
type Weekday string

const (
	Sunday    Weekday = "Sunday"
	Monday    Weekday = "Monday"
	Tuesday   Weekday = "Tuesday"
	Wednesday Weekday = "Wednesday"
	Thursday  Weekday = "Thursday"
	Friday    Weekday = "Friday"
	Saturday  Weekday = "Saturday"
)

// +kubebuilder:validation:Enum=Awake;Asleep
type Phase string

const (
	PhaseAwake  Phase = "Awake"
	PhaseAsleep Phase = "Asleep"
)

type OfficeHoursSpec struct {
	// +kubebuilder:validation:XValidation:rule="(has(self.matchLabels) && size(self.matchLabels) > 0) || (has(self.matchExpressions) && size(self.matchExpressions) > 0)",message="namespaceSelector must not be empty: an empty selector matches every namespace"
	// +required
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+)*$`
	// +required
	TimeZone string `json:"timeZone"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=7
	// +listType=set
	// +required
	Workdays []Weekday `json:"workdays"`

	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	// +required
	Start string `json:"start"`

	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	// +required
	End string `json:"end"`
}

type OfficeHoursStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// +optional
	NextTransition *metav1.Time `json:"nextTransition,omitempty"`

	// +optional
	ManagedWorkloads int32 `json:"managedWorkloads"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=oh
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Next Transition",type=string,JSONPath=`.status.nextTransition`
// +kubebuilder:printcolumn:name="Workloads",type=integer,JSONPath=`.status.managedWorkloads`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OfficeHours is a schedule that keeps the selected namespaces running only
// during working hours.
type OfficeHours struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec OfficeHoursSpec `json:"spec"`

	// +optional
	Status OfficeHoursStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true
type OfficeHoursList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OfficeHours `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OfficeHours{}, &OfficeHoursList{})
}
