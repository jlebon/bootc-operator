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
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// DefaultRootfsPath is the default mount point for the host root
// filesystem inside the daemon container.
const DefaultRootfsPath = "/run/rootfs"

// CommandRunner abstracts command execution for testability. The default
// implementation uses SysProcAttr.Chroot to execute commands inside the
// host rootfs.
type CommandRunner interface {
	// Run executes a command and returns its combined stdout/stderr
	// output. The context controls cancellation.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Client wraps the bootc CLI for executing bootc commands on the host.
type Client struct {
	runner CommandRunner
}

// NewClient creates a Client that executes bootc commands via chroot
// into the host rootfs at DefaultRootfsPath.
func NewClient() *Client {
	return &Client{runner: &chrootRunner{rootfs: DefaultRootfsPath}}
}

// NewClientWithRunner creates a Client that uses the given CommandRunner.
// This is intended for testing with a mock runner.
func NewClientWithRunner(runner CommandRunner) *Client {
	return &Client{runner: runner}
}

// IsBootcHost returns true if the host has bootc available. It runs
// `bootc status --json` and checks for a successful exit. If bootc is
// not available or the command fails, it returns false and the error.
func (c *Client) IsBootcHost(ctx context.Context) (bool, error) {
	_, err := c.runner.Run(ctx, "bootc", "status", "--json")
	if err != nil {
		return false, err
	}
	return true, nil
}

// Status runs `bootc status --json` and returns the parsed Host.
func (c *Client) Status(ctx context.Context) (*Host, error) {
	output, err := c.runner.Run(ctx, "bootc", "status", "--json")
	if err != nil {
		return nil, fmt.Errorf("running bootc status: %w", err)
	}

	var host Host
	if err := json.Unmarshal(output, &host); err != nil {
		return nil, fmt.Errorf("parsing bootc status output: %w", err)
	}

	return &host, nil
}

// Switch runs `bootc switch <image>` to change the tracked image. This
// downloads and stages the new image without rebooting.
func (c *Client) Switch(ctx context.Context, image string) error {
	_, err := c.runner.Run(ctx, "bootc", "switch", image)
	if err != nil {
		return fmt.Errorf("running bootc switch: %w", err)
	}
	return nil
}

// UpgradeDownloadOnly runs `bootc upgrade --download-only` to stage the
// latest version of the currently tracked image without rebooting.
func (c *Client) UpgradeDownloadOnly(ctx context.Context) error {
	_, err := c.runner.Run(ctx, "bootc", "upgrade", "--download-only")
	if err != nil {
		return fmt.Errorf("running bootc upgrade --download-only: %w", err)
	}
	return nil
}

// UpgradeApply runs `bootc upgrade --from-downloaded --apply` to apply a
// previously staged image and reboot. When softReboot is true,
// `--soft-reboot=auto` is passed to minimize disruption.
func (c *Client) UpgradeApply(ctx context.Context, softReboot bool) error {
	args := []string{"upgrade", "--from-downloaded", "--apply"}
	if softReboot {
		args = append(args, "--soft-reboot=auto")
	}
	_, err := c.runner.Run(ctx, "bootc", args...)
	if err != nil {
		return fmt.Errorf("running bootc upgrade --apply: %w", err)
	}
	return nil
}

// Rollback runs `bootc rollback`. When apply is true, `--apply` is
// passed to immediately reboot into the rollback deployment.
func (c *Client) Rollback(ctx context.Context, apply bool) error {
	args := []string{"rollback"}
	if apply {
		args = append(args, "--apply")
	}
	_, err := c.runner.Run(ctx, "bootc", args...)
	if err != nil {
		return fmt.Errorf("running bootc rollback: %w", err)
	}
	return nil
}

// chrootRunner executes commands via chroot into the host rootfs. It
// clears the `container` environment variable because container runtimes
// set `container=oci`, which bootc checks to detect container context
// and returns reduced status.
type chrootRunner struct {
	rootfs string
}

func (r *chrootRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot: r.rootfs,
	}
	// Clear `container` env var. Container runtimes set container=oci,
	// which causes bootc to detect it is running inside a container and
	// return reduced status (null booted entry).
	cmd.Env = filterEnv("container")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("command %q failed: %w (output: %s)", name, err, string(output))
	}
	return output, nil
}

// filterEnv returns os.Environ() with the named variable removed.
func filterEnv(exclude string) []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	prefix := exclude + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}
