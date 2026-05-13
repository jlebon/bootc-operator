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

package e2e

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/test/e2e/e2eutil"
	testutil "github.com/jlebon/bootc-operator/test/util"
)

const (
	pollTimeout  = 60 * time.Second
	pollInterval = 2 * time.Second
)

// TestControllerMembership deploys the controller in a bink cluster,
// creates a BootcNodePool selecting the worker node, and verifies that
// a BootcNode is created and the node is labeled bootc.dev/managed.
func TestControllerMembership(t *testing.T) {
	env := e2eutil.New(t)

	ctx := context.Background()

	// The bink cluster has a node called "node1". Label it as a worker so it
	// matches our pool's default nodeSelector. XXX: lower this down to bink?
	var node corev1.Node
	if err := env.Client.Get(ctx, client.ObjectKey{Name: "node1"}, &node); err != nil {
		t.Fatalf("Failed to get node1: %v", err)
	}
	patch := client.StrategicMergeFrom(node.DeepCopy())
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	maps.Copy(node.Labels, testutil.WorkerLabels())
	if err := env.Client.Patch(ctx, &node, patch); err != nil {
		t.Fatalf("Failed to label node1 as worker: %v", err)
	}

	// Create a pool with a digest ref.
	imageRef := "quay.io/example/myos@sha256:abc123"
	pool := testutil.NewPool("e2e-workers", imageRef, testutil.WithWorkerSelector())
	if err := env.Client.Create(ctx, pool); err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}

	// Wait for BootcNode to appear for node1.
	var bn bootcv1alpha1.BootcNode
	testutil.WaitForCreated(t, pollTimeout, pollInterval, env.Client, client.ObjectKey{Name: "node1"}, &bn)

	// Verify ownerReference.
	owner := metav1.GetControllerOf(&bn)
	if owner == nil || owner.Name != "e2e-workers" {
		t.Errorf("BootcNode owner = %v, want e2e-workers", owner)
	}

	// Verify desiredImage.
	if bn.Spec.DesiredImage != imageRef {
		t.Errorf("BootcNode desiredImage = %q, want %q", bn.Spec.DesiredImage, imageRef)
	}

	// Verify node1 has the managed label.
	testutil.WaitFor(t, pollTimeout, pollInterval, "node1 to be labeled bootc.dev/managed", func() (bool, error) {
		if err := env.Client.Get(ctx, client.ObjectKey{Name: "node1"}, &node); err != nil {
			return false, fmt.Errorf("getting node1: %w", err)
		}
		_, ok := node.Labels[bootcv1alpha1.LabelManaged]
		return ok, nil
	})
}
