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
	"strings"
	"testing"
	"time"
)

// fakeRunner is a CommandRunner that records calls and returns
// pre-configured responses.
type fakeRunner struct {
	calls    []fakeCall
	handlers map[string]fakeHandler
}

type fakeCall struct {
	name string
	args []string
}

type fakeHandler struct {
	output []byte
	err    error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{handlers: make(map[string]fakeHandler)}
}

func (f *fakeRunner) setHandler(key string, output []byte, err error) {
	f.handlers[key] = fakeHandler{output: output, err: err}
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: args})
	key := name + " " + strings.Join(args, " ")
	if h, ok := f.handlers[key]; ok {
		return h.output, h.err
	}
	// Try a prefix match for flexibility.
	for k, h := range f.handlers {
		if strings.HasPrefix(key, k) {
			return h.output, h.err
		}
	}
	return nil, fmt.Errorf("no handler for: %s", key)
}

func TestParseStatusJSON(t *testing.T) {
	statusJSON := `{
		"apiVersion": "org.containers.bootc/v1",
		"kind": "BootcHost",
		"metadata": {"name": "host"},
		"spec": {
			"image": {
				"image": "quay.io/centos-bootc/centos-bootc:stream9",
				"transport": "registry"
			},
			"bootOrder": "default"
		},
		"status": {
			"staged": null,
			"booted": {
				"image": {
					"image": {
						"image": "quay.io/centos-bootc/centos-bootc:stream9",
						"transport": "registry"
					},
					"version": "stream9.20240807.0",
					"timestamp": null,
					"imageDigest": "sha256:47e5ed613a970b6574bfa954ab25bb6e85656552899aa518b5961d9645102b38",
					"architecture": "arm64"
				},
				"incompatible": false,
				"pinned": false,
				"downloadOnly": false,
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

	var host Host
	if err := json.Unmarshal([]byte(statusJSON), &host); err != nil {
		t.Fatalf("Failed to parse status JSON: %v", err)
	}

	if host.APIVersion != "org.containers.bootc/v1" {
		t.Errorf("apiVersion = %q, want %q", host.APIVersion, "org.containers.bootc/v1")
	}
	if host.Kind != "BootcHost" {
		t.Errorf("kind = %q, want %q", host.Kind, "BootcHost")
	}
	if host.Metadata.Name != "host" {
		t.Errorf("metadata.name = %q, want %q", host.Metadata.Name, "host")
	}
	if host.Spec.Image == nil {
		t.Fatal("spec.image is nil")
	}
	if host.Spec.Image.Image != "quay.io/centos-bootc/centos-bootc:stream9" {
		t.Errorf("spec.image.image = %q, want centos-bootc ref", host.Spec.Image.Image)
	}
	if host.Status.Staged != nil {
		t.Errorf("status.staged should be nil, got %+v", host.Status.Staged)
	}
	if host.Status.Booted == nil {
		t.Fatal("status.booted is nil")
	}
	if host.Status.Booted.Image == nil {
		t.Fatal("status.booted.image is nil")
	}
	if host.Status.Booted.Image.ImageDigest != "sha256:47e5ed613a970b6574bfa954ab25bb6e85656552899aa518b5961d9645102b38" {
		t.Errorf("imageDigest = %q", host.Status.Booted.Image.ImageDigest)
	}
	if host.Status.Booted.Image.Version != "stream9.20240807.0" {
		t.Errorf("version = %q", host.Status.Booted.Image.Version)
	}
	if host.Status.Rollback != nil {
		t.Errorf("status.rollback should be nil")
	}
}

func TestParseStatusWithStagedAndRollback(t *testing.T) {
	statusJSON := `{
		"apiVersion": "org.containers.bootc/v1",
		"kind": "BootcHost",
		"metadata": {"name": "host"},
		"spec": {
			"image": {
				"image": "quay.io/example/someimage:latest",
				"transport": "registry"
			}
		},
		"status": {
			"staged": {
				"image": {
					"image": {
						"image": "quay.io/example/someimage:latest",
						"transport": "registry"
					},
					"version": "nightly",
					"timestamp": "2023-10-14T19:22:15Z",
					"imageDigest": "sha256:16dc2b6256b4ff0d2ec18d2dbfb06d117904010c8cf9732cdb022818cf7a7566",
					"architecture": "arm64"
				},
				"incompatible": false,
				"pinned": false,
				"softRebootCapable": true,
				"downloadOnly": true
			},
			"booted": {
				"image": {
					"image": {
						"image": "quay.io/example/someimage:latest",
						"transport": "registry"
					},
					"version": "nightly",
					"timestamp": "2023-09-30T19:22:16Z",
					"imageDigest": "sha256:736b359467c9437c1ac915acaae952aad854e07eb4a16a94999a48af08c83c34",
					"architecture": "arm64"
				},
				"incompatible": false,
				"pinned": false,
				"downloadOnly": false
			},
			"rollback": {
				"image": {
					"image": {
						"image": "quay.io/example/oldimage:v1",
						"transport": "registry"
					},
					"version": "v1.0",
					"timestamp": "2023-08-01T10:00:00Z",
					"imageDigest": "sha256:aaaa",
					"architecture": "arm64"
				},
				"incompatible": false,
				"pinned": false,
				"downloadOnly": false
			},
			"rollbackQueued": false,
			"type": "bootcHost"
		}
	}`

	var host Host
	if err := json.Unmarshal([]byte(statusJSON), &host); err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	if host.Status.Staged == nil {
		t.Fatal("staged is nil")
	}
	if !host.Status.Staged.SoftRebootCapable {
		t.Error("staged.softRebootCapable should be true")
	}
	if !host.Status.Staged.DownloadOnly {
		t.Error("staged.downloadOnly should be true")
	}
	if host.Status.Staged.Image.Timestamp == nil {
		t.Fatal("staged.image.timestamp is nil")
	}
	expected := time.Date(2023, 10, 14, 19, 22, 15, 0, time.UTC)
	if !host.Status.Staged.Image.Timestamp.Equal(expected) {
		t.Errorf("staged.timestamp = %v, want %v", host.Status.Staged.Image.Timestamp, expected)
	}

	if host.Status.Rollback == nil {
		t.Fatal("rollback is nil")
	}
	if host.Status.Rollback.Image.Image.Image != "quay.io/example/oldimage:v1" {
		t.Errorf("rollback image = %q", host.Status.Rollback.Image.Image.Image)
	}
}

func TestParseNullStatus(t *testing.T) {
	statusJSON := `{
		"apiVersion":"org.containers.bootc/v1",
		"kind":"BootcHost",
		"metadata":{"name":"host"},
		"spec":{"image":null,"bootOrder":"default"},
		"status":{
			"staged":null,"booted":null,"rollback":null,
			"rollbackQueued":false,"type":null
		}
	}`

	var host Host
	if err := json.Unmarshal([]byte(statusJSON), &host); err != nil {
		t.Fatalf("Failed to parse null status: %v", err)
	}

	if host.Spec.Image != nil {
		t.Errorf("spec.image should be nil")
	}
	if host.Status.Booted != nil {
		t.Errorf("booted should be nil")
	}
	if host.Status.Staged != nil {
		t.Errorf("staged should be nil")
	}
	if host.Status.Rollback != nil {
		t.Errorf("rollback should be nil")
	}
}

func TestClientStatus(t *testing.T) {
	statusJSON := `{
		"apiVersion": "org.containers.bootc/v1",
		"kind": "BootcHost",
		"metadata": {"name": "host"},
		"spec": {"image": null},
		"status": {
			"staged": null,
			"booted": {
				"image": {
					"image": {"image": "quay.io/test/image:latest", "transport": "registry"},
					"imageDigest": "sha256:abcd1234",
					"architecture": "x86_64"
				},
				"incompatible": false, "pinned": false
			},
			"rollback": null
		}
	}`

	runner := newFakeRunner()
	// Status() uses a bash wrapper to write to a temp file, so match
	// the bash -c prefix.
	runner.setHandler("nsenter -t 1 -m -- bash", []byte(statusJSON), nil)

	c := NewClientWithRunner(runner)
	host, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if host.Status.Booted == nil {
		t.Fatal("booted is nil")
	}
	if host.Status.Booted.Image.ImageDigest != "sha256:abcd1234" {
		t.Errorf("digest = %q", host.Status.Booted.Image.ImageDigest)
	}
}

func TestClientIsBootcHost(t *testing.T) {
	runner := newFakeRunner()

	// When bootc status succeeds, the host is a bootc system.
	runner.setHandler("nsenter -t 1 -m -- systemd-run", []byte("{}"), nil)
	c := NewClientWithRunner(runner)
	if !c.IsBootcHost(context.Background()) {
		t.Error("IsBootcHost should return true when bootc status succeeds")
	}

	// When bootc status fails, the host is not a bootc system.
	runner2 := newFakeRunner()
	runner2.setHandler("nsenter -t 1 -m -- systemd-run", nil, fmt.Errorf("not found"))
	c2 := NewClientWithRunner(runner2)
	if c2.IsBootcHost(context.Background()) {
		t.Error("IsBootcHost should return false when bootc status fails")
	}
}

func TestClientSwitch(t *testing.T) {
	runner := newFakeRunner()
	runner.setHandler("nsenter -t 1 -m -- systemd-run --wait --quiet --collect bootc switch", nil, nil)

	c := NewClientWithRunner(runner)
	if err := c.Switch(context.Background(), "quay.io/test/newimage:v2"); err != nil {
		t.Fatalf("Switch() error: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(runner.calls))
	}
	args := runner.calls[0].args
	if args[len(args)-1] != "quay.io/test/newimage:v2" {
		t.Errorf("last arg = %q, want image ref", args[len(args)-1])
	}
}

func TestClientUpgradeDownloadOnly(t *testing.T) {
	runner := newFakeRunner()
	runner.setHandler("nsenter -t 1 -m -- systemd-run --wait --quiet --collect bootc upgrade --download-only", nil, nil)

	c := NewClientWithRunner(runner)
	if err := c.UpgradeDownloadOnly(context.Background()); err != nil {
		t.Fatalf("UpgradeDownloadOnly() error: %v", err)
	}
}

func TestClientUpgradeApply(t *testing.T) {
	tests := []struct {
		name       string
		softReboot bool
		wantArgs   []string
	}{
		{
			name:       "without soft reboot",
			softReboot: false,
			wantArgs: []string{
				"-t", "1", "-m", "--",
				"systemd-run", "--wait", "--quiet", "--collect",
				"bootc", "upgrade", "--from-downloaded", "--apply",
			},
		},
		{
			name:       "with soft reboot",
			softReboot: true,
			wantArgs: []string{
				"-t", "1", "-m", "--",
				"systemd-run", "--wait", "--quiet", "--collect",
				"bootc", "upgrade",
				"--from-downloaded", "--apply", "--soft-reboot=auto",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner()
			// Match on prefix since the args differ.
			runner.setHandler("nsenter -t 1 -m -- systemd-run --wait --quiet --collect bootc upgrade", nil, nil)

			c := NewClientWithRunner(runner)
			if err := c.UpgradeApply(context.Background(), tt.softReboot); err != nil {
				t.Fatalf("UpgradeApply() error: %v", err)
			}

			if len(runner.calls) != 1 {
				t.Fatalf("expected 1 call, got %d", len(runner.calls))
			}
			got := runner.calls[0].args
			if len(got) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", got, tt.wantArgs)
			}
			for i, a := range tt.wantArgs {
				if got[i] != a {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], a)
				}
			}
		})
	}
}

func TestClientRollback(t *testing.T) {
	tests := []struct {
		name     string
		apply    bool
		wantArgs []string
	}{
		{
			name:  "without apply",
			apply: false,
			wantArgs: []string{
				"-t", "1", "-m", "--",
				"systemd-run", "--wait", "--quiet", "--collect",
				"bootc", "rollback",
			},
		},
		{
			name:  "with apply",
			apply: true,
			wantArgs: []string{
				"-t", "1", "-m", "--",
				"systemd-run", "--wait", "--quiet", "--collect",
				"bootc", "rollback", "--apply",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner()
			runner.setHandler("nsenter -t 1 -m -- systemd-run --wait --quiet --collect bootc rollback", nil, nil)

			c := NewClientWithRunner(runner)
			if err := c.Rollback(context.Background(), tt.apply); err != nil {
				t.Fatalf("Rollback() error: %v", err)
			}

			if len(runner.calls) != 1 {
				t.Fatalf("expected 1 call, got %d", len(runner.calls))
			}
			got := runner.calls[0].args
			if len(got) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", got, tt.wantArgs)
			}
			for i, a := range tt.wantArgs {
				if got[i] != a {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], a)
				}
			}
		})
	}
}

func TestClientStatusParseError(t *testing.T) {
	runner := newFakeRunner()
	runner.setHandler("nsenter -t 1 -m -- bash", []byte("not json"), nil)

	c := NewClientWithRunner(runner)
	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("Status() should return error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parsing bootc status JSON") {
		t.Errorf("error = %q, want to contain 'parsing bootc status JSON'", err.Error())
	}
}

func TestClientStatusCommandError(t *testing.T) {
	runner := newFakeRunner()
	runner.setHandler("nsenter -t 1 -m -- bash", nil, fmt.Errorf("exit status 1"))

	c := NewClientWithRunner(runner)
	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("Status() should return error when command fails")
	}
	if !strings.Contains(err.Error(), "running bootc status") {
		t.Errorf("error = %q, want to contain 'running bootc status'", err.Error())
	}
}
