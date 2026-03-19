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

// HostRootPath is the mount point where the host root filesystem is
// made available inside the daemon container. The DaemonSet mounts
// the host's / here via a hostPath volume.
const HostRootPath = "/run/rootfs"

// NewClient creates a Client that executes bootc commands on the host
// root filesystem via chroot.
//
// The daemon container must mount the host's / at /run/rootfs (see
// HostRootPath). All bootc commands run via:
//
//	chroot /run/rootfs bootc ...
//
// The `container` environment variable is cleared before execution
// because container runtimes (containerd, CRI-O) set `container=oci`
// in all containers, and bootc checks this variable to detect whether
// it is running inside a container. When set, bootc returns a reduced
// status with null booted/staged/rollback fields.
func NewClient() Client {
	return &client{runner: &chrootRunner{root: HostRootPath}}
}

// NewClientWithRunner creates a Client using the given CommandRunner.
// This is primarily useful for testing.
func NewClientWithRunner(runner CommandRunner) Client {
	return &client{runner: runner}
}

type client struct {
	runner CommandRunner
}

func (c *client) IsBootcHost(ctx context.Context) bool {
	_, err := c.runner.Run(ctx, "bootc", "status", "--json")
	return err == nil
}

func (c *client) Status(ctx context.Context) (*Host, error) {
	out, err := c.runner.Run(ctx, "bootc", "status", "--json")
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
	_, err := c.runner.Run(ctx, "bootc", "switch", image)
	if err != nil {
		return fmt.Errorf("running bootc switch: %w", err)
	}
	return nil
}

func (c *client) UpgradeDownloadOnly(ctx context.Context) error {
	_, err := c.runner.Run(ctx, "bootc", "upgrade", "--download-only")
	if err != nil {
		return fmt.Errorf("running bootc upgrade --download-only: %w", err)
	}
	return nil
}

func (c *client) UpgradeApply(ctx context.Context, softReboot bool) error {
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

func (c *client) Rollback(ctx context.Context, apply bool) error {
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

// chrootRunner executes commands inside a chroot using
// syscall.Chroot. The `container` environment variable is cleared
// before execution so that bootc does not detect a container context.
//
// The chroot + exec is done in a child process (via exec.Command)
// rather than in the current process, so the daemon's own root is
// not affected.
type chrootRunner struct {
	root string
}

func (r *chrootRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	// Point to the binary inside the rootfs so exec.Command can stat
	// it. After the chroot, the kernel resolves the post-chroot path
	// (/usr/bin/<name>) for execve.
	hostBin := r.root + "/usr/bin/" + name
	cmd := exec.CommandContext(ctx, hostBin, args...)
	cmd.Path = "/usr/bin/" + name
	// Chroot the child process into the host rootfs before exec.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot: r.root,
	}
	// Clear the `container` env var so bootc doesn't think it's in a
	// container. Container runtimes set container=oci which causes
	// bootc to return reduced status (null booted entry).
	cmd.Env = filterEnv(os.Environ(), "container")
	return cmd.CombinedOutput()
}

// filterEnv returns a copy of env with the named variable removed.
func filterEnv(env []string, name string) []string {
	prefix := name + "="
	result := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}
