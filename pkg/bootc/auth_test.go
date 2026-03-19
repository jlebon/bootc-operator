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
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAuthFile(t *testing.T) {
	tmpDir := t.TempDir()
	data := []byte(`{"auths":{"quay.io":{"auth":"dXNlcjpwYXNz"}}}`)

	if err := WriteAuthFile(tmpDir, data); err != nil {
		t.Fatalf("WriteAuthFile() error: %v", err)
	}

	targetPath := filepath.Join(tmpDir, hostAuthPath)
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("Failed to read auth file: %v", err)
	}
	if string(content) != string(data) {
		t.Errorf("Auth file content mismatch: got %q, want %q", string(content), string(data))
	}

	// Verify file permissions.
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("Failed to stat auth file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("Expected permissions 0600, got %o", info.Mode().Perm())
	}
}

func TestWriteAuthFileCreatesParentDirs(t *testing.T) {
	tmpDir := t.TempDir()
	data := []byte(`{}`)

	// The parent dir /run/ostree/ should be created automatically.
	if err := WriteAuthFile(tmpDir, data); err != nil {
		t.Fatalf("WriteAuthFile() error: %v", err)
	}

	parentDir := filepath.Join(tmpDir, filepath.Dir(hostAuthPath))
	if _, err := os.Stat(parentDir); os.IsNotExist(err) {
		t.Error("Expected parent directory to be created")
	}
}

func TestRemoveAuthFile(t *testing.T) {
	tmpDir := t.TempDir()
	data := []byte(`{}`)

	// Write first, then remove.
	if err := WriteAuthFile(tmpDir, data); err != nil {
		t.Fatalf("WriteAuthFile() error: %v", err)
	}
	if err := RemoveAuthFile(tmpDir); err != nil {
		t.Fatalf("RemoveAuthFile() error: %v", err)
	}

	targetPath := filepath.Join(tmpDir, hostAuthPath)
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Error("Expected auth file to be removed")
	}
}

func TestRemoveAuthFileNotExist(t *testing.T) {
	tmpDir := t.TempDir()

	// Should not error when file doesn't exist.
	if err := RemoveAuthFile(tmpDir); err != nil {
		t.Fatalf("RemoveAuthFile() error for non-existent file: %v", err)
	}
}
