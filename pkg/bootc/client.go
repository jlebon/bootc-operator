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
	"os/exec"
)

// Client defines the interface for interacting with the bootc CLI on a
// host. All methods execute commands in PID 1's mount namespace via
// nsenter so that bootc sees the host filesystem (ostree repo, container
// storage, boot loader config).
type Client interface {
	// IsBootcHost returns true if bootc is available on the host.
	IsBootcHost(ctx context.Context) bool

	// Status runs `bootc status --json` and returns the parsed host state.
	Status(ctx context.Context) (*Host, error)

	// Switch runs `bootc switch <image>` to change the tracked image.
	// This downloads and stages the new image without rebooting.
	Switch(ctx context.Context, image string) error

	// UpgradeDownloadOnly runs `bootc upgrade --download-only` to stage
	// the latest version of the currently tracked image.
	UpgradeDownloadOnly(ctx context.Context) error

	// UpgradeApply runs `bootc upgrade --from-downloaded --apply` to
	// apply a previously staged image and reboot. When softReboot is
	// true, `--soft-reboot=auto` is appended to use soft reboot when the
	// kernel and initramfs are unchanged.
	UpgradeApply(ctx context.Context, softReboot bool) error

	// Rollback runs `bootc rollback`. When apply is true, `--apply` is
	// appended to also reboot into the previous deployment.
	Rollback(ctx context.Context, apply bool) error
}

// CommandRunner abstracts command execution so the client can be tested
// without actually running nsenter/bootc.
type CommandRunner interface {
	// Run executes a command and returns its combined stdout/stderr output.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewClient creates a Client that executes bootc commands on the host.
//
// All commands run via:
//
//	nsenter -t 1 -m -- systemd-run --wait --quiet --collect bootc ...
//
// This two-step approach is necessary because bootc reads its state
// from /sysroot and the ostree repo, which requires both the host
// mount namespace (nsenter -m) and the host cgroup/process context
// (systemd-run spawns the command as a native host process). Without
// systemd-run, bootc detects it is running inside a container (via
// /proc/self/cgroup) and reports a reduced status.
//
// For commands that need stdout (like `bootc status --json`),
// systemd-run writes to a temporary file on the host filesystem
// because `--pipe` mode may be blocked by SELinux policy.
func NewClient() Client {
	return &client{runner: &nsenterRunner{}}
}

// NewClientWithRunner creates a Client using the given CommandRunner.
// This is primarily useful for testing.
func NewClientWithRunner(runner CommandRunner) Client {
	return &client{runner: runner}
}

type client struct {
	runner CommandRunner
}

// hostExecArgs builds the argument list for running a command on the
// host via nsenter + systemd-run.
func hostExecArgs(cmd string, args ...string) []string {
	// nsenter -t 1 -m -- systemd-run --wait --quiet --collect <cmd> <args...>
	prefix := [...]string{
		"-t", "1", "-m", "--",
		"systemd-run", "--wait", "--quiet", "--collect",
	}
	result := make([]string, 0, len(prefix)+1+len(args))
	result = append(result, prefix[:]...)
	result = append(result, cmd)
	result = append(result, args...)
	return result
}

// hostStatusArgs builds the argument list for running `bootc status
// --json` on the host and capturing stdout to a file. systemd-run's
// --pipe mode may be blocked by SELinux, so we redirect output to a
// temporary file and read it back via a wrapper shell command.
func hostStatusArgs() []string {
	return []string{
		"-t", "1", "-m", "--",
		"bash", "-c",
		"systemd-run --wait --quiet --collect " +
			"-p StandardOutput=file:/run/bootc-status.json " +
			"bootc status --json && " +
			"cat /run/bootc-status.json; " +
			"rm -f /run/bootc-status.json",
	}
}

func (c *client) IsBootcHost(ctx context.Context) bool {
	_, err := c.runner.Run(ctx, "nsenter", hostExecArgs("bootc", "status", "--json")...)
	return err == nil
}

func (c *client) Status(ctx context.Context) (*Host, error) {
	out, err := c.runner.Run(ctx, "nsenter", hostStatusArgs()...)
	if err != nil {
		return nil, fmt.Errorf("running bootc status: %w", err)
	}

	var host Host
	if err := json.Unmarshal(out, &host); err != nil {
		return nil, fmt.Errorf("parsing bootc status JSON (len=%d): %w", len(out), err)
	}
	return &host, nil
}

func (c *client) Switch(ctx context.Context, image string) error {
	_, err := c.runner.Run(ctx, "nsenter", hostExecArgs("bootc", "switch", image)...)
	if err != nil {
		return fmt.Errorf("running bootc switch: %w", err)
	}
	return nil
}

func (c *client) UpgradeDownloadOnly(ctx context.Context) error {
	_, err := c.runner.Run(ctx, "nsenter", hostExecArgs("bootc", "upgrade", "--download-only")...)
	if err != nil {
		return fmt.Errorf("running bootc upgrade --download-only: %w", err)
	}
	return nil
}

func (c *client) UpgradeApply(ctx context.Context, softReboot bool) error {
	args := []string{"bootc", "upgrade", "--from-downloaded", "--apply"}
	if softReboot {
		args = append(args, "--soft-reboot=auto")
	}
	_, err := c.runner.Run(ctx, "nsenter", hostExecArgs(args[0], args[1:]...)...)
	if err != nil {
		return fmt.Errorf("running bootc upgrade --apply: %w", err)
	}
	return nil
}

func (c *client) Rollback(ctx context.Context, apply bool) error {
	args := []string{"bootc", "rollback"}
	if apply {
		args = append(args, "--apply")
	}
	_, err := c.runner.Run(ctx, "nsenter", hostExecArgs(args[0], args[1:]...)...)
	if err != nil {
		return fmt.Errorf("running bootc rollback: %w", err)
	}
	return nil
}

// nsenterRunner executes commands directly via os/exec.
type nsenterRunner struct{}

func (r *nsenterRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}
