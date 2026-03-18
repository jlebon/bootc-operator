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
	"encoding/base64"
	"fmt"
)

// AuthFilePath is the path on the host where bootc looks for registry
// credentials. This is the highest-priority auth path that bootc checks
// (above /etc/ostree/auth.json and /usr/lib/ostree/auth.json) and is
// ephemeral (cleared on reboot).
const AuthFilePath = "/run/ostree/auth.json"

// WriteAuthFile writes dockerconfigjson credentials to AuthFilePath on
// the host via nsenter. The daemon calls this before running bootc
// commands that need registry authentication.
//
// The data is base64-encoded and decoded on the host side to avoid shell
// escaping issues with the JSON content.
func WriteAuthFile(ctx context.Context, runner CommandRunner, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)

	// Create the directory and write the file in a single nsenter call
	// to minimise round trips. The base64 decode avoids quoting issues.
	script := fmt.Sprintf(
		"mkdir -p /run/ostree && echo '%s' | base64 -d > %s",
		encoded, AuthFilePath,
	)
	_, err := runner.Run(ctx, "nsenter", "-t", "1", "-m", "--", "sh", "-c", script)
	if err != nil {
		return fmt.Errorf("writing auth file to host: %w", err)
	}
	return nil
}

// RemoveAuthFile removes the auth file from the host. This is called
// after bootc commands complete to avoid leaving credentials on disk
// longer than necessary.
func RemoveAuthFile(ctx context.Context, runner CommandRunner) error {
	_, err := runner.Run(ctx, "nsenter", "-t", "1", "-m", "--", "rm", "-f", AuthFilePath)
	if err != nil {
		return fmt.Errorf("removing auth file from host: %w", err)
	}
	return nil
}
