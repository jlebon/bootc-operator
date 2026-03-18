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
	"testing"
	"time"
)

func TestToBootEntryStatusNil(t *testing.T) {
	s := ToBootEntryStatus(nil)
	if s.Image != "" || s.Version != "" || s.SoftRebootCapable {
		t.Errorf("nil entry should produce zero-value status, got %+v", s)
	}
}

func TestToBootEntryStatusFull(t *testing.T) {
	ts := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	entry := &BootEntry{
		Image: &ImageStatus{
			Image: ImageReference{
				Image:     "quay.io/example/myimage:latest",
				Transport: "registry",
			},
			Version:      "v1.2.3",
			Timestamp:    &ts,
			ImageDigest:  "sha256:abcdef1234567890",
			Architecture: "x86_64",
		},
		SoftRebootCapable: true,
		DownloadOnly:      true,
	}

	s := ToBootEntryStatus(entry)

	// The image ref should be repo@digest (tag stripped).
	want := "quay.io/example/myimage@sha256:abcdef1234567890"
	if s.Image != want {
		t.Errorf("Image = %q, want %q", s.Image, want)
	}
	if s.Version != "v1.2.3" {
		t.Errorf("Version = %q, want %q", s.Version, "v1.2.3")
	}
	if s.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
	if !s.Timestamp.Time.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", s.Timestamp.Time, ts)
	}
	if !s.SoftRebootCapable {
		t.Error("SoftRebootCapable should be true")
	}
}

func TestToBootEntryStatusNoDigest(t *testing.T) {
	entry := &BootEntry{
		Image: &ImageStatus{
			Image: ImageReference{
				Image:     "quay.io/example/myimage:latest",
				Transport: "registry",
			},
		},
	}

	s := ToBootEntryStatus(entry)
	if s.Image != "quay.io/example/myimage:latest" {
		t.Errorf("Image = %q, want unmodified ref when no digest", s.Image)
	}
}

func TestToBootEntryStatusNoImage(t *testing.T) {
	entry := &BootEntry{
		SoftRebootCapable: true,
	}

	s := ToBootEntryStatus(entry)
	if s.Image != "" {
		t.Errorf("Image = %q, want empty when no image status", s.Image)
	}
	if !s.SoftRebootCapable {
		t.Error("SoftRebootCapable should still be set")
	}
}

func TestToBootcNodeStatus(t *testing.T) {
	host := &Host{
		Status: HostStatus{
			Booted: &BootEntry{
				Image: &ImageStatus{
					Image:       ImageReference{Image: "quay.io/test/booted:v1", Transport: "registry"},
					ImageDigest: "sha256:1111",
				},
			},
			Staged: &BootEntry{
				Image: &ImageStatus{
					Image:       ImageReference{Image: "quay.io/test/staged:v2", Transport: "registry"},
					ImageDigest: "sha256:2222",
				},
				SoftRebootCapable: true,
			},
			Rollback: &BootEntry{
				Image: &ImageStatus{
					Image:       ImageReference{Image: "quay.io/test/rollback:v0", Transport: "registry"},
					ImageDigest: "sha256:0000",
				},
			},
		},
	}

	status := ToBootcNodeStatus(host)

	if status.Booted.Image != "quay.io/test/booted@sha256:1111" {
		t.Errorf("Booted.Image = %q", status.Booted.Image)
	}
	if status.Staged.Image != "quay.io/test/staged@sha256:2222" {
		t.Errorf("Staged.Image = %q", status.Staged.Image)
	}
	if !status.Staged.SoftRebootCapable {
		t.Error("Staged.SoftRebootCapable should be true")
	}
	if status.Rollback.Image != "quay.io/test/rollback@sha256:0000" {
		t.Errorf("Rollback.Image = %q", status.Rollback.Image)
	}
}

func TestToBootcNodeStatusNil(t *testing.T) {
	status := ToBootcNodeStatus(nil)
	if status.Booted.Image != "" || status.Staged.Image != "" || status.Rollback.Image != "" {
		t.Errorf("nil host should produce zero-value status, got %+v", status)
	}
}

func TestStripTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"quay.io/example/image:latest", "quay.io/example/image"},
		{"quay.io/example/image", "quay.io/example/image"},
		{"quay.io/example/image@sha256:abc", "quay.io/example/image@sha256:abc"},
		{"quay.io/example/image:latest@sha256:abc", "quay.io/example/image:latest@sha256:abc"},
		{"localhost:5000/image:tag", "localhost:5000/image"},
		{"image", "image"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripTag(tt.input)
			if got != tt.want {
				t.Errorf("stripTag(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatImageRef(t *testing.T) {
	tests := []struct {
		name string
		img  *ImageStatus
		want string
	}{
		{
			name: "nil",
			img:  nil,
			want: "",
		},
		{
			name: "with tag and digest",
			img: &ImageStatus{
				Image:       ImageReference{Image: "quay.io/example/image:latest"},
				ImageDigest: "sha256:abcd",
			},
			want: "quay.io/example/image@sha256:abcd",
		},
		{
			name: "no digest",
			img: &ImageStatus{
				Image: ImageReference{Image: "quay.io/example/image:latest"},
			},
			want: "quay.io/example/image:latest",
		},
		{
			name: "already digest ref",
			img: &ImageStatus{
				Image:       ImageReference{Image: "quay.io/example/image@sha256:existing"},
				ImageDigest: "sha256:existing",
			},
			// When the image ref already has a digest, return it as-is.
			want: "quay.io/example/image@sha256:existing",
		},
		{
			name: "no tag no digest",
			img: &ImageStatus{
				Image: ImageReference{Image: "quay.io/example/image"},
			},
			want: "quay.io/example/image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatImageRef(tt.img)
			if got != tt.want {
				t.Errorf("formatImageRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHelperFunctions(t *testing.T) {
	t.Run("HasStagedImage", func(t *testing.T) {
		if HasStagedImage(nil) {
			t.Error("nil host should not have staged image")
		}
		if HasStagedImage(&Host{}) {
			t.Error("empty host should not have staged image")
		}
		if HasStagedImage(&Host{Status: HostStatus{Staged: &BootEntry{}}}) {
			t.Error("staged entry without image should not have staged image")
		}
		host := &Host{
			Status: HostStatus{
				Staged: &BootEntry{
					Image: &ImageStatus{
						Image:       ImageReference{Image: "test"},
						ImageDigest: "sha256:abc",
					},
				},
			},
		}
		if !HasStagedImage(host) {
			t.Error("host with staged image should have staged image")
		}
	})

	t.Run("StagedImageRef", func(t *testing.T) {
		if ref := StagedImageRef(nil); ref != "" {
			t.Errorf("nil host should return empty, got %q", ref)
		}
		host := &Host{
			Status: HostStatus{
				Staged: &BootEntry{
					Image: &ImageStatus{
						Image:       ImageReference{Image: "quay.io/test:v1"},
						ImageDigest: "sha256:abc",
					},
				},
			},
		}
		want := "quay.io/test@sha256:abc"
		if got := StagedImageRef(host); got != want {
			t.Errorf("StagedImageRef() = %q, want %q", got, want)
		}
	})

	t.Run("BootedImageRef", func(t *testing.T) {
		if ref := BootedImageRef(nil); ref != "" {
			t.Errorf("nil host should return empty, got %q", ref)
		}
		host := &Host{
			Status: HostStatus{
				Booted: &BootEntry{
					Image: &ImageStatus{
						Image:       ImageReference{Image: "quay.io/test:v1"},
						ImageDigest: "sha256:def",
					},
				},
			},
		}
		want := "quay.io/test@sha256:def"
		if got := BootedImageRef(host); got != want {
			t.Errorf("BootedImageRef() = %q, want %q", got, want)
		}
	})

	t.Run("IsDownloadOnly", func(t *testing.T) {
		if IsDownloadOnly(nil) {
			t.Error("nil host should not be download-only")
		}
		if IsDownloadOnly(&Host{}) {
			t.Error("empty host should not be download-only")
		}
		host := &Host{
			Status: HostStatus{
				Staged: &BootEntry{DownloadOnly: true},
			},
		}
		if !IsDownloadOnly(host) {
			t.Error("staged with downloadOnly=true should return true")
		}
		host.Status.Staged.DownloadOnly = false
		if IsDownloadOnly(host) {
			t.Error("staged with downloadOnly=false should return false")
		}
	})
}
