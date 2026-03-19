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

// Package bootc provides a Go wrapper for the bootc CLI. It is used by
// the daemon to execute bootc commands on the host via chroot into the
// host rootfs. The types in this file match the org.containers.bootc/v1
// BootcHost JSON schema returned by `bootc status --json`.
package bootc

import "time"

// Host is the top-level object returned by `bootc status --json`.
// Matches the org.containers.bootc/v1 BootcHost schema.
type Host struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       HostSpec   `json:"spec"`
	Status     HostStatus `json:"status"`
}

// ObjectMeta contains minimal Kubernetes-style metadata.
type ObjectMeta struct {
	Name string `json:"name"`
}

// HostSpec describes the desired state of the bootc host.
type HostSpec struct {
	Image     *ImageReference `json:"image"`
	BootOrder string          `json:"bootOrder"`
}

// HostStatus describes the observed state of the bootc host.
type HostStatus struct {
	Staged           *BootEntry  `json:"staged"`
	Booted           *BootEntry  `json:"booted"`
	Rollback         *BootEntry  `json:"rollback"`
	RollbackQueued   bool        `json:"rollbackQueued"`
	Type             *string     `json:"type"`
	OtherDeployments []BootEntry `json:"otherDeployments,omitempty"`
}

// BootEntry describes a single bootc deployment slot (booted, staged,
// or rollback).
type BootEntry struct {
	Image             *ImageStatus     `json:"image"`
	CachedUpdate      *ImageStatus     `json:"cachedUpdate"`
	Incompatible      bool             `json:"incompatible"`
	Pinned            bool             `json:"pinned"`
	SoftRebootCapable bool             `json:"softRebootCapable"`
	DownloadOnly      bool             `json:"downloadOnly"`
	Store             *string          `json:"store"`
	Ostree            *BootEntryOstree `json:"ostree"`
}

// ImageStatus describes image metadata for a deployment.
type ImageStatus struct {
	Image        ImageReference `json:"image"`
	Version      string         `json:"version,omitempty"`
	Timestamp    *time.Time     `json:"timestamp"`
	ImageDigest  string         `json:"imageDigest"`
	Architecture string         `json:"architecture,omitempty"`
}

// ImageReference describes a container image reference.
type ImageReference struct {
	Image     string `json:"image"`
	Transport string `json:"transport"`
	// Signature can be null, a string ("insecure", "containerPolicy"),
	// or an object ({"ostreeRemote": "fedora"}). We use any since we
	// don't need to inspect it.
	Signature any `json:"signature"`
}

// BootEntryOstree contains OSTree-specific deployment metadata.
type BootEntryOstree struct {
	Stateroot    string `json:"stateroot"`
	Checksum     string `json:"checksum"`
	DeploySerial uint32 `json:"deploySerial"`
}
