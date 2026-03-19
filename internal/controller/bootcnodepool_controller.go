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
	"k8s.io/client-go/tools/events"
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

	// rebootingSinceAnnotation is set on BootcNode resources when the
	// operator advances them to Rebooting. The value is an RFC3339
	// timestamp. This is level-based (survives operator restarts) and
	// is used to detect reboot timeouts for auto rollback.
	rebootingSinceAnnotation = "bootc.dev/rebooting-since"

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

	// defaultHealthCheckTimeout is the default duration the operator
	// waits for a node to become Ready after reboot before triggering
	// a rollback.
	defaultHealthCheckTimeout = 5 * time.Minute

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
	Recorder          events.EventRecorder
	OperatorNamespace string

	// Now returns the current time. Defaults to time.Now when nil.
	// Injected in tests to control time for health check timeout
	// testing.
	Now func() time.Time
}

// now returns the current time, using the injected function if set.
func (r *BootcNodePoolReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// recordPoolEvent emits an event on a BootcNodePool. Safe to call when
// the recorder is nil (e.g. in tests without an event recorder).
func (r *BootcNodePoolReconciler) recordPoolEvent(pool *bootcdevv1alpha1.BootcNodePool, eventType, reason, messageFmt string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(pool, nil, eventType, reason, reason, messageFmt, args...)
}

// recordNodeEvent emits an event on a BootcNode. Safe to call when the
// recorder is nil.
func (r *BootcNodePoolReconciler) recordNodeEvent(bn *bootcdevv1alpha1.BootcNode, eventType, reason, messageFmt string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(bn, nil, eventType, reason, reason, messageFmt, args...)
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
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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

	// Emit RolloutStarted event when a new digest is detected.
	if pool.Status.ResolvedDigest != "" && pool.Status.ResolvedDigest != resolvedDigest {
		r.recordPoolEvent(pool, corev1.EventTypeNormal, "RolloutStarted",
			"New image digest detected: %s", resolvedDigest)
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
	for i := range claimedNodes {
		bn := &claimedNodes[i]
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
			r.recordNodeEvent(bn, corev1.EventTypeNormal, "ImageStaged",
				"Image %s has been staged and is ready to apply", bn.Spec.DesiredImage)
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

	// Determine pool phase. Track the previous phase for event emission.
	prevPhase := pool.Status.Phase

	switch {
	case errorCount > 0:
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseDegraded
		setDegradedCondition(pool, "NodeError",
			fmt.Sprintf("%d node(s) in error state", errorCount))
		if prevPhase != bootcdevv1alpha1.BootcNodePoolPhaseDegraded {
			r.recordPoolEvent(pool, corev1.EventTypeWarning, "RolloutDegraded",
				"%d node(s) in error state", errorCount)
		}
	case len(claimedNodes) == 0:
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseIdle
		clearDegradedCondition(pool)
	case readyCount == int32(len(claimedNodes)):
		pool.Status.Phase = bootcdevv1alpha1.BootcNodePoolPhaseReady
		setAvailableCondition(pool, true, "AllNodesReady", "All nodes are running the desired image")
		setProgressingCondition(pool, false, "RolloutComplete", "All nodes updated")
		clearDegradedCondition(pool)
		if prevPhase != bootcdevv1alpha1.BootcNodePoolPhaseReady && prevPhase != "" {
			r.recordPoolEvent(pool, corev1.EventTypeNormal, "RolloutComplete",
				"All %d node(s) are running the desired image", len(claimedNodes))
		}
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
// rebooting successfully. It also checks for reboot timeouts and
// triggers auto rollback when nodes fail to come up in time.
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

	// Handle completed rollbacks first (nodes that rolled back and
	// are now running the old image again).
	rollbackCompleted, err := r.handleCompletedRollbacks(ctx, claimedNodes, resolvedDigest)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Don't advance if the pool is degraded (either from pre-existing
	// error nodes or from a rollback that just completed).
	if pool.Status.Phase == bootcdevv1alpha1.BootcNodePoolPhaseDegraded || rollbackCompleted {
		return ctrl.Result{}, nil
	}

	// Check for reboot timeouts: nodes that have been rebooting for
	// longer than healthCheck.timeout get rolled back.
	timedOut, err := r.checkRebootTimeouts(ctx, pool, claimedNodes)
	if err != nil {
		return ctrl.Result{}, err
	}
	if timedOut {
		// A timeout was detected and rollback initiated. Don't
		// advance further -- the pool will be set to Degraded on
		// the next computeStatus.
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Uncordon nodes that completed rebooting successfully.
	if err := r.uncordonReadyNodes(ctx, claimedNodes, resolvedDigest); err != nil {
		return ctrl.Result{}, err
	}

	// Count currently updating nodes and find staged candidates.
	currentlyUpdating, stagedNodes := countUpdatingAndStaged(claimedNodes, resolvedDigest)

	// Select batch: how many more can we start?
	available := maxUnavailable - currentlyUpdating
	if available <= 0 || len(stagedNodes) == 0 {
		return ctrl.Result{}, nil
	}

	// Don't start reboots unless all nodes have reached the Staged
	// phase (complete pre-staging before rolling reboots).
	if !allNodesStaged(claimedNodes, resolvedDigest) {
		log.V(1).Info("Waiting for all nodes to finish staging before starting reboots")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Emit StagingComplete when we first start advancing to reboots.
	// This is detected by checking that no nodes are currently
	// updating (first batch) and there are staged candidates.
	if currentlyUpdating == 0 && len(stagedNodes) > 0 {
		r.recordPoolEvent(pool, corev1.EventTypeNormal, "StagingComplete",
			"All %d node(s) have staged the desired image, starting rolling reboots",
			len(claimedNodes))
	}

	// Cordon, drain, and advance staged nodes to Rebooting.
	if err := r.advanceNodesToRebooting(ctx, pool, stagedNodes, available); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue soon to check on reboot progress.
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// countUpdatingAndStaged counts nodes currently in update and finds
// staged candidates that can be advanced to Rebooting.
func countUpdatingAndStaged(
	claimedNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) (int32, []*bootcdevv1alpha1.BootcNode) {
	var currentlyUpdating int32
	var stagedNodes []*bootcdevv1alpha1.BootcNode

	for i := range claimedNodes {
		bn := &claimedNodes[i]
		switch bn.Status.Phase {
		case bootcdevv1alpha1.BootcNodePhaseRebooting, bootcdevv1alpha1.BootcNodePhaseRollingBack:
			currentlyUpdating++
		case bootcdevv1alpha1.BootcNodePhaseStaged:
			if bn.Status.BootedDigest != resolvedDigest &&
				bn.Spec.DesiredPhase == bootcdevv1alpha1.BootcNodeDesiredPhaseStaged {
				stagedNodes = append(stagedNodes, bn)
			}
		}
	}

	// Also count nodes that are cordoned as updating (they are in the
	// process of being drained or rebooted).
	for i := range claimedNodes {
		bn := &claimedNodes[i]
		if bn.Spec.DesiredPhase == bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting &&
			bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseStaged {
			currentlyUpdating++
		}
	}

	return currentlyUpdating, stagedNodes
}

// allNodesStaged checks whether all nodes that need updating have
// reached the Staged phase (or are already rebooting/ready).
func allNodesStaged(
	claimedNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) bool {
	for i := range claimedNodes {
		bn := &claimedNodes[i]
		if bn.Status.BootedDigest == resolvedDigest {
			continue
		}
		if bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseStaged &&
			bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseRebooting &&
			bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseReady {
			return false
		}
	}
	return true
}

// advanceNodesToRebooting cordons, drains, and advances staged nodes to
// the Rebooting phase, setting the rebooting-since annotation for
// timeout tracking.
func (r *BootcNodePoolReconciler) advanceNodesToRebooting(
	ctx context.Context,
	pool *bootcdevv1alpha1.BootcNodePool,
	stagedNodes []*bootcdevv1alpha1.BootcNode,
	available int32,
) error {
	log := logf.FromContext(ctx)

	for i := int32(0); i < available && i < int32(len(stagedNodes)); i++ {
		bn := stagedNodes[i]

		if r.Drainer != nil {
			log.Info("Cordoning node before reboot", "node", bn.Name)
			if err := r.Drainer.Cordon(ctx, bn.Name); err != nil {
				return fmt.Errorf("cordoning node %s: %w", bn.Name, err)
			}

			log.Info("Draining node before reboot", "node", bn.Name)
			if err := r.Drainer.Drain(ctx, bn.Name); err != nil {
				return fmt.Errorf("draining node %s: %w", bn.Name, err)
			}
		}

		log.Info("Advancing BootcNode to Rebooting", "node", bn.Name)
		bn.Spec.DesiredPhase = bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting

		// Set the rebooting-since annotation to track the timeout.
		if bn.Annotations == nil {
			bn.Annotations = make(map[string]string)
		}
		bn.Annotations[rebootingSinceAnnotation] = r.now().UTC().Format(time.RFC3339)

		if err := r.Update(ctx, bn); err != nil {
			return fmt.Errorf("advancing BootcNode %s to Rebooting: %w", bn.Name, err)
		}

		r.recordNodeEvent(bn, corev1.EventTypeNormal, "RebootInitiated",
			"Node drained and reboot initiated for image %s", bn.Spec.DesiredImage)
		r.recordPoolEvent(pool, corev1.EventTypeNormal, "RebootInitiated",
			"Reboot initiated on node %s", bn.Name)
	}

	return nil
}

// getHealthCheckTimeout returns the health check timeout for a pool,
// defaulting to 5 minutes if not specified.
func getHealthCheckTimeout(pool *bootcdevv1alpha1.BootcNodePool) time.Duration {
	if pool.Spec.HealthCheck.Timeout.Duration > 0 {
		return pool.Spec.HealthCheck.Timeout.Duration
	}
	return defaultHealthCheckTimeout
}

// checkRebootTimeouts checks if any rebooting nodes have exceeded the
// health check timeout. If so, it triggers a rollback by setting
// desiredPhase to RollingBack. Returns true if a timeout was detected.
func (r *BootcNodePoolReconciler) checkRebootTimeouts(
	ctx context.Context,
	pool *bootcdevv1alpha1.BootcNodePool,
	claimedNodes []bootcdevv1alpha1.BootcNode,
) (bool, error) {
	log := logf.FromContext(ctx)
	timeout := getHealthCheckTimeout(pool)
	now := r.now()
	timedOut := false

	for i := range claimedNodes {
		bn := &claimedNodes[i]

		// Only check nodes that are in the Rebooting phase (either
		// desired or actual).
		if bn.Spec.DesiredPhase != bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting {
			continue
		}

		// Skip nodes that are already rolling back.
		if bn.Status.Phase == bootcdevv1alpha1.BootcNodePhaseRollingBack {
			continue
		}

		// Check the rebooting-since annotation.
		sinceStr, ok := bn.Annotations[rebootingSinceAnnotation]
		if !ok {
			continue
		}

		since, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			log.Error(err, "Failed to parse rebooting-since annotation",
				"node", bn.Name, "value", sinceStr)
			continue
		}

		if now.Sub(since) <= timeout {
			continue
		}

		// Timeout exceeded. Trigger rollback.
		log.Info("Reboot timeout exceeded, triggering rollback",
			"node", bn.Name,
			"timeout", timeout,
			"elapsed", now.Sub(since))

		bn.Spec.DesiredPhase = bootcdevv1alpha1.BootcNodeDesiredPhaseRollingBack
		if err := r.Update(ctx, bn); err != nil {
			return false, fmt.Errorf("triggering rollback on node %s: %w", bn.Name, err)
		}

		r.recordNodeEvent(bn, corev1.EventTypeWarning, "RollbackTriggered",
			"Health check timeout exceeded (%s), triggering rollback", timeout)
		r.recordPoolEvent(pool, corev1.EventTypeWarning, "RollbackTriggered",
			"Rollback triggered on node %s after timeout (%s)", bn.Name, timeout)
		timedOut = true
	}

	return timedOut, nil
}

// handleCompletedRollbacks detects nodes that have completed a rollback
// (desiredPhase is RollingBack, status is Ready, booted image differs
// from the desired image). It uncordons them, clears the rebooting-since
// annotation, resets desiredPhase to Staged, and sets the node's status
// phase to Error. This will trigger Degraded condition on the pool.
// Returns true if any rollback was completed.
func (r *BootcNodePoolReconciler) handleCompletedRollbacks(
	ctx context.Context,
	claimedNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) (bool, error) {
	log := logf.FromContext(ctx)
	completed := false

	for i := range claimedNodes {
		bn := &claimedNodes[i]

		// A completed rollback: operator told the node to roll back,
		// the node is Ready but on a different (old) image.
		if bn.Spec.DesiredPhase != bootcdevv1alpha1.BootcNodeDesiredPhaseRollingBack {
			continue
		}
		if bn.Status.Phase != bootcdevv1alpha1.BootcNodePhaseReady {
			continue
		}
		if bn.Status.BootedDigest == resolvedDigest {
			// Somehow the node ended up on the desired image after
			// rollback -- treat as successful.
			continue
		}

		log.Info("Node completed rollback to previous image",
			"node", bn.Name,
			"bootedDigest", bn.Status.BootedDigest)

		// Uncordon the node.
		if r.Drainer != nil {
			node := &corev1.Node{}
			if err := r.Get(ctx, types.NamespacedName{Name: bn.Name}, node); err == nil {
				if node.Spec.Unschedulable {
					log.Info("Uncordoning node after rollback", "node", bn.Name)
					if err := r.Drainer.Uncordon(ctx, bn.Name); err != nil {
						log.Error(err, "Failed to uncordon node after rollback", "node", bn.Name)
					}
				}
			}
		}

		// Clear the rebooting-since annotation.
		delete(bn.Annotations, rebootingSinceAnnotation)

		// Reset desiredPhase so the daemon stops trying to roll back.
		bn.Spec.DesiredPhase = bootcdevv1alpha1.BootcNodeDesiredPhaseStaged

		if err := r.Update(ctx, bn); err != nil {
			return false, fmt.Errorf("updating BootcNode %s after rollback: %w", bn.Name, err)
		}

		// Set the node's status to Error so computeStatus picks it
		// up as degraded.
		bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseError
		bn.Status.Message = "Rollback completed: node failed to boot into desired image within timeout"
		if err := r.Status().Update(ctx, bn); err != nil {
			return false, fmt.Errorf("setting Error status on BootcNode %s after rollback: %w", bn.Name, err)
		}

		completed = true
	}

	return completed, nil
}

// uncordonReadyNodes uncordons nodes that have completed rebooting and
// are now running the desired image. It also clears the rebooting-since
// annotation on successful reboot.
func (r *BootcNodePoolReconciler) uncordonReadyNodes(
	ctx context.Context,
	claimedNodes []bootcdevv1alpha1.BootcNode,
	resolvedDigest string,
) error {
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
		if r.Drainer != nil {
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
		}

		// Clear the rebooting-since annotation on successful reboot.
		delete(bn.Annotations, rebootingSinceAnnotation)

		// Reset desiredPhase to Staged since the node has completed
		// the reboot cycle successfully.
		bn.Spec.DesiredPhase = bootcdevv1alpha1.BootcNodeDesiredPhaseStaged
		if err := r.Update(ctx, bn); err != nil {
			return fmt.Errorf("resetting desiredPhase on BootcNode %s: %w", bn.Name, err)
		}

		r.recordNodeEvent(bn, corev1.EventTypeNormal, "UpdateComplete",
			"Node is now running the desired image %s", bn.Status.Booted.Image)
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
