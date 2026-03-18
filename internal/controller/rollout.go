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

package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/pkg/drain"
)

const (
	// rebootingSinceAnnotation is the annotation set on a BootcNode
	// when the operator advances it to desiredPhase=Rebooting. The
	// value is an RFC3339 timestamp used to detect health check
	// timeouts.
	rebootingSinceAnnotation = "bootc.dev/rebooting-since"

	// defaultHealthCheckTimeout is used when the pool's
	// healthCheck.timeout is not set.
	defaultHealthCheckTimeout = 5 * time.Minute
)

// rolloutResult contains the outcome of a rollout orchestration step,
// used to update the pool's status.
type rolloutResult struct {
	// nodesAdvanced is the number of BootcNodes advanced to Rebooting.
	nodesAdvanced int
	// nodesUncordoned is the number of nodes uncordoned after success.
	nodesUncordoned int
	// requeue indicates the controller should requeue sooner than
	// the default re-resolution interval because the rollout is active.
	requeue bool
}

// nodeClassification groups claimed BootcNodes by their current state
// relative to an active rollout.
type nodeClassification struct {
	readyAtDesired []*v1alpha1.BootcNode // running desired image
	staged         []*v1alpha1.BootcNode // staged and ready to reboot
	staging        []*v1alpha1.BootcNode // still downloading
	rebooting      []*v1alpha1.BootcNode // desiredPhase=Rebooting, not yet ready
	rollingBack    []*v1alpha1.BootcNode // rolling back
	rolledBack     []*v1alpha1.BootcNode // completed rollback: Ready on old image with desiredPhase=RollingBack
	errored        []*v1alpha1.BootcNode // error state
	needsStaging   []*v1alpha1.BootcNode // ready but on old image, needs staging
}

// classifyNodes groups claimed BootcNodes by their current state
// relative to the desired image.
func classifyNodes(claimed []*v1alpha1.BootcNode, desiredImage string) nodeClassification {
	var nc nodeClassification
	for _, bn := range claimed {
		switch {
		case bn.Status.Phase == v1alpha1.BootcNodePhaseError:
			nc.errored = append(nc.errored, bn)
		case bn.Status.Phase == v1alpha1.BootcNodePhaseRollingBack:
			nc.rollingBack = append(nc.rollingBack, bn)
		// A node that was rolling back and is now Ready on the old
		// image has completed its rollback. This is distinct from
		// "needsStaging" because the operator should mark it as
		// failed, not re-stage it.
		case bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRollingBack &&
			bn.Status.Phase == v1alpha1.BootcNodePhaseReady &&
			bn.Status.Booted.Image != desiredImage:
			nc.rolledBack = append(nc.rolledBack, bn)
		case bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRebooting &&
			bn.Status.Phase != v1alpha1.BootcNodePhaseReady:
			nc.rebooting = append(nc.rebooting, bn)
		case bn.Status.Phase == v1alpha1.BootcNodePhaseReady &&
			bn.Status.Booted.Image == desiredImage:
			nc.readyAtDesired = append(nc.readyAtDesired, bn)
		case bn.Status.Phase == v1alpha1.BootcNodePhaseStaged:
			nc.staged = append(nc.staged, bn)
		case bn.Status.Phase == v1alpha1.BootcNodePhaseStaging:
			nc.staging = append(nc.staging, bn)
		default:
			nc.needsStaging = append(nc.needsStaging, bn)
		}
	}
	return nc
}

// orchestrateRollout drives the stage-then-reboot lifecycle for claimed
// BootcNodes. It is called after claims are synced and before status is
// updated.
//
// The orchestration follows this flow:
//  1. Uncordon nodes that successfully rebooted into the desired image
//  2. If all nodes are ready, the rollout is complete
//  3. If nodes are still staging, wait for them
//  4. When nodes are staged, select a batch (up to maxUnavailable) and
//     advance them: cordon the Node, set desiredPhase=Rebooting
//
// Cordon/drain is done here (operator-side); the daemon handles the
// actual bootc commands and reboot.
func (r *BootcNodePoolReconciler) orchestrateRollout(
	ctx context.Context,
	pool *v1alpha1.BootcNodePool,
	claimed []*v1alpha1.BootcNode,
	nodeMap map[string]*corev1.Node,
	desiredImage string,
) (*rolloutResult, error) {
	log := logf.FromContext(ctx)
	result := &rolloutResult{}

	if len(claimed) == 0 {
		return result, nil
	}

	nc := classifyNodes(claimed, desiredImage)

	log.Info("Rollout state",
		"pool", pool.Name,
		"readyAtDesired", len(nc.readyAtDesired),
		"staged", len(nc.staged),
		"staging", len(nc.staging),
		"rebooting", len(nc.rebooting),
		"rollingBack", len(nc.rollingBack),
		"rolledBack", len(nc.rolledBack),
		"errored", len(nc.errored),
		"needsStaging", len(nc.needsStaging),
	)

	// Step 1: Uncordon nodes that have successfully rebooted into the
	// desired image.
	if err := r.uncordonReadyNodes(ctx, nc.readyAtDesired, nodeMap, result); err != nil {
		return nil, err
	}

	// Step 2: Handle completed rollbacks -- nodes that were rolling
	// back and are now Ready on the old image. Mark them as failed,
	// uncordon them, and stop the rollout.
	if err := r.handleCompletedRollbacks(ctx, nc.rolledBack, nodeMap, result); err != nil {
		return nil, err
	}

	// Step 3: If any nodes are errored or rolled back, stop the
	// rollout (degraded).
	if len(nc.errored) > 0 || len(nc.rolledBack) > 0 {
		log.Info("Rollout paused due to node errors",
			"pool", pool.Name, "erroredNodes", len(nc.errored),
			"rolledBackNodes", len(nc.rolledBack))
		return result, nil
	}

	// Step 4: If any nodes are rolling back, wait.
	if len(nc.rollingBack) > 0 {
		result.requeue = true
		return result, nil
	}

	// Step 5: Check rebooting nodes for health check timeout. If a
	// node has been rebooting longer than the timeout, trigger a
	// rollback.
	if err := r.checkRebootTimeouts(ctx, pool, nc.rebooting); err != nil {
		return nil, err
	}

	// Step 6: If all nodes are ready at the desired image, rollout is
	// complete.
	if len(nc.readyAtDesired) == len(claimed) {
		log.Info("Rollout complete", "pool", pool.Name)
		return result, nil
	}

	// Step 7: Advance staged nodes to rebooting, respecting
	// maxUnavailable.
	maxUnavailable := max(pool.Spec.Rollout.MaxUnavailable, 1)
	r.advanceStagedNodes(ctx, pool, &nc, nodeMap, maxUnavailable, result)

	return result, nil
}

// uncordonReadyNodes uncordons nodes that have successfully rebooted
// into the desired image, and resets their desiredPhase to Staged.
func (r *BootcNodePoolReconciler) uncordonReadyNodes(
	ctx context.Context,
	readyAtDesired []*v1alpha1.BootcNode,
	nodeMap map[string]*corev1.Node,
	result *rolloutResult,
) error {
	log := logf.FromContext(ctx)
	for _, bn := range readyAtDesired {
		node, ok := nodeMap[bn.Name]
		if !ok {
			continue
		}
		if node.Spec.Unschedulable {
			if err := r.uncordonNode(ctx, node); err != nil {
				return fmt.Errorf("uncordoning node %q: %w", node.Name, err)
			}
			result.nodesUncordoned++
			log.Info("Uncordoned node after successful update", "node", node.Name)
		}
		// Reset the BootcNode's desiredPhase back to Staged and
		// clear the rebooting-since annotation to signal
		// completion.
		needsUpdate := false
		if bn.Spec.DesiredPhase != v1alpha1.BootcNodeDesiredPhaseStaged {
			bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged
			needsUpdate = true
		}
		if _, hasAnnotation := bn.Annotations[rebootingSinceAnnotation]; hasAnnotation {
			delete(bn.Annotations, rebootingSinceAnnotation)
			needsUpdate = true
		}
		if needsUpdate {
			if err := r.Update(ctx, bn); err != nil {
				return fmt.Errorf("resetting desiredPhase on %q: %w", bn.Name, err)
			}
		}
	}
	return nil
}

// checkRebootTimeouts checks rebooting nodes against the pool's health
// check timeout. If a node has been rebooting for longer than the
// timeout, the operator sets desiredPhase=RollingBack to trigger an
// automatic rollback.
func (r *BootcNodePoolReconciler) checkRebootTimeouts(
	ctx context.Context,
	pool *v1alpha1.BootcNodePool,
	rebooting []*v1alpha1.BootcNode,
) error {
	log := logf.FromContext(ctx)
	if len(rebooting) == 0 {
		return nil
	}

	timeout := defaultHealthCheckTimeout
	if pool.Spec.HealthCheck.Timeout.Duration > 0 {
		timeout = pool.Spec.HealthCheck.Timeout.Duration
	}

	now := r.now()

	for _, bn := range rebooting {
		since, ok := bn.Annotations[rebootingSinceAnnotation]
		if !ok {
			// No annotation means the node was set to Rebooting
			// before we started tracking timestamps. Set it now
			// so we start the clock.
			if bn.Annotations == nil {
				bn.Annotations = make(map[string]string)
			}
			bn.Annotations[rebootingSinceAnnotation] = now.Format(time.RFC3339)
			if err := r.Update(ctx, bn); err != nil {
				log.Error(err, "Failed to set rebooting-since annotation", "bootcNode", bn.Name)
			}
			continue
		}

		rebootingSince, err := time.Parse(time.RFC3339, since)
		if err != nil {
			log.Error(err, "Failed to parse rebooting-since annotation", "bootcNode", bn.Name, "value", since)
			continue
		}

		elapsed := now.Sub(rebootingSince)
		if elapsed <= timeout {
			continue
		}

		log.Info("Node exceeded health check timeout, triggering rollback",
			"bootcNode", bn.Name,
			"timeout", timeout,
			"elapsed", elapsed)

		bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRollingBack
		if err := r.Update(ctx, bn); err != nil {
			return fmt.Errorf("setting RollingBack on timed-out node %q: %w", bn.Name, err)
		}
	}

	return nil
}

// handleCompletedRollbacks processes nodes that have completed a
// rollback: they were set to desiredPhase=RollingBack and are now
// Ready on the old (non-desired) image. The operator uncordons them,
// clears the rebooting-since annotation, and marks them as errored
// so the pool enters Degraded state.
func (r *BootcNodePoolReconciler) handleCompletedRollbacks(
	ctx context.Context,
	rolledBack []*v1alpha1.BootcNode,
	nodeMap map[string]*corev1.Node,
	result *rolloutResult,
) error {
	log := logf.FromContext(ctx)
	for _, bn := range rolledBack {
		log.Info("Node completed rollback, marking as failed",
			"bootcNode", bn.Name,
			"bootedImage", bn.Status.Booted.Image,
			"desiredImage", bn.Spec.DesiredImage)

		// Uncordon the node so it can serve workloads again.
		if node, ok := nodeMap[bn.Name]; ok && node.Spec.Unschedulable {
			if err := r.uncordonNode(ctx, node); err != nil {
				return fmt.Errorf("uncordoning rolled-back node %q: %w", node.Name, err)
			}
			result.nodesUncordoned++
		}

		// Clear the rebooting-since annotation and reset
		// desiredPhase to Staged. The node's error status (set
		// below) will prevent the orchestrator from re-advancing
		// it.
		delete(bn.Annotations, rebootingSinceAnnotation)
		bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged
		if err := r.Update(ctx, bn); err != nil {
			return fmt.Errorf("resetting rolled-back node %q: %w", bn.Name, err)
		}

		// Set the status to Error so the pool shows as Degraded.
		// We must re-fetch to avoid conflicts since we just
		// updated the spec.
		fresh := &v1alpha1.BootcNode{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(bn), fresh); err != nil {
			return fmt.Errorf("re-fetching rolled-back node %q: %w", bn.Name, err)
		}
		fresh.Status.Phase = v1alpha1.BootcNodePhaseError
		fresh.Status.Message = fmt.Sprintf("Rollback completed: node returned to %s after health check timeout", bn.Status.Booted.Image)
		if err := r.Status().Update(ctx, fresh); err != nil {
			return fmt.Errorf("marking rolled-back node %q as errored: %w", bn.Name, err)
		}
	}
	return nil
}

// drainerOrNoop returns the reconciler's Drainer, or a noop
// implementation if none is configured (for tests that don't set up a
// Drainer).
func (r *BootcNodePoolReconciler) drainerOrNoop() drain.Drainer {
	if r.Drainer != nil {
		return r.Drainer
	}
	return &noopDrainer{}
}

// noopDrainer is a Drainer that does nothing. Used as a fallback when
// no Drainer is configured (e.g. in tests that predate drain
// integration).
type noopDrainer struct{}

func (n *noopDrainer) Cordon(_ context.Context, _ string) error   { return nil }
func (n *noopDrainer) Drain(_ context.Context, _ string) error    { return nil }
func (n *noopDrainer) Uncordon(_ context.Context, _ string) error { return nil }

// advanceStagedNodes selects staged nodes to advance to Rebooting,
// respecting maxUnavailable. It cordons each selected node and sets
// its desiredPhase to Rebooting.
func (r *BootcNodePoolReconciler) advanceStagedNodes(
	ctx context.Context,
	pool *v1alpha1.BootcNodePool,
	nc *nodeClassification,
	nodeMap map[string]*corev1.Node,
	maxUnavailable int32,
	result *rolloutResult,
) {
	log := logf.FromContext(ctx)

	// Calculate how many nodes we can still advance.
	currentlyUnavailable := int32(len(nc.rebooting))
	available := maxUnavailable - currentlyUnavailable
	if available <= 0 {
		log.Info("Max unavailable reached, waiting for rebooting nodes",
			"pool", pool.Name,
			"maxUnavailable", maxUnavailable,
			"currentlyRebooting", len(nc.rebooting))
		result.requeue = true
		return
	}

	// If no staged nodes are available, wait for staging to complete.
	if len(nc.staged) == 0 {
		if len(nc.staging) > 0 || len(nc.needsStaging) > 0 {
			log.Info("Waiting for nodes to finish staging",
				"pool", pool.Name,
				"staging", len(nc.staging),
				"needsStaging", len(nc.needsStaging))
			result.requeue = true
		}
		return
	}

	// Select staged nodes to advance. Sort by name for deterministic
	// ordering.
	sort.Slice(nc.staged, func(i, j int) bool {
		return nc.staged[i].Name < nc.staged[j].Name
	})

	toAdvance := nc.staged
	if int32(len(toAdvance)) > available {
		toAdvance = toAdvance[:available]
	}

	d := r.drainerOrNoop()

	for _, bn := range toAdvance {
		node, ok := nodeMap[bn.Name]
		if !ok {
			log.Info("Skipping staged node without corresponding Node object", "bootcNode", bn.Name)
			continue
		}

		// Re-verify the node is still staged (staging re-verification).
		if bn.Status.Phase != v1alpha1.BootcNodePhaseStaged {
			log.Info("Node no longer staged, skipping", "bootcNode", bn.Name)
			continue
		}

		// Cordon the node.
		if !node.Spec.Unschedulable {
			if err := r.cordonNode(ctx, node); err != nil {
				log.Error(err, "Failed to cordon node", "node", node.Name)
				continue
			}
			log.Info("Cordoned node for reboot", "node", node.Name)
		}

		// Drain the node: evict all evictable pods before rebooting.
		// DaemonSet pods (including our own daemon) are ignored.
		if err := d.Drain(ctx, node.Name); err != nil {
			log.Error(err, "Failed to drain node", "node", node.Name)
			continue
		}
		log.Info("Drained node for reboot", "node", node.Name)

		// Set desiredPhase=Rebooting to tell the daemon to apply and
		// reboot. Record the timestamp for health check timeout
		// tracking.
		bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
		if bn.Annotations == nil {
			bn.Annotations = make(map[string]string)
		}
		bn.Annotations[rebootingSinceAnnotation] = r.now().Format(time.RFC3339)
		if err := r.Update(ctx, bn); err != nil {
			log.Error(err, "Failed to advance BootcNode to Rebooting", "bootcNode", bn.Name)
			continue
		}
		result.nodesAdvanced++
		log.Info("Advanced node to Rebooting", "bootcNode", bn.Name)
	}

	if result.nodesAdvanced > 0 || len(nc.rebooting) > 0 || len(nc.staging) > 0 {
		result.requeue = true
	}
}

// cordonNode marks a Node as unschedulable.
func (r *BootcNodePoolReconciler) cordonNode(ctx context.Context, node *corev1.Node) error {
	node.Spec.Unschedulable = true
	return r.Update(ctx, node)
}

// uncordonNode marks a Node as schedulable.
func (r *BootcNodePoolReconciler) uncordonNode(ctx context.Context, node *corev1.Node) error {
	node.Spec.Unschedulable = false
	return r.Update(ctx, node)
}
