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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// daemonSetName is the name of the daemon DaemonSet managed by the
	// operator. This is the base name before any Kustomize namePrefix
	// is applied.
	daemonSetName = "bootc-daemon"

	// daemonContainerName is the name of the container in the daemon
	// DaemonSet.
	daemonContainerName = "daemon"

	// skipNodeLabel is the label that excludes a node from daemon
	// scheduling.
	skipNodeLabel = "node.bootc.dev/skip"
)

// DaemonSetReconciler reconciles the daemon DaemonSet. It ensures the
// DaemonSet exists with the correct daemon image and configuration.
// The DaemonSet is a cluster-singleton owned by the operator.
type DaemonSetReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Namespace string
	Image     string
}

// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch

// Reconcile ensures the daemon DaemonSet exists and has the correct
// image. It creates the DaemonSet if it does not exist, and updates
// the daemon container image if it differs from the desired image.
func (r *DaemonSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Only reconcile our own DaemonSet.
	if req.Name != daemonSetName || req.Namespace != r.Namespace {
		return ctrl.Result{}, nil
	}

	// Ensure the ServiceAccount exists.
	if err := r.ensureServiceAccount(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring ServiceAccount: %w", err)
	}

	// Check if the DaemonSet already exists.
	existing := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: daemonSetName, Namespace: r.Namespace}, existing)
	if errors.IsNotFound(err) {
		log.Info("Creating daemon DaemonSet", "image", r.Image)
		ds := r.buildDaemonSet()
		if err := r.Create(ctx, ds); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating daemon DaemonSet: %w", err)
		}
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting daemon DaemonSet: %w", err)
	}

	// Update the DaemonSet if the image has changed.
	if err := r.updateDaemonSetIfNeeded(ctx, existing); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// EnsureDaemonSet creates the daemon DaemonSet if it does not exist.
// Called at operator startup to bootstrap the DaemonSet before the
// reconcile loop takes over.
func (r *DaemonSetReconciler) EnsureDaemonSet(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("daemonset")

	// Ensure the ServiceAccount exists first.
	if err := r.ensureServiceAccount(ctx); err != nil {
		return fmt.Errorf("ensuring ServiceAccount: %w", err)
	}

	existing := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: daemonSetName, Namespace: r.Namespace}, existing)
	if errors.IsNotFound(err) {
		log.Info("Creating daemon DaemonSet at startup", "image", r.Image)
		ds := r.buildDaemonSet()
		if err := r.Create(ctx, ds); err != nil {
			return fmt.Errorf("creating daemon DaemonSet: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting daemon DaemonSet: %w", err)
	}

	// Update if needed.
	return r.updateDaemonSetIfNeeded(ctx, existing)
}

// ensureServiceAccount creates the daemon ServiceAccount if it does
// not exist.
func (r *DaemonSetReconciler) ensureServiceAccount(ctx context.Context) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Name: daemonSetName, Namespace: r.Namespace}, sa)
	if errors.IsNotFound(err) {
		sa = &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      daemonSetName,
				Namespace: r.Namespace,
				Labels:    daemonLabels(),
			},
		}
		return r.Create(ctx, sa)
	}
	return err
}

// updateDaemonSetIfNeeded updates the DaemonSet's daemon container
// image if it differs from the desired image.
func (r *DaemonSetReconciler) updateDaemonSetIfNeeded(ctx context.Context, existing *appsv1.DaemonSet) error {
	log := logf.FromContext(ctx)

	for i := range existing.Spec.Template.Spec.Containers {
		c := &existing.Spec.Template.Spec.Containers[i]
		if c.Name == daemonContainerName && c.Image != r.Image {
			log.Info("Updating daemon DaemonSet image", "old", c.Image, "new", r.Image)
			c.Image = r.Image
			if err := r.Update(ctx, existing); err != nil {
				return fmt.Errorf("updating daemon DaemonSet: %w", err)
			}
			return nil
		}
	}
	return nil
}

// buildDaemonSet constructs the desired daemon DaemonSet spec.
func (r *DaemonSetReconciler) buildDaemonSet() *appsv1.DaemonSet {
	labels := daemonLabels()
	privileged := true
	hostToContainer := corev1.MountPropagationHostToContainer

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonSetName,
			Namespace: r.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": daemonSetName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name": daemonSetName,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: daemonSetName,
					// Run on all nodes except those with the skip label.
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      skipNodeLabel,
												Operator: corev1.NodeSelectorOpDoesNotExist,
											},
										},
									},
								},
							},
						},
					},
					// Tolerate all taints so the daemon runs on every node.
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists},
					},
					Containers: []corev1.Container{
						{
							Name:  daemonContainerName,
							Image: r.Image,
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
								// Explicitly set Unconfined seccomp. On K8s
								// 1.27+ with SeccompDefault, the RuntimeDefault
								// profile is applied even to privileged containers
								// on some containerd versions, blocking chroot.
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeUnconfined,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:             "rootfs",
									MountPath:        "/run/rootfs",
									MountPropagation: &hostToContainer,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "rootfs",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/",
									Type: hostPathTypePtr(corev1.HostPathDirectory),
								},
							},
						},
					},
				},
			},
		},
	}

	return ds
}

// daemonLabels returns the standard labels for daemon resources.
func daemonLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      daemonSetName,
		"app.kubernetes.io/component": "daemon",
		"app.kubernetes.io/part-of":   "bootc-operator",
	}
}

func hostPathTypePtr(t corev1.HostPathType) *corev1.HostPathType {
	return &t
}

// SetupDaemonSetReconciler registers the DaemonSet reconciler with
// the manager. It watches the daemon DaemonSet for drift and
// reconciles it back to the desired state.
func (r *DaemonSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.DaemonSet{}).
		Named("daemonset").
		Complete(r)
}

// Start implements manager.Runnable to bootstrap the DaemonSet at
// startup after the manager's cache is ready.
func (r *DaemonSetReconciler) Start(ctx context.Context) error {
	return r.EnsureDaemonSet(ctx)
}
