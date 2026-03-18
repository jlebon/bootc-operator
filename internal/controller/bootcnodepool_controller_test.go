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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

// helper creates a Node with the given name and labels.
func createNode(ctx context.Context, name string, nodeLabels map[string]string) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: nodeLabels,
		},
	}
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
	return node
}

// helper creates a BootcNode that mimics what the daemon would create.
func createBootcNode(ctx context.Context, name string, ownerNode *corev1.Node) *v1alpha1.BootcNode {
	bn := &v1alpha1.BootcNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Node",
					Name:       ownerNode.Name,
					UID:        ownerNode.UID,
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, bn)).To(Succeed())

	// Set initial status (mimics daemon behavior).
	bn.Status = v1alpha1.BootcNodeStatus{
		Phase: v1alpha1.BootcNodePhaseReady,
		Booted: v1alpha1.BootEntryStatus{
			Image: "quay.io/example/old-image@sha256:olddigest",
		},
	}
	Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())
	return bn
}

// helper creates a BootcNodePool with the given name and spec.
func createPool(ctx context.Context, name string, spec v1alpha1.BootcNodePoolSpec) {
	pool := &v1alpha1.BootcNodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: spec,
	}
	Expect(k8sClient.Create(ctx, pool)).To(Succeed())
}

// reconcilePool runs one reconciliation cycle for the named pool.
func reconcilePool(ctx context.Context, name string) (reconcile.Result, error) {
	reconciler := &BootcNodePoolReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
	}
	return reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
}

// getPool fetches the latest version of a BootcNodePool.
func getPool(ctx context.Context, name string) *v1alpha1.BootcNodePool {
	pool := &v1alpha1.BootcNodePool{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, pool)).To(Succeed())
	return pool
}

// getBootcNode fetches the latest version of a BootcNode.
func getBootcNode(ctx context.Context, name string) *v1alpha1.BootcNode {
	bn := &v1alpha1.BootcNode{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, bn)).To(Succeed())
	return bn
}

var _ = Describe("BootcNodePool Controller", func() {
	const (
		poolName  = "test-pool"
		nodeName1 = "node-1"
		nodeName2 = "node-2"
		nodeName3 = "node-3"
		testImage = "quay.io/example/my-bootc-image:latest"
	)

	ctx := context.Background()

	AfterEach(func() {
		// Clean up all BootcNodePools.
		poolList := &v1alpha1.BootcNodePoolList{}
		Expect(k8sClient.List(ctx, poolList)).To(Succeed())
		for i := range poolList.Items {
			pool := &poolList.Items[i]
			// Remove finalizer so deletion can proceed.
			controllerutil.RemoveFinalizer(pool, finalizerName)
			_ = k8sClient.Update(ctx, pool)
			_ = k8sClient.Delete(ctx, pool)
		}

		// Clean up all BootcNodes.
		bnList := &v1alpha1.BootcNodeList{}
		Expect(k8sClient.List(ctx, bnList)).To(Succeed())
		for i := range bnList.Items {
			_ = k8sClient.Delete(ctx, &bnList.Items[i])
		}

		// Clean up all Nodes.
		nodeList := &corev1.NodeList{}
		Expect(k8sClient.List(ctx, nodeList)).To(Succeed())
		for i := range nodeList.Items {
			_ = k8sClient.Delete(ctx, &nodeList.Items[i])
		}
	})

	Context("When reconciling a new BootcNodePool", func() {
		It("should initialize conditions to Unknown", func() {
			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
			})

			result, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(reResolutionInterval))

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Conditions).To(HaveLen(3))

			available := meta.FindStatusCondition(pool.Status.Conditions, conditionTypeAvailable)
			Expect(available).NotTo(BeNil())

			progressing := meta.FindStatusCondition(pool.Status.Conditions, conditionTypeProgessing)
			Expect(progressing).NotTo(BeNil())

			degraded := meta.FindStatusCondition(pool.Status.Conditions, conditionTypeDegraded)
			Expect(degraded).NotTo(BeNil())
		})

		It("should add the finalizer", func() {
			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(controllerutil.ContainsFinalizer(pool, finalizerName)).To(BeTrue())
		})

		It("should set phase to Idle when no nodeSelector is specified", func() {
			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseIdle))
			Expect(pool.Status.TargetNodes).To(Equal(int32(0)))
		})

		It("should return success for a nonexistent resource", func() {
			result, err := reconcilePool(ctx, "nonexistent")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Context("When nodes match the nodeSelector", func() {
		It("should count target nodes correctly", func() {
			createNode(ctx, nodeName1, map[string]string{
				"node-role.kubernetes.io/worker": "",
			})
			createNode(ctx, nodeName2, map[string]string{
				"node-role.kubernetes.io/worker": "",
			})
			createNode(ctx, nodeName3, map[string]string{
				"node-role.kubernetes.io/control-plane": "",
			})

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"node-role.kubernetes.io/worker": "",
					},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.TargetNodes).To(Equal(int32(2)))
		})

		It("should claim BootcNodes with pool label and spec", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"node-role.kubernetes.io/worker": "",
			})
			createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"node-role.kubernetes.io/worker": "",
					},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn := getBootcNode(ctx, nodeName1)
			Expect(bn.Labels[poolLabelKey]).To(Equal(poolName))
			Expect(bn.Spec.DesiredImage).To(Equal(testImage))
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseStaged))
			Expect(bn.Spec.RebootPolicy).To(Equal(v1alpha1.RebootPolicyAuto))
		})

		It("should propagate disruption rebootPolicy to BootcNode spec", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				Disruption: v1alpha1.DisruptionConfig{
					RebootPolicy: v1alpha1.RebootPolicyFull,
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn := getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.RebootPolicy).To(Equal(v1alpha1.RebootPolicyFull))
		})
	})

	Context("When BootcNodes don't exist yet (daemon hasn't started)", func() {
		It("should skip nodes without BootcNodes and set Staging phase", func() {
			createNode(ctx, nodeName1, map[string]string{
				"node-role.kubernetes.io/worker": "",
			})

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"node-role.kubernetes.io/worker": "",
					},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			// Target nodes = 1 (node matches), but no BootcNode exists
			// yet, so no nodes are claimed. Pool reports Staging since
			// not all nodes are ready.
			Expect(pool.Status.TargetNodes).To(Equal(int32(1)))
			Expect(pool.Status.ReadyNodes).To(Equal(int32(0)))
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseStaging))
		})
	})

	Context("When nodes report various phases", func() {
		It("should report Ready when all nodes are running the desired image", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			// Simulate the node is running the desired image.
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseReady))
			Expect(pool.Status.ReadyNodes).To(Equal(int32(1)))
			Expect(pool.Status.TargetNodes).To(Equal(int32(1)))

			available := meta.FindStatusCondition(pool.Status.Conditions, conditionTypeAvailable)
			Expect(available).NotTo(BeNil())
			Expect(available.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should report Staging when nodes are staging", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			bn.Status.Phase = v1alpha1.BootcNodePhaseStaging
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseStaging))
		})

		It("should report Degraded when a node has an error", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			bn.Status.Phase = v1alpha1.BootcNodePhaseError
			bn.Status.Message = "Failed to stage image"
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseDegraded))

			degraded := meta.FindStatusCondition(pool.Status.Conditions, conditionTypeDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Message).To(ContainSubstring("Failed to stage image"))
		})

		It("should report Rolling when nodes are rebooting", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			bn.Status.Phase = v1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseRolling))
			Expect(pool.Status.UpdatingNodes).To(Equal(int32(1)))
		})

		It("should count staged nodes correctly", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.StagedNodes).To(Equal(int32(1)))
		})
	})

	Context("When a BootcNodePool is deleted", func() {
		It("should release claimed BootcNodes and remove the finalizer", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// First reconcile to claim the node.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Verify the node is claimed.
			bn := getBootcNode(ctx, nodeName1)
			Expect(bn.Labels[poolLabelKey]).To(Equal(poolName))
			Expect(bn.Spec.DesiredImage).To(Equal(testImage))

			// Delete the pool.
			pool := getPool(ctx, poolName)
			Expect(k8sClient.Delete(ctx, pool)).To(Succeed())

			// Reconcile the deletion.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Verify the node is released.
			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Labels[poolLabelKey]).To(BeEmpty())
			Expect(bn.Spec.DesiredImage).To(BeEmpty())
			Expect(bn.Spec.DesiredPhase).To(BeEmpty())

			// Verify the pool is gone (finalizer removed → API server
			// deletes it).
			err = k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, &v1alpha1.BootcNodePool{})
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("When nodeSelector changes", func() {
		It("should release nodes that no longer match", func() {
			node1 := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
				"zone": "us-east-1",
			})
			createBootcNode(ctx, nodeName1, node1)

			node2 := createNode(ctx, nodeName2, map[string]string{
				"role": "worker",
				"zone": "us-west-2",
			})
			createBootcNode(ctx, nodeName2, node2)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// First reconcile claims both nodes.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn1 := getBootcNode(ctx, nodeName1)
			Expect(bn1.Labels[poolLabelKey]).To(Equal(poolName))
			bn2 := getBootcNode(ctx, nodeName2)
			Expect(bn2.Labels[poolLabelKey]).To(Equal(poolName))

			// Update nodeSelector to only match zone=us-east-1.
			pool := getPool(ctx, poolName)
			pool.Spec.NodeSelector = &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"role": "worker",
					"zone": "us-east-1",
				},
			}
			Expect(k8sClient.Update(ctx, pool)).To(Succeed())

			// Reconcile again.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Node1 still claimed, Node2 released.
			bn1 = getBootcNode(ctx, nodeName1)
			Expect(bn1.Labels[poolLabelKey]).To(Equal(poolName))

			bn2 = getBootcNode(ctx, nodeName2)
			Expect(bn2.Labels[poolLabelKey]).To(BeEmpty())
			Expect(bn2.Spec.DesiredImage).To(BeEmpty())
		})
	})

	Context("When pools overlap", func() {
		It("should set Degraded when a node is already claimed by another pool", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node)

			// Create first pool and reconcile to claim the node.
			createPool(ctx, "pool-a", v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})
			_, err := reconcilePool(ctx, "pool-a")
			Expect(err).NotTo(HaveOccurred())

			bn := getBootcNode(ctx, nodeName1)
			Expect(bn.Labels[poolLabelKey]).To(Equal("pool-a"))

			// Create second pool targeting the same node.
			createPool(ctx, "pool-b", v1alpha1.BootcNodePoolSpec{
				Image: "quay.io/example/other-image:latest",
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})
			_, err = reconcilePool(ctx, "pool-b")
			Expect(err).NotTo(HaveOccurred())

			poolB := getPool(ctx, "pool-b")
			Expect(poolB.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseDegraded))

			degraded := meta.FindStatusCondition(poolB.Status.Conditions, conditionTypeDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Message).To(ContainSubstring("pool-a"))
		})
	})

	Context("When image is updated during rollout", func() {
		It("should update desiredImage on claimed BootcNodes", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// First reconcile claims with original image.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn := getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredImage).To(Equal(testImage))

			// Update the pool's image.
			pool := getPool(ctx, poolName)
			newImage := "quay.io/example/my-bootc-image:v2"
			pool.Spec.Image = newImage
			Expect(k8sClient.Update(ctx, pool)).To(Succeed())

			// Reconcile again.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredImage).To(Equal(newImage))
		})
	})

	Context("When observedGeneration is set", func() {
		It("should track the pool's generation", func() {
			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.ObservedGeneration).To(Equal(pool.Generation))
		})
	})

	Context("When requeue is set", func() {
		It("should requeue after the re-resolution interval", func() {
			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
			})

			result, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
		})
	})

	Context("When multiple nodes match", func() {
		It("should claim all matching BootcNodes", func() {
			node1 := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node1)

			node2 := createNode(ctx, nodeName2, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName2, node2)

			node3 := createNode(ctx, nodeName3, map[string]string{
				"role": "control-plane",
			})
			createBootcNode(ctx, nodeName3, node3)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.TargetNodes).To(Equal(int32(2)))

			bn1 := getBootcNode(ctx, nodeName1)
			Expect(bn1.Labels[poolLabelKey]).To(Equal(poolName))
			bn2 := getBootcNode(ctx, nodeName2)
			Expect(bn2.Labels[poolLabelKey]).To(Equal(poolName))

			// Node3 should not be claimed.
			bn3 := getBootcNode(ctx, nodeName3)
			Expect(bn3.Labels[poolLabelKey]).To(BeEmpty())
		})
	})

	Context("When idempotent reconciliation", func() {
		It("should not modify BootcNode when already correctly claimed", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// First reconcile.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn := getBootcNode(ctx, nodeName1)
			rv := bn.ResourceVersion

			// Second reconcile should be a no-op on the BootcNode.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.ResourceVersion).To(Equal(rv))
		})
	})
})
