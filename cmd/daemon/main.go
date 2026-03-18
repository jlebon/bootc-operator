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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/jlebon/bootc-operator/internal/daemon"
	"github.com/jlebon/bootc-operator/pkg/bootc"
)

func main() {
	var nodeName string
	var pollInterval int
	var kubeconfig string

	flag.StringVar(
		&nodeName, "node-name", os.Getenv("NODE_NAME"),
		"The name of the node this daemon is running on (defaults to NODE_NAME env var)",
	)
	flag.IntVar(&pollInterval, "poll-interval", 30, "How often (in seconds) to poll the BootcNode CRD")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig (for out-of-cluster development)")
	klog.InitFlags(nil)
	flag.Parse()

	if nodeName == "" {
		fmt.Fprintf(os.Stderr, "Error: --node-name or NODE_NAME env var must be set\n")
		os.Exit(1)
	}

	klog.InfoS("Starting bootc-daemon", "nodeName", nodeName, "pollInterval", pollInterval)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Initialize client-go rest.Config (in-cluster or from kubeconfig).
	config, err := buildConfig(kubeconfig)
	if err != nil {
		klog.ErrorS(err, "Failed to build Kubernetes client config")
		os.Exit(1)
	}

	// Initialize Kubernetes client for BootcNode and Node operations.
	kubeClient, err := daemon.NewKubeClient(config)
	if err != nil {
		klog.ErrorS(err, "Failed to create Kubernetes client")
		os.Exit(1)
	}

	// Initialize bootc client (uses nsenter to execute commands on the host).
	bootcClient := bootc.NewClient()

	// Create and run the daemon.
	d := daemon.NewDaemon(
		nodeName,
		time.Duration(pollInterval)*time.Second,
		kubeClient,
		bootcClient,
	)

	if err := d.Run(ctx); err != nil {
		klog.ErrorS(err, "Daemon exited with error")
		os.Exit(1)
	}

	klog.InfoS("Shutting down bootc-daemon")
}

// buildConfig creates a rest.Config from the given kubeconfig path, or
// falls back to in-cluster config if kubeconfig is empty.
func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}
