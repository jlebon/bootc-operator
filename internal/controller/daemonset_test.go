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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testNamespace  = "default"
	testDaemonImg  = "quay.io/example/bootc-daemon:v1.0.0"
	testDaemonImg2 = "quay.io/example/bootc-daemon:v2.0.0"
)

var _ = Describe("DaemonSet Reconciler", func() {
	var (
		ctx        context.Context
		reconciler *DaemonSetReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &DaemonSetReconciler{
			Client:         k8sClient,
			Scheme:         k8sClient.Scheme(),
			Namespace:      testNamespace,
			Image:          testDaemonImg,
			ServiceAccount: daemonSetName,
		}
	})

	AfterEach(func() {
		// Clean up the DaemonSet if it exists.
		ds := &appsv1.DaemonSet{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      daemonSetName,
			Namespace: testNamespace,
		}, ds)
		if err == nil {
			_ = k8sClient.Delete(ctx, ds)
		}

	})

	Context("EnsureDaemonSet", func() {
		It("should create the DaemonSet and ServiceAccount when they do not exist", func() {
			err := reconciler.EnsureDaemonSet(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Verify DaemonSet was created.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())

			Expect(ds.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(ds.Spec.Template.Spec.Containers[0].Name).To(Equal(daemonContainerName))
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testDaemonImg))

			// Verify privileged security context.
			Expect(ds.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).NotTo(BeNil())
			Expect(*ds.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).To(BeTrue())

			// Verify host rootfs volume mount.
			Expect(ds.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(1))
			Expect(ds.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath).To(Equal("/run/rootfs"))

			// Verify NODE_NAME env var from downward API.
			Expect(ds.Spec.Template.Spec.Containers[0].Env).To(HaveLen(1))
			Expect(ds.Spec.Template.Spec.Containers[0].Env[0].Name).To(Equal("NODE_NAME"))
			Expect(ds.Spec.Template.Spec.Containers[0].Env[0].ValueFrom.FieldRef.FieldPath).To(Equal("spec.nodeName"))

			// Verify node affinity to skip labeled nodes.
			Expect(ds.Spec.Template.Spec.Affinity).NotTo(BeNil())
			Expect(ds.Spec.Template.Spec.Affinity.NodeAffinity).NotTo(BeNil())
			terms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			Expect(terms).To(HaveLen(1))
			Expect(terms[0].MatchExpressions).To(HaveLen(1))
			Expect(terms[0].MatchExpressions[0].Key).To(Equal(skipNodeLabel))
			Expect(string(terms[0].MatchExpressions[0].Operator)).To(Equal(string(corev1.NodeSelectorOpDoesNotExist)))

			// Verify tolerations.
			Expect(ds.Spec.Template.Spec.Tolerations).To(HaveLen(1))
			Expect(string(ds.Spec.Template.Spec.Tolerations[0].Operator)).To(Equal(string(corev1.TolerationOpExists)))

			// Verify ServiceAccountName is set on the pod spec.
			Expect(ds.Spec.Template.Spec.ServiceAccountName).To(Equal(daemonSetName))
		})

		It("should be idempotent when DaemonSet already exists", func() {
			// Create first.
			err := reconciler.EnsureDaemonSet(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Call again -- should not error.
			err = reconciler.EnsureDaemonSet(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Verify still exists.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testDaemonImg))
		})

		It("should update the image when it changes", func() {
			// Create with initial image.
			err := reconciler.EnsureDaemonSet(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Change the desired image and re-ensure.
			reconciler.Image = testDaemonImg2
			err = reconciler.EnsureDaemonSet(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Verify the image was updated.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testDaemonImg2))
		})
	})

	Context("Reconcile", func() {
		It("should create the DaemonSet when it does not exist", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      daemonSetName,
					Namespace: testNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify DaemonSet was created.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testDaemonImg))
		})

		It("should ignore DaemonSets with different names", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "some-other-daemonset",
					Namespace: testNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// DaemonSet should NOT have been created.
			ds := &appsv1.DaemonSet{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonSetName,
				Namespace: testNamespace,
			}, ds)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("should update the image when the DaemonSet has wrong image", func() {
			// Create with initial image.
			err := reconciler.EnsureDaemonSet(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Manually change the image to simulate drift.
			ds := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			ds.Spec.Template.Spec.Containers[0].Image = "wrong:image"
			Expect(k8sClient.Update(ctx, ds)).To(Succeed())

			// Reconcile should fix the image.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      daemonSetName,
					Namespace: testNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify image was restored.
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      daemonSetName,
				Namespace: testNamespace,
			}, ds)).To(Succeed())
			Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal(testDaemonImg))
		})
	})

	Context("buildDaemonSet", func() {
		It("should build a DaemonSet with correct labels", func() {
			ds := reconciler.buildDaemonSet()
			Expect(ds.Labels["app.kubernetes.io/name"]).To(Equal(daemonSetName))
			Expect(ds.Labels["app.kubernetes.io/component"]).To(Equal("daemon"))
			Expect(ds.Labels["app.kubernetes.io/part-of"]).To(Equal("bootc-operator"))
		})

		It("should set resource requests and limits", func() {
			ds := reconciler.buildDaemonSet()
			container := ds.Spec.Template.Spec.Containers[0]
			Expect(container.Resources.Requests.Cpu().String()).To(Equal("10m"))
			Expect(container.Resources.Requests.Memory().String()).To(Equal("64Mi"))
			Expect(container.Resources.Limits.Memory().String()).To(Equal("128Mi"))
		})

		It("should use the correct service account", func() {
			ds := reconciler.buildDaemonSet()
			Expect(ds.Spec.Template.Spec.ServiceAccountName).To(Equal(daemonSetName))
		})

		It("should mount host rootfs at /run/rootfs", func() {
			ds := reconciler.buildDaemonSet()
			volumes := ds.Spec.Template.Spec.Volumes
			Expect(volumes).To(HaveLen(1))
			Expect(volumes[0].Name).To(Equal("rootfs"))
			Expect(volumes[0].HostPath.Path).To(Equal("/"))
			Expect(*volumes[0].HostPath.Type).To(Equal(corev1.HostPathDirectory))
		})
	})
})
