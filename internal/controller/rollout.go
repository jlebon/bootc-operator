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

	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
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
		"errored", len(nc.errored),
		"needsStaging", len(nc.needsStaging),
	)

	// Step 1: Uncordon nodes that have successfully rebooted into the
	// desired image.
	if err := r.uncordonReadyNodes(ctx, nc.readyAtDesired, nodeMap, result); err != nil {
		return nil, err
	}

	// Step 2: If any nodes are errored, stop the rollout (degraded).
	if len(nc.errored) > 0 {
		log.Info("Rollout paused due to node errors",
			"pool", pool.Name, "erroredNodes", len(nc.errored))
		return result, nil
	}

	// Step 3: If any nodes are rolling back, wait.
	if len(nc.rollingBack) > 0 {
		result.requeue = true
		return result, nil
	}

	// Step 4: If all nodes are ready at the desired image, rollout is
	// complete.
	if len(nc.readyAtDesired) == len(claimed) {
		log.Info("Rollout complete", "pool", pool.Name)
		return result, nil
	}

	// Step 5: Advance staged nodes to rebooting, respecting
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
		// Reset the BootcNode's desiredPhase back to Staged to signal
		// completion.
		if bn.Spec.DesiredPhase != v1alpha1.BootcNodeDesiredPhaseStaged {
			bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseStaged
			if err := r.Update(ctx, bn); err != nil {
				return fmt.Errorf("resetting desiredPhase on %q: %w", bn.Name, err)
			}
		}
	}
	return nil
}

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

		// Set desiredPhase=Rebooting to tell the daemon to apply and
		// reboot.
		bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
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
