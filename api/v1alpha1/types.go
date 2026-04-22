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

import "k8s.io/apimachinery/pkg/util/intstr"

// +kubebuilder:validation:Enum=SoftReboot;Reboot
type RebootPolicy string

const (
	RebootPolicySoftReboot RebootPolicy = "SoftReboot"
	RebootPolicyReboot     RebootPolicy = "Reboot"
)

// +kubebuilder:validation:Enum=Staged;Booted
type DesiredImageState string

const (
	DesiredImageStateStaged DesiredImageState = "Staged"
	DesiredImageStateBooted DesiredImageState = "Booted"
)

type ImagePullSecretReference struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
}

type RolloutConfig struct {
	// +kubebuilder:default=1
	// +kubebuilder:validation:XIntOrString
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
	// +optional
	Paused bool `json:"paused,omitempty"`
}

type DisruptionConfig struct {
	// +kubebuilder:default=Reboot
	RebootPolicy RebootPolicy `json:"rebootPolicy,omitempty"`
}

type BootEntryStatus struct {
	Image             string `json:"image,omitempty"`
	ImageDigest       string `json:"imageDigest,omitempty"`
	Version           string `json:"version,omitempty"`
	Timestamp         string `json:"timestamp,omitempty"`
	Architecture      string `json:"architecture,omitempty"`
	SoftRebootCapable bool   `json:"softRebootCapable,omitempty"`
	Incompatible      bool   `json:"incompatible,omitempty"`
}
