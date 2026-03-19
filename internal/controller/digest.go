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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DigestResolver resolves container image references to digests.
type DigestResolver interface {
	Resolve(ctx context.Context, imageRef string, pullSecret *corev1.Secret) (string, error)
}

// remoteDigestResolver resolves digests by querying the container
// registry using go-containerregistry.
type remoteDigestResolver struct{}

// NewDigestResolver creates a DigestResolver that queries container
// registries.
func NewDigestResolver() DigestResolver {
	return &remoteDigestResolver{}
}

// Resolve resolves an image reference to a digest. If the reference is
// already a digest, it returns the digest directly. If it is a tag, it
// queries the registry to resolve it. An optional pull secret provides
// registry credentials.
func (r *remoteDigestResolver) Resolve(ctx context.Context, imageRef string, pullSecret *corev1.Secret) (string, error) {
	// If the reference already contains a digest, return it directly.
	if digest, ok := extractDigestFromRef(imageRef); ok {
		return digest, nil
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("parsing image reference %q: %w", imageRef, err)
	}

	opts := []remote.Option{remote.WithContext(ctx)}
	if pullSecret != nil {
		keychain, err := keychainFromSecret(pullSecret)
		if err != nil {
			return "", fmt.Errorf("creating keychain from pull secret: %w", err)
		}
		opts = append(opts, remote.WithAuthFromKeychain(keychain))
	}

	desc, err := remote.Head(ref, opts...)
	if err != nil {
		return "", fmt.Errorf("resolving digest for %q: %w", imageRef, err)
	}

	return desc.Digest.String(), nil
}

// extractDigestFromRef checks if an image reference contains a digest
// (e.g. "image@sha256:abc...") and returns it.
func extractDigestFromRef(imageRef string) (string, bool) {
	if idx := strings.LastIndex(imageRef, "@"); idx >= 0 {
		digest := imageRef[idx+1:]
		if strings.HasPrefix(digest, "sha256:") || strings.HasPrefix(digest, "sha512:") {
			return digest, true
		}
	}
	return "", false
}

// keychainFromSecret creates an authn.Keychain from a
// kubernetes.io/dockerconfigjson Secret.
func keychainFromSecret(secret *corev1.Secret) (authn.Keychain, error) {
	if secret.Type != corev1.SecretTypeDockerConfigJson {
		return nil, fmt.Errorf("secret %s/%s is type %s, expected %s",
			secret.Namespace, secret.Name, secret.Type, corev1.SecretTypeDockerConfigJson)
	}

	data, ok := secret.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s missing key %s",
			secret.Namespace, secret.Name, corev1.DockerConfigJsonKey)
	}

	// Parse the dockerconfigjson format to create a keychain.
	var cfg dockerConfigJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing dockerconfigjson: %w", err)
	}

	return &staticKeychain{auths: cfg.Auths}, nil
}

// dockerConfigJSON matches the structure of a .dockerconfigjson file.
type dockerConfigJSON struct {
	Auths map[string]dockerAuthEntry `json:"auths"`
}

// dockerAuthEntry is a single registry auth entry.
type dockerAuthEntry struct {
	Auth     string `json:"auth,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// staticKeychain is a simple keychain backed by parsed docker config.
type staticKeychain struct {
	auths map[string]dockerAuthEntry
}

func (k *staticKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	registry := target.RegistryStr()

	// Try exact match first, then common aliases.
	for _, candidate := range []string{registry, "https://" + registry, "http://" + registry} {
		if entry, ok := k.auths[candidate]; ok {
			if entry.Auth != "" {
				return authn.FromConfig(authn.AuthConfig{Auth: entry.Auth}), nil
			}
			return authn.FromConfig(authn.AuthConfig{
				Username: entry.Username,
				Password: entry.Password,
			}), nil
		}
	}

	return authn.Anonymous, nil
}

// getPullSecret retrieves the pull secret referenced by a
// BootcNodePool, if any. Returns nil if no secret is referenced.
func getPullSecret(ctx context.Context, c client.Reader, secretName, namespace string) (*corev1.Secret, error) {
	if secretName == "" {
		return nil, nil
	}

	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Name: secretName, Namespace: namespace}, secret); err != nil {
		return nil, fmt.Errorf("getting pull secret %s/%s: %w", namespace, secretName, err)
	}

	return secret, nil
}

// imageWithDigest returns a fully qualified image reference with the
// given digest (e.g. "quay.io/example/img@sha256:abc123...").
func imageWithDigest(imageRef, digest string) string {
	baseName := imageRef
	// Strip existing tag or digest.
	if idx := strings.LastIndex(baseName, "@"); idx >= 0 {
		baseName = baseName[:idx]
	} else {
		// Strip tag: find the last colon that is not part of a port.
		for i := len(baseName) - 1; i >= 0; i-- {
			switch baseName[i] {
			case ':':
				if !strings.Contains(baseName[i+1:], "/") {
					baseName = baseName[:i]
				}
				i = -1 // break loop
			case '/':
				i = -1 // break loop
			}
		}
	}
	return baseName + "@" + digest
}
