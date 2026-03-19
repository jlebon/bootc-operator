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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/jlebon/bootc-operator/internal/daemon"
	"github.com/jlebon/bootc-operator/pkg/bootc"
)

func main() {
	var nodeName string
	var pollInterval int

	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"),
		"Name of the Kubernetes node this daemon runs on (default: $NODE_NAME)")
	flag.IntVar(&pollInterval, "poll-interval", 30,
		"Interval in seconds between BootcNode polls")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("daemon")

	if nodeName == "" {
		fmt.Fprintln(os.Stderr, "Error: --node-name or NODE_NAME env var must be set")
		os.Exit(1)
	}

	log.Info("Starting bootc-daemon", "node", nodeName, "pollInterval", pollInterval)

	// Set up in-cluster Kubernetes client.
	config := ctrl.GetConfigOrDie()
	kubeClient, err := daemon.NewKubeClient(config)
	if err != nil {
		log.Error(err, "Failed to create Kubernetes client")
		os.Exit(1)
	}

	// Create the bootc client (executes commands via chroot into the
	// host rootfs at /run/rootfs).
	bootcClient := bootc.NewClient()

	// Create the daemon and run it.
	d := daemon.NewDaemon(
		nodeName,
		time.Duration(pollInterval)*time.Second,
		kubeClient,
		bootcClient,
		log,
	)

	// Set up context with signal handling for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("Received signal, shutting down", "signal", sig)
		cancel()
	}()

	if err := d.Run(ctx); err != nil {
		log.Error(err, "Daemon failed")
		os.Exit(1)
	}
}
