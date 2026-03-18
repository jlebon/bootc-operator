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

// BootcNodeDesiredPhase describes the phase that the daemon should work towards.
// +kubebuilder:validation:Enum=Staged;Rebooting;RollingBack
type BootcNodeDesiredPhase string

const (
	// BootcNodeDesiredPhaseStaged instructs the daemon to download and stage
	// the desired image without rebooting.
	BootcNodeDesiredPhaseStaged BootcNodeDesiredPhase = "Staged"

	// BootcNodeDesiredPhaseRebooting instructs the daemon to apply the staged
	// image and reboot (soft reboot if possible, full otherwise).
	BootcNodeDesiredPhaseRebooting BootcNodeDesiredPhase = "Rebooting"

	// BootcNodeDesiredPhaseRollingBack instructs the daemon to rollback to
	// the previous image and reboot.
	BootcNodeDesiredPhaseRollingBack BootcNodeDesiredPhase = "RollingBack"
)

// BootcNodePhase describes the current phase of the daemon on a node.
// +kubebuilder:validation:Enum=Ready;Staging;Staged;Rebooting;RollingBack;Error
type BootcNodePhase string

const (
	// BootcNodePhaseReady indicates the node is running the desired image.
	BootcNodePhaseReady BootcNodePhase = "Ready"

	// BootcNodePhaseStaging indicates the daemon is downloading/staging
	// the desired image.
	BootcNodePhaseStaging BootcNodePhase = "Staging"

	// BootcNodePhaseStaged indicates the desired image is staged and
	// ready to be applied on reboot.
	BootcNodePhaseStaged BootcNodePhase = "Staged"

	// BootcNodePhaseRebooting indicates the daemon is applying the staged
	// image and rebooting.
	BootcNodePhaseRebooting BootcNodePhase = "Rebooting"

	// BootcNodePhaseRollingBack indicates the daemon is rolling back to
	// the previous image.
	BootcNodePhaseRollingBack BootcNodePhase = "RollingBack"

	// BootcNodePhaseError indicates an error occurred during the update.
	BootcNodePhaseError BootcNodePhase = "Error"
)

// BootcNodeSpec defines the desired bootc state for a single node.
// All fields are optional -- when empty, the daemon just reports status
// without taking any action.
type BootcNodeSpec struct {
	// desiredImage is the container image digest that this node should be
	// running. Always a fully qualified digest reference
	// (e.g. "quay.io/example/my-image@sha256:abc123..."). Set by the
	// operator when a pool claims this node. Empty when no pool has
	// claimed the node.
	// +optional
	DesiredImage string `json:"desiredImage,omitempty"`

	// desiredPhase is the phase that the daemon should work towards.
	// Must be one of "Staged", "Rebooting", or "RollingBack".
	// Set by the operator to drive the daemon through the rollout
	// lifecycle. Empty when no pool has claimed the node.
	// +optional
	DesiredPhase BootcNodeDesiredPhase `json:"desiredPhase,omitempty"`

	// rebootPolicy determines how the daemon reboots this node.
	// Propagated from the owning BootcNodePool's disruption config.
	// Must be one of "Auto", "Full", or "Never". When empty, defaults
	// to "Auto".
	// +optional
	RebootPolicy RebootPolicy `json:"rebootPolicy,omitempty"`
}

// BootEntryStatus describes a single bootc deployment slot (booted,
// staged, or rollback), as reported by `bootc status --json`.
type BootEntryStatus struct {
	// image is the fully qualified container image reference for this
	// deployment (e.g. "quay.io/example/my-image@sha256:abc123...").
	// +optional
	Image string `json:"image,omitempty"`

	// version is the image version label, if available.
	// +optional
	Version string `json:"version,omitempty"`

	// timestamp is when this deployment's image was built.
	// +optional
	Timestamp metav1.Time `json:"timestamp,omitempty"`

	// softRebootCapable indicates whether this deployment can be reached
	// via a soft reboot (userspace-only restart) rather than a full
	// hardware reboot. This is true when the kernel and initramfs are
	// unchanged relative to the currently booted deployment.
	// +optional
	SoftRebootCapable bool `json:"softRebootCapable,omitempty"`
}

// BootcNodeStatus reflects the observed bootc state on a node, as
// reported by the daemon from `bootc status --json`.
type BootcNodeStatus struct {
	// booted is the bootc deployment that the node is currently running.
	// +optional
	Booted BootEntryStatus `json:"booted,omitempty,omitzero"`

	// staged is the bootc deployment that is staged for the next boot,
	// if any.
	// +optional
	Staged BootEntryStatus `json:"staged,omitempty,omitzero"`

	// rollback is the bootc deployment that the node can roll back to,
	// if any.
	// +optional
	Rollback BootEntryStatus `json:"rollback,omitempty,omitzero"`

	// phase is the daemon's current phase on this node.
	// +optional
	Phase BootcNodePhase `json:"phase,omitempty"`

	// message is a human-readable description of the current state or
	// error. Empty when no additional detail is available.
	// +optional
	Message string `json:"message,omitempty"`

	// conditions represent the latest observations of this node's bootc
	// state. Known .status.conditions.type values are: "ImageStaged"
	// and "Healthy".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BootcNode tracks the bootc state of a single cluster node. Created by
// the daemon when it detects the node is a bootc host. The daemon reports
// status; the operator sets the spec when a BootcNodePool claims the node.
// When no pool claims the node, the spec is empty and the daemon just
// reports the current bootc state.
//
// The name MUST match the Kubernetes Node name. The ownerReference
// points to the Node object (not the pool), so the BootcNode is GC'd
// when the Node is deleted.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Pool",type=string,JSONPath=".metadata.labels.bootc\\.dev/pool"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Booted",type=string,JSONPath=".status.booted.image",priority=1
// +kubebuilder:printcolumn:name="Staged",type=string,JSONPath=".status.staged.image",priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type BootcNode struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// The name must match the Kubernetes Node name.
	// The label bootc.dev/pool indicates which BootcNodePool (if any)
	// has claimed this node. Empty or absent when unassigned.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired bootc state for this node. Set by the
	// operator when a BootcNodePool claims this node. Empty when no
	// pool has claimed the node (daemon just reports status).
	// +optional
	Spec BootcNodeSpec `json:"spec,omitempty,omitzero"`

	// status reflects the observed bootc state, reported by the daemon.
	// +optional
	Status BootcNodeStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// BootcNodeList contains a list of BootcNode.
type BootcNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BootcNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BootcNode{}, &BootcNodeList{})
}
