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
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/test/e2e/e2eutil"
	testutil "github.com/jlebon/bootc-operator/test/util"
)

// This is a simple "e2e" test which just tests CRD round-trips for now. We'll
// nuke it or enhance it with more e2e-worthy flows once we have more of the
// controller and daemon implemented.
//
// Note more comprehensive CRD round-trip tests exist in the unit tests.
func TestCRDSmoke(t *testing.T) {
	env := e2eutil.New(t)

	ctx := context.Background()

	t.Run("BootcNodePool", func(t *testing.T) {
		pool := testutil.NewPool("smoke-pool", "quay.io/example/myos:latest")

		if err := env.Client.Create(ctx, pool); err != nil {
			t.Fatalf("Failed to create BootcNodePool: %v", err)
		}

		got := &bootcv1alpha1.BootcNodePool{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(pool), got); err != nil {
			t.Fatalf("Failed to get BootcNodePool: %v", err)
		}
		if got.Spec.Image.Ref != "quay.io/example/myos:latest" {
			t.Errorf("image.ref = %q, want %q", got.Spec.Image.Ref, "quay.io/example/myos:latest")
		}

		if err := env.Client.Delete(ctx, pool); err != nil {
			t.Fatalf("Failed to delete BootcNodePool: %v", err)
		}
	})

	t.Run("BootcNode", func(t *testing.T) {
		node := testutil.NewNode("smoke-node", "quay.io/example/myos@sha256:abc123")

		if err := env.Client.Create(ctx, node); err != nil {
			t.Fatalf("Failed to create BootcNode: %v", err)
		}

		got := &bootcv1alpha1.BootcNode{}
		if err := env.Client.Get(ctx, client.ObjectKeyFromObject(node), got); err != nil {
			t.Fatalf("Failed to get BootcNode: %v", err)
		}
		if got.Spec.DesiredImage != "quay.io/example/myos@sha256:abc123" {
			t.Errorf("desiredImage = %q, want %q", got.Spec.DesiredImage, "quay.io/example/myos@sha256:abc123")
		}
		if got.Spec.DesiredImageState != bootcv1alpha1.DesiredImageStateStaged {
			t.Errorf("desiredImageState = %q, want %q", got.Spec.DesiredImageState, bootcv1alpha1.DesiredImageStateStaged)
		}

		if err := env.Client.Delete(ctx, node); err != nil {
			t.Fatalf("Failed to delete BootcNode: %v", err)
		}
	})
}
