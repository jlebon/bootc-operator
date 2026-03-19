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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/pkg/bootc"
)

// --- Mock KubeClient ---

type mockKubeClient struct {
	bootcNodes map[string]*v1alpha1.BootcNode
	nodes      map[string]*corev1.Node
	createErr  error
	updateErr  error
	statusErr  error

	// Track calls for assertions.
	statusUpdates []*v1alpha1.BootcNode
}

func newMockKubeClient() *mockKubeClient {
	return &mockKubeClient{
		bootcNodes: make(map[string]*v1alpha1.BootcNode),
		nodes:      make(map[string]*corev1.Node),
	}
}

func (m *mockKubeClient) GetBootcNode(_ context.Context, name string) (*v1alpha1.BootcNode, error) {
	bn, ok := m.bootcNodes[name]
	if !ok {
		return nil, nil
	}
	return bn.DeepCopy(), nil
}

func (m *mockKubeClient) CreateBootcNode(_ context.Context, bn *v1alpha1.BootcNode) error {
	if m.createErr != nil {
		return m.createErr
	}
	// Simulate server setting resourceVersion.
	bn.ResourceVersion = "1"
	m.bootcNodes[bn.Name] = bn.DeepCopy()
	return nil
}

func (m *mockKubeClient) UpdateBootcNode(_ context.Context, bn *v1alpha1.BootcNode) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.bootcNodes[bn.Name] = bn.DeepCopy()
	return nil
}

func (m *mockKubeClient) UpdateBootcNodeStatus(_ context.Context, bn *v1alpha1.BootcNode) error {
	if m.statusErr != nil {
		return m.statusErr
	}
	if existing, ok := m.bootcNodes[bn.Name]; ok {
		existing.Status = *bn.Status.DeepCopy()
		m.bootcNodes[bn.Name] = existing
	} else {
		m.bootcNodes[bn.Name] = bn.DeepCopy()
	}
	m.statusUpdates = append(m.statusUpdates, bn.DeepCopy())
	return nil
}

func (m *mockKubeClient) GetNode(_ context.Context, name string) (*corev1.Node, error) {
	node, ok := m.nodes[name]
	if !ok {
		return nil, fmt.Errorf("node %q not found", name)
	}
	return node, nil
}

// --- Mock BootcClient ---

type mockBootcClient struct {
	isBootcHost     bool
	statusResult    *bootc.Host
	statusErr       error
	switchErr       error
	upgradeErr      error
	upgradeApplyErr error
	rollbackErr     error

	// Track calls.
	switchCalls       []string
	upgradeCalls      int
	upgradeApplyCalls []bool
	rollbackCalls     []bool
}

func newMockBootcClient() *mockBootcClient {
	return &mockBootcClient{
		isBootcHost: true,
	}
}

func (m *mockBootcClient) IsBootcHost(_ context.Context) bool {
	return m.isBootcHost
}

func (m *mockBootcClient) Status(_ context.Context) (*bootc.Host, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResult, nil
}

func (m *mockBootcClient) Switch(_ context.Context, image string) error {
	m.switchCalls = append(m.switchCalls, image)
	return m.switchErr
}

func (m *mockBootcClient) UpgradeDownloadOnly(_ context.Context) error {
	m.upgradeCalls++
	return m.upgradeErr
}

func (m *mockBootcClient) UpgradeApply(_ context.Context, softReboot bool) error {
	m.upgradeApplyCalls = append(m.upgradeApplyCalls, softReboot)
	return m.upgradeApplyErr
}

func (m *mockBootcClient) Rollback(_ context.Context, apply bool) error {
	m.rollbackCalls = append(m.rollbackCalls, apply)
	return m.rollbackErr
}

// --- Test Helpers ---

const (
	testNodeName    = "test-node-1"
	testNodeUID     = "test-uid-123"
	testImage       = "quay.io/example/test-image"
	testDigest      = "sha256:abc123def456"
	testImageRef    = testImage + "@" + testDigest
	testOldDigest   = "sha256:old111old222"
	testOldImageRef = testImage + "@" + testOldDigest
)

func testNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNodeName,
			UID:  types.UID(testNodeUID),
		},
	}
}

func testBootcNode() *v1alpha1.BootcNode {
	return &v1alpha1.BootcNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testNodeName,
			ResourceVersion: "1",
		},
		Status: v1alpha1.BootcNodeStatus{
			Phase: v1alpha1.BootcNodePhaseReady,
		},
	}
}

func bootedOnlyHost(imageRef, digest string) *bootc.Host {
	return &bootc.Host{
		Spec: bootc.HostSpec{
			Image: &bootc.ImageReference{Image: imageRef, Transport: "registry"},
		},
		Status: bootc.HostStatus{
			Booted: &bootc.BootEntry{
				Image: &bootc.ImageStatus{
					Image:       bootc.ImageReference{Image: imageRef, Transport: "registry"},
					ImageDigest: digest,
				},
			},
		},
	}
}

func stagedHost(imageRef, bootedDigest, stagedDigest string, softRebootCapable, downloadOnly bool) *bootc.Host {
	return &bootc.Host{
		Spec: bootc.HostSpec{
			Image: &bootc.ImageReference{Image: imageRef, Transport: "registry"},
		},
		Status: bootc.HostStatus{
			Booted: &bootc.BootEntry{
				Image: &bootc.ImageStatus{
					Image:       bootc.ImageReference{Image: imageRef, Transport: "registry"},
					ImageDigest: bootedDigest,
				},
			},
			Staged: &bootc.BootEntry{
				Image: &bootc.ImageStatus{
					Image:       bootc.ImageReference{Image: imageRef, Transport: "registry"},
					ImageDigest: stagedDigest,
				},
				SoftRebootCapable: softRebootCapable,
				DownloadOnly:      downloadOnly,
			},
		},
	}
}

func newTestDaemon(kc *mockKubeClient, bc *mockBootcClient) *Daemon {
	return NewDaemon(testNodeName, slowPollInterval, kc, bc, logr.Discard())
}

// --- Tests ---

func TestRunNonBootcHost(t *testing.T) {
	kc := newMockKubeClient()
	bc := newMockBootcClient()
	bc.isBootcHost = false

	d := newTestDaemon(kc, bc)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := d.Run(ctx)
	if err != nil {
		t.Fatalf("Run() returned error for non-bootc host: %v", err)
	}

	// Should not create a BootcNode.
	if len(kc.bootcNodes) != 0 {
		t.Error("Should not create BootcNode on non-bootc host")
	}
}

func TestEnsureBootcNodeCreation(t *testing.T) {
	kc := newMockKubeClient()
	kc.nodes[testNodeName] = testNode()

	bc := newMockBootcClient()
	bc.statusResult = bootedOnlyHost(testImage+":latest", testOldDigest)

	d := newTestDaemon(kc, bc)

	if err := d.ensureBootcNode(context.Background()); err != nil {
		t.Fatalf("ensureBootcNode() error: %v", err)
	}

	bn, ok := kc.bootcNodes[testNodeName]
	if !ok {
		t.Fatal("Expected BootcNode to be created")
	}

	// Check ownerReference.
	if len(bn.OwnerReferences) != 1 {
		t.Fatalf("Expected 1 ownerReference, got %d", len(bn.OwnerReferences))
	}
	ref := bn.OwnerReferences[0]
	if ref.Kind != "Node" || ref.Name != testNodeName || string(ref.UID) != testNodeUID {
		t.Errorf("Unexpected ownerReference: %+v", ref)
	}

	// Check initial status.
	if bn.Status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("Expected phase Ready, got %s", bn.Status.Phase)
	}
	if bn.Status.BootedDigest != testOldDigest {
		t.Errorf("Expected bootedDigest %s, got %s", testOldDigest, bn.Status.BootedDigest)
	}
}

func TestEnsureBootcNodeAlreadyExists(t *testing.T) {
	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = testBootcNode()

	bc := newMockBootcClient()
	bc.statusResult = bootedOnlyHost(testImage+":latest", testOldDigest)

	d := newTestDaemon(kc, bc)

	if err := d.ensureBootcNode(context.Background()); err != nil {
		t.Fatalf("ensureBootcNode() error: %v", err)
	}

	bn := kc.bootcNodes[testNodeName]
	if bn.Status.BootedDigest != testOldDigest {
		t.Errorf("Expected bootedDigest to be updated to %s, got %s", testOldDigest, bn.Status.BootedDigest)
	}
}

func TestReconcileNoDesiredImage(t *testing.T) {
	bn := testBootcNode()
	host := bootedOnlyHost(testImage+":latest", testOldDigest)

	kc := newMockKubeClient()
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	interval := d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("Expected phase Ready, got %s", bn.Status.Phase)
	}
	if interval != slowPollInterval {
		t.Errorf("Expected slow poll interval, got %v", interval)
	}
}

func TestReconcileStagedAlreadyBootedDesired(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged

	// Already booted into the desired image.
	host := bootedOnlyHost(testImage, testDigest)

	kc := newMockKubeClient()
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	interval := d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("Expected phase Ready, got %s", bn.Status.Phase)
	}
	if interval != slowPollInterval {
		t.Errorf("Expected slow poll interval, got %v", interval)
	}
}

func TestReconcileStagedAlreadyStaged(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged

	// Desired image already staged, booted image is an older version.
	host := stagedHost(testImage+":latest", testOldDigest, testDigest, true, true)

	kc := newMockKubeClient()
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	interval := d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseStaged {
		t.Errorf("Expected phase Staged, got %s", bn.Status.Phase)
	}
	if interval != slowPollInterval {
		t.Errorf("Expected slow poll interval, got %v", interval)
	}
}

func TestReconcileStagedNeedsUpgrade(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged

	// Same image, different digest -- needs upgrade --download-only.
	host := bootedOnlyHost(testImage+":latest", testOldDigest)

	kc := newMockKubeClient()
	bc := newMockBootcClient()
	bc.statusResult = host
	d := newTestDaemon(kc, bc)

	// After staging, bootc status should return the staged image.
	bc.statusResult = stagedHost(testImage+":latest", testOldDigest, testDigest, true, true)

	interval := d.reconcile(context.Background(), bn, host)

	if bc.upgradeCalls != 1 {
		t.Errorf("Expected 1 UpgradeDownloadOnly call, got %d", bc.upgradeCalls)
	}
	if len(bc.switchCalls) != 0 {
		t.Errorf("Expected no Switch calls, got %d", len(bc.switchCalls))
	}
	if bn.Status.Phase != v1alpha1.BootcNodePhaseStaged {
		t.Errorf("Expected phase Staged, got %s", bn.Status.Phase)
	}
	if interval != fastPollInterval {
		t.Errorf("Expected fast poll interval, got %v", interval)
	}
}

func TestReconcileStagedNeedsSwitch(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = "quay.io/example/new-image@" + testDigest
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged

	// Different image ref -- needs switch.
	host := bootedOnlyHost(testImage+":latest", testOldDigest)

	kc := newMockKubeClient()
	bc := newMockBootcClient()
	bc.statusResult = stagedHost("quay.io/example/new-image:latest", testOldDigest, testDigest, false, false)
	d := newTestDaemon(kc, bc)

	interval := d.reconcile(context.Background(), bn, host)

	if len(bc.switchCalls) != 1 {
		t.Fatalf("Expected 1 Switch call, got %d", len(bc.switchCalls))
	}
	if bc.switchCalls[0] != "quay.io/example/new-image@"+testDigest {
		t.Errorf("Switch called with wrong image: %s", bc.switchCalls[0])
	}
	if bc.upgradeCalls != 0 {
		t.Errorf("Expected no UpgradeDownloadOnly calls, got %d", bc.upgradeCalls)
	}
	if bn.Status.Phase != v1alpha1.BootcNodePhaseStaged {
		t.Errorf("Expected phase Staged, got %s", bn.Status.Phase)
	}
	if interval != fastPollInterval {
		t.Errorf("Expected fast poll interval, got %v", interval)
	}
}

func TestReconcileStagedSwitchError(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = "quay.io/example/new-image@" + testDigest
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged

	host := bootedOnlyHost(testImage+":latest", testOldDigest)

	kc := newMockKubeClient()
	bc := newMockBootcClient()
	bc.switchErr = fmt.Errorf("switch failed")
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("Expected phase Error, got %s", bn.Status.Phase)
	}
	if bn.Status.Message == "" {
		t.Error("Expected error message to be set")
	}
}

func TestReconcileStagedUpgradeError(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged

	host := bootedOnlyHost(testImage+":latest", testOldDigest)

	kc := newMockKubeClient()
	bc := newMockBootcClient()
	bc.upgradeErr = fmt.Errorf("download failed")
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("Expected phase Error, got %s", bn.Status.Phase)
	}
}

func TestReconcileRebootingStagedDigestMismatch(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting

	// Staged image has a different digest than desired (wrong image staged).
	wrongDigest := "sha256:wrong999wrong888"
	host := stagedHost(testImage+":latest", testOldDigest, wrongDigest, false, true)

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("Expected phase Error for staged digest mismatch, got %s", bn.Status.Phase)
	}
	if len(bc.upgradeApplyCalls) != 0 {
		t.Error("Should not call UpgradeApply when staged digest doesn't match desired")
	}
}

func TestReconcileRebootingAlreadyBooted(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting

	// Already booted into desired image (post-reboot).
	host := bootedOnlyHost(testImage+":latest", testDigest)

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	interval := d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("Expected phase Ready, got %s", bn.Status.Phase)
	}
	if interval != slowPollInterval {
		t.Errorf("Expected slow poll interval, got %v", interval)
	}
	// Should not call UpgradeApply since already booted.
	if len(bc.upgradeApplyCalls) != 0 {
		t.Error("Should not call UpgradeApply when already booted into desired image")
	}
}

func TestReconcileRebootingStagedLost(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting

	// Not booted into desired, and staging lost.
	host := bootedOnlyHost(testImage+":latest", testOldDigest)

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("Expected phase Error, got %s", bn.Status.Phase)
	}
	if len(bc.upgradeApplyCalls) != 0 {
		t.Error("Should not call UpgradeApply when staged image is lost")
	}
}

func TestReconcileRebootingApplySoftReboot(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
	bn.Spec.RebootPolicy = v1alpha1.RebootPolicyAuto

	// Staged with soft reboot capability.
	host := stagedHost(testImage+":latest", testOldDigest, testDigest, true, true)

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if len(bc.upgradeApplyCalls) != 1 {
		t.Fatalf("Expected 1 UpgradeApply call, got %d", len(bc.upgradeApplyCalls))
	}
	if !bc.upgradeApplyCalls[0] {
		t.Error("Expected softReboot=true for Auto policy with softRebootCapable")
	}
	if bn.Status.Phase != v1alpha1.BootcNodePhaseRebooting {
		t.Errorf("Expected phase Rebooting, got %s", bn.Status.Phase)
	}
}

func TestReconcileRebootingApplyFullReboot(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
	bn.Spec.RebootPolicy = v1alpha1.RebootPolicyFull

	// Staged with soft reboot capability, but policy is Full.
	host := stagedHost(testImage+":latest", testOldDigest, testDigest, true, true)

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if len(bc.upgradeApplyCalls) != 1 {
		t.Fatalf("Expected 1 UpgradeApply call, got %d", len(bc.upgradeApplyCalls))
	}
	if bc.upgradeApplyCalls[0] {
		t.Error("Expected softReboot=false for Full policy")
	}
}

func TestReconcileRebootingAutoNoSoftReboot(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
	bn.Spec.RebootPolicy = v1alpha1.RebootPolicyAuto

	// Staged but soft reboot NOT capable.
	host := stagedHost(testImage+":latest", testOldDigest, testDigest, false, true)

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if len(bc.upgradeApplyCalls) != 1 {
		t.Fatalf("Expected 1 UpgradeApply call, got %d", len(bc.upgradeApplyCalls))
	}
	if bc.upgradeApplyCalls[0] {
		t.Error("Expected softReboot=false when staged is not softRebootCapable")
	}
}

func TestReconcileRebootingApplyError(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting

	host := stagedHost(testImage+":latest", testOldDigest, testDigest, false, true)

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	bc.upgradeApplyErr = fmt.Errorf("apply failed")
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, host)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("Expected phase Error, got %s", bn.Status.Phase)
	}
}

func TestReconcileRollingBack(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRollingBack

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, nil)

	if len(bc.rollbackCalls) != 1 {
		t.Fatalf("Expected 1 Rollback call, got %d", len(bc.rollbackCalls))
	}
	if !bc.rollbackCalls[0] {
		t.Error("Expected Rollback to be called with apply=true")
	}
	if bn.Status.Phase != v1alpha1.BootcNodePhaseRollingBack {
		t.Errorf("Expected phase RollingBack, got %s", bn.Status.Phase)
	}
}

func TestReconcileRollingBackError(t *testing.T) {
	bn := testBootcNode()
	bn.Spec.DesiredImage = testImageRef
	bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRollingBack

	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = bn
	bc := newMockBootcClient()
	bc.rollbackErr = fmt.Errorf("rollback failed")
	d := newTestDaemon(kc, bc)

	d.reconcile(context.Background(), bn, nil)

	if bn.Status.Phase != v1alpha1.BootcNodePhaseError {
		t.Errorf("Expected phase Error, got %s", bn.Status.Phase)
	}
}

func TestShouldSoftReboot(t *testing.T) {
	tests := []struct {
		name     string
		policy   v1alpha1.RebootPolicy
		capable  bool
		expected bool
	}{
		{"Auto with capable", v1alpha1.RebootPolicyAuto, true, true},
		{"Auto without capable", v1alpha1.RebootPolicyAuto, false, false},
		{"Full with capable", v1alpha1.RebootPolicyFull, true, false},
		{"Full without capable", v1alpha1.RebootPolicyFull, false, false},
		{"Never with capable", v1alpha1.RebootPolicyNever, true, false},
		{"Never without capable", v1alpha1.RebootPolicyNever, false, false},
		{"Empty (default Auto) with capable", "", true, true},
		{"Empty (default Auto) without capable", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bn := testBootcNode()
			bn.Spec.RebootPolicy = tt.policy

			host := stagedHost(testImage, testDigest, testDigest, tt.capable, true)

			d := newTestDaemon(nil, nil)
			result := d.shouldSoftReboot(bn, host)
			if result != tt.expected {
				t.Errorf("shouldSoftReboot() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestExtractDigest(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"quay.io/example/img@sha256:abc123", "sha256:abc123"},
		{"quay.io/example/img:latest", ""},
		{"quay.io/example/img", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := extractDigest(tt.input)
			if result != tt.expected {
				t.Errorf("extractDigest(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractImageName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"quay.io/example/img@sha256:abc123", "quay.io/example/img"},
		{"quay.io/example/img:latest", "quay.io/example/img:latest"},
		{"quay.io/example/img", "quay.io/example/img"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := extractImageName(tt.input)
			if result != tt.expected {
				t.Errorf("extractImageName(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractBaseName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"quay.io/example/img@sha256:abc123", "quay.io/example/img"},
		{"quay.io/example/img:latest", "quay.io/example/img"},
		{"quay.io/example/img:v1.2.3", "quay.io/example/img"},
		{"quay.io/example/img", "quay.io/example/img"},
		{"localhost:5000/img:v1", "localhost:5000/img"},
		{"localhost:5000/img", "localhost:5000/img"},
		{"registry:8080/org/repo:tag", "registry:8080/org/repo"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := extractBaseName(tt.input)
			if result != tt.expected {
				t.Errorf("extractBaseName(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestPollOnceReCreatesDeletedBootcNode(t *testing.T) {
	kc := newMockKubeClient()
	kc.nodes[testNodeName] = testNode()

	bc := newMockBootcClient()
	bc.statusResult = bootedOnlyHost(testImage+":latest", testOldDigest)

	d := newTestDaemon(kc, bc)

	// No BootcNode exists -- pollOnce should re-create it.
	interval, err := d.pollOnce(context.Background())
	if err != nil {
		t.Fatalf("pollOnce() error: %v", err)
	}

	if _, ok := kc.bootcNodes[testNodeName]; !ok {
		t.Error("Expected BootcNode to be re-created")
	}
	if interval != slowPollInterval {
		t.Errorf("Expected slow poll interval after re-creation, got %v", interval)
	}
}

func TestPollOnceUpdatesStatus(t *testing.T) {
	kc := newMockKubeClient()
	kc.bootcNodes[testNodeName] = testBootcNode()

	bc := newMockBootcClient()
	bc.statusResult = bootedOnlyHost(testImage+":latest", testOldDigest)

	d := newTestDaemon(kc, bc)

	interval, err := d.pollOnce(context.Background())
	if err != nil {
		t.Fatalf("pollOnce() error: %v", err)
	}

	bn := kc.bootcNodes[testNodeName]
	if bn.Status.BootedDigest != testOldDigest {
		t.Errorf("Expected bootedDigest %s, got %s", testOldDigest, bn.Status.BootedDigest)
	}
	if interval != slowPollInterval {
		t.Errorf("Expected slow poll interval, got %v", interval)
	}
}

func TestUpdateStatusFromHost(t *testing.T) {
	bn := testBootcNode()
	host := stagedHost(testImage+":latest", testOldDigest, testDigest, true, true)

	d := newTestDaemon(nil, nil)
	d.updateStatusFromHost(bn, host)

	if bn.Status.TrackedImage != testImage+":latest" {
		t.Errorf("Expected trackedImage %q, got %q", testImage+":latest", bn.Status.TrackedImage)
	}
	if bn.Status.BootedDigest != testOldDigest {
		t.Errorf("Expected bootedDigest %s, got %s", testOldDigest, bn.Status.BootedDigest)
	}
	if bn.Status.Staged.SoftRebootCapable != true {
		t.Error("Expected staged.softRebootCapable to be true")
	}

	// Phase should NOT be modified by updateStatusFromHost.
	if bn.Status.Phase != v1alpha1.BootcNodePhaseReady {
		t.Errorf("Phase should not be modified, got %s", bn.Status.Phase)
	}
}
