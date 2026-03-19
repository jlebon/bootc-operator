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

// BootcNodePoolSpec defines the desired state for a bootc image rollout.
type BootcNodePoolSpec struct {
	// image is the container image reference to deploy to the targeted nodes.
	// This may be a tag (e.g. "quay.io/example/my-image:latest") or a digest
	// (e.g. "quay.io/example/my-image@sha256:abc123..."). When a tag is
	// specified, the operator periodically resolves it to a digest; a new
	// digest triggers a rollout. When a digest is specified, the operator
	// deploys exactly that image.
	// +kubebuilder:validation:MinLength=1
	// +required
	Image string `json:"image"`

	// imagePullSecret references a Secret in the operator's namespace that
	// contains credentials for pulling the image from a protected registry.
	// The Secret must be of type kubernetes.io/dockerconfigjson.
	//
	// The operator uses this secret for tag-to-digest resolution via
	// go-containerregistry. The daemon writes the secret to
	// /run/ostree/auth.json on the host before running bootc commands.
	// This is the highest-priority auth path that bootc checks (above
	// /etc/ostree/auth.json and /usr/lib/ostree/auth.json), and it is
	// ephemeral (cleared on reboot), so it does not persistently mutate
	// the host.
	//
	// When omitted, no additional credentials are provided. The node's
	// existing auth configuration (e.g. /etc/ostree/auth.json) is used
	// as-is. This is sufficient when the image is public or the node
	// already has credentials configured.
	// +optional
	ImagePullSecret ImagePullSecretReference `json:"imagePullSecret,omitempty,omitzero"`

	// nodeSelector selects the nodes that this image should be deployed to.
	// Only nodes matching all of the selector's requirements are targeted.
	// When omitted, no nodes are targeted.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// rollout configures how the image is rolled out to the targeted nodes.
	// When omitted, default rollout parameters are used.
	// +optional
	Rollout RolloutConfig `json:"rollout,omitempty,omitzero"`

	// disruption configures the disruption policy for node reboots during
	// rollout. When omitted, the operator chooses the least disruptive
	// reboot method available.
	// +optional
	Disruption DisruptionConfig `json:"disruption,omitempty,omitzero"`

	// healthCheck configures how the operator verifies node health after
	// a reboot. When omitted, defaults are used.
	// +optional
	HealthCheck HealthCheckConfig `json:"healthCheck,omitempty,omitzero"`
}

// ImagePullSecretReference references a Secret by name in the operator's
// namespace. Following OpenShift API conventions, we use a specific
// reference type rather than the generic corev1.LocalObjectReference.
type ImagePullSecretReference struct {
	// name is the metadata.name of the referenced Secret. The Secret must
	// be of type kubernetes.io/dockerconfigjson and must exist in the
	// operator's namespace.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
}

// RolloutConfig configures how a bootc image rollout proceeds.
type RolloutConfig struct {
	// maxUnavailable is the maximum number of nodes that may be unavailable
	// (cordoned/rebooting) simultaneously during the reboot phase of a
	// rollout. Must be at least 1. When omitted, defaults to 1.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxUnavailable int32 `json:"maxUnavailable,omitempty"`
}

// RebootPolicy defines how the operator handles reboots during rollout.
// +kubebuilder:validation:Enum=Auto;Full;Never
type RebootPolicy string

const (
	// RebootPolicyAuto uses soft reboot when possible (kernel and initramfs
	// unchanged), falling back to a full reboot otherwise. This minimizes
	// disruption.
	RebootPolicyAuto RebootPolicy = "Auto"

	// RebootPolicyFull always performs a full reboot, even when a soft
	// reboot would be possible.
	RebootPolicyFull RebootPolicy = "Full"

	// RebootPolicyNever stages the image but never reboots. The
	// administrator is responsible for triggering reboots externally.
	RebootPolicyNever RebootPolicy = "Never"
)

// DisruptionConfig configures the disruption policy for node reboots.
type DisruptionConfig struct {
	// rebootPolicy determines how the operator reboots nodes after staging
	// an image. Must be one of "Auto", "Full", or "Never".
	// When omitted, the operator defaults to "Auto", which uses soft reboot
	// when possible and falls back to a full reboot otherwise.
	// +kubebuilder:default=Auto
	// +optional
	RebootPolicy RebootPolicy `json:"rebootPolicy,omitempty"`
}

// HealthCheckConfig configures post-reboot health checking.
type HealthCheckConfig struct {
	// timeout is how long the operator waits for a node to become Ready
	// after a reboot before considering the update failed and triggering
	// a rollback. Must be a valid duration string (e.g. "5m", "10m").
	// When omitted, defaults to "5m".
	// +kubebuilder:default="5m"
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`
}

// BootcNodePoolPhase describes the overall phase of a node pool.
// The steady-state update cycle is: Ready -> Staging -> Rolling -> Ready.
// +kubebuilder:validation:Enum=Idle;Staging;Rolling;Ready;Degraded
type BootcNodePoolPhase string

const (
	// BootcNodePoolPhaseIdle indicates the pool has not yet been
	// reconciled or has no target nodes.
	BootcNodePoolPhaseIdle BootcNodePoolPhase = "Idle"

	// BootcNodePoolPhaseStaging indicates nodes are downloading and
	// staging the desired image (pre-reboot phase).
	BootcNodePoolPhaseStaging BootcNodePoolPhase = "Staging"

	// BootcNodePoolPhaseRolling indicates the operator is performing
	// rolling reboots to apply the staged image.
	BootcNodePoolPhaseRolling BootcNodePoolPhase = "Rolling"

	// BootcNodePoolPhaseReady indicates all target nodes are running
	// the desired image and are healthy.
	BootcNodePoolPhaseReady BootcNodePoolPhase = "Ready"

	// BootcNodePoolPhaseDegraded indicates one or more nodes failed to
	// update and the rollout has been paused.
	BootcNodePoolPhaseDegraded BootcNodePoolPhase = "Degraded"
)

// BootcNodePoolStatus reflects the observed state of a bootc node pool.
type BootcNodePoolStatus struct {
	// observedGeneration is the .metadata.generation that the controller
	// last processed. Used to detect stale status (if observedGeneration
	// < metadata.generation, the controller hasn't reconciled the latest
	// spec change yet).
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase is the overall phase of the pool. Provides an at-a-glance
	// summary of the pool's state. Derived from the conditions and node
	// counters. Must be one of "Idle", "Staging", "Rolling", "Ready",
	// or "Degraded". The steady-state update cycle is:
	// Ready -> Staging -> Rolling -> Ready.
	// +optional
	Phase BootcNodePoolPhase `json:"phase,omitempty"`

	// resolvedDigest is the digest that the operator resolved from the
	// image reference in the spec. When the spec image is already a digest,
	// this matches it directly. When it is a tag, this is the digest the
	// tag currently points to. Empty when resolution has not yet occurred.
	// +optional
	ResolvedDigest string `json:"resolvedDigest,omitempty"`

	// targetNodes is the number of nodes matching the nodeSelector.
	// +optional
	TargetNodes int32 `json:"targetNodes,omitempty"`

	// stagedNodes is the number of nodes that have staged the desired image
	// but have not yet rebooted into it.
	// +optional
	StagedNodes int32 `json:"stagedNodes,omitempty"`

	// readyNodes is the number of nodes that are running the desired image
	// and are in a Ready state.
	// +optional
	ReadyNodes int32 `json:"readyNodes,omitempty"`

	// updatingNodes is the number of nodes that are currently being
	// drained, rebooted, or verified.
	// +optional
	UpdatingNodes int32 `json:"updatingNodes,omitempty"`

	// conditions represent the latest observations of the BootcNodePool's
	// state. Known .status.conditions.type values are: "Available",
	// "Progressing", and "Degraded".
	//
	// The Progressing condition's message includes progress detail,
	// e.g. "5 of 10 nodes staged, 3 of 10 rebooted".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BootcNodePool defines a pool of nodes that should run a specific bootc
// image. The operator resolves the image reference, stages it on matching
// nodes, and orchestrates rolling reboots to apply it. Analogous to a
// MachineConfigPool in the MCO.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.readyNodes"
// +kubebuilder:printcolumn:name="Staged",type=string,JSONPath=".status.stagedNodes",priority=1
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=".status.targetNodes"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type BootcNodePool struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired bootc image and rollout parameters.
	// +required
	Spec BootcNodePoolSpec `json:"spec"`

	// status reflects the observed state of the rollout.
	// +optional
	Status BootcNodePoolStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// BootcNodePoolList contains a list of BootcNodePool.
type BootcNodePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BootcNodePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BootcNodePool{}, &BootcNodePoolList{})
}
