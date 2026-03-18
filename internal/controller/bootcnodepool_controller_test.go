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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	return reconcilePoolWithOpts(ctx, name, nil)
}

// reconcilePoolWithOpts runs one reconciliation cycle with optional
// overrides for the Now function.
func reconcilePoolWithOpts(ctx context.Context, name string, now func() time.Time) (reconcile.Result, error) {
	reconciler := &BootcNodePoolReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		Now:    now,
	}
	return reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
}

// reconcilePoolWithRecorder runs one reconciliation cycle with a fake
// event recorder and returns the recorder (for event assertions) and
// any error.
func reconcilePoolWithRecorder(ctx context.Context, name string) (*record.FakeRecorder, error) {
	recorder := record.NewFakeRecorder(20)
	reconciler := &BootcNodePoolReconciler{
		Client:   k8sClient,
		Scheme:   k8sClient.Scheme(),
		Recorder: recorder,
	}
	_, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
	return recorder, err
}

// drainEvents reads all events from a FakeRecorder's channel and
// returns them as a slice of strings. Each string has the format
// "<type> <reason> <message>".
func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case event := <-recorder.Events:
			events = append(events, event)
		default:
			return events
		}
	}
}

// hasEvent checks whether any event in the slice contains the given
// reason substring and message substring.
func hasEvent(events []string, reason, messageSubstr string) bool {
	for _, e := range events {
		if strings.Contains(e, reason) && strings.Contains(e, messageSubstr) {
			return true
		}
	}
	return false
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

	Context("Rollout orchestration", func() {
		It("should advance a staged node to Rebooting and cordon it", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			// Simulate daemon staging the image.
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// Reconcile: claims the node + orchestrates rollout.
			result, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(activeRolloutInterval))

			// Verify the BootcNode was advanced to Rebooting.
			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))

			// Verify the Node was cordoned.
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName1}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Unschedulable).To(BeTrue())

			// Pool phase should be Rolling.
			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseRolling))
		})

		It("should respect maxUnavailable when advancing nodes", func() {
			// Create 3 worker nodes, all staged.
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				node := createNode(ctx, name, map[string]string{
					"role": "worker",
				})
				bn := createBootcNode(ctx, name, node)
				bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
				bn.Status.Staged.Image = testImage
				Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())
			}

			// maxUnavailable = 1: only 1 node should be advanced.
			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				Rollout: v1alpha1.RolloutConfig{
					MaxUnavailable: 1,
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Count how many BootcNodes have desiredPhase=Rebooting.
			rebootingCount := 0
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				bn := getBootcNode(ctx, name)
				if bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRebooting {
					rebootingCount++
				}
			}
			Expect(rebootingCount).To(Equal(1))
		})

		It("should advance up to maxUnavailable nodes when set to 2", func() {
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				node := createNode(ctx, name, map[string]string{
					"role": "worker",
				})
				bn := createBootcNode(ctx, name, node)
				bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
				bn.Status.Staged.Image = testImage
				Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())
			}

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				Rollout: v1alpha1.RolloutConfig{
					MaxUnavailable: 2,
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			rebootingCount := 0
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				bn := getBootcNode(ctx, name)
				if bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRebooting {
					rebootingCount++
				}
			}
			Expect(rebootingCount).To(Equal(2))
		})

		It("should uncordon a node after successful reboot to desired image", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// Step 1: Stage the image.
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Verify node is cordoned and BootcNode desiredPhase=Rebooting.
			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName1}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Unschedulable).To(BeTrue())

			// Step 2: Simulate successful reboot -- daemon reports
			// Ready with booted image = desired.
			bn = getBootcNode(ctx, nodeName1)
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			bn.Status.Staged = v1alpha1.BootEntryStatus{}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Verify node is uncordoned.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName1}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Unschedulable).To(BeFalse())

			// Pool should be Ready.
			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseReady))
		})

		It("should not advance nodes while they are still staging", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			// Node is still staging (downloading).
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaging
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			result, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(activeRolloutInterval))

			// BootcNode should still have desiredPhase=Staged.
			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseStaged))

			// Node should not be cordoned.
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName1}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Unschedulable).To(BeFalse())
		})

		It("should wait for rebooting nodes before advancing more", func() {
			// Create 2 nodes, both staged.
			node1 := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn1 := createBootcNode(ctx, nodeName1, node1)
			bn1.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn1.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn1)).To(Succeed())

			node2 := createNode(ctx, nodeName2, map[string]string{
				"role": "worker",
			})
			bn2 := createBootcNode(ctx, nodeName2, node2)
			bn2.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn2.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn2)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				Rollout: v1alpha1.RolloutConfig{
					MaxUnavailable: 1,
				},
			})

			// First reconcile: one node advanced.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Simulate the advanced node is now rebooting (daemon
			// picked it up).
			bn1 = getBootcNode(ctx, nodeName1)
			if bn1.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRebooting {
				bn1.Status.Phase = v1alpha1.BootcNodePhaseRebooting
				Expect(k8sClient.Status().Update(ctx, bn1)).To(Succeed())
			}

			// Second reconcile: maxUnavailable=1 is reached, second
			// node should not be advanced.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn2 = getBootcNode(ctx, nodeName2)
			Expect(bn2.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseStaged))
		})

		It("should preserve Rebooting phase during reconciliation", func() {
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

			// First reconcile claims the node.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Manually set desiredPhase to Rebooting to simulate
			// rollout orchestrator having advanced it.
			bn = getBootcNode(ctx, nodeName1)
			bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
			Expect(k8sClient.Update(ctx, bn)).To(Succeed())

			// Reconcile again -- should preserve Rebooting.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))
		})

		It("should stop rollout when a node is in error", func() {
			node1 := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn1 := createBootcNode(ctx, nodeName1, node1)
			bn1.Status.Phase = v1alpha1.BootcNodePhaseError
			bn1.Status.Message = "bootc switch failed"
			Expect(k8sClient.Status().Update(ctx, bn1)).To(Succeed())

			node2 := createNode(ctx, nodeName2, map[string]string{
				"role": "worker",
			})
			bn2 := createBootcNode(ctx, nodeName2, node2)
			bn2.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn2.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn2)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Pool should be Degraded.
			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseDegraded))

			// The staged node should NOT have been advanced to
			// Rebooting (rollout paused).
			bn2 = getBootcNode(ctx, nodeName2)
			Expect(bn2.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseStaged))
		})

		It("should complete full rolling update across multiple batches", func() {
			// Scenario: 3 nodes, maxUnavailable=1, full rolling update.
			nodes := make(map[string]*corev1.Node)
			bns := make(map[string]*v1alpha1.BootcNode)
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				nodes[name] = createNode(ctx, name, map[string]string{
					"role": "worker",
				})
				bns[name] = createBootcNode(ctx, name, nodes[name])
			}

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				Rollout: v1alpha1.RolloutConfig{
					MaxUnavailable: 1,
				},
			})

			// Reconcile 1: claims all nodes, sets desiredPhase=Staged.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Simulate all nodes staging.
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				bn := getBootcNode(ctx, name)
				bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
				bn.Status.Staged.Image = testImage
				Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())
			}

			// Reconcile 2: advances first batch (1 node).
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Find which node was advanced.
			var advancedName string
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				bn := getBootcNode(ctx, name)
				if bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRebooting {
					advancedName = name
					break
				}
			}
			Expect(advancedName).NotTo(BeEmpty())

			// Simulate successful reboot for the advanced node.
			bn := getBootcNode(ctx, advancedName)
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			bn.Status.Staged = v1alpha1.BootEntryStatus{}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile 3: uncordons first node, advances second.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Verify first node is uncordoned.
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: advancedName}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Unschedulable).To(BeFalse())

			// Find second advanced node.
			var secondAdvanced string
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				if name == advancedName {
					continue
				}
				bn := getBootcNode(ctx, name)
				if bn.Spec.DesiredPhase == v1alpha1.BootcNodeDesiredPhaseRebooting {
					secondAdvanced = name
					break
				}
			}
			Expect(secondAdvanced).NotTo(BeEmpty())

			// Simulate second node completing.
			bn = getBootcNode(ctx, secondAdvanced)
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			bn.Status.Staged = v1alpha1.BootEntryStatus{}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile 4: uncordons second, advances third.
			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Find the remaining node.
			var thirdName string
			for _, name := range []string{nodeName1, nodeName2, nodeName3} {
				if name != advancedName && name != secondAdvanced {
					thirdName = name
					break
				}
			}

			// Simulate third node completing.
			bn = getBootcNode(ctx, thirdName)
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			bn.Status.Staged = v1alpha1.BootEntryStatus{}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile 5: all done.
			result, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseReady))
			Expect(pool.Status.ReadyNodes).To(Equal(int32(3)))
			Expect(pool.Status.TargetNodes).To(Equal(int32(3)))

			// Requeue should go back to the slow interval since
			// rollout is complete.
			Expect(result.RequeueAfter).To(Equal(reResolutionInterval))
		})

		It("should report pool phase as Rolling when nodes are staged", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			// Node staged but not yet ready at desired image -- this
			// triggers the orchestrator, which will cordon+advance it.
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
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
		})

		It("should set rebooting-since annotation when advancing to Rebooting", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			fixedTime := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
			_, err := reconcilePoolWithOpts(ctx, poolName, func() time.Time { return fixedTime })
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))
			Expect(bn.Annotations[rebootingSinceAnnotation]).To(Equal(fixedTime.Format(time.RFC3339)))
		})

		It("should clear rebooting-since annotation after successful reboot", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// Stage and advance to Rebooting.
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Annotations[rebootingSinceAnnotation]).NotTo(BeEmpty())

			// Simulate successful reboot.
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			bn.Status.Staged = v1alpha1.BootEntryStatus{}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Annotations[rebootingSinceAnnotation]).To(BeEmpty())
		})

		It("should trigger rollback when health check timeout expires", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				HealthCheck: v1alpha1.HealthCheckConfig{
					Timeout: metav1.Duration{Duration: 2 * time.Minute},
				},
			})

			// Stage and advance to Rebooting at T=0.
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			t0 := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
			_, err := reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t0 })
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))

			// Simulate the node is still rebooting (daemon reports
			// Rebooting phase, not Ready yet).
			bn.Status.Phase = v1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile at T+1m: within timeout, should stay Rebooting.
			t1 := t0.Add(1 * time.Minute)
			_, err = reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t1 })
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))

			// Reconcile at T+3m: past 2m timeout, should trigger
			// rollback.
			t3 := t0.Add(3 * time.Minute)
			_, err = reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t3 })
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRollingBack))
		})

		It("should use default timeout when healthCheck.timeout is not set", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				// No HealthCheck specified -- should default to 5m.
			})

			// Stage and advance to Rebooting.
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			t0 := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
			_, err := reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t0 })
			Expect(err).NotTo(HaveOccurred())

			// Simulate the node is still rebooting.
			bn = getBootcNode(ctx, nodeName1)
			bn.Status.Phase = v1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// At T+4m: within default 5m timeout.
			t4 := t0.Add(4 * time.Minute)
			_, err = reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t4 })
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))

			// At T+6m: past default 5m timeout.
			t6 := t0.Add(6 * time.Minute)
			_, err = reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t6 })
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRollingBack))
		})

		It("should mark completed rollback nodes as errored and degrade pool", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// Simulate a node that was rolling back and has come
			// back on the old image. Set the BootcNode to match
			// what the classify logic expects:
			// desiredPhase=RollingBack, status.Phase=Ready,
			// booted image != desired image.
			bn.Labels = map[string]string{poolLabelKey: poolName}
			bn.Spec.DesiredImage = testImage
			bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRollingBack
			bn.Spec.RebootPolicy = v1alpha1.RebootPolicyAuto
			bn.Annotations = map[string]string{
				rebootingSinceAnnotation: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
			}
			Expect(k8sClient.Update(ctx, bn)).To(Succeed())

			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = "quay.io/example/old-image@sha256:olddigest"
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Cordon the node to simulate it was cordoned during
			// the reboot phase.
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName1}, updatedNode)).To(Succeed())
			updatedNode.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, updatedNode)).To(Succeed())

			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Node should be marked as Error.
			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Status.Phase).To(Equal(v1alpha1.BootcNodePhaseError))
			Expect(bn.Status.Message).To(ContainSubstring("Rollback completed"))
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseStaged))
			Expect(bn.Annotations[rebootingSinceAnnotation]).To(BeEmpty())

			// Node should be uncordoned.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName1}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Unschedulable).To(BeFalse())

			// Pool should be Degraded.
			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseDegraded))
		})

		It("should not trigger rollback on rebooting nodes without timeout", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				HealthCheck: v1alpha1.HealthCheckConfig{
					Timeout: metav1.Duration{Duration: 10 * time.Minute},
				},
			})

			// Stage and advance to Rebooting.
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			t0 := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
			_, err := reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t0 })
			Expect(err).NotTo(HaveOccurred())

			// Node is rebooting.
			bn = getBootcNode(ctx, nodeName1)
			bn.Status.Phase = v1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile at T+5m (within 10m timeout).
			t5 := t0.Add(5 * time.Minute)
			_, err = reconcilePoolWithOpts(ctx, poolName, func() time.Time { return t5 })
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseRolling))
		})

		It("should stop advancing other nodes when a node is rolling back", func() {
			// Create 2 nodes, both staged.
			node1 := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn1 := createBootcNode(ctx, nodeName1, node1)
			bn1.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn1.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn1)).To(Succeed())

			node2 := createNode(ctx, nodeName2, map[string]string{
				"role": "worker",
			})
			bn2 := createBootcNode(ctx, nodeName2, node2)
			bn2.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn2.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn2)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				Rollout: v1alpha1.RolloutConfig{
					MaxUnavailable: 2,
				},
			})

			// Advance both nodes.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Simulate node1 is rolling back.
			bn1 = getBootcNode(ctx, nodeName1)
			bn1.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRollingBack
			Expect(k8sClient.Update(ctx, bn1)).To(Succeed())
			bn1.Status.Phase = v1alpha1.BootcNodePhaseRollingBack
			Expect(k8sClient.Status().Update(ctx, bn1)).To(Succeed())

			// Simulate node2 completed successfully.
			bn2 = getBootcNode(ctx, nodeName2)
			bn2.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn2.Status.Booted.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn2)).To(Succeed())

			// Reconcile: should requeue (waiting for rollback).
			result, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(activeRolloutInterval))
		})

		It("should handle image update mid-rollout by resetting desiredPhase", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// Reconcile: advances node to Rebooting.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredPhase).To(Equal(v1alpha1.BootcNodeDesiredPhaseRebooting))

			// Update pool image -- new image cancels the current
			// rollout. The claimBootcNode sets the new desiredImage,
			// but desiredPhaseForNode preserves Rebooting.
			pool := getPool(ctx, poolName)
			newImage := "quay.io/example/my-bootc-image:v2"
			pool.Spec.Image = newImage
			Expect(k8sClient.Update(ctx, pool)).To(Succeed())

			_, err = reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			bn = getBootcNode(ctx, nodeName1)
			Expect(bn.Spec.DesiredImage).To(Equal(newImage))
			// Since the image changed, the daemon will need to
			// re-stage. The desiredPhase is preserved as Rebooting
			// because desiredPhaseForNode preserves it, but the daemon
			// will detect the image mismatch and re-stage.
		})
	})

	Context("Events", func() {
		It("should emit RolloutStarted when pool transitions from Idle to Staging", func() {
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

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonRolloutStarted, testImage)).To(BeTrue(),
				"expected RolloutStarted event, got: %v", events)
			Expect(hasEvent(events, eventReasonNodeClaimed, nodeName1)).To(BeTrue(),
				"expected NodeClaimed event, got: %v", events)
		})

		It("should emit RebootInitiated when node is advanced to Rebooting", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)
			bn.Status.Phase = v1alpha1.BootcNodePhaseStaged
			bn.Status.Staged.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonRebootInitiated, testImage)).To(BeTrue(),
				"expected RebootInitiated event, got: %v", events)
			Expect(hasEvent(events, eventReasonNodeDrained, "drained")).To(BeTrue(),
				"expected NodeDrained event, got: %v", events)
		})

		It("should emit UpdateComplete when node reboots into desired image", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node)

			// Simulate the node was advanced to Rebooting and
			// cordoned in a previous reconcile.
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// First reconcile to set up claims.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Now simulate the node completed the reboot: it's
			// running the desired image with desiredPhase=Rebooting.
			bn := getBootcNode(ctx, nodeName1)
			bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
			bn.Annotations = map[string]string{
				rebootingSinceAnnotation: time.Now().Format(time.RFC3339),
			}
			Expect(k8sClient.Update(ctx, bn)).To(Succeed())

			bn = getBootcNode(ctx, nodeName1)
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonUpdateComplete, testImage)).To(BeTrue(),
				"expected UpdateComplete event, got: %v", events)
		})

		It("should emit RolloutComplete when all nodes are at desired image", func() {
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

			// First reconcile to set up claims and get past Idle.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Simulate the node is already running the desired image.
			bn := getBootcNode(ctx, nodeName1)
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = testImage
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Need to get the pool into a non-Ready phase first so
			// the transition to Ready triggers the event.
			pool := getPool(ctx, poolName)
			pool.Status.Phase = v1alpha1.BootcNodePoolPhaseStaging
			Expect(k8sClient.Status().Update(ctx, pool)).To(Succeed())

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonRolloutComplete, "all")).To(BeTrue(),
				"expected RolloutComplete event, got: %v", events)
		})

		It("should emit RolloutDegraded when a node enters error state", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)
			bn.Status.Phase = v1alpha1.BootcNodePhaseError
			bn.Status.Message = "bootc switch failed"
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// First reconcile to get past Idle.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Force the pool to be in a non-Degraded phase so the
			// transition triggers the event.
			pool := getPool(ctx, poolName)
			pool.Status.Phase = v1alpha1.BootcNodePoolPhaseStaging
			Expect(k8sClient.Status().Update(ctx, pool)).To(Succeed())

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonRolloutDegraded, "failed")).To(BeTrue(),
				"expected RolloutDegraded event, got: %v", events)
		})

		It("should emit RollbackTriggered when health check timeout exceeded", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			createBootcNode(ctx, nodeName1, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
				HealthCheck: v1alpha1.HealthCheckConfig{
					Timeout: metav1.Duration{Duration: 2 * time.Minute},
				},
			})

			// First reconcile to claim the node.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			// Simulate the node is rebooting with a timestamp far
			// enough in the past to trigger the timeout.
			bn := getBootcNode(ctx, nodeName1)
			bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRebooting
			pastTime := time.Now().Add(-3 * time.Minute)
			bn.Annotations = map[string]string{
				rebootingSinceAnnotation: pastTime.Format(time.RFC3339),
			}
			Expect(k8sClient.Update(ctx, bn)).To(Succeed())

			bn = getBootcNode(ctx, nodeName1)
			bn.Status.Phase = v1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonRollbackTriggered, "timeout")).To(BeTrue(),
				"expected RollbackTriggered event, got: %v", events)
		})

		It("should emit OverlappingPools when a node is claimed by two pools", func() {
			node := createNode(ctx, nodeName1, map[string]string{
				"role": "worker",
			})
			bn := createBootcNode(ctx, nodeName1, node)

			// Claim the node by pool-a.
			bn.Labels = map[string]string{poolLabelKey: "pool-a"}
			Expect(k8sClient.Update(ctx, bn)).To(Succeed())

			// Create pool-b that also matches the node.
			createPool(ctx, "pool-b", v1alpha1.BootcNodePoolSpec{
				Image: testImage,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			recorder, err := reconcilePoolWithRecorder(ctx, "pool-b")
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonOverlappingPools, "pool-a")).To(BeTrue(),
				"expected OverlappingPools event, got: %v", events)
		})

		It("should emit NodeReleased when a node is released from a pool", func() {
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

			bn := getBootcNode(ctx, nodeName1)
			Expect(bn.Labels[poolLabelKey]).To(Equal(poolName))

			// Change the node selector so the node no longer matches.
			pool := getPool(ctx, poolName)
			pool.Spec.NodeSelector = &metav1.LabelSelector{
				MatchLabels: map[string]string{"role": "control-plane"},
			}
			Expect(k8sClient.Update(ctx, pool)).To(Succeed())

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonNodeReleased, poolName)).To(BeTrue(),
				"expected NodeReleased event, got: %v", events)
		})

		It("should emit RollbackComplete when rollback finishes", func() {
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

			// Simulate the node completed a rollback: it was set to
			// RollingBack and is now Ready on the old image.
			bn := getBootcNode(ctx, nodeName1)
			bn.Spec.DesiredPhase = v1alpha1.BootcNodeDesiredPhaseRollingBack
			bn.Annotations = map[string]string{
				rebootingSinceAnnotation: time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
			}
			Expect(k8sClient.Update(ctx, bn)).To(Succeed())

			bn = getBootcNode(ctx, nodeName1)
			bn.Status.Phase = v1alpha1.BootcNodePhaseReady
			bn.Status.Booted.Image = "quay.io/example/old-image@sha256:olddigest"
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonRollbackComplete, "Rollback completed")).To(BeTrue(),
				"expected RollbackComplete event, got: %v", events)
		})

		It("should not emit RolloutStarted when pool is already in Staging phase", func() {
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

			// First reconcile to get into Staging.
			_, err := reconcilePool(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			pool := getPool(ctx, poolName)
			Expect(pool.Status.Phase).To(Equal(v1alpha1.BootcNodePoolPhaseStaging))

			// Second reconcile should not emit RolloutStarted again
			// (phase is already Staging).
			recorder, err := reconcilePoolWithRecorder(ctx, poolName)
			Expect(err).NotTo(HaveOccurred())

			events := drainEvents(recorder)
			Expect(hasEvent(events, eventReasonRolloutStarted, "")).To(BeFalse(),
				"did not expect RolloutStarted event on re-reconcile, got: %v", events)
		})
	})
})
