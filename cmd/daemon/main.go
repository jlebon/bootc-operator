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
	"os"
	"os/signal"
	"syscall"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	var nodeName string
	var pollInterval time.Duration

	flag.StringVar(&nodeName, "node-name", "", "Name of the Kubernetes node this daemon runs on")
	flag.DurationVar(&pollInterval, "poll-interval", 30*time.Second, "Interval for polling bootc status")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	log := zap.New(zap.UseFlagOptions(&opts))
	ctrl.SetLogger(log)
	setupLog := log.WithName("setup")

	if nodeName == "" {
		nodeName = os.Getenv("NODE_NAME")
	}
	if nodeName == "" {
		setupLog.Error(nil, "node-name is required (flag or NODE_NAME env)")
		os.Exit(1)
	}

	setupLog.Info("Starting daemon", "node", nodeName, "pollInterval", pollInterval)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	setupLog.Info("Shutting down")
}
