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
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

// BootcNodePoolReconciler reconciles a BootcNodePool object
type BootcNodePoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=node.bootc.dev,resources=bootcnodepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=node.bootc.dev,resources=bootcnodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=node.bootc.dev,resources=bootcnodepools/finalizers,verbs=update
// +kubebuilder:rbac:groups=node.bootc.dev,resources=bootcnodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=node.bootc.dev,resources=bootcnodes/status,verbs=get
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BootcNodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	// TODO: implement reconciliation logic

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BootcNodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootcv1alpha1.BootcNodePool{}).
		Owns(&bootcv1alpha1.BootcNode{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.mapNodeToPoolRequests), builder.WithPredicates(nodePredicates())).
		Named("bootcnodepool").
		Complete(r)
}

// mapNodeToPoolRequests maps a Node event to the BootcNodePool(s) that should
// be reconciled. It enqueues two sets: (1) pools whose nodeSelector matches
// the node's current labels, and (2) if a BootcNode exists for this node, the
// pool that owns it. The second set is needed so the owning pool can clean up
// when a node's labels change such that it no longer matches, or when the node
// is deleted entirely.
func (r *BootcNodePoolReconciler) mapNodeToPoolRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}
	log := logf.FromContext(ctx).WithValues("node", node.Name)

	var requests []reconcile.Request
	seen := map[types.NamespacedName]bool{}

	// (1) Pools whose selector matches this node's labels.
	var pools bootcv1alpha1.BootcNodePoolList
	if err := r.List(ctx, &pools); err != nil {
		log.Error(err, "Failed to list BootcNodePools in node mapper")
		return nil
	}
	for i := range pools.Items {
		pool := &pools.Items[i]
		matches, err := nodeSelectorMatchesNode(pool.Spec.NodeSelector, node)
		if err != nil {
			log.Error(err, "Failed to evaluate nodeSelector", "pool", pool.Name)
			continue
		}
		if matches {
			key := types.NamespacedName{Name: pool.Name}
			if !seen[key] {
				log.V(1).Info("Node matches pool selector", "pool", pool.Name)
				requests = append(requests, reconcile.Request{NamespacedName: key})
				seen[key] = true
			}
		}
	}

	// (2) Pool that owns the BootcNode for this node (if any).
	var bootcNode bootcv1alpha1.BootcNode
	if err := r.Get(ctx, types.NamespacedName{Name: node.Name}, &bootcNode); err != nil {
		// No BootcNode for this node — nothing else to enqueue.
		return requests
	}
	for _, ref := range bootcNode.OwnerReferences {
		if ref.Kind == "BootcNodePool" {
			key := types.NamespacedName{Name: ref.Name}
			if !seen[key] {
				log.V(1).Info("Node has BootcNode owned by pool", "pool", ref.Name)
				requests = append(requests, reconcile.Request{NamespacedName: key})
				seen[key] = true
			}
		}
	}

	return requests
}

// nodePredicates returns predicates that filter Node events to only those
// relevant to pool membership: label changes, Ready condition changes, and
// spec.unschedulable changes.
func nodePredicates() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok1 := e.ObjectOld.(*corev1.Node)
			newNode, ok2 := e.ObjectNew.(*corev1.Node)
			if !ok1 || !ok2 {
				return true
			}
			return nodeLabelsChanged(oldNode, newNode) ||
				nodeReadyConditionChanged(oldNode, newNode) ||
				nodeUnschedulableChanged(oldNode, newNode)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return true
		},
	}
}

// nodeSelectorMatchesNode evaluates whether a node's labels match a
// LabelSelector.
func nodeSelectorMatchesNode(sel *metav1.LabelSelector, node *corev1.Node) (bool, error) {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(node.Labels)), nil
}

func nodeLabelsChanged(oldNode, newNode *corev1.Node) bool {
	return !reflect.DeepEqual(oldNode.Labels, newNode.Labels)
}

func nodeReadyConditionChanged(oldNode, newNode *corev1.Node) bool {
	return nodeReadyStatus(oldNode) != nodeReadyStatus(newNode)
}

func nodeReadyStatus(node *corev1.Node) corev1.ConditionStatus {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status
		}
	}
	return corev1.ConditionUnknown
}

func nodeUnschedulableChanged(oldNode, newNode *corev1.Node) bool {
	return oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable
}
