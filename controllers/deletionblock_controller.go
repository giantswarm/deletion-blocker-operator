/*
Copyright 2022.

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

package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/giantswarm/deletion-blocker-operator/pkg/key"
	"github.com/giantswarm/microerror"

	corev1alpha1 "github.com/giantswarm/deletion-blocker-operator/api/v1alpha1"
)

const (
	reconcileTimeout = 1 * time.Minute
)

// DeletionBlockReconciler reconciles a DeletionBlock object
type DeletionBlockReconciler struct {
	mgr manager.Manager
	client.Client
	Scheme   *runtime.Scheme
	blockers map[string]context.CancelFunc
}

//+kubebuilder:rbac:groups=core.giantswarm.io,resources=deletionblocks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.giantswarm.io,resources=deletionblocks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.giantswarm.io,resources=deletionblocks/finalizers,verbs=update

func (r *DeletionBlockReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("name", req.Name, "namespace", req.Namespace)
	logger.Info("Reconciling")

	var deletionBlock corev1alpha1.DeletionBlock
	err := r.Get(ctx, req.NamespacedName, &deletionBlock)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, microerror.Mask(err)
	}

	if !deletionBlock.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &deletionBlock, logger)
	}

	return r.reconcileNormal(ctx, &deletionBlock, logger)
}

func (r *DeletionBlockReconciler) reconcileNormal(ctx context.Context, deletionBlock *corev1alpha1.DeletionBlock, logger logr.Logger) (ctrl.Result, error) {
	// If the managed resource doesn't have the finalizer, add it.
	if !controllerutil.ContainsFinalizer(deletionBlock, key.DeletionBlockerFinalizerName) {
		controllerutil.AddFinalizer(deletionBlock, key.DeletionBlockerFinalizerName)
		if err := r.Client.Update(ctx, deletionBlock); err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
	}

	if _, exist := r.blockers[getUniqueName(deletionBlock)]; exist {
		// DeletionBlock specs are immutable.
		// A controller is already spawned for this CR. Nothing to do.
		return reconcile.Result{}, nil
	}

	if err := r.spawnRuleController(deletionBlock, logger); err != nil {
		logger.Info("cannot spawn a new controller", "error", err)
		return reconcile.Result{}, errors.Wrap(err, "cannot spawn a new controller")
	}

	return ctrl.Result{}, nil
}

func (r *DeletionBlockReconciler) reconcileDelete(ctx context.Context, deletionBlock *corev1alpha1.DeletionBlock, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(deletionBlock, key.DeletionBlockerFinalizerName) {
		return ctrl.Result{}, nil
	}

	if cancel, exist := r.blockers[getUniqueName(deletionBlock)]; exist {
		// Shut down the spawned controller
		cancel()
		delete(r.blockers, getUniqueName(deletionBlock))
	}

	managedList := &unstructured.UnstructuredList{}
	managedList.SetGroupVersionKind(deletionBlock.Spec.Rule.Managed.GetSchemaGroupVersionKind())
	if err := r.Client.List(ctx, managedList, &client.ListOptions{}); err != nil {
		return reconcile.Result{}, microerror.Mask(err)
	}

	uniqueFinalizer := getFinalizerNameWithHash(deletionBlock)
	for _, managed := range managedList.Items {
		controllerutil.RemoveFinalizer(&managed, uniqueFinalizer)
		if err := r.Client.Update(ctx, &managed); err != nil {
			logger.Info("cannot remove finalizer", "error", err)
			return reconcile.Result{}, errors.Wrap(err, "cannot remove finalizer")
		}
	}

	logger.Info("Removing finalizer.")
	controllerutil.RemoveFinalizer(deletionBlock, key.DeletionBlockerFinalizerName)
	if err := r.Client.Update(ctx, deletionBlock); err != nil {
		logger.Info("cannot remove finalizer", "error", err)
		return reconcile.Result{}, errors.Wrap(err, "cannot remove finalizer")
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeletionBlockReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.mgr = mgr
	r.blockers = make(map[string]context.CancelFunc)
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.DeletionBlock{}).
		Complete(r)
}

func (r *DeletionBlockReconciler) spawnRuleController(deletionBlock *corev1alpha1.DeletionBlock, l logr.Logger) error {
	options := controller.Options{
		Reconciler: &RuleReconciler{
			client:            r.mgr.GetClient(),
			log:               l,
			DeletionBlockRule: deletionBlock.Spec.Rule,
			Finalizer:         getFinalizerNameWithHash(deletionBlock),
		},
	}

	c, err := controller.NewUnmanaged("deletionrule/"+getUniqueName(deletionBlock), r.mgr, options)
	if err != nil {
		l.Error(err, "unable to create controller")
		return err
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(deletionBlock.Spec.Rule.Managed.GetSchemaGroupVersionKind())
	if err := c.Watch(&source.Kind{Type: u}, &handler.EnqueueRequestForObject{}); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-r.mgr.Elected()
		if err := c.Start(ctx); err != nil {
			l.Info("cannot spawn a controller", "error", err)
		}
	}()

	r.blockers[getUniqueName(deletionBlock)] = cancel
	return nil
}

func getUniqueName(block *corev1alpha1.DeletionBlock) string {
	return block.Namespace + "/" + block.Name
}

func getFinalizerNameWithHash(block *corev1alpha1.DeletionBlock) string {
	hash := sha256.Sum256([]byte(getUniqueName(block)))
	suffix := string(hash[0:4])
	return fmt.Sprintf("%s.%x", key.DeletionBlockerFinalizerName, suffix)
}
