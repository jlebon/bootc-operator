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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bootcdevv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

const defaultImage = "quay.io/example/test-image:latest"

// fakeDigestResolver is a mock DigestResolver for tests.
type fakeDigestResolver struct {
	digest string
	err    error
}

func (f *fakeDigestResolver) Resolve(_ context.Context, _ string, _ *corev1.Secret) (string, error) {
	return f.digest, f.err
}

// fakeDrainer is a mock Drainer for tests. It records which operations
// were performed on which nodes.
type fakeDrainer struct {
	cordonedNodes   []string
	drainedNodes    []string
	uncordonedNodes []string
	cordonErr       error
	drainErr        error
	uncordonErr     error
}

func (f *fakeDrainer) Cordon(_ context.Context, nodeName string) error {
	if f.cordonErr != nil {
		return f.cordonErr
	}
	f.cordonedNodes = append(f.cordonedNodes, nodeName)
	return nil
}

func (f *fakeDrainer) Drain(_ context.Context, nodeName string) error {
	if f.drainErr != nil {
		return f.drainErr
	}
	f.drainedNodes = append(f.drainedNodes, nodeName)
	return nil
}

func (f *fakeDrainer) Uncordon(_ context.Context, nodeName string) error {
	if f.uncordonErr != nil {
		return f.uncordonErr
	}
	f.uncordonedNodes = append(f.uncordonedNodes, nodeName)
	return nil
}

// createNode creates a Node object for testing.
func createNode(ctx context.Context, name string, labels map[string]string) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
	return node
}

// createBootcNode creates a BootcNode object for testing, as the daemon
// would.
func createBootcNode(ctx context.Context, nodeName string, nodeUID types.UID) *bootcdevv1alpha1.BootcNode {
	bn := &bootcdevv1alpha1.BootcNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Node",
					Name:       nodeName,
					UID:        nodeUID,
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, bn)).To(Succeed())

	// Set initial status (like the daemon does).
	bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseReady
	bn.Status.BootedDigest = "sha256:olddigest1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	bn.Status.Booted = bootcdevv1alpha1.BootEntryStatus{
		Image: "quay.io/example/old-image@sha256:olddigest1234567890abcdef1234567890abcdef1234567890abcdef12345678",
	}
	Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())
	return bn
}

// createPool creates a BootcNodePool for testing with the default test
// image.
func createPool(ctx context.Context, name string, selector map[string]string) {
	pool := &bootcdevv1alpha1.BootcNodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: bootcdevv1alpha1.BootcNodePoolSpec{
			Image: defaultImage,
		},
	}
	if selector != nil {
		pool.Spec.NodeSelector = &metav1.LabelSelector{
			MatchLabels: selector,
		}
	}
	Expect(k8sClient.Create(ctx, pool)).To(Succeed())
}

var _ = Describe("BootcNodePool Controller", func() {
	const (
		poolName    = "test-pool"
		testDigest  = "sha256:abc123def4567890abc123def4567890abc123def4567890abc123def4567890"
		workerLabel = "node-role.kubernetes.io/worker"
	)

	var (
		ctx        context.Context
		reconciler *BootcNodePoolReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &BootcNodePoolReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			DigestResolver: &fakeDigestResolver{
				digest: testDigest,
			},
		}
	})

	AfterEach(func() {
		// Clean up all BootcNodePools.
		poolList := &bootcdevv1alpha1.BootcNodePoolList{}
		Expect(k8sClient.List(ctx, poolList)).To(Succeed())
		for i := range poolList.Items {
			pool := &poolList.Items[i]
			// Remove finalizer to allow deletion.
			pool.Finalizers = nil
			_ = k8sClient.Update(ctx, pool)
			_ = k8sClient.Delete(ctx, pool)
		}

		// Clean up all BootcNodes.
		bnList := &bootcdevv1alpha1.BootcNodeList{}
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
		It("should initialize conditions and add finalizer", func() {
			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// First reconcile: initialize conditions.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile: add finalizer.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Third reconcile: full reconcile with conditions + finalizer.
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			// Verify conditions are set.
			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.Conditions).NotTo(BeEmpty())
			Expect(pool.Finalizers).To(ContainElement(finalizerName))

			// With no matching nodes, phase should be Idle.
			Expect(pool.Status.Phase).To(Equal(bootcdevv1alpha1.BootcNodePoolPhaseIdle))
			Expect(pool.Status.ResolvedDigest).To(Equal(testDigest))
		})
	})

	Context("When a deleted BootcNodePool does not exist", func() {
		It("should not return an error", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "nonexistent"},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When matching nodes exist with BootcNodes", func() {
		It("should claim matching BootcNodes", func() {
			// Create a worker node.
			node := createNode(ctx, "worker-1", map[string]string{workerLabel: ""})

			// Create a BootcNode (as daemon would).
			createBootcNode(ctx, "worker-1", node.UID)

			// Create the pool targeting workers.
			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile multiple times to get through init + finalizer + claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Verify the BootcNode was claimed.
			bn := &bootcdevv1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-1"}, bn)).To(Succeed())
			Expect(bn.Labels[poolLabelKey]).To(Equal(poolName))
			Expect(bn.Spec.DesiredImage).To(Equal(
				fmt.Sprintf("quay.io/example/test-image@%s", testDigest)))
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseStaged))
			Expect(bn.Spec.RebootPolicy).To(Equal(bootcdevv1alpha1.RebootPolicyAuto))

			// Verify pool status.
			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.TargetNodes).To(Equal(int32(1)))
			Expect(pool.Status.ResolvedDigest).To(Equal(testDigest))
		})
	})

	Context("When a node no longer matches the selector", func() {
		It("should release the BootcNode", func() {
			// Create a worker node.
			node := createNode(ctx, "worker-2", map[string]string{workerLabel: ""})
			createBootcNode(ctx, "worker-2", node.UID)
			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Verify claimed.
			bn := &bootcdevv1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-2"}, bn)).To(Succeed())
			Expect(bn.Labels[poolLabelKey]).To(Equal(poolName))

			// Remove the worker label from the node.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-2"}, node)).To(Succeed())
			delete(node.Labels, workerLabel)
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			// Reconcile again.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify released.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-2"}, bn)).To(Succeed())
			Expect(bn.Labels[poolLabelKey]).To(BeEmpty())
			Expect(bn.Spec.DesiredImage).To(BeEmpty())
			Expect(bn.Spec.DesiredPhase).To(BeEmpty())
		})
	})

	Context("When overlapping pools claim the same node", func() {
		It("should set Degraded condition on the second pool", func() {
			node := createNode(ctx, "worker-3", map[string]string{workerLabel: ""})
			createBootcNode(ctx, "worker-3", node.UID)

			// First pool claims the node.
			createPool(ctx, "pool-a", map[string]string{workerLabel: ""})
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: "pool-a"},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Verify pool-a claimed it.
			bn := &bootcdevv1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-3"}, bn)).To(Succeed())
			Expect(bn.Labels[poolLabelKey]).To(Equal("pool-a"))

			// Second pool also targets the same node.
			createPool(ctx, "pool-b", map[string]string{workerLabel: ""})
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: "pool-b"},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Verify pool-b is degraded.
			poolB := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "pool-b"}, poolB)).To(Succeed())
			degraded := meta.FindStatusCondition(poolB.Status.Conditions, conditionTypeDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("OverlappingPools"))
		})
	})

	Context("When BootcNodePool is deleted", func() {
		It("should release claimed nodes and remove finalizer", func() {
			node := createNode(ctx, "worker-4", map[string]string{workerLabel: ""})
			createBootcNode(ctx, "worker-4", node.UID)
			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Verify claimed.
			bn := &bootcdevv1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-4"}, bn)).To(Succeed())
			Expect(bn.Labels[poolLabelKey]).To(Equal(poolName))

			// Delete the pool.
			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(k8sClient.Delete(ctx, pool)).To(Succeed())

			// Reconcile deletion.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify BootcNode released.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-4"}, bn)).To(Succeed())
			Expect(bn.Labels[poolLabelKey]).To(BeEmpty())
			Expect(bn.Spec.DesiredImage).To(BeEmpty())

			// Verify pool is gone (finalizer removed, deletion proceeds).
			err = k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("When digest resolution fails", func() {
		It("should set Degraded condition and requeue", func() {
			createPool(ctx, poolName, map[string]string{workerLabel: ""})
			reconciler.DigestResolver = &fakeDigestResolver{
				err: fmt.Errorf("registry unreachable"),
			}

			// Init + finalizer.
			for range 2 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Third reconcile hits digest resolution.
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			degraded := meta.FindStatusCondition(pool.Status.Conditions, conditionTypeDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("DigestResolutionFailed"))
		})
	})

	Context("When pool has no nodeSelector", func() {
		It("should target no nodes and set Idle phase", func() {
			node := createNode(ctx, "worker-5", map[string]string{workerLabel: ""})
			createBootcNode(ctx, "worker-5", node.UID)

			// Pool with no selector.
			pool := &bootcdevv1alpha1.BootcNodePool{
				ObjectMeta: metav1.ObjectMeta{Name: "no-selector"},
				Spec: bootcdevv1alpha1.BootcNodePoolSpec{
					Image: defaultImage,
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())

			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: "no-selector"},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "no-selector"}, pool)).To(Succeed())
			Expect(pool.Status.Phase).To(Equal(bootcdevv1alpha1.BootcNodePoolPhaseIdle))
			Expect(pool.Status.TargetNodes).To(Equal(int32(0)))
		})
	})

	Context("When all nodes are at desired image", func() {
		It("should set Ready phase", func() {
			node := createNode(ctx, "worker-6", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-6", node.UID)

			// Set the booted digest to match the resolved digest.
			bn.Status.BootedDigest = testDigest
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.Phase).To(Equal(bootcdevv1alpha1.BootcNodePoolPhaseReady))
			Expect(pool.Status.ReadyNodes).To(Equal(int32(1)))
			Expect(pool.Status.TargetNodes).To(Equal(int32(1)))
		})
	})

	Context("When rollout orchestration advances nodes to Rebooting", func() {
		It("should cordon, drain, and advance staged nodes to Rebooting phase", func() {
			node := createNode(ctx, "worker-7", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-7", node.UID)

			// Set up reconciler with fake drainer.
			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate daemon staging the image: set phase to Staged.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-7"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
			bn.Status.Staged = bootcdevv1alpha1.BootEntryStatus{
				Image: fmt.Sprintf("quay.io/example/test-image@%s", testDigest),
			}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: should cordon, drain, and advance to Rebooting.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify cordon and drain were called.
			Expect(drainer.cordonedNodes).To(ContainElement("worker-7"))
			Expect(drainer.drainedNodes).To(ContainElement("worker-7"))

			// Verify BootcNode was advanced.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-7"}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting))

			// Verify pool phase.
			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.Phase).To(Equal(bootcdevv1alpha1.BootcNodePoolPhaseRolling))
		})
	})

	Context("When a node completes rebooting", func() {
		It("should uncordon the node and reset desiredPhase", func() {
			node := createNode(ctx, "worker-8", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-8", node.UID)

			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate the full rollout lifecycle:
			// 1. Daemon stages the image.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-8"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// 2. Reconcile advances to Rebooting (cordons + drains).
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// 3. Simulate the node rebooted and is now running the
			//    desired image. Mark the node as cordoned (as the
			//    reconciler would have cordoned it).
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-8"}, node)).To(Succeed())
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-8"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseReady
			bn.Status.BootedDigest = testDigest
			bn.Status.Booted = bootcdevv1alpha1.BootEntryStatus{
				Image: fmt.Sprintf("quay.io/example/test-image@%s", testDigest),
			}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// 4. Reconcile should uncordon and reset desiredPhase.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify uncordon was called.
			Expect(drainer.uncordonedNodes).To(ContainElement("worker-8"))

			// Verify desiredPhase was reset.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-8"}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseStaged))

			// Verify pool is Ready.
			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.Phase).To(Equal(bootcdevv1alpha1.BootcNodePoolPhaseReady))
		})
	})

	Context("When BootcNodePool is deleted with cordoned nodes", func() {
		It("should uncordon nodes and release them", func() {
			node := createNode(ctx, "worker-9", map[string]string{workerLabel: ""})
			createBootcNode(ctx, "worker-9", node.UID)

			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Mark the node as cordoned (as if drain was in progress).
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-9"}, node)).To(Succeed())
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			// Delete the pool.
			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(k8sClient.Delete(ctx, pool)).To(Succeed())

			// Reconcile deletion.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify uncordon was called.
			Expect(drainer.uncordonedNodes).To(ContainElement("worker-9"))

			// Verify BootcNode released.
			bn := &bootcdevv1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-9"}, bn)).To(Succeed())
			Expect(bn.Labels[poolLabelKey]).To(BeEmpty())
			Expect(bn.Spec.DesiredImage).To(BeEmpty())

			// Verify pool is gone.
			err = k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("When advancing a node to Rebooting", func() {
		It("should set the rebooting-since annotation", func() {
			node := createNode(ctx, "worker-anno-1", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-anno-1", node.UID)

			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate daemon staging the image.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-anno-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: should advance to Rebooting with annotation.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify annotation was set.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-anno-1"}, bn)).To(Succeed())
			Expect(bn.Annotations).To(HaveKey(rebootingSinceAnnotation))
			Expect(bn.Annotations[rebootingSinceAnnotation]).NotTo(BeEmpty())
		})
	})

	Context("When a rebooting node exceeds the health check timeout", func() {
		It("should trigger rollback by setting desiredPhase to RollingBack", func() {
			node := createNode(ctx, "worker-timeout-1", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-timeout-1", node.UID)

			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			// Use a fixed "now" time to control timeout behavior.
			fakeNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			reconciler.Now = func() time.Time { return fakeNow }

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate daemon staging the image.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-timeout-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: should advance to Rebooting with annotation
			// set to fakeNow.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify it's Rebooting.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-timeout-1"}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting))

			// Advance time past the default 5m timeout.
			fakeNow = fakeNow.Add(6 * time.Minute)

			// Simulate the daemon has set Rebooting status but node
			// hasn't come back yet (still rebooting).
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-timeout-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: should detect timeout and trigger rollback.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify node was set to RollingBack.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-timeout-1"}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseRollingBack))
		})
	})

	Context("When a node completes rollback", func() {
		It("should uncordon, set Error phase, and degrade the pool", func() {
			node := createNode(ctx, "worker-rollback-1", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-rollback-1", node.UID)

			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			fakeNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			reconciler.Now = func() time.Time { return fakeNow }

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate daemon staging, then advance to Rebooting.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-rollback-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Advance past timeout.
			fakeNow = fakeNow.Add(6 * time.Minute)

			// Mark node as rebooting (daemon started).
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-rollback-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile to trigger timeout → RollingBack.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify RollingBack was set.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-rollback-1"}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseRollingBack))

			// Simulate daemon completed rollback: node is Ready on
			// old image, and the node is cordoned.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-rollback-1"}, node)).To(Succeed())
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-rollback-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseReady
			// Still on old digest -- the rollback put us back.
			bn.Status.BootedDigest = "sha256:olddigest1234567890abcdef1234567890abcdef1234567890abcdef12345678"
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile to handle completed rollback.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify uncordon was called.
			Expect(drainer.uncordonedNodes).To(ContainElement("worker-rollback-1"))

			// Verify BootcNode is in Error state.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-rollback-1"}, bn)).To(Succeed())
			Expect(bn.Status.Phase).To(Equal(bootcdevv1alpha1.BootcNodePhaseError))
			Expect(bn.Status.Message).To(ContainSubstring("Rollback completed"))
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseStaged))
			Expect(bn.Annotations).NotTo(HaveKey(rebootingSinceAnnotation))

			// Verify pool is Degraded.
			pool := &bootcdevv1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.Phase).To(Equal(bootcdevv1alpha1.BootcNodePoolPhaseDegraded))
		})
	})

	Context("When a node reboots successfully", func() {
		It("should clear the rebooting-since annotation", func() {
			node := createNode(ctx, "worker-clear-1", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-clear-1", node.UID)

			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate daemon staging.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-clear-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: advance to Rebooting with annotation.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify annotation was set.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-clear-1"}, bn)).To(Succeed())
			Expect(bn.Annotations).To(HaveKey(rebootingSinceAnnotation))

			// Simulate successful reboot: node is Ready on desired
			// image, node is cordoned.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-clear-1"}, node)).To(Succeed())
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-clear-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseReady
			bn.Status.BootedDigest = testDigest
			bn.Status.Booted = bootcdevv1alpha1.BootEntryStatus{
				Image: fmt.Sprintf("quay.io/example/test-image@%s", testDigest),
			}
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: should uncordon and clear annotation.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify annotation was cleared.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-clear-1"}, bn)).To(Succeed())
			Expect(bn.Annotations).NotTo(HaveKey(rebootingSinceAnnotation))
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseStaged))
		})
	})

	Context("When reboot timeout has not been exceeded", func() {
		It("should not trigger rollback", func() {
			node := createNode(ctx, "worker-notimeout-1", map[string]string{workerLabel: ""})
			bn := createBootcNode(ctx, "worker-notimeout-1", node.UID)

			drainer := &fakeDrainer{}
			reconciler.Drainer = drainer

			fakeNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			reconciler.Now = func() time.Time { return fakeNow }

			createPool(ctx, poolName, map[string]string{workerLabel: ""})

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: poolName},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate daemon staging.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-notimeout-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: advance to Rebooting.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Only advance 2 minutes (less than 5m timeout).
			fakeNow = fakeNow.Add(2 * time.Minute)

			// Simulate daemon rebooting.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-notimeout-1"}, bn)).To(Succeed())
			bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseRebooting
			Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())

			// Reconcile: should NOT trigger rollback.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify node is still Rebooting (not RollingBack).
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "worker-notimeout-1"}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredPhase).To(Equal(bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting))
		})
	})

	Context("maxUnavailable limits concurrent reboots", func() {
		It("should not exceed maxUnavailable", func() {
			// Create 3 worker nodes.
			for i := range 3 {
				name := fmt.Sprintf("mu-worker-%d", i)
				node := createNode(ctx, name, map[string]string{workerLabel: ""})
				createBootcNode(ctx, name, node.UID)
			}

			// Pool with maxUnavailable=1.
			pool := &bootcdevv1alpha1.BootcNodePool{
				ObjectMeta: metav1.ObjectMeta{Name: "mu-pool"},
				Spec: bootcdevv1alpha1.BootcNodePoolSpec{
					Image: defaultImage,
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{workerLabel: ""},
					},
					Rollout: bootcdevv1alpha1.RolloutConfig{
						MaxUnavailable: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())

			// Reconcile to claim.
			for range 3 {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: "mu-pool"},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Simulate all nodes as Staged.
			bnList := &bootcdevv1alpha1.BootcNodeList{}
			Expect(k8sClient.List(ctx, bnList)).To(Succeed())
			for i := range bnList.Items {
				bn := &bnList.Items[i]
				if bn.Labels[poolLabelKey] != "mu-pool" {
					continue
				}
				bn.Status.Phase = bootcdevv1alpha1.BootcNodePhaseStaged
				Expect(k8sClient.Status().Update(ctx, bn)).To(Succeed())
			}

			// Reconcile: should advance only 1 node.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "mu-pool"},
			})
			Expect(err).NotTo(HaveOccurred())

			// Count how many nodes are now Rebooting.
			Expect(k8sClient.List(ctx, bnList)).To(Succeed())
			rebootingCount := 0
			for _, bn := range bnList.Items {
				if bn.Labels[poolLabelKey] != "mu-pool" {
					continue
				}
				if bn.Spec.DesiredPhase == bootcdevv1alpha1.BootcNodeDesiredPhaseRebooting {
					rebootingCount++
				}
			}
			Expect(rebootingCount).To(Equal(1))
		})
	})
})

var _ = Describe("Digest Resolution", func() {
	Context("extractDigestFromRef", func() {
		It("should extract sha256 digest", func() {
			digest, ok := extractDigestFromRef("quay.io/example/img@sha256:abc123")
			Expect(ok).To(BeTrue())
			Expect(digest).To(Equal("sha256:abc123"))
		})

		It("should return false for tag refs", func() {
			_, ok := extractDigestFromRef("quay.io/example/img:latest")
			Expect(ok).To(BeFalse())
		})

		It("should return false for bare refs", func() {
			_, ok := extractDigestFromRef("quay.io/example/img")
			Expect(ok).To(BeFalse())
		})
	})

	Context("imageWithDigest", func() {
		It("should create ref with digest from tag ref", func() {
			result := imageWithDigest("quay.io/example/img:latest", "sha256:abc123")
			Expect(result).To(Equal("quay.io/example/img@sha256:abc123"))
		})

		It("should replace existing digest", func() {
			result := imageWithDigest("quay.io/example/img@sha256:old", "sha256:new")
			Expect(result).To(Equal("quay.io/example/img@sha256:new"))
		})

		It("should handle bare ref", func() {
			result := imageWithDigest("quay.io/example/img", "sha256:abc123")
			Expect(result).To(Equal("quay.io/example/img@sha256:abc123"))
		})

		It("should handle ref with port", func() {
			result := imageWithDigest("localhost:5000/img:latest", "sha256:abc123")
			Expect(result).To(Equal("localhost:5000/img@sha256:abc123"))
		})
	})
})
