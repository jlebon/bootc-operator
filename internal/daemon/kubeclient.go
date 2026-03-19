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

package daemon

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/jlebon/bootc-operator/api/v1alpha1"
)

// KubeClient abstracts Kubernetes API operations for testability. The
// daemon only needs to interact with its own BootcNode and its Node.
type KubeClient interface {
	// GetBootcNode retrieves the BootcNode with the given name.
	// Returns nil and no error if the BootcNode does not exist.
	GetBootcNode(ctx context.Context, name string) (*v1alpha1.BootcNode, error)

	// CreateBootcNode creates a new BootcNode.
	CreateBootcNode(ctx context.Context, bn *v1alpha1.BootcNode) error

	// UpdateBootcNode updates the spec and metadata of a BootcNode.
	UpdateBootcNode(ctx context.Context, bn *v1alpha1.BootcNode) error

	// UpdateBootcNodeStatus updates the status subresource of a BootcNode.
	UpdateBootcNodeStatus(ctx context.Context, bn *v1alpha1.BootcNode) error

	// GetNode retrieves the Node with the given name.
	GetNode(ctx context.Context, name string) (*corev1.Node, error)
}

// realKubeClient implements KubeClient using client-go REST clients.
// It uses a typed REST client for BootcNode CRD operations and the
// standard kubernetes clientset for Node operations.
type realKubeClient struct {
	bootcREST  rest.Interface
	coreClient kubernetes.Interface
}

// NewKubeClient creates a KubeClient from a rest.Config. It registers
// the bootc.dev/v1alpha1 scheme and configures REST clients for both
// the BootcNode CRD and core Kubernetes types.
func NewKubeClient(config *rest.Config) (KubeClient, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding v1alpha1 to scheme: %w", err)
	}

	bootcConfig := *config
	bootcConfig.GroupVersion = &v1alpha1.GroupVersion
	bootcConfig.APIPath = "/apis"
	bootcConfig.NegotiatedSerializer = serializer.NewCodecFactory(scheme)

	bootcREST, err := rest.RESTClientFor(&bootcConfig)
	if err != nil {
		return nil, fmt.Errorf("creating bootc REST client: %w", err)
	}

	coreClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating core client: %w", err)
	}

	return &realKubeClient{
		bootcREST:  bootcREST,
		coreClient: coreClient,
	}, nil
}

func (c *realKubeClient) GetBootcNode(ctx context.Context, name string) (*v1alpha1.BootcNode, error) {
	bn := &v1alpha1.BootcNode{}
	err := c.bootcREST.Get().
		Resource("bootcnodes").
		Name(name).
		Do(ctx).
		Into(bn)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting BootcNode %q: %w", name, err)
	}
	return bn, nil
}

func (c *realKubeClient) CreateBootcNode(ctx context.Context, bn *v1alpha1.BootcNode) error {
	result := &v1alpha1.BootcNode{}
	err := c.bootcREST.Post().
		Resource("bootcnodes").
		Body(bn).
		Do(ctx).
		Into(result)
	if err != nil {
		return fmt.Errorf("creating BootcNode %q: %w", bn.Name, err)
	}
	// Update the passed-in object with server-set fields (resourceVersion, etc.)
	*bn = *result
	return nil
}

func (c *realKubeClient) UpdateBootcNode(ctx context.Context, bn *v1alpha1.BootcNode) error {
	result := &v1alpha1.BootcNode{}
	err := c.bootcREST.Put().
		Resource("bootcnodes").
		Name(bn.Name).
		Body(bn).
		Do(ctx).
		Into(result)
	if err != nil {
		return fmt.Errorf("updating BootcNode %q: %w", bn.Name, err)
	}
	*bn = *result
	return nil
}

func (c *realKubeClient) UpdateBootcNodeStatus(ctx context.Context, bn *v1alpha1.BootcNode) error {
	result := &v1alpha1.BootcNode{}
	err := c.bootcREST.Put().
		Resource("bootcnodes").
		SubResource("status").
		Name(bn.Name).
		Body(bn).
		Do(ctx).
		Into(result)
	if err != nil {
		return fmt.Errorf("updating BootcNode %q status: %w", bn.Name, err)
	}
	*bn = *result
	return nil
}

func (c *realKubeClient) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	node, err := c.coreClient.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Node %q: %w", name, err)
	}
	return node, nil
}
