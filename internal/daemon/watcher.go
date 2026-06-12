// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const (
	// ostree backend
	DefaultPrimaryPath = "/proc/1/root/ostree/bootc"
	// composefs backend
	DefaultFallbackPath = "/proc/1/root/sysroot/state/deploy"
)

type StatusWatcher struct {
	PollInterval time.Duration
	PrimaryPath  string
	FallbackPath string
	Events       chan event.GenericEvent
	NodeName     string
	Ready        chan struct{}
}

func (w *StatusWatcher) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("status-watcher")

	watchPath := w.resolveWatchPath()

	fsWatcher := w.setupFsnotify(log, watchPath)

	if fsWatcher != nil {
		defer func() { _ = fsWatcher.Close() }()
	}

	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()

	if w.Ready != nil {
		close(w.Ready)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.fsEvents(fsWatcher):
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Chmod) {
				log.V(1).Info("Detected bootc status change via fsnotify")
				w.sendEvent()
			}
		case err, ok := <-w.fsErrors(fsWatcher):
			if !ok {
				return nil
			}
			log.Error(err, "fsnotify error")
		case <-ticker.C:
			log.V(1).Info("Polling bootc status")
			w.sendEvent()
		}
	}
}

func (w *StatusWatcher) setupFsnotify(log logr.Logger, watchPath string) *fsnotify.Watcher {
	if watchPath == "" {
		log.Info("No bootc status path found, using polling only")
		return nil
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error(err, "Failed to create fsnotify watcher, falling back to polling")
		return nil
	}

	if err := fsWatcher.Add(watchPath); err != nil {
		log.Error(err, "Failed to watch path, falling back to polling", "path", watchPath)
		_ = fsWatcher.Close()
		return nil
	}

	log.Info("Watching path for bootc status changes", "path", watchPath)
	return fsWatcher
}

func (w *StatusWatcher) resolveWatchPath() string {
	if _, err := os.Stat(w.PrimaryPath); err == nil {
		return w.PrimaryPath
	}
	if _, err := os.Stat(w.FallbackPath); err == nil {
		return w.FallbackPath
	}
	return ""
}

func (w *StatusWatcher) sendEvent() {
	ev := event.GenericEvent{
		Object: &bootcv1alpha1.BootcNode{
			ObjectMeta: metav1.ObjectMeta{Name: w.NodeName},
		},
	}
	select {
	case w.Events <- ev:
	default:
	}
}

func (w *StatusWatcher) fsEvents(watcher *fsnotify.Watcher) <-chan fsnotify.Event {
	if watcher == nil {
		return nil
	}
	return watcher.Events
}

func (w *StatusWatcher) fsErrors(watcher *fsnotify.Watcher) <-chan error {
	if watcher == nil {
		return nil
	}
	return watcher.Errors
}
