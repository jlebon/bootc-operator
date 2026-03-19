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
	"os"
	"path/filepath"
)

// hostAuthPath is the path on the host where bootc looks for registry
// auth credentials. This is /run/ostree/auth.json, which is the
// highest-priority auth path that bootc checks (above
// /etc/ostree/auth.json and /usr/lib/ostree/auth.json). It is
// ephemeral (cleared on reboot), so it does not persistently mutate
// the host.
const hostAuthPath = "/run/ostree/auth.json"

// WriteAuthFile writes dockerconfigjson data to the bootc auth path
// on the host via the rootfs mount. rootfsPath is the mount point of
// the host rootfs inside the daemon container (typically /run/rootfs).
func WriteAuthFile(rootfsPath string, data []byte) error {
	targetPath := filepath.Join(rootfsPath, hostAuthPath)

	// Ensure the parent directory exists.
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("creating auth directory: %w", err)
	}

	// Write with restrictive permissions (readable by root only).
	if err := os.WriteFile(targetPath, data, 0o600); err != nil {
		return fmt.Errorf("writing auth file: %w", err)
	}

	return nil
}

// RemoveAuthFile removes the bootc auth file from the host via the
// rootfs mount. It is not an error if the file does not exist.
func RemoveAuthFile(rootfsPath string) error {
	targetPath := filepath.Join(rootfsPath, hostAuthPath)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing auth file: %w", err)
	}
	return nil
}
