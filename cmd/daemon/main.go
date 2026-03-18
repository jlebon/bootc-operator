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

	"k8s.io/klog/v2"
)

func main() {
	var nodeName string
	var pollInterval int
	var kubeconfig string

	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "The name of the node this daemon is running on (defaults to NODE_NAME env var)")
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

	// TODO: Initialize client-go rest.Config (in-cluster or from kubeconfig)
	// TODO: Initialize bootc client
	// TODO: Check if host is a bootc system
	// TODO: Create BootcNode CRD if it doesn't exist
	// TODO: Start poll loop

	<-ctx.Done()
	klog.InfoS("Shutting down bootc-daemon")
}
