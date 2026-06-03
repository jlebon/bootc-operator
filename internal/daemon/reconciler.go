// SPDX-License-Identifier: Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	bootcv1alpha1 "github.com/jlebon/bootc-operator/api/v1alpha1"
	"github.com/jlebon/bootc-operator/internal/bootc"
)

// switchOp tracks the state of an in-flight bootc switch operation.
type switchOp struct {
	mu     sync.Mutex
	image  string
	cancel context.CancelFunc
	err    error
}

// BootcNodeReconciler reconciles the BootcNode for the node this daemon
// runs on. It reads bootc status from the host, detects image mismatches,
// and drives updates via bootc switch.
type BootcNodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	NodeName string
	Executor bootc.Executor

	inflight      switchOp
	switchDone    chan event.GenericEvent
	StatusChanged chan event.GenericEvent
}

func (r *BootcNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.switchDone = make(chan event.GenericEvent, 1)

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&bootcv1alpha1.BootcNode{}).
		WatchesRawSource(source.Channel(r.switchDone, &handler.EnqueueRequestForObject{})).
		Named("bootcnode")

	if r.StatusChanged != nil {
		builder = builder.WatchesRawSource(source.Channel(r.StatusChanged, &handler.EnqueueRequestForObject{}))
	}

	return builder.Complete(r)
}

func (r *BootcNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("node", r.NodeName)

	if req.Name != r.NodeName {
		return ctrl.Result{}, nil
	}

	var bn bootcv1alpha1.BootcNode
	if err := r.Get(ctx, req.NamespacedName, &bn); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("BootcNode not found, waiting for controller to create it")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching BootcNode: %w", err)
	}

	specChanged := bn.Generation > bn.Status.ObservedGeneration
	orig := bn.DeepCopy()
	patch := client.MergeFrom(orig)
	bn.Status.ObservedGeneration = bn.Generation

	result, reconcileErr := r.reconcileBootcNode(ctx, &bn, specChanged)

	if !reflect.DeepEqual(bn.Status, orig.Status) {
		if patchErr := r.Status().Patch(ctx, &bn, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patching BootcNode status: %w", patchErr)
		}
	}

	return result, reconcileErr
}

func (r *BootcNodeReconciler) reconcileBootcNode(ctx context.Context, bn *bootcv1alpha1.BootcNode, specChanged bool) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("node", r.NodeName)

	if err := r.populateBootcFields(ctx, bn); err != nil {
		log.Error(err, "Failed to populate bootc status")
		apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
			Type:               bootcv1alpha1.NodeDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             bootcv1alpha1.NodeReasonError,
			Message:            fmt.Sprintf("failed to get bootc status: %v", err),
			ObservedGeneration: bn.Generation,
		})
		return ctrl.Result{}, fmt.Errorf("populating bootc fields: %w", err)
	}

	if bn.Status.Booted == nil {
		apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
			Type:               bootcv1alpha1.NodeDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             bootcv1alpha1.NodeReasonError,
			Message:            "bootc status has no booted entry",
			ObservedGeneration: bn.Generation,
		})
		return ctrl.Result{}, fmt.Errorf("bootc status has no booted entry")
	}

	// Node is idle
	if !imageNeedsUpdate(bn.Spec.DesiredImage, bn.Status.Booted.ImageDigest) {
		apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
			Type:               bootcv1alpha1.NodeIdle,
			Status:             metav1.ConditionTrue,
			Reason:             bootcv1alpha1.NodeReasonIdle,
			ObservedGeneration: bn.Generation,
		})
		apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
			Type:               bootcv1alpha1.NodeDegraded,
			Status:             metav1.ConditionFalse,
			Reason:             bootcv1alpha1.NodeReasonHealthy,
			ObservedGeneration: bn.Generation,
		})
		return ctrl.Result{}, nil
	}

	switchErr := r.inflight.takeErr()

	if switchErr != nil {
		apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
			Type:               bootcv1alpha1.NodeIdle,
			Status:             metav1.ConditionTrue,
			Reason:             bootcv1alpha1.NodeReasonIdle,
			ObservedGeneration: bn.Generation,
		})
		apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
			Type:               bootcv1alpha1.NodeDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             bootcv1alpha1.NodeReasonError,
			Message:            fmt.Sprintf("bootc switch failed: %v", switchErr),
			ObservedGeneration: bn.Generation,
		})
		// Requeue with a delay to retry transient failures (e.g. network
		// blips, registry timeouts) without hammering the registry.
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	reboot := bn.Spec.DesiredImageState == bootcv1alpha1.DesiredImageStateBooted
	desiredImage := bn.Spec.DesiredImage

	if skip := r.inflight.acquire(log, desiredImage); skip {
		return ctrl.Result{}, nil
	}

	_, desiredDigest, _ := strings.Cut(desiredImage, "@")
	alreadyStaged := bn.Status.Staged != nil && bn.Status.Staged.ImageDigest == desiredDigest

	idleCond := apimeta.FindStatusCondition(bn.Status.Conditions, bootcv1alpha1.NodeIdle)

	// We always transition through Staged before Rebooting. Without this,
	// a fast controller can drain and set DesiredImageState=Booted before
	// we reconcile after staging completes, causing us to skip Staged.
	var reason string
	switch {
	case !alreadyStaged:
		reason = bootcv1alpha1.NodeReasonStaging
		switchCtx, cancel := context.WithCancel(context.Background())
		r.inflight.start(desiredImage, cancel)
		log.Info("Starting staging", "image", desiredImage)
		go r.inflight.run(switchCtx, r.NodeName, desiredImage, r.Executor, r.switchDone)

	case reboot && idleCond != nil && (idleCond.Reason == bootcv1alpha1.NodeReasonStaged || idleCond.Reason == bootcv1alpha1.NodeReasonRebooting):
		reason = bootcv1alpha1.NodeReasonRebooting
		if idleCond.Reason == bootcv1alpha1.NodeReasonStaged {
			log.Info("Starting reboot", "image", desiredImage)
			if err := r.Executor.Reboot(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("reboot: %w", err)
			}
		}

	default:
		reason = bootcv1alpha1.NodeReasonStaged
		log.Info("Image staged", "image", desiredImage)
	}

	apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
		Type:               bootcv1alpha1.NodeIdle,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		ObservedGeneration: bn.Generation,
	})
	apimeta.SetStatusCondition(&bn.Status.Conditions, metav1.Condition{
		Type:               bootcv1alpha1.NodeDegraded,
		Status:             metav1.ConditionFalse,
		Reason:             bootcv1alpha1.NodeReasonHealthy,
		ObservedGeneration: bn.Generation,
	})

	return ctrl.Result{}, nil
}

func (s *switchOp) takeErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.err
	s.err = nil
	return err
}

// acquire checks whether a switch operation is already in flight.
// It returns true if the reconciler should skip (operation in progress),
// or false after cancelling any stale in-flight switch.
func (s *switchOp) acquire(log logr.Logger, image string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.image == image {
		log.Info("Switch already in progress for this image", "image", image)
		return true
	}
	if s.cancel != nil {
		log.Info("Cancelling in-flight switch", "old", s.image, "new", image)
		s.cancel()
		s.image = ""
		s.cancel = nil
	}
	return false
}

func (s *switchOp) start(image string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.image = image
	s.cancel = cancel
	s.err = nil
}

func (s *switchOp) run(ctx context.Context, nodeName, image string, executor bootc.Executor, done chan<- event.GenericEvent) {
	log := logf.FromContext(context.Background()).WithValues("node", nodeName, "image", image)

	err := executor.Switch(ctx, image)

	s.mu.Lock()
	if ctx.Err() != nil {
		log.Info("Switch cancelled")
	} else if err != nil {
		log.Error(err, "Switch failed")
		s.err = err
	}
	s.image = ""
	s.cancel = nil
	s.mu.Unlock()

	if ctx.Err() == nil {
		done <- event.GenericEvent{
			Object: &bootcv1alpha1.BootcNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
			},
		}
	}
}

func (r *BootcNodeReconciler) populateBootcFields(ctx context.Context, bn *bootcv1alpha1.BootcNode) error {
	data, err := r.Executor.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting bootc status: %w", err)
	}

	status, err := bootc.ParseStatus(data)
	if err != nil {
		return fmt.Errorf("failed to parse bootc status: %w", err)
	}

	bn.Status.Booted = convertBootEntry(status.Status.Booted)
	bn.Status.Staged = convertBootEntry(status.Status.Staged)
	bn.Status.Rollback = convertBootEntry(status.Status.Rollback)

	return nil
}

// imageNeedsUpdate compares only the digest portion of desiredImage against
// bootedDigest. It assumes upgrades always come from the same image repository.
// TODO: also compare the image repository to detect cross-image switches.
func imageNeedsUpdate(desiredImage, bootedDigest string) bool {
	_, digest, ok := strings.Cut(desiredImage, "@")
	if !ok {
		return true
	}
	return digest != bootedDigest
}

func convertBootEntry(entry *bootc.BootEntry) *bootcv1alpha1.ImageInfo {
	if entry == nil || entry.Image == nil {
		return nil
	}
	img := entry.Image

	info := &bootcv1alpha1.ImageInfo{
		Image:             img.Image.Image,
		ImageDigest:       img.ImageDigest,
		Architecture:      img.Architecture,
		Incompatible:      entry.Incompatible,
		SoftRebootCapable: entry.SoftRebootCapable,
	}

	if img.Version != nil {
		info.Version = *img.Version
	}

	if img.Timestamp != nil {
		t := metav1.NewTime(*img.Timestamp)
		info.Timestamp = &t
	}

	return info
}
