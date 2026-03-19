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
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

var _ = Describe("Digest Resolution", func() {
	ctx := context.Background()

	Describe("isDigestReference", func() {
		It("should return true for digest references", func() {
			Expect(isDigestReference("quay.io/example/img@sha256:abc123def456")).To(BeTrue())
		})

		It("should return false for tag references", func() {
			Expect(isDigestReference("quay.io/example/img:latest")).To(BeFalse())
		})

		It("should return false for bare references", func() {
			Expect(isDigestReference("quay.io/example/img")).To(BeFalse())
		})
	})

	Describe("parseDockerConfigJSON", func() {
		It("should parse valid dockerconfigjson", func() {
			config := `{"auths":{"registry.example.com":{"auth":"dXNlcjpwYXNz"}}}`
			kc, err := parseDockerConfigJSON([]byte(config))
			Expect(err).NotTo(HaveOccurred())
			Expect(kc).NotTo(BeNil())
		})

		It("should return error for invalid JSON", func() {
			_, err := parseDockerConfigJSON([]byte("not json"))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("secretKeychain", func() {
		var kc *secretKeychain

		BeforeEach(func() {
			kc = &secretKeychain{
				auths: map[string]dockerAuthEntry{
					"registry.example.com":        {Auth: "dXNlcjpwYXNz"},
					"https://index.docker.io/v1/": {Auth: "ZG9ja2VyOnBhc3M="},
				},
			}
		})

		It("should resolve exact match", func() {
			auth, err := kc.Resolve(&fakeResource{registry: "registry.example.com"})
			Expect(err).NotTo(HaveOccurred())
			Expect(auth).NotTo(Equal(authn.Anonymous))
		})

		It("should resolve with https prefix and trailing slash", func() {
			auth, err := kc.Resolve(&fakeResource{registry: "index.docker.io"})
			Expect(err).NotTo(HaveOccurred())
			Expect(auth).NotTo(Equal(authn.Anonymous))
		})

		It("should return anonymous for unknown registry", func() {
			auth, err := kc.Resolve(&fakeResource{registry: "unknown.io"})
			Expect(err).NotTo(HaveOccurred())
			Expect(auth).To(Equal(authn.Anonymous))
		})
	})

	Describe("RegistryResolver", func() {
		It("should return digest references unchanged", func() {
			resolver := &RegistryResolver{}
			digest := "quay.io/example/img@sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
			resolved, err := resolver.Resolve(ctx, digest, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(Equal(digest))
		})

		It("should resolve tag to digest from registry", func() {
			// Start an in-memory registry.
			regHandler := registry.New()
			server := httptest.NewServer(regHandler)
			defer server.Close()

			// Push a random image.
			host := strings.TrimPrefix(server.URL, "http://")
			imgRef := host + "/test/image:latest"
			ref, err := name.ParseReference(imgRef)
			Expect(err).NotTo(HaveOccurred())

			img, err := random.Image(256, 1)
			Expect(err).NotTo(HaveOccurred())

			err = remote.Write(ref, img, remote.WithTransport(http.DefaultTransport))
			Expect(err).NotTo(HaveOccurred())

			// Get the expected digest.
			expectedDigest, err := img.Digest()
			Expect(err).NotTo(HaveOccurred())

			// Resolve the tag.
			resolver := &RegistryResolver{}
			resolved, err := resolver.Resolve(ctx, imgRef, "")
			Expect(err).NotTo(HaveOccurred())

			// The resolved reference should contain the digest.
			Expect(resolved).To(ContainSubstring("@sha256:"))
			Expect(resolved).To(ContainSubstring(expectedDigest.Hex))
			Expect(resolved).To(HavePrefix(host + "/test/image@"))
		})

		It("should return error for nonexistent image", func() {
			regHandler := registry.New()
			server := httptest.NewServer(regHandler)
			defer server.Close()

			host := strings.TrimPrefix(server.URL, "http://")
			resolver := &RegistryResolver{}
			_, err := resolver.Resolve(ctx, host+"/nonexistent/image:latest", "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("resolving digest"))
		})

		It("should return error for invalid image reference", func() {
			resolver := &RegistryResolver{}
			_, err := resolver.Resolve(ctx, "INVALID:::ref", "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parsing image reference"))
		})
	})

	Describe("RegistryResolver with imagePullSecret", func() {
		const testNamespace = "test-digest-ns"

		BeforeEach(func() {
			// Create the namespace for secrets.
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}
			_ = k8sClient.Create(ctx, ns)
		})

		AfterEach(func() {
			// Clean up secrets.
			secretList := &corev1.SecretList{}
			_ = k8sClient.List(ctx, secretList)
			for i := range secretList.Items {
				if secretList.Items[i].Namespace == testNamespace {
					_ = k8sClient.Delete(ctx, &secretList.Items[i])
				}
			}
		})

		It("should read credentials from a valid dockerconfigjson secret", func() {
			// Create a dockerconfigjson secret.
			configJSON := `{"auths":{"localhost:5000":{"auth":"dXNlcjpwYXNz"}}}`
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-pull-secret",
					Namespace: testNamespace,
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(configJSON),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			resolver := &RegistryResolver{
				Client:    k8sClient,
				Namespace: testNamespace,
			}

			// The keychain should resolve without error.
			kc, err := resolver.keychainForSecret(ctx, "my-pull-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(kc).NotTo(BeNil())

			// Verify the keychain returns auth for the matching registry.
			auth, err := kc.Resolve(&fakeResource{registry: "localhost:5000"})
			Expect(err).NotTo(HaveOccurred())
			Expect(auth).NotTo(Equal(authn.Anonymous))
		})

		It("should return error for non-existent secret", func() {
			resolver := &RegistryResolver{
				Client:    k8sClient,
				Namespace: testNamespace,
			}
			_, err := resolver.keychainForSecret(ctx, "nonexistent-secret")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("getting imagePullSecret"))
		})

		It("should return error for wrong secret type", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wrong-type-secret",
					Namespace: testNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"key": []byte("value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			resolver := &RegistryResolver{
				Client:    k8sClient,
				Namespace: testNamespace,
			}
			_, err := resolver.keychainForSecret(ctx, "wrong-type-secret")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("expected"))
		})

		It("should return error for wrong secret type", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "opaque-secret",
					Namespace: testNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"some-key": []byte("some-value"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			resolver := &RegistryResolver{
				Client:    k8sClient,
				Namespace: testNamespace,
			}
			_, err := resolver.keychainForSecret(ctx, "opaque-secret")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("expected"))
		})

		It("should return default keychain when secret name is empty", func() {
			resolver := &RegistryResolver{
				Client:    k8sClient,
				Namespace: testNamespace,
			}
			kc, err := resolver.keychainForSecret(ctx, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(kc).To(Equal(authn.DefaultKeychain))
		})
	})

	Describe("Controller integration with ImageResolver", func() {
		const testSuffix = "digest-integ"

		AfterEach(func() {
			cleanupResources(ctx, testSuffix)
		})

		It("should use ImageResolver to resolve tags for pool reconciliation", func() {
			poolName := "pool-" + testSuffix
			nodeName := "node-" + testSuffix
			resolvedDigest := "quay.io/example/my-image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

			// Create node and BootcNode.
			node := createNode(ctx, nodeName, map[string]string{"role": "worker"})
			createBootcNode(ctx, nodeName, node)

			// Create a pool with a tag reference.
			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: "quay.io/example/my-image:latest",
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			// Reconcile with a mock resolver that returns a fixed digest.
			mockResolver := &mockImageResolver{
				resolvedDigest: resolvedDigest,
			}
			reconciler := &BootcNodePoolReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				ImageResolver: mockResolver,
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify the resolver was called with the correct arguments.
			Expect(mockResolver.lastImageRef).To(Equal("quay.io/example/my-image:latest"))
			Expect(mockResolver.lastSecretName).To(BeEmpty())

			// Verify the pool's status has the resolved digest.
			pool := &v1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.ResolvedDigest).To(Equal(resolvedDigest))

			// Verify the BootcNode was claimed with the resolved digest.
			bn := &v1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredImage).To(Equal(resolvedDigest))
		})

		It("should pass imagePullSecret name to the resolver", func() {
			poolName := "pool-secret-" + testSuffix
			nodeName := "node-secret-" + testSuffix
			resolvedDigest := "quay.io/example/private@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

			node := createNode(ctx, nodeName, map[string]string{"role": "private"})
			createBootcNode(ctx, nodeName, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: "quay.io/example/private:v1",
				ImagePullSecret: v1alpha1.ImagePullSecretReference{
					Name: "my-registry-creds",
				},
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "private"},
				},
			})

			mockResolver := &mockImageResolver{
				resolvedDigest: resolvedDigest,
			}
			reconciler := &BootcNodePoolReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				ImageResolver: mockResolver,
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(mockResolver.lastSecretName).To(Equal("my-registry-creds"))
		})

		It("should handle resolver errors gracefully (requeue, no crash)", func() {
			poolName := "pool-err-" + testSuffix

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: "quay.io/example/failing:latest",
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "worker"},
				},
			})

			mockResolver := &mockImageResolver{
				err: fmt.Errorf("registry unavailable"),
			}
			reconciler := &BootcNodePoolReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				ImageResolver: mockResolver,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})

			// Should not return an error (logged instead), but should requeue.
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(reResolutionInterval))
		})

		It("should work without an ImageResolver (nil fallback)", func() {
			poolName := "pool-noresolver-" + testSuffix
			nodeName := "node-noresolver-" + testSuffix

			node := createNode(ctx, nodeName, map[string]string{"role": "noresolver"})
			createBootcNode(ctx, nodeName, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: "quay.io/example/img:latest",
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "noresolver"},
				},
			})

			// Reconcile with no ImageResolver set (nil).
			reconciler := &BootcNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			// The BootcNode should be claimed with the raw tag
			// (no digest resolution).
			bn := &v1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredImage).To(Equal("quay.io/example/img:latest"))
		})

		It("should detect new digest on re-reconcile and update BootcNodes", func() {
			poolName := "pool-redigest-" + testSuffix
			nodeName := "node-redigest-" + testSuffix
			firstDigest := "quay.io/example/img@sha256:1111111111111111111111111111111111111111111111111111111111111111"
			secondDigest := "quay.io/example/img@sha256:2222222222222222222222222222222222222222222222222222222222222222"

			node := createNode(ctx, nodeName, map[string]string{"role": "redigest"})
			createBootcNode(ctx, nodeName, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: "quay.io/example/img:latest",
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "redigest"},
				},
			})

			// First reconcile: resolves to first digest.
			mockResolver := &mockImageResolver{resolvedDigest: firstDigest}
			reconciler := &BootcNodePoolReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				ImageResolver: mockResolver,
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			bn := &v1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredImage).To(Equal(firstDigest))

			// Second reconcile: tag now points to a new digest.
			mockResolver.resolvedDigest = secondDigest
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredImage).To(Equal(secondDigest))

			// Pool status should reflect the new digest.
			pool := &v1alpha1.BootcNodePool{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName}, pool)).To(Succeed())
			Expect(pool.Status.ResolvedDigest).To(Equal(secondDigest))
		})

		It("should resolve using a real in-memory registry", func() {
			poolName := "pool-realreg-" + testSuffix
			nodeName := "node-realreg-" + testSuffix

			// Start an in-memory registry.
			regHandler := registry.New()
			server := httptest.NewServer(regHandler)
			defer server.Close()

			// Push a random image.
			host := strings.TrimPrefix(server.URL, "http://")
			imgRef := host + "/test/real-image:v1"
			ref, err := name.ParseReference(imgRef)
			Expect(err).NotTo(HaveOccurred())

			img, err := random.Image(256, 1)
			Expect(err).NotTo(HaveOccurred())

			err = remote.Write(ref, img, remote.WithTransport(http.DefaultTransport))
			Expect(err).NotTo(HaveOccurred())

			expectedDigest, err := img.Digest()
			Expect(err).NotTo(HaveOccurred())

			// Create resources.
			node := createNode(ctx, nodeName, map[string]string{"role": "realreg"})
			createBootcNode(ctx, nodeName, node)

			createPool(ctx, poolName, v1alpha1.BootcNodePoolSpec{
				Image: imgRef,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"role": "realreg"},
				},
			})

			// Use a real RegistryResolver.
			resolver := &RegistryResolver{
				Client:    k8sClient,
				Namespace: "default",
			}
			reconciler := &BootcNodePoolReconciler{
				Client:        k8sClient,
				Scheme:        k8sClient.Scheme(),
				ImageResolver: resolver,
			}
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: poolName},
			})
			Expect(reconcileErr).NotTo(HaveOccurred())

			// Verify the BootcNode has the resolved digest.
			bn := &v1alpha1.BootcNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, bn)).To(Succeed())
			Expect(bn.Spec.DesiredImage).To(ContainSubstring("@sha256:"))
			Expect(bn.Spec.DesiredImage).To(ContainSubstring(expectedDigest.Hex))
		})
	})
})

// mockImageResolver is a test double for ImageResolver.
type mockImageResolver struct {
	resolvedDigest string
	err            error
	lastImageRef   string
	lastSecretName string
}

func (m *mockImageResolver) Resolve(_ context.Context, imageRef string, secretName string) (string, error) {
	m.lastImageRef = imageRef
	m.lastSecretName = secretName
	if m.err != nil {
		return "", m.err
	}
	return m.resolvedDigest, nil
}

// fakeResource implements authn.Resource for testing keychains.
type fakeResource struct {
	registry string
}

func (r *fakeResource) String() string      { return r.registry }
func (r *fakeResource) RegistryStr() string { return r.registry }

// cleanupResources deletes all test resources with the given suffix.
func cleanupResources(ctx context.Context, suffix string) {
	// Clean up pools.
	poolList := &v1alpha1.BootcNodePoolList{}
	_ = k8sClient.List(ctx, poolList)
	for i := range poolList.Items {
		if strings.HasSuffix(poolList.Items[i].Name, suffix) {
			_ = k8sClient.Delete(ctx, &poolList.Items[i])
		}
	}

	// Clean up BootcNodes.
	bnList := &v1alpha1.BootcNodeList{}
	_ = k8sClient.List(ctx, bnList)
	for i := range bnList.Items {
		if strings.HasSuffix(bnList.Items[i].Name, suffix) {
			_ = k8sClient.Delete(ctx, &bnList.Items[i])
		}
	}

	// Clean up Nodes.
	nodeList := &corev1.NodeList{}
	_ = k8sClient.List(ctx, nodeList)
	for i := range nodeList.Items {
		if strings.HasSuffix(nodeList.Items[i].Name, suffix) {
			_ = k8sClient.Delete(ctx, &nodeList.Items[i])
		}
	}
}
