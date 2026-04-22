/*
Copyright 2026.

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

type BootcNodeSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	DesiredImage string `json:"desiredImage"`
	// +kubebuilder:default=Staged
	DesiredImageState DesiredImageState `json:"desiredImageState,omitempty"`
	// +optional
	PullSecretRef *ImagePullSecretReference `json:"pullSecretRef,omitempty"`
	// +optional
	PullSecretHash string `json:"pullSecretHash,omitempty"`
}

type BootcNodeStatus struct {
	// +optional
	Booted *BootEntryStatus `json:"booted,omitempty"`
	// +optional
	Staged *BootEntryStatus `json:"staged,omitempty"`
	// +optional
	Rollback *BootEntryStatus `json:"rollback,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Desired Image",type=string,JSONPath=`.spec.desiredImage`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.spec.desiredImageState`
// +kubebuilder:printcolumn:name="Booted",type=string,JSONPath=`.status.booted.imageDigest`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type BootcNode struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec BootcNodeSpec `json:"spec"`

	// +optional
	Status BootcNodeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

type BootcNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BootcNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BootcNode{}, &BootcNodeList{})
}
