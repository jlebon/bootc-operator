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

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// newFakeClientWithEviction creates a fake clientset with discovery
// resources registered for pod eviction support (required by
// k8s.io/kubectl/pkg/drain).
func newFakeClientWithEviction(objs ...runtime.Object) *fake.Clientset {
	client := fake.NewSimpleClientset(objs...) //nolint:staticcheck // NewClientset requires generated apply configs

	// Register v1 resources including pods/eviction so
	// CheckEvictionSupport finds them.
	fakeDiscovery, ok := client.Discovery().(*fakediscovery.FakeDiscovery)
	if ok {
		fakeDiscovery.Resources = []*metav1.APIResourceList{
			{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{
					{Name: "pods", Kind: "Pod", Namespaced: true},
					{
						Name:    "pods/eviction",
						Kind:    "Eviction",
						Group:   "policy",
						Version: "v1",
					},
					{Name: "nodes", Kind: "Node"},
				},
			},
		}
	}

	// Handle eviction subresource: accept the eviction and delete the
	// pod from the tracker so drain doesn't wait for termination.
	// We must delete via the tracker directly because reactor
	// callbacks run under Fake's mutex lock, and calling back into
	// the client would deadlock.
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		evictionAction, ok := action.(k8stesting.CreateAction)
		if !ok {
			return true, nil, nil
		}
		eviction, ok := evictionAction.GetObject().(*policyv1.Eviction)
		if !ok {
			return true, nil, nil
		}
		// Delete the pod from the object tracker to simulate
		// successful eviction.
		_ = client.Tracker().Delete(
			corev1.SchemeGroupVersion.WithResource("pods"),
			eviction.Namespace, eviction.Name)
		return true, nil, nil
	})

	return client
}

func TestCordon(t *testing.T) {
	tests := []struct {
		name         string
		node         *corev1.Node
		expectErr    bool
		expectCordon bool
	}{
		{
			name: "cordon an uncordoned node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
				Spec:       corev1.NodeSpec{Unschedulable: false},
			},
			expectCordon: true,
		},
		{
			name: "cordon an already cordoned node is idempotent",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
				Spec:       corev1.NodeSpec{Unschedulable: true},
			},
			expectCordon: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.node) //nolint:staticcheck // NewClientset requires generated apply configs
			d := New(client, Options{Timeout: 30 * time.Second})

			err := d.Cordon(context.Background(), tt.node.Name)
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify the node is now cordoned.
			node, err := client.CoreV1().Nodes().Get(context.Background(), tt.node.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("getting node: %v", err)
			}
			if tt.expectCordon && !node.Spec.Unschedulable {
				t.Error("expected node to be unschedulable")
			}
		})
	}
}

func TestCordonNonexistentNode(t *testing.T) {
	client := fake.NewSimpleClientset() //nolint:staticcheck // NewClientset requires generated apply configs
	d := New(client, Options{Timeout: 30 * time.Second})

	err := d.Cordon(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

func TestUncordon(t *testing.T) {
	tests := []struct {
		name           string
		node           *corev1.Node
		expectErr      bool
		expectSchedule bool
	}{
		{
			name: "uncordon a cordoned node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
				Spec:       corev1.NodeSpec{Unschedulable: true},
			},
			expectSchedule: true,
		},
		{
			name: "uncordon an already schedulable node is idempotent",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
				Spec:       corev1.NodeSpec{Unschedulable: false},
			},
			expectSchedule: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.node) //nolint:staticcheck // NewClientset requires generated apply configs
			d := New(client, Options{Timeout: 30 * time.Second})

			err := d.Uncordon(context.Background(), tt.node.Name)
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify the node is now schedulable.
			node, err := client.CoreV1().Nodes().Get(context.Background(), tt.node.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("getting node: %v", err)
			}
			if tt.expectSchedule && node.Spec.Unschedulable {
				t.Error("expected node to be schedulable")
			}
		})
	}
}

func TestUncordonNonexistentNode(t *testing.T) {
	client := fake.NewSimpleClientset() //nolint:staticcheck // NewClientset requires generated apply configs
	d := New(client, Options{Timeout: 30 * time.Second})

	err := d.Uncordon(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent node")
	}
}

func TestDrainEmptyNode(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	client := newFakeClientWithEviction(node)
	d := New(client, Options{Timeout: 30 * time.Second})

	err := d.Drain(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error draining empty node: %v", err)
	}
}

func TestDrainNodeWithPods(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	client := newFakeClientWithEviction(node, pod)
	d := New(client, Options{Timeout: 30 * time.Second})

	err := d.Drain(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error draining node with pods: %v", err)
	}
}

func TestDrainNodeWithDaemonSetPod(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	isController := true
	dsPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds-pod",
			Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "DaemonSet",
					Name:       "my-ds",
					Controller: &isController,
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{
				{Name: "ds-container", Image: "ds-image"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	client := newFakeClientWithEviction(node, dsPod)
	d := New(client, Options{Timeout: 30 * time.Second})

	// DaemonSet pods should be ignored during drain
	// (IgnoreAllDaemonSets is true).
	err := d.Drain(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error draining node with DaemonSet pod: %v", err)
	}
}

func TestDrainDefaultTimeout(t *testing.T) {
	client := fake.NewSimpleClientset() //nolint:staticcheck // NewClientset requires generated apply configs
	d := New(client, Options{})

	// Verify the default timeout is applied.
	impl, ok := d.(*drainer)
	if !ok {
		t.Fatal("expected *drainer type")
	}
	if impl.options.Timeout != defaultDrainTimeout {
		t.Errorf("expected default timeout %v, got %v", defaultDrainTimeout, impl.options.Timeout)
	}
}

func TestDrainWithPDB(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	isController := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pdb-pod",
			Namespace: "default",
			Labels:    map[string]string{"app": "web"},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "web-rs",
					Controller: &isController,
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{
				{Name: "web", Image: "nginx"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	minAvail := intstr.FromInt32(0)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-pdb",
			Namespace: "default",
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvail,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
		},
	}
	client := newFakeClientWithEviction(node, pod, pdb)
	d := New(client, Options{Timeout: 30 * time.Second})

	err := d.Drain(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error draining node with PDB: %v", err)
	}
}
