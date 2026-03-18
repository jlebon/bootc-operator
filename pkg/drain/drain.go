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
// for the bootc-operator. It wraps k8s.io/kubectl/pkg/drain to evict
// pods from nodes before rebooting, respecting PodDisruptionBudgets.
package drain

import (
	"context"
	"fmt"
	"io"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubedrain "k8s.io/kubectl/pkg/drain"
)

const (
	// DefaultDrainTimeout is the default time to wait for pods to be
	// evicted during drain. If pods are not evicted within this time,
	// the drain fails.
	DefaultDrainTimeout = 5 * time.Minute

	// DefaultGracePeriodSeconds uses the pod's own
	// terminationGracePeriodSeconds (-1 means "use the pod's value").
	DefaultGracePeriodSeconds = -1
)

// Drainer performs node cordon, drain, and uncordon operations.
type Drainer interface {
	// Cordon marks a node as unschedulable so no new pods are scheduled
	// on it.
	Cordon(ctx context.Context, nodeName string) error

	// Drain evicts all evictable pods from a node, respecting
	// PodDisruptionBudgets. The node must be cordoned first. DaemonSet
	// pods are ignored.
	Drain(ctx context.Context, nodeName string) error

	// Uncordon marks a node as schedulable, allowing new pods to be
	// scheduled on it.
	Uncordon(ctx context.Context, nodeName string) error
}

// Options configures the drain behavior.
type Options struct {
	// Timeout is how long to wait for pods to be evicted during drain.
	// When zero, DefaultDrainTimeout is used.
	Timeout time.Duration

	// GracePeriodSeconds overrides the pod's terminationGracePeriodSeconds.
	// Use -1 (default) to respect each pod's own setting, or 0 to delete
	// immediately.
	GracePeriodSeconds int

	// DeleteEmptyDirData allows deletion of pods using emptyDir volumes.
	// When false (default), pods with emptyDir volumes block the drain.
	DeleteEmptyDirData bool

	// Force allows deletion of pods not managed by a controller (bare
	// pods). When false (default), unmanaged pods block the drain.
	Force bool

	// Out receives informational drain progress messages.
	// When nil, output is discarded.
	Out io.Writer

	// ErrOut receives drain error messages.
	// When nil, error output is discarded.
	ErrOut io.Writer
}

// drainer implements Drainer using k8s.io/kubectl/pkg/drain.
type drainer struct {
	clientset kubernetes.Interface
	opts      Options
}

// NewDrainer creates a Drainer that wraps kubectl's drain logic.
func NewDrainer(clientset kubernetes.Interface, opts Options) Drainer {
	if opts.Timeout == 0 {
		opts.Timeout = DefaultDrainTimeout
	}
	if opts.GracePeriodSeconds == 0 {
		opts.GracePeriodSeconds = DefaultGracePeriodSeconds
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.ErrOut == nil {
		opts.ErrOut = io.Discard
	}
	return &drainer{
		clientset: clientset,
		opts:      opts,
	}
}

func (d *drainer) Cordon(ctx context.Context, nodeName string) error {
	node, err := d.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting node %q: %w", nodeName, err)
	}

	if node.Spec.Unschedulable {
		return nil // already cordoned
	}

	helper := d.newHelper(ctx)
	return kubedrain.RunCordonOrUncordon(helper, node, true)
}

func (d *drainer) Drain(ctx context.Context, nodeName string) error {
	helper := d.newHelper(ctx)
	return kubedrain.RunNodeDrain(helper, nodeName)
}

func (d *drainer) Uncordon(ctx context.Context, nodeName string) error {
	node, err := d.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting node %q: %w", nodeName, err)
	}

	if !node.Spec.Unschedulable {
		return nil // already schedulable
	}

	helper := d.newHelper(ctx)
	return kubedrain.RunCordonOrUncordon(helper, node, false)
}

// newHelper creates a kubectl drain Helper configured from the
// Drainer's options.
func (d *drainer) newHelper(ctx context.Context) *kubedrain.Helper {
	return &kubedrain.Helper{
		Ctx:                 ctx,
		Client:              d.clientset,
		Force:               d.opts.Force,
		GracePeriodSeconds:  d.opts.GracePeriodSeconds,
		IgnoreAllDaemonSets: true,
		Timeout:             d.opts.Timeout,
		DeleteEmptyDirData:  d.opts.DeleteEmptyDirData,
		Out:                 d.opts.Out,
		ErrOut:              d.opts.ErrOut,
	}
}
