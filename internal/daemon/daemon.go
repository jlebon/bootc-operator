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

// Package daemon implements the per-node bootc agent that runs as a
// DaemonSet. It periodically polls its own BootcNode CRD, reports bootc
// status, and executes staging/reboot/rollback commands as directed by
// the operator.
package daemon

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/pkg/bootc"
)

const (
	// slowPollInterval is used when the node is idle or Ready.
	slowPollInterval = 30 * time.Second

	// fastPollInterval is used when the node is in an active phase
	// (Staging, Rebooting).
	fastPollInterval = 5 * time.Second
)

// KubeClient abstracts the Kubernetes API operations needed by the
// daemon. This avoids importing controller-runtime or using informers;
// the daemon uses plain GET/CREATE/UPDATE calls.
type KubeClient interface {
	// GetBootcNode fetches the BootcNode CRD by name.
	GetBootcNode(ctx context.Context, name string) (*v1alpha1.BootcNode, error)

	// CreateBootcNode creates a new BootcNode CRD.
	CreateBootcNode(ctx context.Context, node *v1alpha1.BootcNode) (*v1alpha1.BootcNode, error)

	// UpdateBootcNodeStatus updates the status subresource of a BootcNode.
	UpdateBootcNodeStatus(ctx context.Context, node *v1alpha1.BootcNode) (*v1alpha1.BootcNode, error)

	// GetNode fetches a Kubernetes Node by name.
	GetNode(ctx context.Context, name string) (*corev1.Node, error)
}

// Daemon is the per-node bootc agent. It polls the BootcNode CRD and
// drives the local bootc state machine.
type Daemon struct {
	nodeName     string
	pollInterval time.Duration
	kubeClient   KubeClient
	bootcClient  bootc.Client
}

// NewDaemon creates a new Daemon for the given node.
func NewDaemon(nodeName string, pollInterval time.Duration, kubeClient KubeClient, bootcClient bootc.Client) *Daemon {
	return &Daemon{
		nodeName:     nodeName,
		pollInterval: pollInterval,
		kubeClient:   kubeClient,
		bootcClient:  bootcClient,
	}
}

// Run is the main entry point for the daemon. It checks if the host is
// a bootc system, creates the BootcNode CRD if needed, and enters the
// poll loop. It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if !d.bootcClient.IsBootcHost(ctx) {
		klog.InfoS("Host is not a bootc system, staying idle")
		<-ctx.Done()
		return nil
	}

	klog.InfoS("Host is a bootc system, initializing")

	if err := d.ensureBootcNode(ctx); err != nil {
		return fmt.Errorf("ensuring BootcNode exists: %w", err)
	}

	return d.pollLoop(ctx)
}

// ensureBootcNode creates the BootcNode CRD if it doesn't exist. The
// name matches the Kubernetes Node name, and ownerReferences point to
// the Node object for automatic GC.
func (d *Daemon) ensureBootcNode(ctx context.Context) error {
	_, err := d.kubeClient.GetBootcNode(ctx, d.nodeName)
	if err == nil {
		klog.InfoS("BootcNode already exists", "name", d.nodeName)
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("getting BootcNode: %w", err)
	}

	// Look up the Node object for the ownerReference.
	node, err := d.kubeClient.GetNode(ctx, d.nodeName)
	if err != nil {
		return fmt.Errorf("getting Node for ownerReference: %w", err)
	}

	// Get initial status from bootc.
	host, err := d.bootcClient.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting initial bootc status: %w", err)
	}

	status := bootc.ToBootcNodeStatus(host)
	status.Phase = v1alpha1.BootcNodePhaseReady

	bootcNode := &v1alpha1.BootcNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: d.nodeName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Node",
					Name:       node.Name,
					UID:        node.UID,
				},
			},
		},
		Status: status,
	}

	created, err := d.kubeClient.CreateBootcNode(ctx, bootcNode)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			klog.InfoS("BootcNode was created concurrently", "name", d.nodeName)
			return nil
		}
		return fmt.Errorf("creating BootcNode: %w", err)
	}

	// Update status subresource (create doesn't set status).
	created.Status = status
	if _, err := d.kubeClient.UpdateBootcNodeStatus(ctx, created); err != nil {
		return fmt.Errorf("setting initial BootcNode status: %w", err)
	}

	klog.InfoS("Created BootcNode", "name", d.nodeName)
	return nil
}

// pollLoop periodically fetches the BootcNode CRD and reconciles the
// local state. The poll interval adapts based on whether the node is
// in an active phase.
func (d *Daemon) pollLoop(ctx context.Context) error {
	interval := d.pollInterval
	if interval == 0 {
		interval = slowPollInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Do an initial reconcile immediately.
	d.reconcileOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			klog.InfoS("Poll loop stopped")
			return nil
		case <-ticker.C:
			newInterval := d.reconcileOnce(ctx)
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
				klog.V(4).InfoS("Adjusted poll interval", "interval", interval)
			}
		}
	}
}

// reconcileOnce performs one poll cycle: fetch BootcNode, get bootc
// status, run the state machine, update status. Returns the desired
// poll interval for the next cycle.
func (d *Daemon) reconcileOnce(ctx context.Context) time.Duration {
	bn, err := d.kubeClient.GetBootcNode(ctx, d.nodeName)
	if err != nil {
		klog.ErrorS(err, "Failed to get BootcNode", "name", d.nodeName)
		return d.effectiveSlowInterval()
	}

	host, err := d.bootcClient.Status(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to get bootc status")
		return d.effectiveSlowInterval()
	}

	newStatus, nextInterval := d.reconcile(ctx, bn, host)
	bn.Status = newStatus
	if _, err := d.kubeClient.UpdateBootcNodeStatus(ctx, bn); err != nil {
		klog.ErrorS(err, "Failed to update BootcNode status", "name", d.nodeName)
	}

	return nextInterval
}

// reconcile implements the state machine. Given the current BootcNode
// spec and the live bootc host status, it computes the new status and
// executes any needed bootc commands.
func (d *Daemon) reconcile(ctx context.Context, bn *v1alpha1.BootcNode, host *bootc.Host) (v1alpha1.BootcNodeStatus, time.Duration) {
	status := bootc.ToBootcNodeStatus(host)

	// No pool has claimed this node -- just report status.
	if bn.Spec.DesiredImage == "" {
		status.Phase = v1alpha1.BootcNodePhaseReady
		return status, d.effectiveSlowInterval()
	}

	desiredImage := bn.Spec.DesiredImage
	bootedImage := bootc.BootedImageRef(host)

	// Already running the desired image (unless we're being told to
	// roll back, which means the operator wants us on the previous image).
	if bootedImage == desiredImage && bn.Spec.DesiredPhase != v1alpha1.BootcNodeDesiredPhaseRollingBack {
		status.Phase = v1alpha1.BootcNodePhaseReady
		return status, d.effectiveSlowInterval()
	}

	switch bn.Spec.DesiredPhase {
	case v1alpha1.BootcNodeDesiredPhaseStaged:
		return d.reconcileStaged(ctx, bn, host, status)
	case v1alpha1.BootcNodeDesiredPhaseRebooting:
		return d.reconcileRebooting(ctx, bn, host, status)
	case v1alpha1.BootcNodeDesiredPhaseRollingBack:
		return d.reconcileRollingBack(ctx, bn, host, status)
	default:
		// Unknown desired phase; just report status.
		klog.InfoS("Unknown desired phase", "desiredPhase", bn.Spec.DesiredPhase)
		return status, d.effectiveSlowInterval()
	}
}

// reconcileStaged handles the Staged desired phase: download and stage
// the desired image if not already staged.
func (d *Daemon) reconcileStaged(ctx context.Context, bn *v1alpha1.BootcNode, host *bootc.Host, status v1alpha1.BootcNodeStatus) (v1alpha1.BootcNodeStatus, time.Duration) {
	desiredImage := bn.Spec.DesiredImage
	stagedImage := bootc.StagedImageRef(host)

	// Already staged the desired image -- verify it's still valid.
	if stagedImage == desiredImage {
		status.Phase = v1alpha1.BootcNodePhaseStaged
		return status, d.effectiveSlowInterval()
	}

	// Need to stage: determine whether to switch or upgrade.
	status.Phase = v1alpha1.BootcNodePhaseStaging
	status.Message = "Staging image"

	if err := d.stageImage(ctx, host, desiredImage); err != nil {
		status.Phase = v1alpha1.BootcNodePhaseError
		status.Message = fmt.Sprintf("Failed to stage image: %v", err)
		klog.ErrorS(err, "Failed to stage image", "image", desiredImage)
		return status, d.effectiveSlowInterval()
	}

	// Re-check status after staging to confirm it took effect.
	newHost, err := d.bootcClient.Status(ctx)
	if err != nil {
		klog.ErrorS(err, "Failed to get bootc status after staging")
		// Return staging status; next poll will re-verify.
		return status, d.effectiveFastInterval()
	}

	status = bootc.ToBootcNodeStatus(newHost)
	newStaged := bootc.StagedImageRef(newHost)
	if newStaged == desiredImage {
		status.Phase = v1alpha1.BootcNodePhaseStaged
		status.Message = ""
		klog.InfoS("Image staged successfully", "image", desiredImage)
	} else {
		status.Phase = v1alpha1.BootcNodePhaseStaging
		status.Message = "Staged image does not match desired image after staging"
		klog.InfoS("Staged image mismatch after staging", "expected", desiredImage, "actual", newStaged)
	}

	return status, d.effectiveFastInterval()
}

// reconcileRebooting handles the Rebooting desired phase: apply the
// staged image and reboot.
func (d *Daemon) reconcileRebooting(ctx context.Context, bn *v1alpha1.BootcNode, host *bootc.Host, status v1alpha1.BootcNodeStatus) (v1alpha1.BootcNodeStatus, time.Duration) {
	desiredImage := bn.Spec.DesiredImage
	bootedImage := bootc.BootedImageRef(host)

	// Already booted into the desired image (post-reboot).
	if bootedImage == desiredImage {
		status.Phase = v1alpha1.BootcNodePhaseReady
		status.Message = ""
		klog.InfoS("Node is running desired image after reboot", "image", desiredImage)
		return status, d.effectiveSlowInterval()
	}

	// Verify the desired image is staged before rebooting.
	stagedImage := bootc.StagedImageRef(host)
	if stagedImage != desiredImage {
		// Image not staged (may have been GC'd). Report error so the
		// operator knows to re-stage.
		status.Phase = v1alpha1.BootcNodePhaseError
		status.Message = "Desired image is not staged; cannot reboot"
		klog.InfoS("Cannot reboot: desired image not staged", "desired", desiredImage, "staged", stagedImage)
		return status, d.effectiveSlowInterval()
	}

	// Apply the staged image and reboot.
	status.Phase = v1alpha1.BootcNodePhaseRebooting
	status.Message = "Applying staged image and rebooting"

	softReboot := shouldSoftReboot(bn.Spec.RebootPolicy, host)
	klog.InfoS("Applying staged image", "image", desiredImage, "softReboot", softReboot)

	if err := d.bootcClient.UpgradeApply(ctx, softReboot); err != nil {
		// If UpgradeApply returns, either an error occurred or the node
		// has already started rebooting (in which case this code may
		// not execute). Report the error; if the node reboots, it will
		// come back on the new image and the next poll will resolve it.
		status.Phase = v1alpha1.BootcNodePhaseError
		status.Message = fmt.Sprintf("Failed to apply image: %v", err)
		klog.ErrorS(err, "Failed to apply staged image")
		return status, d.effectiveSlowInterval()
	}

	// If we reach here, the reboot hasn't happened yet (shouldn't
	// normally happen with --apply). Keep reporting Rebooting.
	return status, d.effectiveFastInterval()
}

// reconcileRollingBack handles the RollingBack desired phase: rollback
// to the previous image and reboot.
func (d *Daemon) reconcileRollingBack(ctx context.Context, _ *v1alpha1.BootcNode, _ *bootc.Host, status v1alpha1.BootcNodeStatus) (v1alpha1.BootcNodeStatus, time.Duration) {
	status.Phase = v1alpha1.BootcNodePhaseRollingBack
	status.Message = "Rolling back to previous image"

	klog.InfoS("Rolling back to previous image")

	if err := d.bootcClient.Rollback(ctx, true); err != nil {
		status.Phase = v1alpha1.BootcNodePhaseError
		status.Message = fmt.Sprintf("Failed to rollback: %v", err)
		klog.ErrorS(err, "Failed to rollback")
		return status, d.effectiveSlowInterval()
	}

	// If we reach here, the rollback hasn't triggered a reboot yet.
	return status, d.effectiveFastInterval()
}

// stageImage determines whether to use `bootc switch` or
// `bootc upgrade --download-only` to stage the desired image.
func (d *Daemon) stageImage(ctx context.Context, host *bootc.Host, desiredImage string) error {
	bootedImage := bootc.BootedImageRef(host)
	if needsSwitch(bootedImage, desiredImage) {
		klog.InfoS("Switching to new image", "from", bootedImage, "to", desiredImage)
		return d.bootcClient.Switch(ctx, desiredImage)
	}
	klog.InfoS("Upgrading current image", "image", desiredImage)
	return d.bootcClient.UpgradeDownloadOnly(ctx)
}

// needsSwitch returns true if the desired image is a different image
// reference (different repository), requiring `bootc switch` instead of
// `bootc upgrade`.
func needsSwitch(bootedRef, desiredRef string) bool {
	bootedRepo := imageRepo(bootedRef)
	desiredRepo := imageRepo(desiredRef)
	return bootedRepo != desiredRepo
}

// imageRepo extracts the repository part of an image reference,
// stripping both tag and digest. For example:
// "quay.io/example/image@sha256:abc" → "quay.io/example/image"
// "quay.io/example/image:latest" → "quay.io/example/image"
func imageRepo(ref string) string {
	// Strip digest.
	if idx := indexFromEnd(ref, '@'); idx >= 0 {
		ref = ref[:idx]
	}
	// Strip tag (scan from end, stop at '/').
	for i := len(ref) - 1; i >= 0; i-- {
		switch ref[i] {
		case ':':
			return ref[:i]
		case '/':
			return ref
		}
	}
	return ref
}

// indexFromEnd finds the last occurrence of a byte in s.
func indexFromEnd(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// shouldSoftReboot determines whether to use soft reboot based on the
// reboot policy and the staged deployment's capability.
func shouldSoftReboot(policy v1alpha1.RebootPolicy, host *bootc.Host) bool {
	switch policy {
	case v1alpha1.RebootPolicyFull:
		return false
	case v1alpha1.RebootPolicyNever:
		return false
	default: // Auto or empty
		return host != nil && host.Status.Staged != nil && host.Status.Staged.SoftRebootCapable
	}
}

func (d *Daemon) effectiveSlowInterval() time.Duration {
	if d.pollInterval > 0 {
		return d.pollInterval
	}
	return slowPollInterval
}

func (d *Daemon) effectiveFastInterval() time.Duration {
	return fastPollInterval
}
