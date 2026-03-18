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

package drain

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// newFakeClientset creates a fake clientset with eviction API support
// registered in discovery.
func newFakeClientset(objects ...runtime.Object) *k8sfake.Clientset {
	//nolint:staticcheck // NewSimpleClientset is needed because NewClientset requires generated apply configs
	clientset := k8sfake.NewSimpleClientset(objects...)

	// Register eviction subresource support in the fake discovery
	// client so that CheckEvictionSupport finds it. The drain helper
	// looks for eviction as a subresource under "v1" (core API
	// group), with Group and Version set to policy/v1.
	fakeDiscovery := clientset.Discovery().(*fake.FakeDiscovery)
	fakeDiscovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:    "pods/eviction",
					Kind:    "Eviction",
					Group:   "policy",
					Version: "v1",
				},
			},
		},
	}
	return clientset
}

// addEvictionReactor sets up a reactor that handles eviction
// subresource calls by deleting the pod from the tracker (simulating
// a successful eviction). We use the tracker directly to avoid
// re-entering Fake.Invokes() which would deadlock.
func addEvictionReactor(clientset *k8sfake.Clientset) {
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		createAction := action.(k8stesting.CreateAction)
		evictionName := createAction.GetObject().(metav1.Object).GetName()
		ns := createAction.GetNamespace()
		// Delete directly from the object tracker to avoid deadlock
		// with the Fake mutex.
		err := clientset.Tracker().Delete(
			corev1.SchemeGroupVersion.WithResource("pods"),
			ns,
			evictionName,
		)
		return true, nil, err
	})
}

// testCordonOrUncordon is a shared helper that tests both cordon and
// uncordon operations (they have identical test structure).
func testCordonOrUncordon(t *testing.T, operation string, initUnschedulable, wantUnschedulable bool) {
	t.Helper()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec: corev1.NodeSpec{
			Unschedulable: initUnschedulable,
		},
	}
	clientset := newFakeClientset(node)

	d := NewDrainer(clientset, Options{})
	var err error
	switch operation {
	case "cordon":
		err = d.Cordon(context.Background(), "test-node")
	case "uncordon":
		err = d.Uncordon(context.Background(), "test-node")
	}
	if err != nil {
		t.Fatalf("%s() error = %v", operation, err)
	}

	updatedNode, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting node: %v", err)
	}
	if updatedNode.Spec.Unschedulable != wantUnschedulable {
		t.Errorf("expected Unschedulable=%v, got %v", wantUnschedulable, updatedNode.Spec.Unschedulable)
	}
}

func TestCordon(t *testing.T) {
	t.Run("cordons a schedulable node", func(t *testing.T) {
		testCordonOrUncordon(t, "cordon", false, true)
	})
	t.Run("no-op when already cordoned", func(t *testing.T) {
		testCordonOrUncordon(t, "cordon", true, true)
	})
}

func TestCordonNodeNotFound(t *testing.T) {
	clientset := newFakeClientset()

	d := NewDrainer(clientset, Options{})
	err := d.Cordon(context.Background(), "nonexistent-node")
	if err == nil {
		t.Fatal("expected error when node does not exist")
	}
}

func TestUncordon(t *testing.T) {
	t.Run("uncordons an unschedulable node", func(t *testing.T) {
		testCordonOrUncordon(t, "uncordon", true, false)
	})
	t.Run("no-op when already schedulable", func(t *testing.T) {
		testCordonOrUncordon(t, "uncordon", false, false)
	})
}

func TestUncordonNodeNotFound(t *testing.T) {
	clientset := newFakeClientset()

	d := NewDrainer(clientset, Options{})
	err := d.Uncordon(context.Background(), "nonexistent-node")
	if err == nil {
		t.Fatal("expected error when node does not exist")
	}
}

func TestDrainEmptyNode(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
		},
	}
	clientset := newFakeClientset(node)

	d := NewDrainer(clientset, Options{
		Timeout: 10 * time.Second,
	})
	err := d.Drain(context.Background(), "test-node")
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
}

func TestDrainEvictsPods(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
		},
	}

	// A pod managed by a ReplicaSet (evictable).
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "test-rs",
					UID:        "rs-uid",
					Controller: boolPtr(true),
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	clientset := newFakeClientset(node, pod)
	addEvictionReactor(clientset)

	d := NewDrainer(clientset, Options{
		Timeout:            10 * time.Second,
		Force:              true,
		DeleteEmptyDirData: true,
	})
	err := d.Drain(context.Background(), "test-node")
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	// Verify the pod was evicted (deleted).
	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Errorf("expected 0 pods after drain, got %d", len(pods.Items))
	}
}

func TestDrainIgnoresDaemonSetPods(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
		},
	}

	// Create the DaemonSet object so the drain helper can look it up.
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-ds",
			Namespace: "default",
			UID:       "ds-uid-12345",
		},
	}

	// A DaemonSet pod (should be ignored during drain).
	dsPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "DaemonSet",
					Name:       "test-ds",
					UID:        "ds-uid-12345",
					Controller: boolPtr(true),
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	clientset := newFakeClientset(node, ds, dsPod)
	addEvictionReactor(clientset)

	d := NewDrainer(clientset, Options{
		Timeout: 10 * time.Second,
	})
	err := d.Drain(context.Background(), "test-node")
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	// DaemonSet pod should still exist (not evicted).
	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Errorf("expected DaemonSet pod to still exist, got %d pods", len(pods.Items))
	}
}

func TestDrainWithPDBRejection(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
		},
	}

	// A pod that would be subject to PDB.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pdb-pod",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "test-rs",
					UID:        "rs-uid",
					Controller: boolPtr(true),
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "test-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	clientset := newFakeClientset(node, pod)

	// Simulate PDB rejection: every eviction attempt returns 429
	// TooManyRequests.
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "eviction" {
			return true, nil, &apierrors.StatusError{
				ErrStatus: metav1.Status{
					Status:  metav1.StatusFailure,
					Code:    429,
					Reason:  metav1.StatusReasonTooManyRequests,
					Message: "Cannot evict pod as it would violate the pod's disruption budget",
				},
			}
		}
		return false, nil, nil
	})

	d := NewDrainer(clientset, Options{
		// Use a very short timeout so the test doesn't hang.
		Timeout: 1 * time.Second,
	})
	err := d.Drain(context.Background(), "test-node")
	// The drain should fail because the PDB prevents eviction.
	if err == nil {
		t.Fatal("expected error when PDB prevents eviction")
	}

	// The pod should still exist.
	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Errorf("expected pod to still exist, got %d pods", len(pods.Items))
	}
}

func TestNewDrainerDefaults(t *testing.T) {
	clientset := newFakeClientset()

	d := NewDrainer(clientset, Options{}).(*drainer)

	if d.opts.Timeout != DefaultDrainTimeout {
		t.Errorf("expected default timeout %v, got %v", DefaultDrainTimeout, d.opts.Timeout)
	}
	if d.opts.GracePeriodSeconds != DefaultGracePeriodSeconds {
		t.Errorf("expected default grace period %d, got %d", DefaultGracePeriodSeconds, d.opts.GracePeriodSeconds)
	}
}

func TestNewDrainerCustomOptions(t *testing.T) {
	clientset := newFakeClientset()

	d := NewDrainer(clientset, Options{
		Timeout:            10 * time.Second,
		GracePeriodSeconds: 30,
		DeleteEmptyDirData: true,
		Force:              true,
	}).(*drainer)

	if d.opts.Timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", d.opts.Timeout)
	}
	if d.opts.GracePeriodSeconds != 30 {
		t.Errorf("expected grace period 30, got %d", d.opts.GracePeriodSeconds)
	}
	if !d.opts.DeleteEmptyDirData {
		t.Error("expected DeleteEmptyDirData to be true")
	}
	if !d.opts.Force {
		t.Error("expected Force to be true")
	}
}

func boolPtr(b bool) *bool {
	return &b
}
