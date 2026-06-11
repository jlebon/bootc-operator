// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	testutil "github.com/jlebon/bootc-operator/test/util"

	"github.com/jlebon/bootc-operator/internal/bootc"
)

type fakeExecutor struct {
	mu        sync.Mutex
	status    bootc.Status
	statusErr error

	switchErr    error
	switchImg    string
	switchApply  bool
	switchHook   func()
}

func (f *fakeExecutor) Status(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	data, err := json.Marshal(f.status)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (f *fakeExecutor) Switch(_ context.Context, image string, apply bool) error {
	f.mu.Lock()
	f.switchImg = image
	f.switchApply = apply
	hook := f.switchHook
	err := f.switchErr
	f.mu.Unlock()

	if hook != nil {
		hook()
	}
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	_, digest, _ := strings.Cut(image, "@")
	f.status.Status.Staged = newBootEntry(image, digest)
	return nil
}

func (f *fakeExecutor) setStatusErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusErr = err
}

func (f *fakeExecutor) setSwitchErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchErr = err
}

func (f *fakeExecutor) setSwitchHook(hook func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchHook = hook
}

func (f *fakeExecutor) getSwitchImg() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.switchImg
}

func (f *fakeExecutor) getSwitchApply() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.switchApply
}

func (f *fakeExecutor) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = bootc.Status{}
	f.statusErr = nil
	f.switchErr = nil
	f.switchImg = ""
	f.switchHook = nil
	f.switchApply = false
}

func newBootEntry(image, digest string) *bootc.BootEntry {
	return &bootc.BootEntry{
		Image: &bootc.ImageStatus{
			Image:        bootc.ImageReference{Image: image, Transport: "registry"},
			ImageDigest:  digest,
			Architecture: "amd64",
		},
	}
}

func newBootcStatus(bootedDigest string) bootc.Status {
	return bootc.Status{
		APIVersion: "org.containers.bootc/v1alpha1",
		Kind:       "BootcHost",
		Spec: bootc.StatusSpec{
			Image:     &bootc.ImageReference{Image: testutil.ImageTaggedRef, Transport: "registry"},
			BootOrder: "default",
		},
		Status: bootc.StatusBody{
			Booted: newBootEntry(testutil.ImageTaggedRef, bootedDigest),
		},
	}
}
