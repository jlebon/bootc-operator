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

package bootc

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

// ToBootEntryStatus converts a bootc BootEntry to our API's
// BootEntryStatus type. Returns a zero-value BootEntryStatus if the
// entry is nil.
func ToBootEntryStatus(entry *BootEntry) v1alpha1.BootEntryStatus {
	if entry == nil {
		return v1alpha1.BootEntryStatus{}
	}

	s := v1alpha1.BootEntryStatus{
		SoftRebootCapable: entry.SoftRebootCapable,
	}

	if entry.Image != nil {
		s.Image = formatImageRef(entry.Image)
		s.ImageDigest = entry.Image.ImageDigest
		s.Version = entry.Image.Version
		if entry.Image.Timestamp != nil {
			s.Timestamp = metav1.NewTime(*entry.Image.Timestamp)
		}
	}
	return s
}

// ToBootcNodeStatus maps a full bootc Host to our API's BootcNodeStatus,
// populating the tracked image, booted, staged, and rollback entries.
func ToBootcNodeStatus(host *Host) v1alpha1.BootcNodeStatus {
	if host == nil {
		return v1alpha1.BootcNodeStatus{}
	}
	status := v1alpha1.BootcNodeStatus{
		Booted:   ToBootEntryStatus(host.Status.Booted),
		Staged:   ToBootEntryStatus(host.Status.Staged),
		Rollback: ToBootEntryStatus(host.Status.Rollback),
	}
	if host.Spec.Image != nil {
		status.TrackedImage = host.Spec.Image.Image
	}
	return status
}

// formatImageRef builds a fully qualified image reference from an
// ImageStatus. When the image has a digest, the reference is in
// "repo@digest" format (tag stripped). Otherwise, just the image name
// is returned. If the reference already contains a digest (has '@'),
// it is returned unchanged.
func formatImageRef(img *ImageStatus) string {
	if img == nil {
		return ""
	}
	ref := img.Image.Image
	if img.ImageDigest == "" {
		return ref
	}
	// If the reference already contains a digest, return it as-is.
	if strings.Contains(ref, "@") {
		return ref
	}
	return fmt.Sprintf("%s@%s", stripTag(ref), img.ImageDigest)
}

// stripTag removes a tag from an image reference, leaving just the
// repository. For example, "quay.io/example/image:latest" becomes
// "quay.io/example/image". If there is no tag (or the reference is
// already a digest reference containing '@'), it is returned unchanged.
func stripTag(ref string) string {
	// If the reference contains '@', it is a digest reference; return
	// unchanged to avoid stripping digest internals (e.g. "sha256:...").
	if strings.Contains(ref, "@") {
		return ref
	}
	// Scan from the end for a ':' that separates repository from tag.
	// Stop at '/' to avoid matching port numbers (e.g. "localhost:5000").
	for i := len(ref) - 1; i >= 0; i-- {
		switch ref[i] {
		case ':':
			return ref[:i]
		case '/':
			return ref
		}
	}
	return ref
}

// HasStagedImage returns true if the host has a staged deployment.
func HasStagedImage(host *Host) bool {
	return host != nil && host.Status.Staged != nil && host.Status.Staged.Image != nil
}

// StagedImageRef returns the fully qualified image reference for the
// staged deployment, or empty string if nothing is staged.
func StagedImageRef(host *Host) string {
	if !HasStagedImage(host) {
		return ""
	}
	return formatImageRef(host.Status.Staged.Image)
}

// BootedImageRef returns the fully qualified image reference for the
// currently booted deployment, or empty string if unavailable.
func BootedImageRef(host *Host) string {
	if host == nil || host.Status.Booted == nil || host.Status.Booted.Image == nil {
		return ""
	}
	return formatImageRef(host.Status.Booted.Image)
}

// IsDownloadOnly returns true if the staged deployment was created with
// --download-only and may be garbage collected on unexpected reboot.
func IsDownloadOnly(host *Host) bool {
	return host != nil && host.Status.Staged != nil && host.Status.Staged.DownloadOnly
}
