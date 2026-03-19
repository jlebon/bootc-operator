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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// ImageResolver resolves container image references to digests.
type ImageResolver interface {
	// Resolve resolves an image reference to a fully qualified digest
	// reference (e.g. "quay.io/example/img@sha256:abc..."). If the
	// input is already a digest reference, it is returned as-is. If
	// it is a tag, the registry is queried (using HEAD) to determine
	// the current digest.
	//
	// secretName is the name of a kubernetes.io/dockerconfigjson
	// Secret in the operator's namespace to use for authentication.
	// When empty, no additional credentials are provided (the
	// default keychain is used, which includes ~/.docker/config.json
	// and cloud provider credentials).
	Resolve(ctx context.Context, imageRef string, secretName string) (string, error)
}

// RegistryResolver resolves image tags to digests by querying the
// container registry via go-containerregistry.
type RegistryResolver struct {
	// Client is used to read Secrets for registry authentication.
	Client client.Reader

	// Namespace is the operator's namespace where imagePullSecrets
	// are stored.
	Namespace string

	// RemoteOptions allows injecting additional remote.Option values
	// for testing (e.g. to override the transport).
	RemoteOptions []remote.Option
}

// NewRegistryResolver creates a RegistryResolver that reads Secrets
// from the given client in the specified namespace.
func NewRegistryResolver(c client.Reader, namespace string) *RegistryResolver {
	return &RegistryResolver{
		Client:    c,
		Namespace: namespace,
	}
}

// Resolve resolves an image reference to a digest. If the reference
// already contains a digest (@sha256:...), it is returned unchanged.
// Otherwise the registry is queried via HEAD to resolve the tag.
func (r *RegistryResolver) Resolve(ctx context.Context, imageRef string, secretName string) (string, error) {
	log := logf.FromContext(ctx)

	// If already a digest reference, return as-is.
	if isDigestReference(imageRef) {
		return imageRef, nil
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("parsing image reference %q: %w", imageRef, err)
	}

	opts := append([]remote.Option{remote.WithContext(ctx)}, r.RemoteOptions...)

	// Set up authentication.
	keychain, err := r.keychainForSecret(ctx, secretName)
	if err != nil {
		return "", fmt.Errorf("setting up auth for image %q: %w", imageRef, err)
	}
	opts = append(opts, remote.WithAuthFromKeychain(keychain))

	// Use HEAD to get the digest without downloading the manifest.
	desc, err := remote.Head(ref, opts...)
	if err != nil {
		return "", fmt.Errorf("resolving digest for %q: %w", imageRef, err)
	}

	// Construct the digest reference: repo@sha256:hex
	repo := ref.Context().String()
	digest := desc.Digest.String()
	resolved := repo + "@" + digest

	log.Info("Resolved image tag to digest", "tag", imageRef, "digest", resolved)
	return resolved, nil
}

// isDigestReference returns true if the image reference contains a
// digest (i.e. uses @sha256:).
func isDigestReference(imageRef string) bool {
	return strings.Contains(imageRef, "@sha256:")
}

// keychainForSecret returns an authn.Keychain that uses credentials
// from the named Secret (if provided), falling back to the default
// keychain.
func (r *RegistryResolver) keychainForSecret(ctx context.Context, secretName string) (authn.Keychain, error) {
	if secretName == "" {
		return authn.DefaultKeychain, nil
	}

	secret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: r.Namespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("getting imagePullSecret %q in namespace %q: %w", secretName, r.Namespace, err)
	}

	if secret.Type != corev1.SecretTypeDockerConfigJson {
		return nil, fmt.Errorf("imagePullSecret %q is type %q, expected %q", secretName, secret.Type, corev1.SecretTypeDockerConfigJson)
	}

	configData, ok := secret.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return nil, fmt.Errorf("imagePullSecret %q missing key %q", secretName, corev1.DockerConfigJsonKey)
	}

	keychain, err := parseDockerConfigJSON(configData)
	if err != nil {
		return nil, fmt.Errorf("parsing dockerconfigjson from secret %q: %w", secretName, err)
	}

	return keychain, nil
}

// dockerConfig represents the structure of a .dockerconfigjson Secret.
type dockerConfig struct {
	Auths map[string]dockerAuthEntry `json:"auths"`
}

// dockerAuthEntry is a single registry auth entry.
type dockerAuthEntry struct {
	Auth string `json:"auth"`
}

// secretKeychain implements authn.Keychain using credentials parsed
// from a kubernetes.io/dockerconfigjson Secret.
type secretKeychain struct {
	auths map[string]dockerAuthEntry
}

// Resolve returns the authenticator for the given registry. It matches
// the registry host against the auths map keys, trying several
// variations (exact, with https://, with trailing slash, with /v1/ or
// /v2/ path suffixes) because Docker config files use inconsistent key
// formats.
func (k *secretKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	registry := target.RegistryStr()

	// Build a list of candidate keys to try.
	candidates := []string{
		registry,
		"https://" + registry,
		"https://" + registry + "/",
		"https://" + registry + "/v1/",
		"https://" + registry + "/v2/",
	}

	for _, candidate := range candidates {
		if entry, ok := k.auths[candidate]; ok {
			return authn.FromConfig(authn.AuthConfig{Auth: entry.Auth}), nil
		}
	}

	// Also try matching auths keys that contain the registry as a
	// substring (handles cases like "https://index.docker.io/v1/"
	// matching registry "index.docker.io").
	for key, entry := range k.auths {
		if strings.Contains(key, registry) {
			return authn.FromConfig(authn.AuthConfig{Auth: entry.Auth}), nil
		}
	}

	// No match, fall back to anonymous.
	return authn.Anonymous, nil
}

// parseDockerConfigJSON parses a dockerconfigjson byte slice into a
// Keychain.
func parseDockerConfigJSON(data []byte) (authn.Keychain, error) {
	var config dockerConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("unmarshalling dockerconfigjson: %w", err)
	}

	return &secretKeychain{auths: config.Auths}, nil
}
