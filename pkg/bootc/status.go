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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jlebon/bootc-operator/api/v1alpha1"
)

// ToBootEntryStatus converts a bootc BootEntry to our CRD's
// BootEntryStatus. Returns a zero-value BootEntryStatus if entry is nil.
func ToBootEntryStatus(entry *BootEntry) v1alpha1.BootEntryStatus {
	if entry == nil || entry.Image == nil {
		return v1alpha1.BootEntryStatus{}
	}

	status := v1alpha1.BootEntryStatus{
		Image:             ImageRefWithDigest(entry.Image),
		Version:           entry.Image.Version,
		SoftRebootCapable: entry.SoftRebootCapable,
	}

	if entry.Image.Timestamp != nil {
		status.Timestamp = metav1.NewTime(*entry.Image.Timestamp)
	}

	return status
}

// ToBootcNodeStatus converts a bootc Host to a partial BootcNodeStatus.
// It populates the bootc-derived fields (trackedImage, bootedDigest,
// booted, staged, rollback) but does NOT set phase, message, or
// conditions -- those are managed by the daemon's state machine.
func ToBootcNodeStatus(host *Host) v1alpha1.BootcNodeStatus {
	status := v1alpha1.BootcNodeStatus{
		Booted:   ToBootEntryStatus(host.Status.Booted),
		Staged:   ToBootEntryStatus(host.Status.Staged),
		Rollback: ToBootEntryStatus(host.Status.Rollback),
	}

	// trackedImage comes from the host spec (what bootc is tracking).
	if host.Spec.Image != nil {
		status.TrackedImage = host.Spec.Image.Image
	}

	// bootedDigest is the resolved digest of the booted image.
	if host.Status.Booted != nil && host.Status.Booted.Image != nil {
		status.BootedDigest = host.Status.Booted.Image.ImageDigest
	}

	return status
}

// ImageRefWithDigest returns a fully qualified image reference with
// digest (e.g. "quay.io/example/my-image@sha256:abc123...") from an
// ImageStatus.
func ImageRefWithDigest(imgStatus *ImageStatus) string {
	if imgStatus == nil {
		return ""
	}
	if imgStatus.ImageDigest == "" {
		return imgStatus.Image.Image
	}
	return fmt.Sprintf("%s@%s", imageNameWithoutTag(imgStatus.Image.Image), imgStatus.ImageDigest)
}

// HasStagedImage returns true if the host has a staged deployment.
func HasStagedImage(host *Host) bool {
	return host.Status.Staged != nil && host.Status.Staged.Image != nil
}

// StagedImageDigest returns the digest of the staged image, or empty if
// nothing is staged.
func StagedImageDigest(host *Host) string {
	if !HasStagedImage(host) {
		return ""
	}
	return host.Status.Staged.Image.ImageDigest
}

// BootedImageDigest returns the digest of the booted image, or empty if
// status is unavailable.
func BootedImageDigest(host *Host) string {
	if host.Status.Booted == nil || host.Status.Booted.Image == nil {
		return ""
	}
	return host.Status.Booted.Image.ImageDigest
}

// BootedImageRef returns the image reference string of the booted
// image, or empty if status is unavailable.
func BootedImageRef(host *Host) string {
	if host.Status.Booted == nil || host.Status.Booted.Image == nil {
		return ""
	}
	return host.Status.Booted.Image.Image.Image
}

// TrackedImageRef returns the image reference string that bootc is
// tracking (from the host spec), or empty if not tracking any image.
func TrackedImageRef(host *Host) string {
	if host.Spec.Image == nil {
		return ""
	}
	return host.Spec.Image.Image
}

// IsDownloadOnly returns true if the staged deployment was created with
// --download-only (ephemeral, may be GC'd on unexpected reboot).
func IsDownloadOnly(host *Host) bool {
	return host.Status.Staged != nil && host.Status.Staged.DownloadOnly
}

// imageNameWithoutTag strips the tag or digest from a container image
// reference, returning just the name (e.g. "quay.io/example/my-image:latest"
// -> "quay.io/example/my-image"). If no tag or digest is present, returns
// the image as-is.
func imageNameWithoutTag(image string) string {
	// First check for digest separator (@). If present, strip everything
	// from '@' onward. This must be checked before ':' because digests
	// contain colons (e.g. "@sha256:abc123").
	for i := range image {
		if image[i] == '@' {
			return image[:i]
		}
	}

	// No digest found; look for a tag separator (:) scanning from the
	// right. A colon is a tag separator only if there is no '/' after
	// it (otherwise it's a port, e.g. "host:5000/repo").
	for i := len(image) - 1; i >= 0; i-- {
		switch image[i] {
		case ':':
			// No '/' after this ':' means it's a tag.
			hasSlashAfter := false
			for j := i + 1; j < len(image); j++ {
				if image[j] == '/' {
					hasSlashAfter = true
					break
				}
			}
			if !hasSlashAfter {
				return image[:i]
			}
		case '/':
			// Reached a path separator before finding a tag.
			return image
		}
	}
	return image
}
