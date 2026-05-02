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
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

func TestBootcNodePoolCRD(t *testing.T) {
	ctx := context.Background()

	pool := NewTestPool("workers",
		WithRebootPolicy(bootcv1alpha1.RebootPolicyAllowSoftReboot),
		WithPullSecret(testSecretName, testSecretNS),
	)

	// Save the spec before Create, which mutates pool in-place with
	// the server response (including defaults).
	wantSpec := *pool.Spec.DeepCopy()

	// Create
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("Failed to create BootcNodePool: %v", err)
	}
	t.Cleanup(func() {
		if err := k8sClient.Delete(ctx, pool); client.IgnoreNotFound(err) != nil {
			t.Logf("cleanup: failed to delete pool: %v", err)
		}
	})

	// Retrieve and verify spec round-trips. We set all defaulted
	// fields explicitly (RebootPolicy), so the input and output specs
	// should match exactly.
	got := &bootcv1alpha1.BootcNodePool{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pool), got); err != nil {
		t.Fatalf("Failed to get BootcNodePool: %v", err)
	}
	if !reflect.DeepEqual(got.Spec, wantSpec) {
		t.Errorf("spec mismatch:\n  got:  %+v\n  want: %+v", got.Spec, wantSpec)
	}

	// Update status. Use a fixed timestamp truncated to seconds to match the
	// precision the API server stores so we can just `DeepEqual` the whole thing.
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	got.Status = bootcv1alpha1.BootcNodePoolStatus{
		ObservedGeneration: got.Generation,
		TargetDigest:       testDigestA,
		DeployedDigest:     testDigestB,
		UpdateAvailable:    true,
		NodeCount:          3,
		UpdatedCount:       1,
		UpdatingCount:      1,
		DegradedCount:      1,
		Conditions: []metav1.Condition{
			{
				Type:               bootcv1alpha1.PoolUpToDate,
				Status:             metav1.ConditionFalse,
				Reason:             bootcv1alpha1.PoolRolloutInProgress,
				Message:            "1/3 updated; 1 staging",
				LastTransitionTime: now,
			},
			{
				Type:               bootcv1alpha1.PoolDegraded,
				Status:             metav1.ConditionTrue,
				Reason:             bootcv1alpha1.PoolStagingFailed,
				Message:            "node worker-3 failed to stage",
				LastTransitionTime: now,
			},
		},
	}

	wantStatus := *got.Status.DeepCopy() // snapshot before Update
	if err := k8sClient.Status().Update(ctx, got); err != nil {
		t.Fatalf("Failed to update BootcNodePool status: %v", err)
	}

	// Verify status round-trips
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pool), got); err != nil {
		t.Fatalf("Failed to get BootcNodePool after status update: %v", err)
	}
	// Copy canonical timestamps from the server response into our
	// expected status so DeepEqual ignores timezone/precision differences.
	for i := range wantStatus.Conditions {
		wantStatus.Conditions[i].LastTransitionTime = got.Status.Conditions[i].LastTransitionTime
	}
	if !reflect.DeepEqual(got.Status, wantStatus) {
		t.Errorf("status mismatch:\n  got:  %+v\n  want: %+v", got.Status, wantStatus)
	}
}

func TestBootcNodeCRD(t *testing.T) {
	ctx := context.Background()

	node := NewTestNode("worker-1",
		WithNodePullSecret(testSecretName, testSecretNS, testSecretHash),
	)

	// Save the spec before Create, which mutates node in-place.
	wantSpec := *node.Spec.DeepCopy()

	// Create
	if err := k8sClient.Create(ctx, node); err != nil {
		t.Fatalf("Failed to create BootcNode: %v", err)
	}
	t.Cleanup(func() {
		if err := k8sClient.Delete(ctx, node); client.IgnoreNotFound(err) != nil {
			t.Logf("cleanup: failed to delete node: %v", err)
		}
	})

	// Retrieve and verify spec round-trips. BootcNodeSpec has no
	// defaulted fields, so the input and output should match exactly.
	got := &bootcv1alpha1.BootcNode{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(node), got); err != nil {
		t.Fatalf("Failed to get BootcNode: %v", err)
	}
	if !reflect.DeepEqual(got.Spec, wantSpec) {
		t.Errorf("spec mismatch:\n  got:  %+v\n  want: %+v", got.Spec, wantSpec)
	}

	// Update status. Use a fixed timestamp truncated to seconds to match the
	// precision the API server stores so we can just `DeepEqual` the whole thing.
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	ts := metav1.NewTime(time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC))
	got.Status = bootcv1alpha1.BootcNodeStatus{
		ObservedGeneration: got.Generation,
		Booted: &bootcv1alpha1.ImageInfo{
			Image:             testImageDigestRefB,
			ImageDigest:       testDigestB,
			Version:           "9.4",
			Timestamp:         &ts,
			Architecture:      "amd64",
			SoftRebootCapable: true,
			Incompatible:      false,
		},
		Staged: &bootcv1alpha1.ImageInfo{
			Image:             testImageDigestRefA,
			ImageDigest:       testDigestA,
			SoftRebootCapable: true,
		},
		Rollback: &bootcv1alpha1.ImageInfo{
			Image:       testImageDigestRefC,
			ImageDigest: testDigestC,
		},
		Conditions: []metav1.Condition{
			{
				Type:               bootcv1alpha1.NodeIdle,
				Status:             metav1.ConditionFalse,
				Reason:             bootcv1alpha1.NodeReasonStaged,
				Message:            "Image staged, awaiting desiredImageState: Booted",
				LastTransitionTime: now,
			},
		},
	}
	wantStatus := *got.Status.DeepCopy() // snapshot before Update
	if err := k8sClient.Status().Update(ctx, got); err != nil {
		t.Fatalf("Failed to update BootcNode status: %v", err)
	}

	// Verify status round-trips
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(node), got); err != nil {
		t.Fatalf("Failed to get BootcNode after status update: %v", err)
	}
	// Copy canonical timestamps from the server response so
	// DeepEqual ignores timezone/precision differences.
	for i := range wantStatus.Conditions {
		wantStatus.Conditions[i].LastTransitionTime = got.Status.Conditions[i].LastTransitionTime
	}
	if wantStatus.Booted != nil && wantStatus.Booted.Timestamp != nil {
		wantStatus.Booted.Timestamp = got.Status.Booted.Timestamp
	}
	if !reflect.DeepEqual(got.Status, wantStatus) {
		t.Errorf("status mismatch:\n  got:  %+v\n  want: %+v", got.Status, wantStatus)
	}

}

func TestBootcNodePoolEnumValidation(t *testing.T) {
	ctx := context.Background()

	pool := NewTestPool("invalid-reboot-policy",
		WithRebootPolicy("Invalid"),
	)
	if err := k8sClient.Create(ctx, pool); err == nil {
		k8sClient.Delete(ctx, pool)
		t.Fatal("Expected creation with invalid rebootPolicy to fail, but it succeeded")
	}
}

func TestBootcNodeEnumValidation(t *testing.T) {
	ctx := context.Background()

	node := NewTestNode("invalid-image-state")
	node.Spec.DesiredImageState = "Invalid"
	if err := k8sClient.Create(ctx, node); err == nil {
		k8sClient.Delete(ctx, node)
		t.Fatal("Expected creation with invalid desiredImageState to fail, but it succeeded")
	}
}

func TestBootcNodePoolMinLengthValidation(t *testing.T) {
	ctx := context.Background()

	pool := NewTestPool("empty-image-ref")
	pool.Spec.Image.Ref = ""
	if err := k8sClient.Create(ctx, pool); err == nil {
		k8sClient.Delete(ctx, pool)
		t.Fatal("Expected creation with empty image.ref to fail, but it succeeded")
	}
}

func TestBootcNodeMinLengthValidation(t *testing.T) {
	ctx := context.Background()

	node := NewTestNode("empty-desired-image")
	node.Spec.DesiredImage = ""
	if err := k8sClient.Create(ctx, node); err == nil {
		k8sClient.Delete(ctx, node)
		t.Fatal("Expected creation with empty desiredImage to fail, but it succeeded")
	}
}
