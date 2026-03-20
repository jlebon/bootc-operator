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

// Package daemon implements the bootc-daemon per-node agent. The daemon
// runs as a DaemonSet pod on every bootc-capable node. It creates its
// own BootcNode CRD, periodically polls it, reports bootc status, and
// executes bootc commands when instructed by the operator.
package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/pkg/bootc"
)

const (
	// slowPollInterval is used when the node is idle or Ready.
	slowPollInterval = 30 * time.Second

	// fastPollInterval is used when the node is in an active phase
	// (Staging, Rebooting, RollingBack).
	fastPollInterval = 5 * time.Second
)

// BootcClient abstracts the bootc CLI for testability.
type BootcClient interface {
	IsBootcHost(ctx context.Context) (bool, error)
	Status(ctx context.Context) (*bootc.Host, error)
	Switch(ctx context.Context, image string) error
	UpgradeDownloadOnly(ctx context.Context) error
	UpgradeApply(ctx context.Context, softReboot bool) error
	Rollback(ctx context.Context, apply bool) error
}

// Daemon is the per-node bootc agent. It periodically polls its own
// BootcNode CRD and executes bootc commands to converge toward the
// desired state set by the operator.
type Daemon struct {
	nodeName     string
	pollInterval time.Duration
	kubeClient   KubeClient
	bootcClient  BootcClient
	log          logr.Logger
}

// NewDaemon creates a new Daemon for the given node.
func NewDaemon(nodeName string, pollInterval time.Duration, kubeClient KubeClient, bootcClient BootcClient, log logr.Logger) *Daemon {
	return &Daemon{
		nodeName:     nodeName,
		pollInterval: pollInterval,
		kubeClient:   kubeClient,
		bootcClient:  bootcClient,
		log:          log,
	}
}

// Run starts the daemon main loop. It blocks until the context is
// cancelled. On startup, it checks whether the host is a bootc system.
// If not, it stays idle. If yes, it creates the BootcNode CRD and
// enters the poll loop.
func (d *Daemon) Run(ctx context.Context) error {
	isBootc, err := d.bootcClient.IsBootcHost(ctx)
	if err != nil {
		d.log.Info("Could not detect bootc on host, staying idle", "error", err)
		<-ctx.Done()
		return nil
	}
	if !isBootc {
		d.log.Info("Host is not a bootc system, staying idle")
		<-ctx.Done()
		return nil
	}

	d.log.Info("Host is a bootc system, initializing")

	if err := d.ensureBootcNode(ctx); err != nil {
		return fmt.Errorf("ensuring BootcNode: %w", err)
	}

	return d.pollLoop(ctx)
}

// ensureBootcNode creates the BootcNode CRD if it doesn't exist and
// sets the initial status from bootc. If it already exists, it updates
// the status to reflect the current bootc state.
func (d *Daemon) ensureBootcNode(ctx context.Context) error {
	host, err := d.bootcClient.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting bootc status: %w", err)
	}

	existing, err := d.kubeClient.GetBootcNode(ctx, d.nodeName)
	if err != nil {
		return fmt.Errorf("checking for existing BootcNode: %w", err)
	}

	if existing != nil {
		d.log.Info("BootcNode already exists, updating status")
		d.updateStatusFromHost(existing, host)
		if err := d.kubeClient.UpdateBootcNodeStatus(ctx, existing); err != nil {
			return fmt.Errorf("updating BootcNode status: %w", err)
		}
		return nil
	}

	d.log.Info("Creating BootcNode")

	node, err := d.kubeClient.GetNode(ctx, d.nodeName)
	if err != nil {
		return fmt.Errorf("getting Node for ownerReference: %w", err)
	}

	bn := &v1alpha1.BootcNode{
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
	}
	bn.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("BootcNode"))

	if err := d.kubeClient.CreateBootcNode(ctx, bn); err != nil {
		return fmt.Errorf("creating BootcNode: %w", err)
	}

	// Set initial status after creation (status subresource is separate).
	d.updateStatusFromHost(bn, host)
	// Set initial phase to Ready since no pool has claimed us yet.
	bn.Status.Phase = v1alpha1.BootcNodePhaseReady
	if err := d.kubeClient.UpdateBootcNodeStatus(ctx, bn); err != nil {
		return fmt.Errorf("setting initial BootcNode status: %w", err)
	}

	d.log.Info("Created BootcNode", "name", bn.Name)
	return nil
}

// pollLoop is the main daemon loop. It periodically GETs its own
// BootcNode, runs bootc status, reconciles state, and updates status.
func (d *Daemon) pollLoop(ctx context.Context) error {
	// Use the configured poll interval as the base, but switch to fast
	// polling when in an active phase.
	interval := d.pollInterval

	for {
		select {
		case <-ctx.Done():
			d.log.Info("Poll loop stopped")
			return nil
		case <-time.After(interval):
		}

		newInterval, err := d.pollOnce(ctx)
		if err != nil {
			d.log.Error(err, "Poll cycle failed")
			// Continue polling on error; use slow interval to avoid
			// hammering the API server.
			interval = d.pollInterval
			continue
		}
		interval = newInterval
	}
}

// pollOnce executes a single poll cycle. It returns the interval to use
// for the next poll.
func (d *Daemon) pollOnce(ctx context.Context) (time.Duration, error) {
	bn, err := d.kubeClient.GetBootcNode(ctx, d.nodeName)
	if err != nil {
		return 0, fmt.Errorf("getting BootcNode: %w", err)
	}
	if bn == nil {
		// BootcNode was deleted. Re-create it.
		d.log.Info("BootcNode was deleted, re-creating")
		if err := d.ensureBootcNode(ctx); err != nil {
			return 0, fmt.Errorf("re-creating BootcNode: %w", err)
		}
		return d.pollInterval, nil
	}

	host, err := d.bootcClient.Status(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting bootc status: %w", err)
	}

	// Update bootc-derived status fields.
	d.updateStatusFromHost(bn, host)

	// Run the state machine to determine what action to take.
	nextInterval := d.reconcile(ctx, bn, host)

	// Persist the updated status.
	if err := d.kubeClient.UpdateBootcNodeStatus(ctx, bn); err != nil {
		return 0, fmt.Errorf("updating BootcNode status: %w", err)
	}

	return nextInterval, nil
}

// reconcile implements the daemon state machine. It examines the
// BootcNode's spec (set by the operator) and the current bootc host
// state, then takes the appropriate action and updates the BootcNode's
// phase accordingly. Returns the recommended next poll interval.
func (d *Daemon) reconcile(ctx context.Context, bn *v1alpha1.BootcNode, host *bootc.Host) time.Duration {
	// No pool has claimed this node -- just report status.
	if bn.Spec.DesiredImage == "" {
		bn.Status.Phase = v1alpha1.BootcNodePhaseReady
		bn.Status.Message = ""
		return d.pollInterval
	}

	switch bn.Spec.DesiredPhase {
	case v1alpha1.BootcNodeDesiredPhaseStaged:
		return d.reconcileStaged(ctx, bn, host)
	case v1alpha1.BootcNodeDesiredPhaseRebooting:
		return d.reconcileRebooting(ctx, bn, host)
	case v1alpha1.BootcNodeDesiredPhaseRollingBack:
		return d.reconcileRollingBack(ctx, bn)
	default:
		// Desired phase is empty or unknown -- just report status.
		bn.Status.Phase = v1alpha1.BootcNodePhaseReady
		bn.Status.Message = ""
		return d.pollInterval
	}
}

// reconcileStaged handles the Staged desired phase. The daemon should
// download and stage the desired image without rebooting.
func (d *Daemon) reconcileStaged(ctx context.Context, bn *v1alpha1.BootcNode, host *bootc.Host) time.Duration {
	desiredDigest := extractDigest(bn.Spec.DesiredImage)

	// Check if we're already booted into the desired image.
	if bootc.BootedImageDigest(host) == desiredDigest {
		bn.Status.Phase = v1alpha1.BootcNodePhaseReady
		bn.Status.Message = "Running desired image"
		return d.pollInterval
	}

	// Check if the desired image is already staged.
	if bootc.HasStagedImage(host) && bootc.StagedImageDigest(host) == desiredDigest {
		bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
		bn.Status.Message = "Desired image staged"
		return d.pollInterval
	}

	// Need to stage the image. Determine whether to switch or upgrade.
	bn.Status.Phase = v1alpha1.BootcNodePhaseStaging
	bn.Status.Message = "Staging desired image"
	d.log.Info("Staging image", "desired", bn.Spec.DesiredImage)

	desiredRef := extractBaseName(bn.Spec.DesiredImage)
	trackedRef := extractBaseName(bootc.TrackedImageRef(host))

	if desiredRef != "" && trackedRef != "" && desiredRef != trackedRef {
		// Different image reference -- need to switch.
		d.log.Info("Switching to new image", "from", trackedRef, "to", desiredRef)
		if err := d.bootcClient.Switch(ctx, bn.Spec.DesiredImage); err != nil {
			bn.Status.Phase = v1alpha1.BootcNodePhaseError
			bn.Status.Message = fmt.Sprintf("Failed to switch image: %v", err)
			d.log.Error(err, "Failed to switch image")
			return d.pollInterval
		}
	} else {
		// Same image reference (or unknown) -- use upgrade --download-only.
		d.log.Info("Downloading image update")
		if err := d.bootcClient.UpgradeDownloadOnly(ctx); err != nil {
			bn.Status.Phase = v1alpha1.BootcNodePhaseError
			bn.Status.Message = fmt.Sprintf("Failed to download image: %v", err)
			d.log.Error(err, "Failed to download image")
			return d.pollInterval
		}
	}

	// Re-check status to verify staging succeeded.
	newHost, err := d.bootcClient.Status(ctx)
	if err != nil {
		bn.Status.Phase = v1alpha1.BootcNodePhaseError
		bn.Status.Message = fmt.Sprintf("Failed to verify staging: %v", err)
		d.log.Error(err, "Failed to get status after staging")
		return d.pollInterval
	}

	// Update status fields from the new host state.
	d.updateStatusFromHost(bn, newHost)

	if bootc.HasStagedImage(newHost) && bootc.StagedImageDigest(newHost) == desiredDigest {
		bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
		bn.Status.Message = "Desired image staged"
		d.log.Info("Image staged successfully")
	} else {
		// Staging didn't produce the expected digest. This could happen
		// if the tag resolved to a different digest between operator
		// resolution and daemon staging.
		bn.Status.Phase = v1alpha1.BootcNodePhaseStaging
		bn.Status.Message = "Staged image digest does not match desired; will retry"
		d.log.Info("Staged digest mismatch, will retry",
			"stagedDigest", bootc.StagedImageDigest(newHost),
			"desiredDigest", desiredDigest)
	}

	return fastPollInterval
}

// reconcileRebooting handles the Rebooting desired phase. The daemon
// should apply the staged image and reboot.
func (d *Daemon) reconcileRebooting(ctx context.Context, bn *v1alpha1.BootcNode, host *bootc.Host) time.Duration {
	desiredDigest := extractDigest(bn.Spec.DesiredImage)

	// Check if we've already rebooted into the desired image (this
	// happens after the reboot when the daemon restarts).
	if bootc.BootedImageDigest(host) == desiredDigest {
		bn.Status.Phase = v1alpha1.BootcNodePhaseReady
		bn.Status.Message = "Running desired image after reboot"
		d.log.Info("Node is running desired image after reboot")
		return d.pollInterval
	}

	// Verify the desired image is still staged.
	if !bootc.HasStagedImage(host) || bootc.StagedImageDigest(host) != desiredDigest {
		// Staged image was lost (e.g. GC'd after unexpected reboot).
		// Report error; the operator should set desiredPhase back to
		// Staged to re-trigger staging.
		bn.Status.Phase = v1alpha1.BootcNodePhaseError
		bn.Status.Message = "Staged image lost before reboot"
		d.log.Info("Staged image lost, reporting error")
		return d.pollInterval
	}

	// Determine soft reboot capability.
	softReboot := d.shouldSoftReboot(bn, host)

	bn.Status.Phase = v1alpha1.BootcNodePhaseRebooting
	bn.Status.Message = "Applying staged image and rebooting"
	d.log.Info("Applying staged image", "softReboot", softReboot)

	// Persist status before rebooting so the operator sees the Rebooting
	// phase even if the reboot happens instantly.
	if err := d.kubeClient.UpdateBootcNodeStatus(ctx, bn); err != nil {
		d.log.Error(err, "Failed to update status before reboot")
		// Continue with the reboot anyway -- worst case the operator
		// sees Staged instead of Rebooting briefly.
	}

	if err := d.bootcClient.UpgradeApply(ctx, softReboot); err != nil {
		bn.Status.Phase = v1alpha1.BootcNodePhaseError
		bn.Status.Message = fmt.Sprintf("Failed to apply upgrade: %v", err)
		d.log.Error(err, "Failed to apply upgrade")
		return d.pollInterval
	}

	// If we reach here, the reboot didn't happen (shouldn't happen in
	// production, but possible in tests). Keep the Rebooting phase.
	return fastPollInterval
}

// reconcileRollingBack handles the RollingBack desired phase. The daemon
// should rollback to the previous image and reboot.
func (d *Daemon) reconcileRollingBack(ctx context.Context, bn *v1alpha1.BootcNode) time.Duration {
	bn.Status.Phase = v1alpha1.BootcNodePhaseRollingBack
	bn.Status.Message = "Rolling back to previous image"
	d.log.Info("Rolling back")

	// Persist status before rebooting.
	if err := d.kubeClient.UpdateBootcNodeStatus(ctx, bn); err != nil {
		d.log.Error(err, "Failed to update status before rollback")
	}

	if err := d.bootcClient.Rollback(ctx, true); err != nil {
		bn.Status.Phase = v1alpha1.BootcNodePhaseError
		bn.Status.Message = fmt.Sprintf("Failed to rollback: %v", err)
		d.log.Error(err, "Failed to rollback")
		return d.pollInterval
	}

	// If we reach here, the reboot didn't happen (shouldn't normally).
	return fastPollInterval
}

// shouldSoftReboot determines whether a soft reboot should be used
// based on the reboot policy and host capabilities.
func (d *Daemon) shouldSoftReboot(bn *v1alpha1.BootcNode, host *bootc.Host) bool {
	policy := bn.Spec.RebootPolicy
	if policy == "" {
		policy = v1alpha1.RebootPolicyAuto
	}

	switch policy {
	case v1alpha1.RebootPolicyAuto:
		// Use soft reboot if the staged deployment supports it.
		if host.Status.Staged != nil {
			return host.Status.Staged.SoftRebootCapable
		}
		return false
	case v1alpha1.RebootPolicyFull:
		return false
	case v1alpha1.RebootPolicyNever:
		// The operator should not set desiredPhase=Rebooting when
		// rebootPolicy=Never, but if it does, we don't reboot.
		return false
	default:
		return false
	}
}

// updateStatusFromHost updates the bootc-derived fields on the
// BootcNode status from a bootc Host. It does NOT modify phase,
// message, or conditions -- those are managed by the state machine.
func (d *Daemon) updateStatusFromHost(bn *v1alpha1.BootcNode, host *bootc.Host) {
	status := bootc.ToBootcNodeStatus(host)
	bn.Status.TrackedImage = status.TrackedImage
	bn.Status.BootedDigest = status.BootedDigest
	bn.Status.Booted = status.Booted
	bn.Status.Staged = status.Staged
	bn.Status.Rollback = status.Rollback
}

// extractDigest extracts the digest from a fully qualified image
// reference (e.g. "quay.io/example/img@sha256:abc..." -> "sha256:abc...").
// Returns an empty string if no digest is present.
func extractDigest(imageRef string) string {
	if idx := strings.LastIndex(imageRef, "@"); idx >= 0 {
		return imageRef[idx+1:]
	}
	return ""
}

// extractImageName extracts the image name (without digest) from a fully
// qualified image reference (e.g. "quay.io/example/img@sha256:abc..." ->
// "quay.io/example/img").
func extractImageName(imageRef string) string {
	if idx := strings.LastIndex(imageRef, "@"); idx >= 0 {
		return imageRef[:idx]
	}
	return imageRef
}

// extractBaseName strips both the tag and digest from an image reference,
// returning just the repository name. This is used to compare whether two
// references point to the same image (ignoring version).
// Examples:
//
//	"quay.io/example/img:latest"      -> "quay.io/example/img"
//	"quay.io/example/img@sha256:abc"  -> "quay.io/example/img"
//	"quay.io/example/img"             -> "quay.io/example/img"
//	"localhost:5000/img:v1"           -> "localhost:5000/img"
func extractBaseName(imageRef string) string {
	// First strip digest if present.
	if idx := strings.LastIndex(imageRef, "@"); idx >= 0 {
		imageRef = imageRef[:idx]
	}

	// Then strip tag. A colon is a tag separator only if no slash
	// follows it (otherwise it's a port like "host:5000/repo").
	for i := len(imageRef) - 1; i >= 0; i-- {
		switch imageRef[i] {
		case ':':
			// Check if there's a slash after this colon.
			hasSlashAfter := strings.Contains(imageRef[i+1:], "/")
			if !hasSlashAfter {
				return imageRef[:i]
			}
		case '/':
			// Reached a path separator before finding a tag.
			return imageRef
		}
	}
	return imageRef
}
