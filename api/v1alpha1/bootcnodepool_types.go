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

type ImageSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Ref string `json:"ref"`
}

type BootcNodePoolSpec struct {
	// +kubebuilder:validation:Required
	NodeSelector *metav1.LabelSelector `json:"nodeSelector"`
	// +kubebuilder:validation:Required
	Image ImageSpec `json:"image"`
	// +optional
	Rollout RolloutConfig `json:"rollout,omitempty"`
	// +optional
	Disruption DisruptionConfig `json:"disruption,omitempty"`
	// +optional
	PullSecretRef *ImagePullSecretReference `json:"pullSecretRef,omitempty"`
}

type BootcNodePoolStatus struct {
	DeployedDigest  string `json:"deployedDigest,omitempty"`
	TargetDigest    string `json:"targetDigest,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable,omitempty"`
	NodeCount       int32  `json:"nodeCount,omitempty"`
	UpdatedCount    int32  `json:"updatedCount,omitempty"`
	UpdatingCount   int32  `json:"updatingCount,omitempty"`
	DegradedCount   int32  `json:"degradedCount,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image.ref`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.status.targetDigest`,priority=1
// +kubebuilder:printcolumn:name="Nodes",type=integer,JSONPath=`.status.nodeCount`
// +kubebuilder:printcolumn:name="Updated",type=integer,JSONPath=`.status.updatedCount`
// +kubebuilder:printcolumn:name="Updating",type=integer,JSONPath=`.status.updatingCount`
// +kubebuilder:printcolumn:name="Degraded",type=integer,JSONPath=`.status.degradedCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type BootcNodePool struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec BootcNodePoolSpec `json:"spec"`

	// +optional
	Status BootcNodePoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

type BootcNodePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BootcNodePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BootcNodePool{}, &BootcNodePoolList{})
}
