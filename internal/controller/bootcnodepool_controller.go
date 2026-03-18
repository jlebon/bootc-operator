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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/pkg/drain"
)

const (
	// finalizerName is the finalizer added to BootcNodePool resources.
	finalizerName = "bootc.dev/cleanup"

	// poolLabelKey is the label set on BootcNode resources to indicate
	// which pool has claimed them.
	poolLabelKey = "bootc.dev/pool"

	// reResolutionInterval is how often the controller re-queues to
	// check for new image digests (tag re-resolution).
	reResolutionInterval = 5 * time.Minute

	// activeRolloutInterval is the requeue interval used when a rollout
	// is in progress (nodes staging, rebooting, or waiting).
	activeRolloutInterval = 15 * time.Second

	// Condition types for BootcNodePool status.
	conditionTypeAvailable  = "Available"
	conditionTypeProgessing = "Progressing"
	conditionTypeDegraded   = "Degraded"
)

// BootcNodePoolReconciler reconciles a BootcNodePool object
type BootcNodePoolReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Drainer drain.Drainer
}

// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools/finalizers,verbs=update
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;delete
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile moves the cluster closer to the desired state defined by a
// BootcNodePool. It follows the kubebuilder deploy-image reconciler
// pattern: fetch → init conditions → finalizer → deletion handling →
// node matching → claiming/releasing → status update.
func (r *BootcNodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the BootcNodePool CR.
	pool := &v1alpha1.BootcNodePool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		if errors.IsNotFound(err) {
			log.Info("BootcNodePool not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Initialize status conditions if empty.
	if len(pool.Status.Conditions) == 0 {
		initConditions(pool)
		if err := r.Status().Update(ctx, pool); err != nil {
			return ctrl.Result{}, err
		}
		// Re-fetch after status update to avoid conflicts.
		if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Add finalizer if not present.
	if !controllerutil.ContainsFinalizer(pool, finalizerName) {
		controllerutil.AddFinalizer(pool, finalizerName)
		if err := r.Update(ctx, pool); err != nil {
			return ctrl.Result{}, err
		}
		// Re-fetch after finalizer update.
		if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Handle deletion.
	if !pool.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, pool)
	}

	// 5. Resolve image reference.
	desiredImage := resolveImage(pool.Spec.Image)

	// 6. List all BootcNodes in the cluster.
	bootcNodeList := &v1alpha1.BootcNodeList{}
	if err := r.List(ctx, bootcNodeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing BootcNodes: %w", err)
	}

	// 7. List all Nodes to evaluate the nodeSelector.
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing Nodes: %w", err)
	}

	// Build a map of node name → Node for lookups.
	nodeMap := make(map[string]*corev1.Node, len(nodeList.Items))
	for i := range nodeList.Items {
		nodeMap[nodeList.Items[i].Name] = &nodeList.Items[i]
	}

	// 8. Determine which nodes match the pool's nodeSelector.
	matchingNodeNames, err := r.matchingNodes(pool, nodeMap)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("evaluating nodeSelector: %w", err)
	}

	// Build a map of BootcNode name → BootcNode.
	bootcNodeMap := make(map[string]*v1alpha1.BootcNode, len(bootcNodeList.Items))
	for i := range bootcNodeList.Items {
		bootcNodeMap[bootcNodeList.Items[i].Name] = &bootcNodeList.Items[i]
	}

	// 9. Check for overlapping pools.
	if overlapping := r.detectOverlaps(pool, matchingNodeNames, bootcNodeMap); overlapping != "" {
		return r.setDegraded(ctx, pool, fmt.Sprintf("Node %q is already claimed by pool %q", overlapping, bootcNodeMap[overlapping].Labels[poolLabelKey]))
	}

	// 10. Claim matching BootcNodes, release non-matching ones.
	claimed, err := r.syncClaims(ctx, pool, desiredImage, matchingNodeNames, bootcNodeMap)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("syncing claims: %w", err)
	}

	// 11. Orchestrate the rollout: advance staged nodes to rebooting,
	// uncordon successfully rebooted nodes, etc.
	rollout, err := r.orchestrateRollout(ctx, pool, claimed, nodeMap, desiredImage)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("orchestrating rollout: %w", err)
	}

	// Re-fetch claimed BootcNodes after rollout may have modified them.
	// This ensures status counters reflect the latest state.
	for i, bn := range claimed {
		fresh := &v1alpha1.BootcNode{}
		if err := r.Get(ctx, types.NamespacedName{Name: bn.Name}, fresh); err == nil {
			claimed[i] = fresh
		}
	}

	// 12. Compute and update status.
	if err := r.updateStatus(ctx, req.NamespacedName, pool, desiredImage, claimed, int32(len(matchingNodeNames))); err != nil {
		return ctrl.Result{}, err
	}

	// Use a shorter requeue interval during active rollout.
	requeueAfter := reResolutionInterval
	if rollout.requeue {
		requeueAfter = activeRolloutInterval
	}

	log.Info("Reconciliation complete", "pool", pool.Name, "targetNodes", len(matchingNodeNames), "claimed", len(claimed))
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileDelete handles BootcNodePool deletion: release all claimed
// BootcNodes and remove the finalizer.
func (r *BootcNodePoolReconciler) reconcileDelete(ctx context.Context, pool *v1alpha1.BootcNodePool) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Handling BootcNodePool deletion", "pool", pool.Name)

	// Find all BootcNodes claimed by this pool and release them.
	bootcNodeList := &v1alpha1.BootcNodeList{}
	if err := r.List(ctx, bootcNodeList, client.MatchingLabels{poolLabelKey: pool.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing claimed BootcNodes: %w", err)
	}

	for i := range bootcNodeList.Items {
		bn := &bootcNodeList.Items[i]
		if err := r.releaseBootcNode(ctx, bn); err != nil {
			return ctrl.Result{}, fmt.Errorf("releasing BootcNode %q: %w", bn.Name, err)
		}
		log.Info("Released BootcNode during pool deletion", "bootcNode", bn.Name)
	}

	// Remove the finalizer.
	controllerutil.RemoveFinalizer(pool, finalizerName)
	if err := r.Update(ctx, pool); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("BootcNodePool deletion complete", "pool", pool.Name)
	return ctrl.Result{}, nil
}

// matchingNodes returns the set of node names that match the pool's
// nodeSelector. When no nodeSelector is specified, no nodes match.
func (r *BootcNodePoolReconciler) matchingNodes(pool *v1alpha1.BootcNodePool, nodeMap map[string]*corev1.Node) (map[string]bool, error) {
	result := make(map[string]bool)

	if pool.Spec.NodeSelector == nil {
		return result, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing nodeSelector: %w", err)
	}

	for name, node := range nodeMap {
		if selector.Matches(labels.Set(node.Labels)) {
			result[name] = true
		}
	}

	return result, nil
}

// detectOverlaps checks whether any matching node is already claimed by
// a different pool. Returns the name of the first conflicting node, or
// empty string if no overlaps.
func (r *BootcNodePoolReconciler) detectOverlaps(pool *v1alpha1.BootcNodePool, matchingNodeNames map[string]bool, bootcNodeMap map[string]*v1alpha1.BootcNode) string {
	for nodeName := range matchingNodeNames {
		bn, exists := bootcNodeMap[nodeName]
		if !exists {
			continue
		}
		claimedBy := bn.Labels[poolLabelKey]
		if claimedBy != "" && claimedBy != pool.Name {
			return nodeName
		}
	}
	return ""
}

// syncClaims ensures all matching BootcNodes are claimed by this pool
// (with the correct spec) and all previously claimed BootcNodes that no
// longer match are released. Returns the list of currently claimed
// BootcNodes.
func (r *BootcNodePoolReconciler) syncClaims(ctx context.Context, pool *v1alpha1.BootcNodePool, desiredImage string, matchingNodeNames map[string]bool, bootcNodeMap map[string]*v1alpha1.BootcNode) ([]*v1alpha1.BootcNode, error) {
	log := logf.FromContext(ctx)
	var claimed []*v1alpha1.BootcNode

	// Claim matching BootcNodes.
	for nodeName := range matchingNodeNames {
		bn, exists := bootcNodeMap[nodeName]
		if !exists {
			// No BootcNode for this node yet (daemon hasn't created
			// it). Skip -- it will be claimed when the daemon creates
			// it and the reconciler re-runs.
			continue
		}
		if err := r.claimBootcNode(ctx, pool, bn, desiredImage); err != nil {
			return nil, fmt.Errorf("claiming BootcNode %q: %w", bn.Name, err)
		}
		claimed = append(claimed, bn)
	}

	// Release BootcNodes previously claimed by this pool that no longer
	// match the nodeSelector.
	for name, bn := range bootcNodeMap {
		claimedBy := bn.Labels[poolLabelKey]
		if claimedBy != pool.Name {
			continue
		}
		if matchingNodeNames[name] {
			continue
		}
		if err := r.releaseBootcNode(ctx, bn); err != nil {
			return nil, fmt.Errorf("releasing BootcNode %q: %w", bn.Name, err)
		}
		log.Info("Released BootcNode", "bootcNode", bn.Name, "reason", "no longer matches nodeSelector")
	}

	return claimed, nil
}

// claimBootcNode sets the pool label and spec on a BootcNode. No-ops
// if already correctly claimed.
func (r *BootcNodePoolReconciler) claimBootcNode(ctx context.Context, pool *v1alpha1.BootcNodePool, bn *v1alpha1.BootcNode, desiredImage string) error {
	needsUpdate := false

	// Ensure pool label is set.
	if bn.Labels == nil {
		bn.Labels = make(map[string]string)
	}
	if bn.Labels[poolLabelKey] != pool.Name {
		bn.Labels[poolLabelKey] = pool.Name
		needsUpdate = true
	}

	// Set spec fields.
	if bn.Spec.DesiredImage != desiredImage {
		bn.Spec.DesiredImage = desiredImage
		needsUpdate = true
	}

	desiredPhase := desiredPhaseForNode(bn)
	if bn.Spec.DesiredPhase != desiredPhase {
		bn.Spec.DesiredPhase = desiredPhase
		needsUpdate = true
	}

	rebootPolicy := pool.Spec.Disruption.RebootPolicy
	if rebootPolicy == "" {
		rebootPolicy = v1alpha1.RebootPolicyAuto
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

// desiredPhaseForNode determines the correct desiredPhase for a
// BootcNode during the claiming step. The initial desired phase is
// always Staged -- the rollout orchestrator advances nodes to
// Rebooting after staging is complete and batch constraints are met.
//
// If the node is already being advanced by the rollout orchestrator
// (desiredPhase=Rebooting or RollingBack), we preserve that phase.
func desiredPhaseForNode(bn *v1alpha1.BootcNode) v1alpha1.BootcNodeDesiredPhase {
	// Preserve Rebooting/RollingBack if already set by the orchestrator.
	if bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRebooting ||
		bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRollingBack {
		return bn.Spec.DesiredPhase
	}
	return v1alpha1.BootcNodeDesiredPhaseStaged
}

// releaseBootcNode clears the pool label and spec on a BootcNode.
func (r *BootcNodePoolReconciler) releaseBootcNode(ctx context.Context, bn *v1alpha1.BootcNode) error {
	needsUpdate := false

	if bn.Labels != nil && bn.Labels[poolLabelKey] != "" {
		delete(bn.Labels, poolLabelKey)
		needsUpdate = true
	}

	if bn.Spec.DesiredImage != "" || bn.Spec.DesiredPhase != "" || bn.Spec.RebootPolicy != "" {
		bn.Spec = v1alpha1.BootcNodeSpec{}
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	return r.Update(ctx, bn)
}

// resolveImage extracts the image digest from an image reference. For
// MVP, if the reference contains @sha256:, we treat it as already
// resolved. Otherwise, we use the tag reference as-is (full digest
// resolution via go-containerregistry is deferred to the digest resolver
// item in the plan).
func resolveImage(imageRef string) string {
	return imageRef
}

// updateStatus re-fetches the pool, computes status from the claimed
// BootcNodes, and updates the status subresource.
func (r *BootcNodePoolReconciler) updateStatus(ctx context.Context, key types.NamespacedName, pool *v1alpha1.BootcNodePool, desiredImage string, claimed []*v1alpha1.BootcNode, targetNodes int32) error {
	// Re-fetch before status update to avoid "object modified" errors.
	if err := r.Get(ctx, key, pool); err != nil {
		return err
	}

	pool.Status.ObservedGeneration = pool.Generation
	pool.Status.TargetNodes = targetNodes

	if strings.Contains(desiredImage, "@sha256:") {
		pool.Status.ResolvedDigest = desiredImage
	}

	// Count nodes by phase.
	var readyNodes, stagedNodes, updatingNodes int32
	for _, bn := range claimed {
		switch bn.Status.Phase {
		case v1alpha1.BootcNodePhaseReady:
			// Only count as ready if running the desired image.
			if bn.Status.Booted.Image == desiredImage {
				readyNodes++
			}
		case v1alpha1.BootcNodePhaseStaged:
			stagedNodes++
		case v1alpha1.BootcNodePhaseStaging:
			// Staging counts toward neither staged nor updating.
		case v1alpha1.BootcNodePhaseRebooting, v1alpha1.BootcNodePhaseRollingBack:
			updatingNodes++
		case v1alpha1.BootcNodePhaseError:
			// Error nodes are not counted in any progress bucket.
		}
	}

	pool.Status.ReadyNodes = readyNodes
	pool.Status.StagedNodes = stagedNodes
	pool.Status.UpdatingNodes = updatingNodes

	// Compute phase.
	pool.Status.Phase = computePhase(pool, claimed, desiredImage)

	// Update conditions.
	updateConditions(pool, claimed)

	return r.Status().Update(ctx, pool)
}

// computePhase determines the overall pool phase from node states.
func computePhase(pool *v1alpha1.BootcNodePool, claimed []*v1alpha1.BootcNode, desiredImage string) v1alpha1.BootcNodePoolPhase {
	if pool.Status.TargetNodes == 0 {
		return v1alpha1.BootcNodePoolPhaseIdle
	}

	hasError := false
	hasStaging := false
	hasStaged := false
	hasRebooting := false
	allReady := true

	for _, bn := range claimed {
		switch bn.Status.Phase {
		case v1alpha1.BootcNodePhaseError:
			hasError = true
			allReady = false
		case v1alpha1.BootcNodePhaseStaging:
			hasStaging = true
			allReady = false
		case v1alpha1.BootcNodePhaseStaged:
			hasStaged = true
			allReady = false
		case v1alpha1.BootcNodePhaseRebooting, v1alpha1.BootcNodePhaseRollingBack:
			hasRebooting = true
			allReady = false
		case v1alpha1.BootcNodePhaseReady:
			if bn.Status.Booted.Image != desiredImage {
				allReady = false
			}
		default:
			allReady = false
		}
	}

	// If we have fewer claimed BootcNodes than target nodes (daemons
	// haven't created their BootcNodes yet), we're not all ready.
	if int32(len(claimed)) < pool.Status.TargetNodes {
		allReady = false
	}

	if hasError {
		return v1alpha1.BootcNodePoolPhaseDegraded
	}
	if allReady {
		return v1alpha1.BootcNodePoolPhaseReady
	}
	if hasRebooting {
		return v1alpha1.BootcNodePoolPhaseRolling
	}
	if hasStaging {
		return v1alpha1.BootcNodePoolPhaseStaging
	}
	if hasStaged {
		// Nodes are staged and waiting to be advanced to rebooting.
		// During a rolling update, some nodes may be staged while
		// others are already ready (completed earlier batches).
		return v1alpha1.BootcNodePoolPhaseRolling
	}

	// No clear signal yet -- nodes may not have reported status.
	return v1alpha1.BootcNodePoolPhaseStaging
}

// updateConditions sets the standard conditions on the pool based on
// current state.
func updateConditions(pool *v1alpha1.BootcNodePool, claimed []*v1alpha1.BootcNode) {
	now := metav1.Now()

	// Available: true when at least one node is running the desired image.
	availableStatus := metav1.ConditionFalse
	availableMessage := "No nodes are running the desired image"
	if pool.Status.ReadyNodes > 0 {
		availableStatus = metav1.ConditionTrue
		availableMessage = fmt.Sprintf("%d of %d nodes running desired image", pool.Status.ReadyNodes, pool.Status.TargetNodes)
	}
	if pool.Status.TargetNodes == 0 {
		availableStatus = metav1.ConditionFalse
		availableMessage = "No nodes targeted by nodeSelector"
	}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:               conditionTypeAvailable,
		Status:             availableStatus,
		ObservedGeneration: pool.Generation,
		LastTransitionTime: now,
		Reason:             string(pool.Status.Phase),
		Message:            availableMessage,
	})

	// Progressing: true when a rollout is in progress (not all nodes ready).
	progressingStatus := metav1.ConditionFalse
	progressingMessage := "All nodes are up to date"
	if pool.Status.Phase == v1alpha1.BootcNodePoolPhaseStaging || pool.Status.Phase == v1alpha1.BootcNodePoolPhaseRolling {
		progressingStatus = metav1.ConditionTrue
		progressingMessage = fmt.Sprintf("%d of %d nodes staged, %d updating", pool.Status.StagedNodes, pool.Status.TargetNodes, pool.Status.UpdatingNodes)
	}
	if pool.Status.Phase == v1alpha1.BootcNodePoolPhaseIdle {
		progressingStatus = metav1.ConditionFalse
		progressingMessage = "No nodes targeted"
	}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:               conditionTypeProgessing,
		Status:             progressingStatus,
		ObservedGeneration: pool.Generation,
		LastTransitionTime: now,
		Reason:             string(pool.Status.Phase),
		Message:            progressingMessage,
	})

	// Degraded: true when any node is in error.
	degradedStatus := metav1.ConditionFalse
	degradedMessage := "No errors"
	for _, bn := range claimed {
		if bn.Status.Phase == v1alpha1.BootcNodePhaseError {
			degradedStatus = metav1.ConditionTrue
			degradedMessage = fmt.Sprintf("Node %q is in error state: %s", bn.Name, bn.Status.Message)
			break
		}
	}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:               conditionTypeDegraded,
		Status:             degradedStatus,
		ObservedGeneration: pool.Generation,
		LastTransitionTime: now,
		Reason:             string(pool.Status.Phase),
		Message:            degradedMessage,
	})
}

// setDegraded sets the Degraded condition and phase on a pool and
// returns a result with requeue.
func (r *BootcNodePoolReconciler) setDegraded(ctx context.Context, pool *v1alpha1.BootcNodePool, message string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Pool is degraded", "pool", pool.Name, "reason", message)

	pool.Status.Phase = v1alpha1.BootcNodePoolPhaseDegraded
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:               conditionTypeDegraded,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: pool.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             "OverlappingPool",
		Message:            message,
	})

	if err := r.Status().Update(ctx, pool); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: reResolutionInterval}, nil
}

// initConditions sets all conditions to Unknown on a new BootcNodePool.
func initConditions(pool *v1alpha1.BootcNodePool) {
	now := metav1.Now()
	pool.Status.Phase = v1alpha1.BootcNodePoolPhaseIdle
	pool.Status.Conditions = []metav1.Condition{
		{
			Type:               conditionTypeAvailable,
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: pool.Generation,
			LastTransitionTime: now,
			Reason:             "Initializing",
			Message:            "Reconciliation has not yet completed",
		},
		{
			Type:               conditionTypeProgessing,
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: pool.Generation,
			LastTransitionTime: now,
			Reason:             "Initializing",
			Message:            "Reconciliation has not yet completed",
		},
		{
			Type:               conditionTypeDegraded,
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: pool.Generation,
			LastTransitionTime: now,
			Reason:             "Initializing",
			Message:            "Reconciliation has not yet completed",
		},
	}
}

// findBootcNodePoolsForBootcNode maps a BootcNode event to the
// BootcNodePool(s) that should be reconciled. A BootcNode triggers
// reconciliation of the pool that claims it (via the pool label), plus
// any pool whose nodeSelector matches the node.
func (r *BootcNodePoolReconciler) findBootcNodePoolsForBootcNode(ctx context.Context, obj client.Object) []reconcile.Request {
	bn, ok := obj.(*v1alpha1.BootcNode)
	if !ok {
		return nil
	}

	var requests []reconcile.Request

	// If the BootcNode is claimed by a pool, reconcile that pool.
	if poolName := bn.Labels[poolLabelKey]; poolName != "" {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: poolName},
		})
	}

	// Also reconcile any pool that might want to claim this node.
	pools := &v1alpha1.BootcNodePoolList{}
	if err := r.List(ctx, pools); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list BootcNodePools for BootcNode mapping")
		return requests
	}

	for i := range pools.Items {
		pool := &pools.Items[i]
		if pool.Spec.NodeSelector == nil {
			continue
		}

		// Look up the corresponding Node to check the selector.
		node := &corev1.Node{}
		if err := r.Get(ctx, types.NamespacedName{Name: bn.Name}, node); err != nil {
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
		if err != nil {
			continue
		}

		if selector.Matches(labels.Set(node.Labels)) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pool.Name},
			})
		}
	}

	return dedup(requests)
}

// findBootcNodePoolsForNode maps a Node event to the BootcNodePool(s)
// that should be reconciled (any pool whose nodeSelector matches).
func (r *BootcNodePoolReconciler) findBootcNodePoolsForNode(ctx context.Context, obj client.Object) []reconcile.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}

	pools := &v1alpha1.BootcNodePoolList{}
	if err := r.List(ctx, pools); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list BootcNodePools for Node mapping")
		return nil
	}

	var requests []reconcile.Request
	for i := range pools.Items {
		pool := &pools.Items[i]
		if pool.Spec.NodeSelector == nil {
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
		if err != nil {
			continue
		}

		if selector.Matches(labels.Set(node.Labels)) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pool.Name},
			})
		}
	}

	return requests
}

// dedup removes duplicate reconcile.Requests.
func dedup(requests []reconcile.Request) []reconcile.Request {
	seen := make(map[types.NamespacedName]bool, len(requests))
	result := make([]reconcile.Request, 0, len(requests))
	for _, r := range requests {
		if !seen[r.NamespacedName] {
			seen[r.NamespacedName] = true
			result = append(result, r)
		}
	}
	return result
}

// SetupWithManager sets up the controller with the Manager.
func (r *BootcNodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BootcNodePool{}).
		Watches(&v1alpha1.BootcNode{}, handler.EnqueueRequestsFromMapFunc(
			r.findBootcNodePoolsForBootcNode,
		)).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(
			r.findBootcNodePoolsForNode,
		)).
		Named("bootcnodepool").
		Complete(r)
}
