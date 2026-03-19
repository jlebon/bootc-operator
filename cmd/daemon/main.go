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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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

	// Wait for signal to exit
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	log.Info("Received signal, shutting down", "signal", sig)
}
