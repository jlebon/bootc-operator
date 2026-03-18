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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	v1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
)

// kubeClient implements KubeClient using client-go REST clients. It
// uses a typed client for Node operations and a REST client for
// BootcNode operations (since BootcNode is a custom resource).
type kubeClient struct {
	clientset  kubernetes.Interface
	bootcREST  rest.Interface
	coreClient kubernetes.Interface
}

// NewKubeClient creates a KubeClient from a rest.Config. It sets up
// both a standard clientset (for Node operations) and a REST client
// configured for the bootc.dev/v1alpha1 API group (for BootcNode
// operations).
func NewKubeClient(config *rest.Config) (KubeClient, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding bootc scheme: %w", err)
	}

	crdConfig := rest.CopyConfig(config)
	crdConfig.GroupVersion = &v1alpha1.GroupVersion
	crdConfig.APIPath = "/apis"
	crdConfig.NegotiatedSerializer = serializer.NewCodecFactory(scheme)

	bootcREST, err := rest.RESTClientFor(crdConfig)
	if err != nil {
		return nil, fmt.Errorf("creating bootc REST client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes clientset: %w", err)
	}

	return &kubeClient{
		clientset:  clientset,
		bootcREST:  bootcREST,
		coreClient: clientset,
	}, nil
}

func (c *kubeClient) GetBootcNode(ctx context.Context, name string) (*v1alpha1.BootcNode, error) {
	result := &v1alpha1.BootcNode{}
	err := c.bootcREST.Get().
		Resource("bootcnodes").
		Name(name).
		Do(ctx).
		Into(result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *kubeClient) CreateBootcNode(ctx context.Context, node *v1alpha1.BootcNode) (*v1alpha1.BootcNode, error) {
	result := &v1alpha1.BootcNode{}
	err := c.bootcREST.Post().
		Resource("bootcnodes").
		Body(node).
		Do(ctx).
		Into(result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *kubeClient) UpdateBootcNodeStatus(ctx context.Context, node *v1alpha1.BootcNode) (*v1alpha1.BootcNode, error) {
	result := &v1alpha1.BootcNode{}
	err := c.bootcREST.Put().
		Resource("bootcnodes").
		Name(node.Name).
		SubResource("status").
		Body(node).
		Do(ctx).
		Into(result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *kubeClient) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	return c.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
}
