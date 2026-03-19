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
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jlebon/bootc-operator/api/v1alpha1"
)

// mockRunner records calls and returns configured responses.
type mockRunner struct {
	calls   []mockCall
	outputs map[string]mockResult
}

type mockCall struct {
	name string
	args []string
}

type mockResult struct {
	output []byte
	err    error
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		outputs: make(map[string]mockResult),
	}
}

func (m *mockRunner) setOutput(key string, output []byte, err error) {
	m.outputs[key] = mockResult{output: output, err: err}
}

func (m *mockRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, mockCall{name: name, args: args})
	key := name
	for _, a := range args {
		key += " " + a
	}
	if result, ok := m.outputs[key]; ok {
		return result.output, result.err
	}
	return nil, fmt.Errorf("unexpected command: %s", key)
}

const centosBootcImage = "quay.io/centos-bootc/centos-bootc:stream9"

// sampleBootedOnlyJSON is a realistic `bootc status --json` output for
// a node with only a booted deployment.
var sampleBootedOnlyJSON = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "metadata": {"name": "host"},
  "spec": {
    "image": {
      "image": "quay.io/centos-bootc/centos-bootc:stream9",
      "transport": "registry",
      "signature": null
    },
    "bootOrder": "default"
  },
  "status": {
    "staged": null,
    "booted": {
      "image": {
        "image": {
          "image": "quay.io/centos-bootc/centos-bootc:stream9",
          "transport": "registry",
          "signature": null
        },
        "version": "stream9.20240807.0",
        "timestamp": "2024-08-07T12:00:00Z",
        "imageDigest": "sha256:47e5ed613a970b6574bfa954ab25bb6e85656552899aa518b5961d9645102b38",
        "architecture": "amd64"
      },
      "cachedUpdate": null,
      "incompatible": false,
      "pinned": false,
      "softRebootCapable": false,
      "downloadOnly": false,
      "store": "ostreeContainer",
      "ostree": {
        "checksum": "439f6bd2e2361bee292c1f31840d798c5ac5ba76483b8021dc9f7b0164ac0f48",
        "deploySerial": 0,
        "stateroot": "default"
      }
    },
    "rollback": null,
    "rollbackQueued": false,
    "type": "bootcHost"
  }
}`

// sampleStagedBootedJSON is a realistic output with both staged and
// booted deployments.
var sampleStagedBootedJSON = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "metadata": {"name": "host"},
  "spec": {
    "image": {
      "image": "quay.io/example/someimage:latest",
      "transport": "registry",
      "signature": null
    },
    "bootOrder": "default"
  },
  "status": {
    "staged": {
      "image": {
        "image": {
          "image": "quay.io/example/someimage:latest",
          "transport": "registry",
          "signature": null
        },
        "version": "nightly",
        "timestamp": "2024-10-14T19:22:15Z",
        "imageDigest": "sha256:16dc2b6256b4ff0d2ec18d2dbfb06d117904010c8cf9732cdb022818cf7a7566",
        "architecture": "amd64"
      },
      "cachedUpdate": null,
      "incompatible": false,
      "pinned": false,
      "softRebootCapable": true,
      "downloadOnly": true,
      "store": "ostreeContainer",
      "ostree": {
        "checksum": "3c6dad657109522e0b2e49bf44b5420f16f0b438b5b9357e5132211cfbad135d",
        "deploySerial": 0,
        "stateroot": "default"
      }
    },
    "booted": {
      "image": {
        "image": {
          "image": "quay.io/example/someimage:latest",
          "transport": "registry",
          "signature": null
        },
        "version": "nightly",
        "timestamp": "2024-09-30T19:22:16Z",
        "imageDigest": "sha256:736b359467c9437c1ac915acaae952aad854e07eb4a16a94999a48af08c83c34",
        "architecture": "amd64"
      },
      "cachedUpdate": null,
      "incompatible": false,
      "pinned": false,
      "softRebootCapable": false,
      "downloadOnly": false,
      "store": "ostreeContainer",
      "ostree": {
        "checksum": "26836632adf6228d64ef07a26fd3efaf177104efd1f341a2cf7909a3e4e2c72c",
        "deploySerial": 0,
        "stateroot": "default"
      }
    },
    "rollback": null,
    "rollbackQueued": false,
    "type": "bootcHost"
  }
}`

// sampleNullStatusJSON is output when bootc is running in a container
// (reduced status).
var sampleNullStatusJSON = `{
  "apiVersion": "org.containers.bootc/v1",
  "kind": "BootcHost",
  "metadata": {"name": "host"},
  "spec": {"image": null, "bootOrder": "default"},
  "status": {
    "staged": null,
    "booted": null,
    "rollback": null,
    "rollbackQueued": false,
    "type": null
  }
}`

// --- JSON Parsing Tests ---

func TestParseBootedOnlyStatus(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleBootedOnlyJSON), &host); err != nil {
		t.Fatalf("Failed to parse booted-only JSON: %v", err)
	}

	if host.APIVersion != "org.containers.bootc/v1" {
		t.Errorf("Expected apiVersion org.containers.bootc/v1, got %s", host.APIVersion)
	}
	if host.Kind != "BootcHost" {
		t.Errorf("Expected kind BootcHost, got %s", host.Kind)
	}

	// Spec
	if host.Spec.Image == nil {
		t.Fatal("Expected spec.image to be set")
	}
	if host.Spec.Image.Image != centosBootcImage {
		t.Errorf("Unexpected spec.image.image: %s", host.Spec.Image.Image)
	}
	if host.Spec.Image.Transport != "registry" {
		t.Errorf("Unexpected spec.image.transport: %s", host.Spec.Image.Transport)
	}

	// Status
	if host.Status.Staged != nil {
		t.Error("Expected staged to be nil")
	}
	if host.Status.Rollback != nil {
		t.Error("Expected rollback to be nil")
	}
	if host.Status.Booted == nil {
		t.Fatal("Expected booted to be set")
	}
	if host.Status.Booted.Image == nil {
		t.Fatal("Expected booted.image to be set")
	}
	if host.Status.Booted.Image.ImageDigest != "sha256:47e5ed613a970b6574bfa954ab25bb6e85656552899aa518b5961d9645102b38" {
		t.Errorf("Unexpected booted.image.imageDigest: %s", host.Status.Booted.Image.ImageDigest)
	}
	if host.Status.Booted.Image.Version != "stream9.20240807.0" {
		t.Errorf("Unexpected booted.image.version: %s", host.Status.Booted.Image.Version)
	}
	if host.Status.Booted.Image.Architecture != "amd64" {
		t.Errorf("Unexpected booted.image.architecture: %s", host.Status.Booted.Image.Architecture)
	}
	if host.Status.Booted.SoftRebootCapable {
		t.Error("Expected softRebootCapable to be false for booted")
	}
}

func TestParseStagedBootedStatus(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleStagedBootedJSON), &host); err != nil {
		t.Fatalf("Failed to parse staged+booted JSON: %v", err)
	}

	if host.Status.Staged == nil {
		t.Fatal("Expected staged to be set")
	}
	if !host.Status.Staged.SoftRebootCapable {
		t.Error("Expected staged.softRebootCapable to be true")
	}
	if !host.Status.Staged.DownloadOnly {
		t.Error("Expected staged.downloadOnly to be true")
	}
	if host.Status.Staged.Image.ImageDigest != "sha256:16dc2b6256b4ff0d2ec18d2dbfb06d117904010c8cf9732cdb022818cf7a7566" {
		t.Errorf("Unexpected staged digest: %s", host.Status.Staged.Image.ImageDigest)
	}

	if host.Status.Booted == nil {
		t.Fatal("Expected booted to be set")
	}
	if host.Status.Booted.Image.ImageDigest != "sha256:736b359467c9437c1ac915acaae952aad854e07eb4a16a94999a48af08c83c34" {
		t.Errorf("Unexpected booted digest: %s", host.Status.Booted.Image.ImageDigest)
	}
}

func TestParseNullStatus(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleNullStatusJSON), &host); err != nil {
		t.Fatalf("Failed to parse null status JSON: %v", err)
	}

	if host.Spec.Image != nil {
		t.Error("Expected spec.image to be nil")
	}
	if host.Status.Booted != nil {
		t.Error("Expected booted to be nil")
	}
	if host.Status.Staged != nil {
		t.Error("Expected staged to be nil")
	}
	if host.Status.Rollback != nil {
		t.Error("Expected rollback to be nil")
	}
}

func TestParseTimestamp(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleBootedOnlyJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if host.Status.Booted.Image.Timestamp == nil {
		t.Fatal("Expected timestamp to be set")
	}
	expected := time.Date(2024, 8, 7, 12, 0, 0, 0, time.UTC)
	if !host.Status.Booted.Image.Timestamp.Equal(expected) {
		t.Errorf("Expected timestamp %v, got %v", expected, *host.Status.Booted.Image.Timestamp)
	}
}

// --- Client Method Tests ---

func TestClientStatus(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc status --json", []byte(sampleBootedOnlyJSON), nil)
	client := NewClientWithRunner(runner)

	host, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if host.Status.Booted == nil {
		t.Fatal("Expected booted to be set")
	}
	if len(runner.calls) != 1 {
		t.Errorf("Expected 1 call, got %d", len(runner.calls))
	}
}

func TestClientStatusError(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc status --json", nil, fmt.Errorf("command failed"))
	client := NewClientWithRunner(runner)

	_, err := client.Status(context.Background())
	if err == nil {
		t.Fatal("Expected error from Status()")
	}
}

func TestClientIsBootcHost(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc status --json", []byte(sampleBootedOnlyJSON), nil)
	client := NewClientWithRunner(runner)

	if !client.IsBootcHost(context.Background()) {
		t.Error("Expected IsBootcHost() to return true")
	}
}

func TestClientIsBootcHostFalse(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc status --json", nil, fmt.Errorf("bootc not found"))
	client := NewClientWithRunner(runner)

	if client.IsBootcHost(context.Background()) {
		t.Error("Expected IsBootcHost() to return false")
	}
}

func TestClientSwitch(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc switch quay.io/example/new-image:latest", nil, nil)
	client := NewClientWithRunner(runner)

	err := client.Switch(context.Background(), "quay.io/example/new-image:latest")
	if err != nil {
		t.Fatalf("Switch() error: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("Expected 1 call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "bootc" {
		t.Errorf("Expected command bootc, got %s", call.name)
	}
	if len(call.args) != 2 || call.args[0] != "switch" || call.args[1] != "quay.io/example/new-image:latest" {
		t.Errorf("Unexpected args: %v", call.args)
	}
}

func TestClientUpgradeDownloadOnly(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc upgrade --download-only", nil, nil)
	client := NewClientWithRunner(runner)

	err := client.UpgradeDownloadOnly(context.Background())
	if err != nil {
		t.Fatalf("UpgradeDownloadOnly() error: %v", err)
	}
	call := runner.calls[0]
	if len(call.args) != 2 || call.args[0] != "upgrade" || call.args[1] != "--download-only" {
		t.Errorf("Unexpected args: %v", call.args)
	}
}

func TestClientUpgradeApplyWithSoftReboot(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc upgrade --from-downloaded --apply --soft-reboot=auto", nil, nil)
	client := NewClientWithRunner(runner)

	err := client.UpgradeApply(context.Background(), true)
	if err != nil {
		t.Fatalf("UpgradeApply() error: %v", err)
	}
	call := runner.calls[0]
	expectedArgs := []string{"upgrade", "--from-downloaded", "--apply", "--soft-reboot=auto"}
	if len(call.args) != len(expectedArgs) {
		t.Fatalf("Expected %d args, got %d: %v", len(expectedArgs), len(call.args), call.args)
	}
	for i, arg := range expectedArgs {
		if call.args[i] != arg {
			t.Errorf("Arg %d: expected %q, got %q", i, arg, call.args[i])
		}
	}
}

func TestClientUpgradeApplyWithoutSoftReboot(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc upgrade --from-downloaded --apply", nil, nil)
	client := NewClientWithRunner(runner)

	err := client.UpgradeApply(context.Background(), false)
	if err != nil {
		t.Fatalf("UpgradeApply() error: %v", err)
	}
	call := runner.calls[0]
	expectedArgs := []string{"upgrade", "--from-downloaded", "--apply"}
	if len(call.args) != len(expectedArgs) {
		t.Fatalf("Expected %d args, got %d: %v", len(expectedArgs), len(call.args), call.args)
	}
}

func TestClientRollbackApply(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc rollback --apply", nil, nil)
	client := NewClientWithRunner(runner)

	err := client.Rollback(context.Background(), true)
	if err != nil {
		t.Fatalf("Rollback() error: %v", err)
	}
	call := runner.calls[0]
	if len(call.args) != 2 || call.args[0] != "rollback" || call.args[1] != "--apply" {
		t.Errorf("Unexpected args: %v", call.args)
	}
}

func TestClientRollbackNoApply(t *testing.T) {
	runner := newMockRunner()
	runner.setOutput("bootc rollback", nil, nil)
	client := NewClientWithRunner(runner)

	err := client.Rollback(context.Background(), false)
	if err != nil {
		t.Fatalf("Rollback() error: %v", err)
	}
	call := runner.calls[0]
	if len(call.args) != 1 || call.args[0] != "rollback" {
		t.Errorf("Unexpected args: %v", call.args)
	}
}

// --- Status Mapping Tests ---

func TestToBootEntryStatusNil(t *testing.T) {
	result := ToBootEntryStatus(nil)
	if result.Image != "" || result.Version != "" || result.SoftRebootCapable {
		t.Error("Expected zero-value BootEntryStatus for nil input")
	}
}

func TestToBootEntryStatusNilImage(t *testing.T) {
	entry := &BootEntry{Image: nil}
	result := ToBootEntryStatus(entry)
	if result.Image != "" {
		t.Error("Expected empty image for nil Image")
	}
}

func TestToBootEntryStatusFull(t *testing.T) {
	ts := time.Date(2024, 10, 14, 19, 22, 15, 0, time.UTC)
	entry := &BootEntry{
		Image: &ImageStatus{
			Image: ImageReference{
				Image:     "quay.io/example/someimage:latest",
				Transport: "registry",
			},
			Version:      "nightly",
			Timestamp:    &ts,
			ImageDigest:  "sha256:16dc2b6256b4ff0d2ec18d2dbfb06d117904010c8cf9732cdb022818cf7a7566",
			Architecture: "amd64",
		},
		SoftRebootCapable: true,
	}

	result := ToBootEntryStatus(entry)
	expected := "quay.io/example/someimage@sha256:16dc2b6256b4ff0d2ec18d2dbfb06d117904010c8cf9732cdb022818cf7a7566"
	if result.Image != expected {
		t.Errorf("Expected image %q, got %q", expected, result.Image)
	}
	if result.Version != "nightly" {
		t.Errorf("Expected version nightly, got %s", result.Version)
	}
	if !result.SoftRebootCapable {
		t.Error("Expected softRebootCapable to be true")
	}
	expectedTime := metav1.NewTime(ts)
	if !result.Timestamp.Equal(&expectedTime) {
		t.Errorf("Expected timestamp %v, got %v", expectedTime, result.Timestamp)
	}
}

func TestToBootcNodeStatus(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleStagedBootedJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	status := ToBootcNodeStatus(&host)

	if status.TrackedImage != "quay.io/example/someimage:latest" {
		t.Errorf("Unexpected trackedImage: %s", status.TrackedImage)
	}
	if status.BootedDigest != "sha256:736b359467c9437c1ac915acaae952aad854e07eb4a16a94999a48af08c83c34" {
		t.Errorf("Unexpected bootedDigest: %s", status.BootedDigest)
	}
	if status.Booted.Image == "" {
		t.Error("Expected booted.image to be set")
	}
	if status.Staged.Image == "" {
		t.Error("Expected staged.image to be set")
	}
	if !status.Staged.SoftRebootCapable {
		t.Error("Expected staged.softRebootCapable to be true")
	}
	if status.Rollback.Image != "" {
		t.Error("Expected rollback.image to be empty")
	}

	// Phase and message should NOT be set (managed by daemon state machine).
	if status.Phase != "" {
		t.Errorf("Expected phase to be empty, got %s", status.Phase)
	}
	if status.Message != "" {
		t.Errorf("Expected message to be empty, got %s", status.Message)
	}
}

func TestToBootcNodeStatusNullBooted(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleNullStatusJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	status := ToBootcNodeStatus(&host)

	if status.TrackedImage != "" {
		t.Errorf("Expected empty trackedImage, got %s", status.TrackedImage)
	}
	if status.BootedDigest != "" {
		t.Errorf("Expected empty bootedDigest, got %s", status.BootedDigest)
	}
}

// --- Status Helper Tests ---

func TestHasStagedImage(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleStagedBootedJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if !HasStagedImage(&host) {
		t.Error("Expected HasStagedImage() to be true")
	}
}

func TestHasStagedImageFalse(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleBootedOnlyJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if HasStagedImage(&host) {
		t.Error("Expected HasStagedImage() to be false")
	}
}

func TestStagedImageDigest(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleStagedBootedJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	digest := StagedImageDigest(&host)
	expected := "sha256:16dc2b6256b4ff0d2ec18d2dbfb06d117904010c8cf9732cdb022818cf7a7566"
	if digest != expected {
		t.Errorf("Expected %s, got %s", expected, digest)
	}
}

func TestBootedImageDigest(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleBootedOnlyJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	digest := BootedImageDigest(&host)
	expected := "sha256:47e5ed613a970b6574bfa954ab25bb6e85656552899aa518b5961d9645102b38"
	if digest != expected {
		t.Errorf("Expected %s, got %s", expected, digest)
	}
}

func TestBootedImageRef(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleBootedOnlyJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	ref := BootedImageRef(&host)
	if ref != centosBootcImage {
		t.Errorf("Unexpected booted image ref: %s", ref)
	}
}

func TestTrackedImageRef(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleBootedOnlyJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	ref := TrackedImageRef(&host)
	if ref != centosBootcImage {
		t.Errorf("Unexpected tracked image ref: %s", ref)
	}
}

func TestTrackedImageRefNull(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleNullStatusJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	ref := TrackedImageRef(&host)
	if ref != "" {
		t.Errorf("Expected empty tracked image ref, got %s", ref)
	}
}

func TestIsDownloadOnly(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleStagedBootedJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if !IsDownloadOnly(&host) {
		t.Error("Expected IsDownloadOnly() to be true")
	}
}

func TestIsDownloadOnlyFalse(t *testing.T) {
	var host Host
	if err := json.Unmarshal([]byte(sampleBootedOnlyJSON), &host); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}
	if IsDownloadOnly(&host) {
		t.Error("Expected IsDownloadOnly() to be false")
	}
}

// --- imageNameWithoutTag Tests ---

func TestImageNameWithoutTag(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"quay.io/example/my-image:latest", "quay.io/example/my-image"},
		{"quay.io/example/my-image:v1.2.3", "quay.io/example/my-image"},
		{"quay.io/example/my-image@sha256:abc123", "quay.io/example/my-image"},
		{"quay.io/example/my-image", "quay.io/example/my-image"},
		{"localhost:5000/my-image:latest", "localhost:5000/my-image"},
		{"localhost:5000/my-image", "localhost:5000/my-image"},
		{"registry.example.com:8080/org/repo:v2", "registry.example.com:8080/org/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := imageNameWithoutTag(tt.input)
			if result != tt.expected {
				t.Errorf("imageNameWithoutTag(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// --- ImageRefWithDigest Tests ---

func TestImageRefWithDigest(t *testing.T) {
	imgStatus := &ImageStatus{
		Image: ImageReference{
			Image:     "quay.io/example/my-image:latest",
			Transport: "registry",
		},
		ImageDigest: "sha256:abc123def456",
	}
	result := ImageRefWithDigest(imgStatus)
	expected := "quay.io/example/my-image@sha256:abc123def456"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestImageRefWithDigestNoDigest(t *testing.T) {
	imgStatus := &ImageStatus{
		Image: ImageReference{
			Image:     "quay.io/example/my-image:latest",
			Transport: "registry",
		},
	}
	result := ImageRefWithDigest(imgStatus)
	if result != "quay.io/example/my-image:latest" {
		t.Errorf("Expected raw image ref, got %q", result)
	}
}

func TestImageRefWithDigestNil(t *testing.T) {
	result := ImageRefWithDigest(nil)
	if result != "" {
		t.Errorf("Expected empty string, got %q", result)
	}
}

// Ensure that the mapped types satisfy the expected shapes. This is a
// compile-time check, not a runtime test.
var _ v1alpha1.BootEntryStatus = ToBootEntryStatus(nil)
