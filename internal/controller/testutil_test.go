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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

// Test constants for image refs and digests.
const (
	testImageTaggedRef = "quay.io/example/myos:latest"

	testDigestA         = "sha256:06f961b802bc46ee168555f066d28f4f0e9afdf3f88174c1ee6f9de004fc30a0" // "A"
	testDigestB         = "sha256:c0cde77fa8fef97d476c10aad3d2d54fcc2f336140d073651c2dcccf1e379fd6" // "B"
	testDigestC         = "sha256:12f37a8a84034d3e623d726fe10e5031f4df997ac13f4d5571b5a90c41fb84fe" // "C"
	testImageDigestRefA = "quay.io/example/myos@" + testDigestA
	testImageDigestRefB = "quay.io/example/myos@" + testDigestB
	testImageDigestRefC = "quay.io/example/myos@" + testDigestC

	testSecretName = "my-pull-secret"
	testSecretNS   = "bootc-operator"
	testSecretHash = "sha256:b37e50cedcd3e3f1ff64f4afc0422084ae694253cf399326868e07a35f4a45fb" // "secret"
)

// PoolOption configures a BootcNodePool for testing.
type PoolOption func(*bootcv1alpha1.BootcNodePool)

// NewTestPool creates a BootcNodePool with sensible defaults for
// testing. Override any field via functional options.
func NewTestPool(name string, opts ...PoolOption) *bootcv1alpha1.BootcNodePool {
	pool := &bootcv1alpha1.BootcNodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: bootcv1alpha1.BootcNodePoolSpec{
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"node-role.kubernetes.io/worker": "",
				},
			},
			Image: bootcv1alpha1.ImageSpec{
				Ref: testImageTaggedRef,
			},
		},
	}
	for _, o := range opts {
		o(pool)
	}
	return pool
}

// WithRebootPolicy sets the disruption reboot policy.
func WithRebootPolicy(p bootcv1alpha1.RebootPolicy) PoolOption {
	return func(pool *bootcv1alpha1.BootcNodePool) {
		if pool.Spec.Disruption == nil {
			pool.Spec.Disruption = &bootcv1alpha1.DisruptionSpec{}
		}
		pool.Spec.Disruption.RebootPolicy = p
	}
}

// WithPullSecret sets the pull secret reference on a pool.
func WithPullSecret(name, namespace string) PoolOption {
	return func(pool *bootcv1alpha1.BootcNodePool) {
		pool.Spec.PullSecretRef = &bootcv1alpha1.PullSecretRef{
			Name:      name,
			Namespace: namespace,
		}
	}
}

// NodeOption configures a BootcNode for testing.
type NodeOption func(*bootcv1alpha1.BootcNode)

// NewTestNode creates a BootcNode with sensible defaults for testing.
// Override any field via functional options.
func NewTestNode(name string, opts ...NodeOption) *bootcv1alpha1.BootcNode {
	node := &bootcv1alpha1.BootcNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: bootcv1alpha1.BootcNodeSpec{
			DesiredImage:      testImageDigestRefA,
			DesiredImageState: bootcv1alpha1.DesiredImageStateStaged,
		},
	}
	for _, o := range opts {
		o(node)
	}
	return node
}

// WithNodePullSecret sets the pull secret reference and hash on a node.
func WithNodePullSecret(name, namespace, hash string) NodeOption {
	return func(node *bootcv1alpha1.BootcNode) {
		node.Spec.PullSecretRef = &bootcv1alpha1.PullSecretRef{
			Name:      name,
			Namespace: namespace,
		}
		node.Spec.PullSecretHash = hash
	}
}
