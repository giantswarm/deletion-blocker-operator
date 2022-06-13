package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/giantswarm/deletion-blocker-operator/pkg/domain"
	"github.com/giantswarm/microerror"
)

type RuleReconciler struct {
	client client.Client
	log    logr.Logger

	DeletionBlockRule domain.DeletionBlockRule
	Finalizer         string
}

func (r *RuleReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger = r.log.WithValues("name", req.Name, "namespace", req.Namespace)
	logger.Info("Reconciling")

	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	managed := &unstructured.Unstructured{}
	managed.SetGroupVersionKind(r.DeletionBlockRule.Managed.GetSchemaGroupVersionKind())
	if err := r.client.Get(ctx, req.NamespacedName, managed); err != nil {
		return reconcile.Result{}, errors.Wrap(IgnoreNotFound(err), "cannot get Rule ")
	}

	// Handle deleted clusters
	if !managed.GetDeletionTimestamp().IsZero() {
		return r.reconcileDelete(ctx, logger, managed)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, logger, managed)
}

func (r *RuleReconciler) reconcileNormal(ctx context.Context, logger logr.Logger, managed *unstructured.Unstructured) (reconcile.Result, error) {
	// If the managed resource doesn't have the finalizer, add it.
	if !controllerutil.ContainsFinalizer(managed, r.Finalizer) {
		controllerutil.AddFinalizer(managed, r.Finalizer)
		if err := r.client.Update(ctx, managed); err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
	}

	return reconcile.Result{}, nil
}

func (r *RuleReconciler) reconcileDelete(ctx context.Context, logger logr.Logger, managed *unstructured.Unstructured) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(managed, r.Finalizer) {
		return ctrl.Result{}, nil
	}

	dependents := &unstructured.UnstructuredList{}
	dependents.SetGroupVersionKind(r.DeletionBlockRule.Dependent.GetSchemaGroupVersionKind())
	if err := r.client.List(ctx, dependents, &client.ListOptions{Namespace: managed.GetNamespace()}); err != nil {
		return reconcile.Result{}, microerror.Mask(IgnoreNotFound(err))
	}

	allowed, err := r.DeletionBlockRule.CheckIsDeletionAllowed(*managed, *dependents)
	if err != nil {
		return reconcile.Result{}, microerror.Mask(IgnoreNotFound(err))
	}
	logger.Info("Deletion is", "allowed:", allowed)
	if allowed {
		logger.Info("We can delete the finalizer. Removing finalizer")
		controllerutil.RemoveFinalizer(managed, r.Finalizer)
		if err := r.client.Update(ctx, managed); err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
		return ctrl.Result{}, nil
	} else {
		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

}

// IgnoreNotFound returns nil on NotFound errors.
// All other values that are not NotFound errors or nil are returned unmodified.
func IgnoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
