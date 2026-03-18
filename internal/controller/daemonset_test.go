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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("DaemonSet Reconciler", func() {
	const (
		testNamespace   = "test-daemon-ns"
		testImage       = "quay.io/example/bootc-daemon:v1"
		testImage2      = "quay.io/example/bootc-daemon:v2"
		dsReconcileName = "daemonset"
	)

	ctx := context.Background()

	// createTestNamespace creates a namespace for the test.
	createTestNamespace := func() {
		ns := &corev1.Namespace{}
		ns.Name = testNamespace
		// Ignore error if namespace already exists.
		_ = k8sClient.Create(ctx, ns)
	}

	// newDSReconciler creates a DaemonSetReconciler for testing.
	newDSReconciler := func(image string) *DaemonSetReconciler {
		return &DaemonSetReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			DaemonImage: image,
			Namespace:   testNamespace,
		}
	}

	// reconcileDS runs one reconciliation cycle for the daemon DaemonSet.
	reconcileDS := func(r *DaemonSetReconciler) (reconcile.Result, error) {
		return r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			},
		})
	}

	BeforeEach(func() {
		createTestNamespace()
	})

	AfterEach(func() {
		// Clean up DaemonSet.
		ds := &appsv1.DaemonSet{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      daemonDaemonSetName,
			Namespace: testNamespace,
		}, ds); err == nil {
			_ = k8sClient.Delete(ctx, ds)
		}

		// Clean up ServiceAccount.
		sa := &corev1.ServiceAccount{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      daemonServiceAccountName,
			Namespace: testNamespace,
		}, sa); err == nil {
			_ = k8sClient.Delete(ctx, sa)
		}

		// Clean up ClusterRole.
		cr := &rbacv1.ClusterRole{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name: daemonClusterRoleName,
		}, cr); err == nil {
			_ = k8sClient.Delete(ctx, cr)
		}

		// Clean up ClusterRoleBinding.
		crb := &rbacv1.ClusterRoleBinding{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name: daemonClusterRoleBindingName,
		}, crb); err == nil {
			_ = k8sClient.Delete(ctx, crb)
		}
	})

	Context("When EnsureDaemonResources is called", func() {
		It("should create all daemon resources", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			// Verify ServiceAccount.
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonServiceAccountName,
				Namespace: testNamespace,
			}, sa)).To(Succeed())
			Expect(sa.Labels[daemonAppLabel]).To(Equal(daemonAppLabelValue))

			// Verify ClusterRole.
			cr := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: daemonClusterRoleName,
			}, cr)).To(Succeed())
			Expect(cr.Rules).To(HaveLen(3))
			// bootcnodes get/create/update
			Expect(cr.Rules[0].Resources).To(ContainElement("bootcnodes"))
			Expect(cr.Rules[0].Verbs).To(ContainElements("get", "create", "update"))
			// bootcnodes/status get/update
			Expect(cr.Rules[1].Resources).To(ContainElement("bootcnodes/status"))
			// nodes get
			Expect(cr.Rules[2].Resources).To(ContainElement("nodes"))
			Expect(cr.Rules[2].Verbs).To(ContainElement("get"))

			// Verify ClusterRoleBinding.
			crb := &rbacv1.ClusterRoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: daemonClusterRoleBindingName,
			}, crb)).To(Succeed())
			Expect(crb.RoleRef.Name).To(Equal(daemonClusterRoleName))
			Expect(crb.Subjects).To(HaveLen(1))
			Expect(crb.Subjects[0].Name).To(Equal(daemonServiceAccountName))
			Expect(crb.Subjects[0].Namespace).To(Equal(testNamespace))

			// Verify DaemonSet.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testImage))
			Expect(ds.Spec.Template.Spec.HostPID).To(BeTrue())
			Expect(*ds.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).To(BeTrue())
			Expect(ds.Spec.Template.Spec.ServiceAccountName).To(Equal(daemonServiceAccountName))
		})

		It("should be idempotent (no error on second call)", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())
		})
	})

	Context("When the DaemonSet already exists", func() {
		It("should update the DaemonSet image when it changes", func() {
			// Create with v1 image.
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			// Verify v1 image.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testImage))

			// Simulate an operator upgrade with new daemon image.
			r2 := newDSReconciler(testImage2)
			_, err := reconcileDS(r2)
			Expect(err).NotTo(HaveOccurred())

			// Verify image was updated to v2.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testImage2))
		})

		It("should not update the DaemonSet when nothing changed", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			// Get the resource version.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			rv := ds.ResourceVersion

			// Reconcile again -- should be a no-op.
			_, err := reconcileDS(r)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.ResourceVersion).To(Equal(rv))
		})
	})

	Context("When reconciling a different DaemonSet", func() {
		It("should ignore DaemonSets that are not the daemon", func() {
			r := newDSReconciler(testImage)

			// Reconcile with a different name -- should be a no-op.
			result, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "some-other-ds",
					Namespace: testNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("DaemonSet spec correctness", func() {
		It("should have the correct node affinity (skip label)", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())

			affinity := ds.Spec.Template.Spec.Affinity
			Expect(affinity).NotTo(BeNil())
			Expect(affinity.NodeAffinity).NotTo(BeNil())
			Expect(affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).NotTo(BeNil())
			terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			Expect(terms).To(HaveLen(1))
			Expect(terms[0].MatchExpressions).To(HaveLen(1))
			Expect(terms[0].MatchExpressions[0].Key).To(Equal(skipNodeLabelKey))
			Expect(terms[0].MatchExpressions[0].Operator).To(Equal(corev1.NodeSelectorOpDoesNotExist))
		})

		It("should tolerate all taints", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())

			Expect(ds.Spec.Template.Spec.Tolerations).To(HaveLen(1))
			Expect(ds.Spec.Template.Spec.Tolerations[0].Operator).To(Equal(corev1.TolerationOpExists))
		})

		It("should pass NODE_NAME env var to the daemon container", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())

			container := ds.Spec.Template.Spec.Containers[0]
			Expect(container.Env).To(HaveLen(1))
			Expect(container.Env[0].Name).To(Equal("NODE_NAME"))
			Expect(container.Env[0].ValueFrom.FieldRef.FieldPath).To(Equal("spec.nodeName"))
			Expect(container.Args).To(ContainElement("--node-name=$(NODE_NAME)"))
		})

		It("should set correct resource requests and limits", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonDaemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())

			container := ds.Spec.Template.Spec.Containers[0]
			Expect(container.Resources.Requests.Cpu().String()).To(Equal("10m"))
			Expect(container.Resources.Requests.Memory().String()).To(Equal("64Mi"))
			Expect(container.Resources.Limits.Memory().String()).To(Equal("128Mi"))
		})
	})

	Context("ClusterRole updates", func() {
		It("should update ClusterRole rules when they change", func() {
			r := newDSReconciler(testImage)
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			// Tamper with the ClusterRole rules.
			cr := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: daemonClusterRoleName,
			}, cr)).To(Succeed())
			cr.Rules = []rbacv1.PolicyRule{} // empty rules
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())

			// Reconcile should restore the rules.
			Expect(r.EnsureDaemonResources(ctx)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: daemonClusterRoleName,
			}, cr)).To(Succeed())
			Expect(cr.Rules).To(HaveLen(3))
		})
	})
})
