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
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

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
	blockers map[string]chan struct{}
}

//+kubebuilder:rbac:groups=core.giantswarm.io,resources=deletionblocks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core.giantswarm.io,resources=deletionblocks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core.giantswarm.io,resources=deletionblocks/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the DeletionBlock object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
func (r *DeletionBlockReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	log := logger.WithValues("name", req.Name, "namespace", req.NamespacedName)
	log.Info("Reconciling")

	client := r.mgr.GetClient()

	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	var deletionBlock corev1alpha1.DeletionBlock
	err := r.Get(ctx, req.NamespacedName, &deletionBlock)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, microerror.Mask(err)
	}

	log.Info("CR", "CR", deletionBlock)

	log = log.WithValues(
		"managed-kind", deletionBlock.Spec.Rule.Managed.Kind,
		"managed-version", deletionBlock.Spec.Rule.Managed.Version)

	if IsBeingDeleted(deletionBlock) {
		if stop, running := r.blockers[deletionBlock.GetName()]; running {
			// Shut down the controller that watches this experiment kind.
			close(stop)
			delete(r.blockers, deletionBlock.GetName())
		}
		// TODO(erkan)
		// meta.RemoveFinalizer(e, "experiment")
		if err := client.Update(ctx, &deletionBlock); err != nil {
			log.Info("cannot remove finalizer", "error", err)
			return reconcile.Result{}, errors.Wrap(err, "cannot remove finalizer")
		}
		return reconcile.Result{}, nil
	}

	// TODO (erkan)
	// meta.AddFinalizer(e, "experiment")
	if err := client.Update(ctx, &deletionBlock); err != nil {
		log.Info("cannot add finalizer", "error", err)
		return reconcile.Result{}, errors.Wrap(err, "cannot add finalizer")
	}

	if _, running := r.blockers[deletionBlock.GetName()]; running {
		// For the purposes of this example we assume experiments
		// are immutable. We're already running, so there's nothing to do.
		return reconcile.Result{}, nil
	}

	stop := make(chan struct{})
	if err := runRule(r.mgr, deletionBlock, log, stop); err != nil {
		log.Info("cannot run experiment", "error", err)
		return reconcile.Result{}, errors.Wrap(err, "cannot stop experiment")
	}
	r.blockers[deletionBlock.GetName()] = stop

	return reconcile.Result{}, nil
}

func IsBeingDeleted(block corev1alpha1.DeletionBlock) bool {
	return !block.DeletionTimestamp.IsZero()
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeletionBlockReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.mgr = mgr
	r.blockers = make(map[string]chan struct{})
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.DeletionBlock{}).
		Complete(r)
}

func runRule(m ctrl.Manager, deletionBlock corev1alpha1.DeletionBlock, l logr.Logger, stop <-chan struct{}) error {
	o := controller.Options{
		Reconciler: &RuleReconciler{client: m.GetClient(), log: l, DeletionBlockRule: deletionBlock.Spec.Rule},
	}

	managed := deletionBlock.Spec.Rule.Managed
	c, err := controller.New("deletionrule/"+managed.Kind, m, o)
	if err != nil {
		return err
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(managed.GetSchemaGroupVersionKind())
	if err := c.Watch(&source.Kind{Type: u}, &handler.EnqueueRequestForObject{}); err != nil {
		return err
	}

	go func() {
		<-m.Elected()
		if err := c.Start(context.TODO()); err != nil {
			l.Info("cannot run experiment controller", "error", err)
		}
	}()

	return nil
}
