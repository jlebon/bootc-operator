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

// Package e2eutil provides helpers for running end-to-end tests against
// bink-managed Kubernetes clusters. Each test gets its own cluster for
// full isolation.
package e2eutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

// config holds resolved settings for the test cluster.
type config struct {
	memory int
}

// Option configures how the test cluster is created.
type Option func(*config)

// binkPath is resolved once on first use via resolveBinkPath.
var (
	binkPath     string
	binkPathOnce sync.Once
)

// Env holds a live cluster context for a single e2e test.
type Env struct {
	// Client is a controller-runtime client with the bootc CRD scheme
	// registered. Ready to create/get/list CRD objects.
	Client client.Client

	// Kubeconfig is the path to the kubeconfig file for this cluster.
	Kubeconfig string

	// Cluster is the bink cluster name.
	Cluster string
}

// New creates a bink cluster, deploys the operator manifests, and
// returns an Env ready for testing. The cluster is torn down
// automatically when the test ends (including on failure).
func New(t *testing.T, opts ...Option) *Env {
	t.Helper()

	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}

	resolveBinkPath(t)
	clusterName := generateClusterName(t)

	// Find the project root (where config/default/ lives).
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Fatalf("finding project root: %v", err)
	}

	startCluster(t, clusterName, cfg.memory)

	// Register cleanup early so the cluster is torn down even if
	// subsequent steps fail.
	t.Cleanup(func() {
		stopCluster(t, clusterName)
	})

	kubeconfigPath := exposeAPI(t, clusterName)
	applyManifests(t, kubeconfigPath, projectRoot)
	k8sClient := buildClient(t, kubeconfigPath)
	waitForNodeReady(t, k8sClient, "node1")

	return &Env{
		Client:     k8sClient,
		Kubeconfig: kubeconfigPath,
		Cluster:    clusterName,
	}
}

// WithMemory sets the VM memory in MB. If not set, bink's default is used.
func WithMemory(mb int) Option {
	return func(c *config) {
		c.memory = mb
	}
}

// resolveBinkPath locates the bink binary on first call (via BINK_PATH
// env var or PATH lookup) and caches the result.
func resolveBinkPath(t *testing.T) {
	t.Helper()

	var resolveErr error
	binkPathOnce.Do(func() {
		if p := os.Getenv("BINK_PATH"); p != "" {
			if _, err := os.Stat(p); err != nil {
				resolveErr = fmt.Errorf("BINK_PATH=%q not found: %w", p, err)
				return
			}
			binkPath = p
			return
		}
		if p, err := exec.LookPath("bink"); err != nil {
			resolveErr = fmt.Errorf("PATH lookup failed: %w", err)
		} else {
			binkPath = p
		}
	})

	if binkPath == "" {
		// Since this is only set when Once is triggered, we implicitly only
		// log the actual failure once here. Other calls would get the generic
		// error. I don't think that's a problem.
		if resolveErr != nil {
			t.Log(resolveErr)
		}
		t.Fatal("bink binary not found (set BINK_PATH or add to PATH)")
	}
}

// generateClusterName creates a unique cluster name for test isolation.
func generateClusterName(t *testing.T) string {
	t.Helper()

	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generating random cluster name: %v", err)
	}
	return "e2e-" + hex.EncodeToString(b)
}

// findProjectRoot walks up from the working directory looking for go.mod.
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod in any parent directory")
		}
		dir = parent
	}
}

// startCluster runs `bink cluster start`.
func startCluster(t *testing.T, clusterName string, memory int) {
	t.Helper()

	args := []string{"cluster", "start",
		"--cluster-name", clusterName,
		"--api-port", "0",
	}
	if memory > 0 {
		args = append(args, "--memory", fmt.Sprintf("%d", memory))
	}

	t.Logf("Starting bink cluster %q...", clusterName)
	if err := runBink(t, args...); err != nil {
		t.Fatalf("starting cluster: %v", err)
	}
}

// stopCluster runs `bink cluster stop --remove-data`.
func stopCluster(t *testing.T, clusterName string) {
	t.Logf("Tearing down bink cluster %q...", clusterName)
	if err := runBink(t, "cluster", "stop", "--remove-data", "--cluster-name", clusterName); err != nil {
		// Log but don't fail — we're in cleanup.
		t.Logf("WARNING: failed to stop cluster %q: %v", clusterName, err)
	}
}

// exposeAPI runs `bink api expose` and returns the kubeconfig path.
func exposeAPI(t *testing.T, clusterName string) string {
	t.Helper()
	t.Logf("Exposing API for cluster %q...", clusterName)

	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := runBink(t, "api", "expose",
		"--cluster-name", clusterName,
		"--kubeconfig", kubeconfigPath,
	); err != nil {
		t.Fatalf("exposing API: %v", err)
	}
	return kubeconfigPath
}

// applyManifests runs `kubectl apply -k config/default/` to deploy the
// operator manifests (CRDs, RBAC, manager Deployment).
func applyManifests(t *testing.T, kubeconfigPath, projectRoot string) {
	t.Helper()
	t.Log("Applying operator manifests (config/default/)...")

	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Fatal("kubectl not found on PATH")
	}

	// We shell out to kubectl rather than using a Go client because
	// kubectl handles kustomize rendering and server-side apply in one
	// shot — reimplementing that in Go would be significant.
	kustomizeDir := filepath.Join(projectRoot, "config", "default")
	cmd := exec.Command(kubectl, "apply",
		"--kubeconfig", kubeconfigPath,
		"-k", kustomizeDir,
		"--server-side",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("applying manifests: %v", err)
	}
}

// buildClient creates a controller-runtime client from the kubeconfig
// with the bootc CRD scheme registered.
func buildClient(t *testing.T, kubeconfigPath string) client.Client {
	t.Helper()

	if err := bootcv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("adding bootc scheme: %v", err)
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatalf("building rest config from kubeconfig: %v", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("creating controller-runtime client: %v", err)
	}

	return c
}

// waitForNodeReady polls until the named node has condition Ready=True.
func waitForNodeReady(t *testing.T, c client.Client, nodeName string) {
	t.Helper()
	t.Logf("Waiting for node %q to be Ready...", nodeName)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for node %q to become Ready", nodeName)
		case <-ticker.C:
			node := &corev1.Node{}
			if err := c.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
				t.Logf("  node %q not found yet: %v", nodeName, err)
				continue
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					t.Logf("  node %q is Ready", nodeName)
					return
				}
			}
		}
	}
}

// runBink executes a bink command and returns any error.
func runBink(t *testing.T, args ...string) error {
	t.Helper()

	if binkPath == "" {
		panic("runBink called before resolveBinkPath")
	}
	cmd := exec.Command(binkPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
