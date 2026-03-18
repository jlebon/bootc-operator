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
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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
	// daemonDaemonSetName is the name of the daemon DaemonSet managed
	// by the operator.
	daemonDaemonSetName = "bootc-daemon"

	// daemonServiceAccountName is the name of the daemon's
	// ServiceAccount.
	daemonServiceAccountName = "bootc-daemon"

	// daemonClusterRoleName is the name of the daemon's ClusterRole.
	daemonClusterRoleName = "bootc-daemon"

	// daemonClusterRoleBindingName is the name of the daemon's
	// ClusterRoleBinding.
	daemonClusterRoleBindingName = "bootc-daemon"

	// daemonAppLabel is the label applied to daemon pods.
	daemonAppLabel = "app.kubernetes.io/name"

	// daemonAppLabelValue is the value of the daemon app label.
	daemonAppLabelValue = "bootc-daemon"

	// daemonComponentLabel is the component label for the daemon.
	daemonComponentLabel = "app.kubernetes.io/component"

	// daemonComponentLabelValue is the value of the daemon component
	// label.
	daemonComponentLabelValue = "daemon"

	// daemonPartOfLabel is the part-of label for the daemon.
	daemonPartOfLabel = "app.kubernetes.io/part-of"

	// daemonPartOfLabelValue is the value of the part-of label.
	daemonPartOfLabelValue = "bootc-operator"

	// skipNodeLabelKey is the label that excludes nodes from running
	// the daemon.
	skipNodeLabelKey = "node.bootc.dev/skip"
)

// DaemonSetReconciler ensures the bootc-daemon DaemonSet, its
// ServiceAccount, ClusterRole, and ClusterRoleBinding exist with the
// correct configuration. On operator upgrade, the DaemonSet image is
// updated to match the DAEMON_IMAGE env var.
type DaemonSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// DaemonImage is the container image to use for the daemon
	// DaemonSet. Read from the DAEMON_IMAGE env var at startup.
	DaemonImage string

	// Namespace is the namespace where the daemon resources are
	// deployed. Typically the operator's own namespace.
	Namespace string
}

// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch

// Reconcile ensures the daemon DaemonSet and its associated RBAC
// resources exist and are up to date.
func (r *DaemonSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Only reconcile our own DaemonSet.
	if req.Name != daemonDaemonSetName || req.Namespace != r.Namespace {
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling daemon DaemonSet")

	// Ensure the ServiceAccount exists.
	if err := r.ensureServiceAccount(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring ServiceAccount: %w", err)
	}

	// Ensure the ClusterRole exists.
	if err := r.ensureClusterRole(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring ClusterRole: %w", err)
	}

	// Ensure the ClusterRoleBinding exists.
	if err := r.ensureClusterRoleBinding(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring ClusterRoleBinding: %w", err)
	}

	// Ensure the DaemonSet exists and is up to date.
	if err := r.ensureDaemonSet(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring DaemonSet: %w", err)
	}

	log.Info("Daemon DaemonSet reconciliation complete")
	return ctrl.Result{}, nil
}

// EnsureDaemonResources creates or updates all daemon resources. Called
// at operator startup to bootstrap the DaemonSet before the watch-based
// reconciler takes over.
func (r *DaemonSetReconciler) EnsureDaemonResources(ctx context.Context) error {
	log := logf.FromContext(ctx)
	log.Info("Bootstrapping daemon resources", "namespace", r.Namespace, "image", r.DaemonImage)

	if err := r.ensureServiceAccount(ctx); err != nil {
		return fmt.Errorf("ensuring ServiceAccount: %w", err)
	}
	if err := r.ensureClusterRole(ctx); err != nil {
		return fmt.Errorf("ensuring ClusterRole: %w", err)
	}
	if err := r.ensureClusterRoleBinding(ctx); err != nil {
		return fmt.Errorf("ensuring ClusterRoleBinding: %w", err)
	}
	if err := r.ensureDaemonSet(ctx); err != nil {
		return fmt.Errorf("ensuring DaemonSet: %w", err)
	}

	log.Info("Daemon resources bootstrapped successfully")
	return nil
}

// ensureServiceAccount creates the daemon ServiceAccount if it does not
// exist.
func (r *DaemonSetReconciler) ensureServiceAccount(ctx context.Context) error {
	desired := r.desiredServiceAccount()
	existing := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		logf.FromContext(ctx).Info("Creating daemon ServiceAccount", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	return err
}

// ensureClusterRole creates or updates the daemon ClusterRole.
func (r *DaemonSetReconciler) ensureClusterRole(ctx context.Context) error {
	desired := r.desiredClusterRole()
	existing := &rbacv1.ClusterRole{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name}, existing)
	if errors.IsNotFound(err) {
		logf.FromContext(ctx).Info("Creating daemon ClusterRole", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update rules if changed.
	if !equality.Semantic.DeepEqual(existing.Rules, desired.Rules) {
		existing.Rules = desired.Rules
		logf.FromContext(ctx).Info("Updating daemon ClusterRole rules", "name", existing.Name)
		return r.Update(ctx, existing)
	}
	return nil
}

// ensureClusterRoleBinding creates or updates the daemon
// ClusterRoleBinding.
func (r *DaemonSetReconciler) ensureClusterRoleBinding(ctx context.Context) error {
	desired := r.desiredClusterRoleBinding()
	existing := &rbacv1.ClusterRoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name}, existing)
	if errors.IsNotFound(err) {
		logf.FromContext(ctx).Info("Creating daemon ClusterRoleBinding", "name", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update if subjects or roleRef changed.
	if !equality.Semantic.DeepEqual(existing.Subjects, desired.Subjects) ||
		!equality.Semantic.DeepEqual(existing.RoleRef, desired.RoleRef) {
		existing.Subjects = desired.Subjects
		existing.RoleRef = desired.RoleRef
		logf.FromContext(ctx).Info("Updating daemon ClusterRoleBinding", "name", existing.Name)
		return r.Update(ctx, existing)
	}
	return nil
}

// ensureDaemonSet creates or updates the daemon DaemonSet.
func (r *DaemonSetReconciler) ensureDaemonSet(ctx context.Context) error {
	desired := r.desiredDaemonSet()
	existing := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if errors.IsNotFound(err) {
		logf.FromContext(ctx).Info("Creating daemon DaemonSet", "name", desired.Name, "image", r.DaemonImage)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Update the DaemonSet if the spec has drifted. We compare the
	// full pod template spec to catch image changes, resource changes,
	// etc. The selector is immutable and must not be updated.
	if !equality.Semantic.DeepEqual(existing.Spec.Template, desired.Spec.Template) {
		existing.Spec.Template = desired.Spec.Template
		logf.FromContext(ctx).Info("Updating daemon DaemonSet", "name", existing.Name, "image", r.DaemonImage)
		return r.Update(ctx, existing)
	}
	return nil
}

// desiredServiceAccount returns the ServiceAccount for the daemon.
func (r *DaemonSetReconciler) desiredServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonServiceAccountName,
			Namespace: r.Namespace,
			Labels:    daemonLabels(),
		},
	}
}

// desiredClusterRole returns the ClusterRole for the daemon. This
// mirrors the manually-managed config/rbac/daemon_role.yaml.
func (r *DaemonSetReconciler) desiredClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   daemonClusterRoleName,
			Labels: daemonLabels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"bootc.dev"},
				Resources: []string{"bootcnodes"},
				Verbs:     []string{"get", "create", "update"},
			},
			{
				APIGroups: []string{"bootc.dev"},
				Resources: []string{"bootcnodes/status"},
				Verbs:     []string{"get", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get"},
			},
		},
	}
}

// desiredClusterRoleBinding returns the ClusterRoleBinding for the
// daemon.
func (r *DaemonSetReconciler) desiredClusterRoleBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   daemonClusterRoleBindingName,
			Labels: daemonLabels(),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     daemonClusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      daemonServiceAccountName,
				Namespace: r.Namespace,
			},
		},
	}
}

// desiredDaemonSet returns the DaemonSet for the daemon. This mirrors
// the static config/daemon/daemonset.yaml but uses the DAEMON_IMAGE
// env var for the container image.
func (r *DaemonSetReconciler) desiredDaemonSet() *appsv1.DaemonSet {
	privileged := true
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonDaemonSetName,
			Namespace: r.Namespace,
			Labels:    daemonLabels(),
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					daemonAppLabel: daemonAppLabelValue,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						daemonAppLabel: daemonAppLabelValue,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: daemonServiceAccountName,
					HostPID:            true,
					// Run on all nodes except those with the skip label.
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      skipNodeLabelKey,
												Operator: corev1.NodeSelectorOpDoesNotExist,
											},
										},
									},
								},
							},
						},
					},
					// Tolerate everything so the daemon runs on
					// control plane, tainted, and all other nodes.
					Tolerations: []corev1.Toleration{
						{
							Operator: corev1.TolerationOpExists,
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "daemon",
							Image: r.DaemonImage,
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
							Args: []string{"--node-name=$(NODE_NAME)"},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
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
				},
			},
		},
	}
}

// daemonLabels returns the standard labels for daemon resources.
func daemonLabels() map[string]string {
	return map[string]string{
		daemonAppLabel:       daemonAppLabelValue,
		daemonComponentLabel: daemonComponentLabelValue,
		daemonPartOfLabel:    daemonPartOfLabelValue,
	}
}

// SetupWithManager sets up the DaemonSet controller with the Manager.
func (r *DaemonSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.DaemonSet{}).
		Named("daemonset").
		Complete(r)
}
