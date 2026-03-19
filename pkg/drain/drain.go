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

// Package drain provides node cordon, drain, and uncordon operations
// for the bootc-operator. It wraps k8s.io/kubectl/pkg/drain to handle
// pod eviction with proper PDB respect, DaemonSet tolerance, and
// configurable timeouts.
package drain

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubectldrain "k8s.io/kubectl/pkg/drain"
)

const (
	// defaultDrainTimeout is the default maximum time to wait for pod
	// evictions to complete during a drain.
	defaultDrainTimeout = 5 * time.Minute

	// defaultGracePeriod uses the pod's own terminationGracePeriodSeconds.
	defaultGracePeriod = -1
)

// Drainer provides node cordon, drain, and uncordon operations.
type Drainer interface {
	// Cordon marks a node as unschedulable, preventing new pods from
	// being scheduled onto it. Idempotent: returns nil if the node is
	// already cordoned.
	Cordon(ctx context.Context, nodeName string) error

	// Drain evicts all non-DaemonSet pods from a node. The node must
	// be cordoned first. Respects PodDisruptionBudgets. Returns an
	// error if pods cannot be evicted within the configured timeout.
	Drain(ctx context.Context, nodeName string) error

	// Uncordon marks a node as schedulable again. Idempotent: returns
	// nil if the node is already uncordoned.
	Uncordon(ctx context.Context, nodeName string) error
}

// Options configures drain behavior.
type Options struct {
	// Timeout is the maximum time to wait for pod evictions to
	// complete. When zero, defaults to 5 minutes.
	Timeout time.Duration
}

// drainer implements Drainer using k8s.io/kubectl/pkg/drain.
type drainer struct {
	client  kubernetes.Interface
	options Options
}

// New creates a Drainer that wraps kubectl drain operations.
func New(client kubernetes.Interface, opts Options) Drainer {
	if opts.Timeout == 0 {
		opts.Timeout = defaultDrainTimeout
	}
	return &drainer{
		client:  client,
		options: opts,
	}
}

// Cordon marks a node as unschedulable.
func (d *drainer) Cordon(ctx context.Context, nodeName string) error {
	node, err := d.getNode(ctx, nodeName)
	if err != nil {
		return err
	}

	helper := d.newHelper(ctx)
	if err := kubectldrain.RunCordonOrUncordon(helper, node, true); err != nil {
		return fmt.Errorf("cordoning node %s: %w", nodeName, err)
	}

	return nil
}

// Drain evicts all pods from a cordoned node, respecting PDBs and
// tolerating DaemonSet pods.
func (d *drainer) Drain(ctx context.Context, nodeName string) error {
	helper := d.newHelper(ctx)
	if err := kubectldrain.RunNodeDrain(helper, nodeName); err != nil {
		return fmt.Errorf("draining node %s: %w", nodeName, err)
	}

	return nil
}

// Uncordon marks a node as schedulable again.
func (d *drainer) Uncordon(ctx context.Context, nodeName string) error {
	node, err := d.getNode(ctx, nodeName)
	if err != nil {
		return err
	}

	helper := d.newHelper(ctx)
	if err := kubectldrain.RunCordonOrUncordon(helper, node, false); err != nil {
		return fmt.Errorf("uncordoning node %s: %w", nodeName, err)
	}

	return nil
}

// newHelper creates a kubectl drain Helper with the configured options.
func (d *drainer) newHelper(ctx context.Context) *kubectldrain.Helper {
	return &kubectldrain.Helper{
		Ctx:                 ctx,
		Client:              d.client,
		Force:               true,
		GracePeriodSeconds:  defaultGracePeriod,
		IgnoreAllDaemonSets: true,
		Timeout:             d.options.Timeout,
		DeleteEmptyDirData:  true,
		Out:                 io.Discard,
		ErrOut:              io.Discard,
	}
}

// getNode fetches a Node object by name.
func (d *drainer) getNode(ctx context.Context, nodeName string) (*corev1.Node, error) {
	node, err := d.client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting node %s: %w", nodeName, err)
	}
	return node, nil
}
