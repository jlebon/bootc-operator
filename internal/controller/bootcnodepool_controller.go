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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bootcdevv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/pkg/drain"
)

const (
	// finalizerName is the finalizer added to BootcNodePool resources
	// to ensure cleanup on deletion.
	finalizerName = "bootc.dev/cleanup"

	// poolLabelKey is the label set on BootcNode resources to indicate
	// which pool has claimed them.
	poolLabelKey = "bootc.dev/pool"

	// conditionTypeAvailable indicates whether the pool is operating
	// normally.
	conditionTypeAvailable = "Available"

	// conditionTypeProgressing indicates whether a rollout is in
	// progress.
	conditionTypeProgressing = "Progressing"

	// conditionTypeDegraded indicates whether the pool has encountered
	// an error.
	conditionTypeDegraded = "Degraded"

	// reResolutionInterval is how often the operator re-resolves tags
	// to detect new digests.
	reResolutionInterval = 5 * time.Minute

	// operatorNamespace is the namespace where the operator is
	// deployed. Used for looking up pull secrets.
	// TODO: read from downward API env var instead of hardcoding.
	defaultOperatorNamespace = "bootc-operator"
)

// BootcNodePoolReconciler reconciles a BootcNodePool object.
type BootcNodePoolReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	DigestResolver    DigestResolver
	Drainer           drain.Drainer
	OperatorNamespace string
}

// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools/finalizers,verbs=update
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;delete
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile moves the cluster state toward the desired state specified
// by the BootcNodePool object. It resolves the image to a digest, claims
// matching BootcNodes, orchestrates staged rollouts, and updates status.
func (r *BootcNodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the BootcNodePool CR.
	pool := &bootcdevv1alpha1.BootcNodePool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		if errors.IsNotFound(err) {
			log.Info("BootcNodePool not found, skipping reconciliation")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching BootcNodePool: %w", err)
	}

	// 2. Initialize status conditions if empty.
	if len(pool.Status.Conditions) == 0 {
		initializeConditions(pool)
		if err := r.Status().Update(ctx, pool); err != nil {
			return ctrl.Result{}, fmt.Errorf("initializing conditions: %w", err)
		}
		// Re-fetch after status update to avoid stale resourceVersion.
		if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
			return ctrl.Result{}, fmt.Errorf("re-fetching after init: %w", err)
		}
	}

	// 3. Add finalizer if not present.
	if !controllerutil.ContainsFinalizer(pool, finalizerName) {
		controllerutil.AddFinalizer(pool, finalizerName)
		if err := r.Update(ctx, pool); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		// Re-fetch after adding finalizer.
		if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
			return ctrl.Result{}, fmt.Errorf("re-fetching after finalizer: %w", err)
		}
	}

	// 4. Handle deletion.
	if !pool.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, pool)
	}

	// 5. Resolve image tag to digest.
	resolvedDigest, err := r.resolveDigest(ctx, pool)
	if err != nil {
		log.Error(err, "Failed to resolve image digest")
		setDegradedCondition(pool, "DigestResolutionFailed", err.Error())
		if statusErr := r.updateStatus(ctx, pool); statusErr != nil {
			log.Error(statusErr, "Failed to update status after digest resolution failure")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// 6. List all BootcNodes and Nodes, match against nodeSelector.
	matchingBootcNodes, err := r.findMatchingBootcNodes(ctx, pool)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("finding matching BootcNodes: %w", err)
	}

	// 7. Check for overlapping pools.
	if err := r.checkOverlappingPools(ctx, pool, matchingBootcNodes); err != nil {
		setDegradedCondition(pool, "OverlappingPools", err.Error())
		if statusErr := r.updateStatus(ctx, pool); statusErr != nil {
			log.Error(statusErr, "Failed to update status after overlap check")
		}
		return ctrl.Result{}, nil
	}

	// 8. Claim matching BootcNodes and release non-matching ones.
	if err := r.claimAndReleaseNodes(ctx, pool, matchingBootcNodes, resolvedDigest); err != nil {
		return ctrl.Result{}, fmt.Errorf("claiming/releasing nodes: %w", err)
	}

	// 9. List claimed nodes and orchestrate rollout.
	claimedNodes, err := r.listClaimedBootcNodes(ctx, pool.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing claimed BootcNodes: %w", err)
	}

	// Pre-compute status to determine pool phase for orchestration
	// decisions (e.g. don't orchestrate if Degraded).
	r.computeStatus(pool, claimedNodes, resolvedDigest)

	// 10. Orchestrate rollout: advance staged nodes to rebooting.
	result, err := r.orchestrateRollout(ctx, pool, claimedNodes, resolvedDigest)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("orchestrating rollout: %w", err)
	}

	// Re-list and re-compute after orchestration, since it may have
	// updated BootcNode specs (desiredPhase changes).
	claimedNodes, err = r.listClaimedBootcNodes(ctx, pool.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("re-listing claimed BootcNodes: %w", err)
	}
	r.computeStatus(pool, claimedNodes, resolvedDigest)

	// 11. Re-fetch CR and update status.
	if err := r.updateStatus(ctx, pool); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	// Re-resolve tags periodically.
	if result.RequeueAfter == 0 {
		result.RequeueAfter = reResolutionInterval
	}

	return result, nil
}

// reconcileDelete handles BootcNodePool deletion. It uncordons any
// drained nodes, releases all claimed BootcNodes, and removes the
// finalizer.
func (r *BootcNodePoolReconciler) reconcileDelete(ctx context.Context, pool *bootcdevv1alpha1.BootcNodePool) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Handling BootcNodePool deletion")

	// Release all claimed BootcNodes.
	claimed, err := r.listClaimedBootcNodes(ctx, pool.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing claimed nodes for cleanup: %w", err)
	}

	for i := range claimed {
		// Uncordon any nodes that were cordoned during rollout.
		if r.Drainer != nil {
			node := &corev1.Node{}
			if err := r.Get(ctx, types.NamespacedName{Name: claimed[i].Name}, node); err == nil {
				if node.Spec.Unschedulable {
					log.Info("Uncordoning node during pool deletion", "node", claimed[i].Name)
					if err := r.Drainer.Uncordon(ctx, claimed[i].Name); err != nil {
						log.Error(err, "Failed to uncordon node during deletion", "node", claimed[i].Name)
						// Continue with cleanup even if uncordon fails.
					}
				}
			}
		}

		if err := r.releaseBootcNode(ctx, &claimed[i]); err != nil {
			return ctrl.Result{}, fmt.Errorf("releasing BootcNode %s: %w", claimed[i].Name, err)
		}
	}

	// Remove finalizer.
	controllerutil.RemoveFinalizer(pool, finalizerName)
	if err := r.Update(ctx, pool); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	log.Info("BootcNodePool deletion complete")
	return ctrl.Result{}, nil
}

// resolveDigest resolves the pool's image reference to a digest.
func (r *BootcNodePoolReconciler) resolveDigest(ctx context.Context, pool *bootcdevv1alpha1.BootcNodePool) (string, error) {
	ns := r.OperatorNamespace
	if ns == "" {
		ns = defaultOperatorNamespace
	}

	pullSecret, err := getPullSecret(ctx, r.Client, pool.Spec.ImagePullSecret.Name, ns)
	if err != nil {
		return "", err
	}

	digest, err := r.DigestResolver.Resolve(ctx, pool.Spec.Image, pullSecret)
	if err != nil {
		return "", err
	}

	return digest, nil
}

// findMatchingBootcNodes returns the BootcNodes whose corresponding
// Node matches the pool's nodeSelector.
func (r *BootcNodePoolReconciler) findMatchingBootcNodes(ctx context.Context, pool *bootcdevv1alpha1.BootcNodePool) ([]bootcdevv1alpha1.BootcNode, error) {
	// List all BootcNodes.
	bootcNodeList := &bootcdevv1alpha1.BootcNodeList{}
	if err := r.List(ctx, bootcNodeList); err != nil {
		return nil, fmt.Errorf("listing BootcNodes: %w", err)
	}

	if pool.Spec.NodeSelector == nil {
		// No selector means no nodes are targeted.
		return nil, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing nodeSelector: %w", err)
	}

	// List Nodes matching the selector.
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("listing Nodes: %w", err)
	}

	// Build a set of matching node names.
	matchingNodeNames := make(map[string]struct{}, len(nodeList.Items))
	for _, node := range nodeList.Items {
		matchingNodeNames[node.Name] = struct{}{}
	}

	// Filter BootcNodes to those whose name matches a selected Node.
	// BootcNode name == Node name by convention.
	var matching []bootcdevv1alpha1.BootcNode
	for _, bn := range bootcNodeList.Items {
		if _, ok := matchingNodeNames[bn.Name]; ok {
			matching = append(matching, bn)
		}
	}

	return matching, nil
}

// checkOverlappingPools checks if any matching BootcNode is already
// claimed by a different pool.
func (r *BootcNodePoolReconciler) checkOverlappingPools(_ context.Context, pool *bootcdevv1alpha1.BootcNodePool, matchingBootcNodes []bootcdevv1alpha1.BootcNode) error {
	for _, bn := range matchingBootcNodes {
		claimedBy := bn.Labels[poolLabelKey]
		if claimedBy != "" && claimedBy != pool.Name {
			return fmt.Errorf("node %s is already claimed by pool %s", bn.Name, claimedBy)
		}
	}
	return nil
}

// claimAndReleaseNodes claims matching BootcNodes for this pool and
// releases previously-claimed nodes that no longer match.
func (r *BootcNodePoolReconciler) claimAndReleaseNodes(
	ctx context.Context,
	pool *bootcdevv1alpha1.BootcNodePool,
	matchingBootcNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) error {
	log := logf.FromContext(ctx)

	// Build set of matching node names for fast lookup.
	matchingNames := make(map[string]struct{}, len(matchingBootcNodes))
	for _, bn := range matchingBootcNodes {
		matchingNames[bn.Name] = struct{}{}
	}

	// Claim matching BootcNodes.
	desiredImage := imageWithDigest(pool.Spec.Image, resolvedDigest)
	for i := range matchingBootcNodes {
		bn := &matchingBootcNodes[i]
		if err := r.claimBootcNode(ctx, pool, bn, desiredImage); err != nil {
			return fmt.Errorf("claiming BootcNode %s: %w", bn.Name, err)
		}
	}

	// Release previously-claimed nodes that no longer match.
	claimed, err := r.listClaimedBootcNodes(ctx, pool.Name)
	if err != nil {
		return fmt.Errorf("listing claimed BootcNodes: %w", err)
	}

	for i := range claimed {
		if _, ok := matchingNames[claimed[i].Name]; !ok {
			log.Info("Releasing BootcNode that no longer matches", "node", claimed[i].Name)
			if err := r.releaseBootcNode(ctx, &claimed[i]); err != nil {
				return fmt.Errorf("releasing BootcNode %s: %w", claimed[i].Name, err)
			}
		}
	}

	return nil
}

// claimBootcNode sets the pool label and desired spec on a BootcNode.
func (r *BootcNodePoolReconciler) claimBootcNode(
	ctx context.Context,
	pool *bootcdevv1alpha1.BootcNodePool,
	bn *bootcdevv1alpha1.BootcNode,
	desiredImage string,
) error {
	needsUpdate := false

	// Set pool label.
	if bn.Labels == nil {
		bn.Labels = make(map[string]string)
	}
	if bn.Labels[poolLabelKey] != pool.Name {
		bn.Labels[poolLabelKey] = pool.Name
		needsUpdate = true
	}

	// Set desired spec fields. On initial claim or image change, set
	// desiredPhase to Staged to trigger staging.
	if bn.Spec.DesiredImage != desiredImage {
		bn.Spec.DesiredImage = desiredImage
		bn.Spec.DesiredPhase = bootcdevv1alpha1.BootcNodeDesiredPhaseStaged
		needsUpdate = true
	}

	// Propagate reboot policy from pool.
	rebootPolicy := pool.Spec.Disruption.RebootPolicy
	if rebootPolicy == "" {
		rebootPolicy = bootcdevv1alpha1.RebootPolicyAuto
	}
	if bn.Spec.RebootPolicy != rebootPolicy {
		bn.Spec.RebootPolicy = rebootPolicy
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	return r.Update(ctx, bn)
}

// releaseBootcNode clears the pool label and desired spec on a
// BootcNode, releasing it from pool management.
func (r *BootcNodePoolReconciler) releaseBootcNode(ctx context.Context, bn *bootcdevv1alpha1.BootcNode) error {
	needsUpdate := false

	if bn.Labels != nil {
		if _, ok := bn.Labels[poolLabelKey]; ok {
			delete(bn.Labels, poolLabelKey)
			needsUpdate = true
		}
	}

	if bn.Spec.DesiredImage != "" || bn.Spec.DesiredPhase != "" || bn.Spec.RebootPolicy != "" {
		bn.Spec = bootcdevv1alpha1.BootcNodeSpec{}
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	return r.Update(ctx, bn)
}

// listClaimedBootcNodes returns all BootcNodes claimed by the given pool.
func (r *BootcNodePoolReconciler) listClaimedBootcNodes(ctx context.Context, poolName string) ([]bootcdevv1alpha1.BootcNode, error) {
	list := &bootcdevv1alpha1.BootcNodeList{}
	if err := r.List(ctx, list, client.MatchingLabels{poolLabelKey: poolName}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// computeStatus updates the pool's status counters and phase based on
// the current state of claimed BootcNodes.
func (r *BootcNodePoolReconciler) computeStatus(
	pool *bootcdevv1alpha1.BootcNodePool,
	claimedNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) {
	pool.Status.ObservedGeneration = pool.Generation
	pool.Status.ResolvedDigest = resolvedDigest
	pool.Status.TargetNodes = int32(len(claimedNodes))

	var readyCount, stagedCount, updatingCount, errorCount int32
	for _, bn := range claimedNodes {
		switch {
		case bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseReady &&
			bn.Status.BootedDigest == resolvedDigest:
			// Running the desired image -- count as ready regardless
			// of desiredPhase (operator may not have reset it yet).
			readyCount++
		case bn.Spec.DesiredPhase == bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting ||
			bn.Spec.DesiredPhase == bootcdevv1alpha1.BootcNodeDesiredPhaseRollingBack:
			// Operator has instructed a reboot/rollback. Count as
			// updating even if the daemon hasn't started yet
			// (status.Phase may still be Staged).
			updatingCount++
		case bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseReady:
			// Ready but on old image -- still needs update.
			stagedCount++
		case bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseStaged:
			stagedCount++
		case bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseStaging:
			stagedCount++
		case bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseRebooting ||
			bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseRollingBack:
			updatingCount++
		case bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseError:
			errorCount++
		default:
			stagedCount++
		}
	}

	pool.Status.ReadyNodes = readyCount
	pool.Status.StagedNodes = stagedCount
	pool.Status.UpdatingNodes = updatingCount

	// Determine pool phase.
	switch {
	case errorCount > 0:
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseDegraded
		setDegradedCondition(pool, "NodeError",
			fmt.Sprintf("%d node(s) in error state", errorCount))
	case len(claimedNodes) == 0:
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseIdle
		clearDegradedCondition(pool)
	case readyCount == int32(len(claimedNodes)):
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseReady
		setAvailableCondition(pool, true, "AllNodesReady", "All nodes are running the desired image")
		setProgressingCondition(pool, false, "RolloutComplete", "All nodes updated")
		clearDegradedCondition(pool)
	case updatingCount > 0:
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseRolling
		setProgressingCondition(pool, true, "Rolling",
			fmt.Sprintf("%d of %d nodes updated, %d rebooting",
				readyCount, len(claimedNodes), updatingCount))
		clearDegradedCondition(pool)
	default:
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseStaging
		setProgressingCondition(pool, true, "Staging",
			fmt.Sprintf("%d of %d nodes staged",
				stagedCount, len(claimedNodes)))
		clearDegradedCondition(pool)
	}
}

// orchestrateRollout advances the rollout by selecting staged nodes for
// reboot, respecting maxUnavailable. It cordons and drains nodes before
// advancing them to Rebooting, and uncordons nodes that have completed
// rebooting successfully.
func (r *BootcNodePoolReconciler) orchestrateRollout(
	ctx context.Context,
	pool *bootcdevv1alpha1.BootcNodePool,
	claimedNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	maxUnavailable := pool.Spec.Rollout.MaxUnavailable
	if maxUnavailable <= 0 {
		maxUnavailable = 1
	}

	// Don't advance if the pool is degraded.
	if pool.Status.Phase == bootcdevv1alpha1.BootcNodePoolPhaseDegraded {
		return ctrl.Result{}, nil
	}

	// Uncordon nodes that completed rebooting successfully.
	if err := r.uncordonReadyNodes(ctx, claimedNodes, resolvedDigest); err != nil {
		return ctrl.Result{}, err
	}

	// Count currently updating nodes.
	var currentlyUpdating int32
	var stagedNodes []*bootcdevv1alpha1.BootcNode
	for i := range claimedNodes {
		bn := &claimedNodes[i]
		switch bn.Status.Phase {
		case bootcdevv1alpha1.BootcNodePhaseRebooting, bootcdevv1alpha1.BootcNodePhaseRollingBack:
			currentlyUpdating++
		case bootcdevv1alpha1.BootcNodePhaseStaged:
			// Only advance nodes that are staged and not yet at the
			// desired image.
			if bn.Status.BootedDigest != resolvedDigest &&
				bn.Spec.DesiredPhase == bootcdevv1alpha1.BootcNodeDesiredPhaseStaged {
				stagedNodes = append(stagedNodes, bn)
			}
		case bootcdevv1alpha1.BootcNodePhaseReady:
			// Node completed reboot -- if it was previously set to
			// Rebooting, it has successfully updated.
		}
	}

	// Also count nodes that are cordoned as updating (they are in the
	// process of being drained or rebooted).
	for i := range claimedNodes {
		bn := &claimedNodes[i]
		if bn.Spec.DesiredPhase == bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting &&
			bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseStaged {
			// Already advancing (cordoned/draining) but daemon
			// hasn't started rebooting yet.
			currentlyUpdating++
		}
	}

	// Select batch: how many more can we start?
	available := maxUnavailable - currentlyUpdating
	if available <= 0 || len(stagedNodes) == 0 {
		return ctrl.Result{}, nil
	}

	// Don't start reboots unless all nodes have reached the Staged
	// phase (complete pre-staging before rolling reboots).
	allStaged := true
	for i := range claimedNodes {
		bn := &claimedNodes[i]
		if bn.Status.BootedDigest == resolvedDigest {
			continue // already at desired image
		}
		if bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseStaged &&
			bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseRebooting &&
			bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseReady {
			allStaged = false
			break
		}
	}

	if !allStaged {
		log.V(1).Info("Waiting for all nodes to finish staging before starting reboots")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Cordon, drain, and advance staged nodes to Rebooting.
	for i := int32(0); i < available && i < int32(len(stagedNodes)); i++ {
		bn := stagedNodes[i]

		// Cordon the node to prevent new pods from scheduling.
		if r.Drainer != nil {
			log.Info("Cordoning node before reboot", "node", bn.Name)
			if err := r.Drainer.Cordon(ctx, bn.Name); err != nil {
				return ctrl.Result{}, fmt.Errorf("cordoning node %s: %w", bn.Name, err)
			}

			// Drain the node to evict pods.
			log.Info("Draining node before reboot", "node", bn.Name)
			if err := r.Drainer.Drain(ctx, bn.Name); err != nil {
				return ctrl.Result{}, fmt.Errorf("draining node %s: %w", bn.Name, err)
			}
		}

		log.Info("Advancing BootcNode to Rebooting", "node", bn.Name)
		bn.Spec.DesiredPhase = bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting
		if err := r.Update(ctx, bn); err != nil {
			return ctrl.Result{}, fmt.Errorf("advancing BootcNode %s to Rebooting: %w", bn.Name, err)
		}
	}

	// Requeue soon to check on reboot progress.
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// uncordonReadyNodes uncordons nodes that have completed rebooting and
// are now running the desired image.
func (r *BootcNodePoolReconciler) uncordonReadyNodes(
	ctx context.Context,
	claimedNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) error {
	if r.Drainer == nil {
		return nil
	}

	log := logf.FromContext(ctx)

	for i := range claimedNodes {
		bn := &claimedNodes[i]
		// A node that was rebooting and is now Ready with the desired
		// image needs to be uncordoned.
		if bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseReady {
			continue
		}
		if bn.Status.BootedDigest != resolvedDigest {
			continue
		}
		if bn.Spec.DesiredPhase != bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting {
			continue
		}

		// Check if the node is actually cordoned.
		node := &corev1.Node{}
		if err := r.Get(ctx, types.NamespacedName{Name: bn.Name}, node); err != nil {
			return fmt.Errorf("getting node %s for uncordon: %w", bn.Name, err)
		}

		if node.Spec.Unschedulable {
			log.Info("Uncordoning node after successful reboot", "node", bn.Name)
			if err := r.Drainer.Uncordon(ctx, bn.Name); err != nil {
				return fmt.Errorf("uncordoning node %s: %w", bn.Name, err)
			}
		}

		// Reset desiredPhase to Staged since the node has completed
		// the reboot cycle successfully.
		bn.Spec.DesiredPhase = bootcdevv1alpha1.BootcNodeDesiredPhaseStaged
		if err := r.Update(ctx, bn); err != nil {
			return fmt.Errorf("resetting desiredPhase on BootcNode %s: %w", bn.Name, err)
		}
	}

	return nil
}

// updateStatus re-fetches the pool and persists the status subresource.
func (r *BootcNodePoolReconciler) updateStatus(ctx context.Context, pool *bootcdevv1alpha1.BootcNodePool) error {
	// Save computed status.
	status := pool.Status

	// Re-fetch to get latest resourceVersion.
	if err := r.Get(ctx, types.NamespacedName{Name: pool.Name}, pool); err != nil {
		return fmt.Errorf("re-fetching before status update: %w", err)
	}

	// Restore computed status.
	pool.Status = status

	return r.Status().Update(ctx, pool)
}

// initializeConditions sets all conditions to Unknown on a newly created
// BootcNodePool.
func initializeConditions(pool *bootcdevv1alpha1.BootcNodePool) {
	now := metav1.Now()
	pool.Status.Conditions = []metav1.Condition{
		{
			Type:               conditionTypeAvailable,
			Status:             metav1.ConditionUnknown,
			Reason:             "Initializing",
			Message:            "Pool is being initialized",
			LastTransitionTime: now,
		},
		{
			Type:               conditionTypeProgressing,
			Status:             metav1.ConditionUnknown,
			Reason:             "Initializing",
			Message:            "Pool is being initialized",
			LastTransitionTime: now,
		},
		{
			Type:               conditionTypeDegraded,
			Status:             metav1.ConditionFalse,
			Reason:             "Initializing",
			Message:            "Pool is being initialized",
			LastTransitionTime: now,
		},
	}
	pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseIdle
}

// setAvailableCondition updates the Available condition.
func setAvailableCondition(pool *bootcdevv1alpha1.BootcNodePool, available bool, reason, message string) {
	status := metav1.ConditionFalse
	if available {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:    conditionTypeAvailable,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// setProgressingCondition updates the Progressing condition.
func setProgressingCondition(pool *bootcdevv1alpha1.BootcNodePool, progressing bool, reason, message string) {
	status := metav1.ConditionFalse
	if progressing {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:    conditionTypeProgressing,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// setDegradedCondition marks the pool as degraded.
func setDegradedCondition(pool *bootcdevv1alpha1.BootcNodePool, reason, message string) {
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:    conditionTypeDegraded,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})
}

// clearDegradedCondition clears the degraded condition.
func clearDegradedCondition(pool *bootcdevv1alpha1.BootcNodePool) {
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:    conditionTypeDegraded,
		Status:  metav1.ConditionFalse,
		Reason:  "OK",
		Message: "",
	})
}

// findBootcNodePoolsForBootcNode maps a BootcNode change to the pool
// that has claimed it (if any).
func (r *BootcNodePoolReconciler) findBootcNodePoolsForBootcNode(ctx context.Context, obj client.Object) []reconcile.Request {
	bn, ok := obj.(*bootcdevv1alpha1.BootcNode)
	if !ok {
		return nil
	}

	// If the BootcNode has a pool label, reconcile that pool.
	if poolName := bn.Labels[poolLabelKey]; poolName != "" {
		return []reconcile.Request{
			{NamespacedName: types.NamespacedName{Name: poolName}},
		}
	}

	// Otherwise, this might be a newly created BootcNode that a pool
	// could claim. Check all pools.
	return r.findAllBootcNodePools(ctx)
}

// findBootcNodePoolsForNode maps a Node change to pools whose
// nodeSelector might be affected.
func (r *BootcNodePoolReconciler) findBootcNodePoolsForNode(ctx context.Context, obj client.Object) []reconcile.Request {
	// A node's labels changed, so any pool's nodeSelector could now
	// match or no longer match. Reconcile all pools.
	return r.findAllBootcNodePools(ctx)
}

// findAllBootcNodePools returns reconcile requests for all pools.
func (r *BootcNodePoolReconciler) findAllBootcNodePools(ctx context.Context) []reconcile.Request {
	poolList := &bootcdevv1alpha1.BootcNodePoolList{}
	if err := r.List(ctx, poolList); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list BootcNodePools")
		return nil
	}

	requests := make([]reconcile.Request, len(poolList.Items))
	for i, pool := range poolList.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{Name: pool.Name},
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *BootcNodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootcdevv1alpha1.BootcNodePool{}).
		Watches(&bootcdevv1alpha1.BootcNode{}, handler.EnqueueRequestsFromMapFunc(
			r.findBootcNodePoolsForBootcNode,
		)).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(
			r.findBootcNodePoolsForNode,
		)).
		Named("bootcnodepool").
		Complete(r)
}
