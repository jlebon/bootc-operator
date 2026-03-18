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

// Package bootc provides a Go client for the bootc CLI. It wraps bootc
// commands (status, switch, upgrade, rollback) and parses the
// org.containers.bootc/v1 BootcHost JSON schema that `bootc status --json`
// produces.
package bootc

import "time"

// Host is the top-level structure returned by `bootc status --json`.
// It follows the org.containers.bootc/v1 BootcHost schema.
type Host struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       HostSpec   `json:"spec"`
	Status     HostStatus `json:"status"`
}

// ObjectMeta is a minimal Kubernetes-style metadata subset used by bootc.
type ObjectMeta struct {
	Name string `json:"name,omitempty"`
}

// HostSpec is the desired state portion of the bootc host.
type HostSpec struct {
	Image     *ImageReference `json:"image"`
	BootOrder string          `json:"bootOrder,omitempty"`
}

// ImageReference identifies a container image with its transport and
// optional signature verification.
type ImageReference struct {
	Image     string `json:"image"`
	Transport string `json:"transport"`
	Signature any    `json:"signature,omitempty"`
}

// HostStatus is the observed state of the bootc host.
type HostStatus struct {
	Staged           *BootEntry  `json:"staged"`
	Booted           *BootEntry  `json:"booted"`
	Rollback         *BootEntry  `json:"rollback"`
	RollbackQueued   bool        `json:"rollbackQueued,omitempty"`
	Type             *string     `json:"type,omitempty"`
	OtherDeployments []BootEntry `json:"otherDeployments,omitempty"`
}

// BootEntry represents a single bootc deployment slot (staged, booted, or
// rollback).
type BootEntry struct {
	Image             *ImageStatus `json:"image"`
	CachedUpdate      *ImageStatus `json:"cachedUpdate,omitempty"`
	Incompatible      bool         `json:"incompatible,omitempty"`
	Pinned            bool         `json:"pinned,omitempty"`
	SoftRebootCapable bool         `json:"softRebootCapable,omitempty"`
	DownloadOnly      bool         `json:"downloadOnly,omitempty"`
	Store             *string      `json:"store,omitempty"`
	Ostree            *BootOstree  `json:"ostree,omitempty"`
}

// ImageStatus describes the image running in a deployment slot.
type ImageStatus struct {
	Image        ImageReference `json:"image"`
	Version      string         `json:"version,omitempty"`
	Timestamp    *time.Time     `json:"timestamp"`
	ImageDigest  string         `json:"imageDigest"`
	Architecture string         `json:"architecture,omitempty"`
}

// BootOstree holds ostree-specific deployment metadata.
type BootOstree struct {
	Stateroot    string `json:"stateroot"`
	Checksum     string `json:"checksum"`
	DeploySerial uint32 `json:"deploySerial"`
}
