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

package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/pkg/bootc"
)

// --- Fake bootc client ---

type fakeBootcClient struct {
	isBootcHost       bool
	statusResult      *bootc.Host
	statusErr         error
	switchCalls       []string
	upgradeDownload   int
	upgradeApplyCalls []bool // records softReboot arg
	rollbackCalls     []bool // records apply arg
	switchErr         error
	upgradeDownErr    error
	upgradeApplyErr   error
	rollbackErr       error

	// postSwitchStatus replaces statusResult after Switch is called.
	postSwitchStatus *bootc.Host
	// postUpgradeDownloadStatus replaces statusResult after UpgradeDownloadOnly.
	postUpgradeDownloadStatus *bootc.Host
}

func (f *fakeBootcClient) IsBootcHost(context.Context) bool {
	return f.isBootcHost
}

func (f *fakeBootcClient) Status(context.Context) (*bootc.Host, error) {
	return f.statusResult, f.statusErr
}

func (f *fakeBootcClient) Switch(_ context.Context, image string) error {
	f.switchCalls = append(f.switchCalls, image)
	if f.switchErr != nil {
		return f.switchErr
	}
	if f.postSwitchStatus != nil {
		f.statusResult = f.postSwitchStatus
	}
	return nil
}

func (f *fakeBootcClient) UpgradeDownloadOnly(context.Context) error {
	f.upgradeDownload++
	if f.upgradeDownErr != nil {
		return f.upgradeDownErr
	}
	if f.postUpgradeDownloadStatus != nil {
		f.statusResult = f.postUpgradeDownloadStatus
	}
	return nil
}

func (f *fakeBootcClient) UpgradeApply(_ context.Context, softReboot bool) error {
	f.upgradeApplyCalls = append(f.upgradeApplyCalls, softReboot)
	return f.upgradeApplyErr
}

func (f *fakeBootcClient) Rollback(_ context.Context, apply bool) error {
	f.rollbackCalls = append(f.rollbackCalls, apply)
	return f.rollbackErr
}

// --- Fake kube client ---

type fakeKubeClient struct {
	bootcNodes    map[string]*v1alpha1.BootcNode
	nodes         map[string]*corev1.Node
	createErr     error
	updateErr     error
	lastUpdated   *v1alpha1.BootcNode
	statusUpdates int
	createCalls   int
}

func newFakeKubeClient() *fakeKubeClient {
	return &fakeKubeClient{
		bootcNodes: make(map[string]*v1alpha1.BootcNode),
		nodes:      make(map[string]*corev1.Node),
	}
}

func (f *fakeKubeClient) GetBootcNode(_ context.Context, name string) (*v1alpha1.BootcNode, error) {
	if bn, ok := f.bootcNodes[name]; ok {
		return bn.DeepCopy(), nil
	}
	return nil, errors.NewNotFound(schema.GroupResource{
		Group:    "bootc.dev",
		Resource: "bootcnodes",
	}, name)
}

func (f *fakeKubeClient) CreateBootcNode(_ context.Context, node *v1alpha1.BootcNode) (*v1alpha1.BootcNode, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	copy := node.DeepCopy()
	copy.UID = "test-uid"
	f.bootcNodes[node.Name] = copy
	return copy, nil
}

func (f *fakeKubeClient) UpdateBootcNodeStatus(_ context.Context, node *v1alpha1.BootcNode) (*v1alpha1.BootcNode, error) {
	f.statusUpdates++
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	copy := node.DeepCopy()
	f.bootcNodes[node.Name] = copy
	f.lastUpdated = copy
	return copy, nil
}

func (f *fakeKubeClient) GetNode(_ context.Context, name string) (*corev1.Node, error) {
	if n, ok := f.nodes[name]; ok {
		return n, nil
	}
	return nil, errors.NewNotFound(schema.GroupResource{
		Group:    "",
		Resource: "nodes",
	}, name)
}

// --- Helper constructors ---

func makeHost(bootedImage, stagedImage string) *bootc.Host {
	host := &bootc.Host{
		APIVersion: "org.containers.bootc/v1",
		Kind:       "BootcHost",
		Status: bootc.HostStatus{
			Booted: &bootc.BootEntry{
				Image: &bootc.ImageStatus{
					Image:       bootc.ImageReference{Image: stripDigest(bootedImage)},
					ImageDigest: extractDigest(bootedImage),
				},
			},
		},
	}
	if stagedImage != "" {
		host.Status.Staged = &bootc.BootEntry{
			Image: &bootc.ImageStatus{
				Image:       bootc.ImageReference{Image: stripDigest(stagedImage)},
				ImageDigest: extractDigest(stagedImage),
			},
		}
	}
	return host
}

func makeHostSoftRebootCapable(bootedImage, stagedImage string) *bootc.Host {
	host := makeHost(bootedImage, stagedImage)
	if host.Status.Staged != nil {
		host.Status.Staged.SoftRebootCapable = true
	}
	return host
}

func stripDigest(ref string) string {
	if idx := indexFromEnd(ref, '@'); idx >= 0 {
		return ref[:idx]
	}
	return ref
}

func extractDigest(ref string) string {
	if idx := indexFromEnd(ref, '@'); idx >= 0 {
		return ref[idx+1:]
	}
	return ""
}

func makeBootcNode(name string, spec v1alpha1.BootcNodeSpec) *v1alpha1.BootcNode {
	return &v1alpha1.BootcNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  "test-uid",
		},
		Spec: spec,
	}
}

func makeNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  "node-uid-123",
		},
	}
}

const (
	testNode        = "node-1"
	testImage       = "quay.io/example/image@sha256:aaa111"
	testImageDigest = "sha256:aaa111"
	testImage2      = "quay.io/example/image@sha256:bbb222"
	testOtherImage  = "quay.io/example/other@sha256:ccc333"
)

// --- Tests ---

func TestReconcileNoDesiredImage(t *testing.T) {
	// When no pool has claimed the node (spec is empty), the daemon
	// should just report Ready status.
	bc := &fakeBootcClient{statusResult: makeHost(testImage, "")}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{})

	status, interval := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("expected phase Ready, got %s", status.Phase)
	}
	if interval != 30*time.Second {
		t.Errorf("expected slow poll interval, got %v", interval)
	}
	if status.Booted.Image == "" {
		t.Error("expected booted image to be reported")
	}
}

func TestReconcileAlreadyAtDesiredImage(t *testing.T) {
	// When the node is already running the desired image and the
	// desired phase is not RollingBack, it should report Ready.
	bc := &fakeBootcClient{statusResult: makeHost(testImage, "")}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseStaged,
	})

	status, interval := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("expected phase Ready, got %s", status.Phase)
	}
	if interval != 30*time.Second {
		t.Errorf("expected slow poll interval, got %v", interval)
	}
}

func TestReconcileStagedAlreadyStaged(t *testing.T) {
	// When desired phase is Staged and the image is already staged,
	// report Staged.
	host := makeHost(testImage, testImage2)
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseStaged,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseStaged {
		t.Errorf("expected phase Staged, got %s", status.Phase)
	}
	// No bootc commands should have been called.
	if len(bc.switchCalls) > 0 || bc.upgradeDownload > 0 {
		t.Error("expected no bootc staging commands")
	}
}

func TestReconcileStagedNeedsSwitchDifferentRepo(t *testing.T) {
	// When the desired image is from a different repo, bootc switch
	// should be called.
	host := makeHost(testImage, "")
	postHost := makeHost(testImage, testOtherImage)

	bc := &fakeBootcClient{
		statusResult:     host,
		postSwitchStatus: postHost,
	}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testOtherImage,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseStaged,
	})

	status, interval := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseStaged {
		t.Errorf("expected phase Staged, got %s", status.Phase)
	}
	if len(bc.switchCalls) != 1 || bc.switchCalls[0] != testOtherImage {
		t.Errorf("expected switch call with %s, got %v", testOtherImage, bc.switchCalls)
	}
	if interval != fastPollInterval {
		t.Errorf("expected fast poll interval after staging, got %v", interval)
	}
}

func TestReconcileStagedNeedsUpgradeSameRepo(t *testing.T) {
	// When the desired image is from the same repo (different digest),
	// bootc upgrade --download-only should be called.
	host := makeHost(testImage, "")
	postHost := makeHost(testImage, testImage2)

	bc := &fakeBootcClient{
		statusResult:              host,
		postUpgradeDownloadStatus: postHost,
	}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseStaged,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseStaged {
		t.Errorf("expected phase Staged, got %s", status.Phase)
	}
	if bc.upgradeDownload != 1 {
		t.Errorf("expected 1 upgrade --download-only call, got %d", bc.upgradeDownload)
	}
	if len(bc.switchCalls) > 0 {
		t.Error("expected no switch calls for same-repo upgrade")
	}
}

func TestReconcileStagedStagingError(t *testing.T) {
	// When staging fails, the daemon should report Error phase.
	host := makeHost(testImage, "")
	bc := &fakeBootcClient{
		statusResult:   host,
		upgradeDownErr: fmt.Errorf("disk full"),
	}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseStaged,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("expected phase Error, got %s", status.Phase)
	}
	if status.Message == "" {
		t.Error("expected error message to be set")
	}
}

func TestReconcileRebootingAlreadyBooted(t *testing.T) {
	// When in Rebooting phase but already booted into the desired
	// image (post-reboot), report Ready.
	host := makeHost(testImage2, "")
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRebooting,
	})

	status, interval := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("expected phase Ready, got %s", status.Phase)
	}
	if interval != 30*time.Second {
		t.Errorf("expected slow poll interval, got %v", interval)
	}
	if len(bc.upgradeApplyCalls) > 0 {
		t.Error("expected no upgrade --apply calls when already booted")
	}
}

func TestReconcileRebootingImageStaged(t *testing.T) {
	// When in Rebooting phase with image staged, apply and reboot.
	host := makeHost(testImage, testImage2)
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRebooting,
		RebootPolicy: v1alpha1.RebootPolicyAuto,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseRebooting {
		t.Errorf("expected phase Rebooting, got %s", status.Phase)
	}
	if len(bc.upgradeApplyCalls) != 1 {
		t.Fatalf("expected 1 upgrade --apply call, got %d", len(bc.upgradeApplyCalls))
	}
}

func TestReconcileRebootingSoftRebootAuto(t *testing.T) {
	// With Auto reboot policy and softRebootCapable staged image,
	// soft reboot should be used.
	host := makeHostSoftRebootCapable(testImage, testImage2)
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRebooting,
		RebootPolicy: v1alpha1.RebootPolicyAuto,
	})

	d.reconcile(context.Background(), bn, bc.statusResult)

	if len(bc.upgradeApplyCalls) != 1 {
		t.Fatalf("expected 1 upgrade --apply call, got %d", len(bc.upgradeApplyCalls))
	}
	if !bc.upgradeApplyCalls[0] {
		t.Error("expected soft reboot with Auto policy and capable image")
	}
}

func TestReconcileRebootingFullPolicy(t *testing.T) {
	// With Full reboot policy, soft reboot should NOT be used even if
	// the staged image supports it.
	host := makeHostSoftRebootCapable(testImage, testImage2)
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRebooting,
		RebootPolicy: v1alpha1.RebootPolicyFull,
	})

	d.reconcile(context.Background(), bn, bc.statusResult)

	if len(bc.upgradeApplyCalls) != 1 {
		t.Fatalf("expected 1 upgrade --apply call, got %d", len(bc.upgradeApplyCalls))
	}
	if bc.upgradeApplyCalls[0] {
		t.Error("expected NO soft reboot with Full policy")
	}
}

func TestReconcileRebootingImageNotStaged(t *testing.T) {
	// When in Rebooting phase but the image is not staged (e.g. GC'd),
	// report Error.
	host := makeHost(testImage, "")
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRebooting,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("expected phase Error, got %s", status.Phase)
	}
	if status.Message == "" {
		t.Error("expected error message about image not staged")
	}
}

func TestReconcileRebootingApplyError(t *testing.T) {
	// When apply fails, report Error.
	host := makeHost(testImage, testImage2)
	bc := &fakeBootcClient{
		statusResult:    host,
		upgradeApplyErr: fmt.Errorf("bootloader error"),
	}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage2,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRebooting,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("expected phase Error, got %s", status.Phase)
	}
}

func TestReconcileRollingBack(t *testing.T) {
	// RollingBack desired phase should call rollback --apply.
	host := makeHost(testImage2, "")
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRollingBack,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseRollingBack {
		t.Errorf("expected phase RollingBack, got %s", status.Phase)
	}
	if len(bc.rollbackCalls) != 1 || !bc.rollbackCalls[0] {
		t.Error("expected rollback --apply call")
	}
}

func TestReconcileRollingBackError(t *testing.T) {
	// When rollback fails, report Error.
	host := makeHost(testImage2, "")
	bc := &fakeBootcClient{
		statusResult: host,
		rollbackErr:  fmt.Errorf("no previous deployment"),
	}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRollingBack,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("expected phase Error, got %s", status.Phase)
	}
}

func TestReconcileRollingBackWhenAtDesiredImage(t *testing.T) {
	// Even if the node is running the desired image, if the desired
	// phase is RollingBack, it should execute the rollback (the
	// operator is telling us the image is bad).
	host := makeHost(testImage, "")
	bc := &fakeBootcClient{statusResult: host}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)
	bn := makeBootcNode(testNode, v1alpha1.BootcNodeSpec{
		DesiredImage: testImage,
		DesiredPhase: v1alpha1.BootcNodeDesiredPhaseRollingBack,
	})

	status, _ := d.reconcile(context.Background(), bn, bc.statusResult)

	if status.Phase != v1alpha1.BootcNodePhaseRollingBack {
		t.Errorf("expected phase RollingBack, got %s", status.Phase)
	}
	if len(bc.rollbackCalls) != 1 {
		t.Error("expected rollback call even when at desired image")
	}
}

func TestEnsureBootcNodeCreatesNew(t *testing.T) {
	// ensureBootcNode should create a BootcNode when one doesn't exist.
	host := makeHost(testImage, "")
	bc := &fakeBootcClient{
		isBootcHost:  true,
		statusResult: host,
	}
	kc := newFakeKubeClient()
	kc.nodes[testNode] = makeNode(testNode)

	d := NewDaemon(testNode, 30*time.Second, kc, bc)

	err := d.ensureBootcNode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if kc.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", kc.createCalls)
	}
	// Status should also be updated after create.
	if kc.statusUpdates != 1 {
		t.Errorf("expected 1 status update, got %d", kc.statusUpdates)
	}

	bn := kc.bootcNodes[testNode]
	if bn == nil {
		t.Fatal("expected BootcNode to be stored")
	}
	if len(bn.OwnerReferences) != 1 {
		t.Errorf("expected 1 owner reference, got %d", len(bn.OwnerReferences))
	}
	if bn.OwnerReferences[0].Kind != "Node" {
		t.Errorf("expected owner ref kind Node, got %s", bn.OwnerReferences[0].Kind)
	}
}

func TestEnsureBootcNodeAlreadyExists(t *testing.T) {
	// ensureBootcNode should be a no-op when the BootcNode exists.
	bc := &fakeBootcClient{
		isBootcHost:  true,
		statusResult: makeHost(testImage, ""),
	}
	kc := newFakeKubeClient()
	kc.bootcNodes[testNode] = makeBootcNode(testNode, v1alpha1.BootcNodeSpec{})
	// Also store a second node to verify makeBootcNode works with
	// different names.
	kc.bootcNodes["node-2"] = makeBootcNode("node-2", v1alpha1.BootcNodeSpec{})

	d := NewDaemon(testNode, 30*time.Second, kc, bc)

	err := d.ensureBootcNode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if kc.createCalls != 0 {
		t.Errorf("expected no create calls, got %d", kc.createCalls)
	}
}

func TestRunNotBootcHost(t *testing.T) {
	// When the host is not a bootc system, Run should block until
	// context is cancelled.
	bc := &fakeBootcClient{isBootcHost: false}
	kc := newFakeKubeClient()

	d := NewDaemon(testNode, 30*time.Second, kc, bc)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := d.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNeedsSwitch(t *testing.T) {
	tests := []struct {
		name     string
		booted   string
		desired  string
		expected bool
	}{
		{
			name:     "same repo different digest",
			booted:   "quay.io/example/image@sha256:aaa",
			desired:  "quay.io/example/image@sha256:bbb",
			expected: false,
		},
		{
			name:     "different repo",
			booted:   "quay.io/example/image@sha256:aaa",
			desired:  "quay.io/example/other@sha256:bbb",
			expected: true,
		},
		{
			name:     "same repo with tag",
			booted:   "quay.io/example/image:latest",
			desired:  "quay.io/example/image@sha256:bbb",
			expected: false,
		},
		{
			name:     "different registry",
			booted:   "registry.example.com/image@sha256:aaa",
			desired:  "quay.io/example/image@sha256:bbb",
			expected: true,
		},
		{
			name:     "same image no digest or tag",
			booted:   "quay.io/example/image",
			desired:  "quay.io/example/image",
			expected: false,
		},
		{
			name:     "registry with port same repo",
			booted:   "localhost:5000/image@sha256:aaa",
			desired:  "localhost:5000/image@sha256:bbb",
			expected: false,
		},
		{
			name:     "registry with port different repo",
			booted:   "localhost:5000/image@sha256:aaa",
			desired:  "localhost:5000/other@sha256:bbb",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsSwitch(tt.booted, tt.desired)
			if got != tt.expected {
				t.Errorf("needsSwitch(%q, %q) = %v, want %v", tt.booted, tt.desired, got, tt.expected)
			}
		})
	}
}

func TestImageRepo(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"quay.io/example/image@sha256:abc", "quay.io/example/image"},
		{"quay.io/example/image:latest", "quay.io/example/image"},
		{"quay.io/example/image", "quay.io/example/image"},
		{"localhost:5000/image@sha256:abc", "localhost:5000/image"},
		{"localhost:5000/image:v1", "localhost:5000/image"},
		{"localhost:5000/image", "localhost:5000/image"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := imageRepo(tt.input)
			if got != tt.expected {
				t.Errorf("imageRepo(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestShouldSoftReboot(t *testing.T) {
	capableHost := makeHostSoftRebootCapable(testImage, testImage2)
	notCapableHost := makeHost(testImage, testImage2)
	noStagedHost := makeHost(testImage, "")

	tests := []struct {
		name     string
		policy   v1alpha1.RebootPolicy
		host     *bootc.Host
		expected bool
	}{
		{"Auto + capable", v1alpha1.RebootPolicyAuto, capableHost, true},
		{"Auto + not capable", v1alpha1.RebootPolicyAuto, notCapableHost, false},
		{"Auto + no staged", v1alpha1.RebootPolicyAuto, noStagedHost, false},
		{"Full + capable", v1alpha1.RebootPolicyFull, capableHost, false},
		{"Full + not capable", v1alpha1.RebootPolicyFull, notCapableHost, false},
		{"Never + capable", v1alpha1.RebootPolicyNever, capableHost, false},
		{"empty + capable (defaults to Auto)", "", capableHost, true},
		{"empty + not capable", "", notCapableHost, false},
		{"Auto + nil host", v1alpha1.RebootPolicyAuto, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSoftReboot(tt.policy, tt.host)
			if got != tt.expected {
				t.Errorf("shouldSoftReboot(%q, ...) = %v, want %v", tt.policy, got, tt.expected)
			}
		})
	}
}
