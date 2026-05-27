// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	"github.com/distribution/reference"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

// driveRollout iterates owned BootcNodes, classifies each by the state
// table, and logs their states. Transition logic is added in later
// commits.
func (r *BootcNodePoolReconciler) driveRollout(ctx context.Context, pool *bootcv1alpha1.BootcNodePool, ownedBootcNodes map[string]*bootcv1alpha1.BootcNode) error {
	log := logf.FromContext(ctx).WithValues("pool", pool.Name)

	for _, bn := range ownedBootcNodes {
		state, err := classifyNode(bn)
		if err != nil {
			// This can happen transiently (e.g. daemon hasn't populated
			// booted status yet). Skip the node; it will be re-evaluated
			// when the daemon updates the BootcNode.
			log.V(1).Info("Skipping unclassifiable node", "node", bn.Name, "error", err)
			continue
		}
		log.V(1).Info("Classified node", "node", bn.Name, "state", state.String())
	}

	return nil
}

// nodeState represents the effective state of a BootcNode as seen by
// the controller.
type nodeState int

const (
	// nodeStateIdle means the node is running the desired image and the
	// daemon has no active update cycle.
	nodeStateIdle nodeState = iota

	// nodeStateStaging means the daemon is pulling/staging the image.
	nodeStateStaging

	// nodeStateStaged means the image is staged and locked, awaiting a
	// reboot slot.
	nodeStateStaged

	// nodeStateRebooting means a reboot is in progress.
	nodeStateRebooting

	// nodeStateDegraded means the daemon has reported an error via the
	// BootcNode Degraded condition.
	nodeStateDegraded
)

func (s nodeState) String() string {
	switch s {
	case nodeStateIdle:
		return "Idle"
	case nodeStateStaging:
		return "Staging"
	case nodeStateStaged:
		return "Staged"
	case nodeStateRebooting:
		return "Rebooting"
	case nodeStateDegraded:
		return "Degraded"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

// classifyNode determines the effective state of a BootcNode.
func classifyNode(bn *bootcv1alpha1.BootcNode) (nodeState, error) {
	// Check Degraded first — takes priority over activity state.
	if apimeta.IsStatusConditionTrue(bn.Status.Conditions, bootcv1alpha1.NodeDegraded) {
		return nodeStateDegraded, nil
	}

	if bn.Status.Booted == nil {
		// The only way this can happen really is on a brand new BootcNode and
		// the daemon is still being provisioned. Let's just treat this as Idle
		// for now and it should resolve in a future reconciliation (though
		// ideally eventually we can handle 'stuck' states like this... see
		// related comment below).
		return nodeStateIdle, nil
	}

	// We could pass in the pool here and use targetDigest instead to avoid
	// parsing. Though I do also like how this function takes just a BootcNode.
	// The errors here should never happen since we literally just synced those
	// specs and we didn't e.g. re-Get() them either.
	ref, err := parseImageRef(bn.Spec.DesiredImage)
	if err != nil {
		return 0, fmt.Errorf("unparseable desiredImage %q: %w", bn.Spec.DesiredImage, err)
	}
	digested, ok := ref.(reference.Digested)
	if !ok {
		return 0, fmt.Errorf("non-digested desiredImage %q", bn.Spec.DesiredImage)
	}

	if digested.Digest().String() == bn.Status.Booted.ImageDigest {
		// Image matches; nothing for the controller to act on
		// regardless of whether the daemon has settled yet.
		return nodeStateIdle, nil
	}

	// OK, the node isn't yet booting the desired digest. Let's dig into where
	// it is in the update cycle.

	if apimeta.IsStatusConditionFalse(bn.Status.Conditions, bootcv1alpha1.NodeIdle) {
		idleCond := apimeta.FindStatusCondition(bn.Status.Conditions, bootcv1alpha1.NodeIdle)
		switch idleCond.Reason {
		case bootcv1alpha1.NodeReasonStaging:
			return nodeStateStaging, nil
		case bootcv1alpha1.NodeReasonStaged:
			return nodeStateStaged, nil
		case bootcv1alpha1.NodeReasonRebooting:
			return nodeStateRebooting, nil
		}
	}

	// Image doesn't match and daemon is either Idle, has no conditions,
	// or has an unrecognized Idle reason. Classify as Idle since the
	// daemon should eventually react to the spec change.

	// Hmm, daemon is idle... weird. It could just be that we're racing with the
	// daemon reconciliation. Or something more broken might be happening (e.g.
	// daemon not running at all). For now we don't try to detect 'stuck' nodes,
	// but may in the future. It'll still show up as holding up the pool's
	// `deployedDigest` field and the updatingCount stat.
	return nodeStateIdle, nil
}
