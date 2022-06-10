package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/giantswarm/deletion-blocker-operator/pkg/domain"
	"github.com/giantswarm/deletion-blocker-operator/pkg/key"
	"github.com/giantswarm/microerror"
)

type RuleReconciler struct {
	client client.Client
	log    logr.Logger

	DeletionBlockRule domain.DeletionBlockRule
}

func (r *RuleReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.log.WithValues("request", req)
	log.V(1).Info("Debugging")

	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	managed := &unstructured.Unstructured{}
	managed.SetGroupVersionKind(r.DeletionBlockRule.Managed.GetSchemaGroupVersionKind())
	if err := r.client.Get(ctx, req.NamespacedName, managed); err != nil {
		return reconcile.Result{}, errors.Wrap(IgnoreNotFound(err), "cannot get Rule ")
	}

	// Handle deleted clusters
	if !managed.GetDeletionTimestamp().IsZero() {
		return r.reconcileDelete(ctx, log, managed)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, log, managed)
}

func (r *RuleReconciler) reconcileNormal(ctx context.Context, log logr.Logger, managed *unstructured.Unstructured) (reconcile.Result, error) {
	// If the managed resource doesn't have the finalizer, add it.
	if !controllerutil.ContainsFinalizer(managed, key.DeletionBlockerFinalizerName) {
		controllerutil.AddFinalizer(managed, key.DeletionBlockerFinalizerName)
		if err := r.client.Update(ctx, managed); err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
	}

	return reconcile.Result{}, nil
}

func (r *RuleReconciler) reconcileDelete(ctx context.Context, log logr.Logger, managed *unstructured.Unstructured) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(managed, key.DeletionBlockerFinalizerName) {
		return ctrl.Result{}, nil
	}

	dependents := &unstructured.UnstructuredList{}
	dependents.SetGroupVersionKind(r.DeletionBlockRule.Dependent.GetSchemaGroupVersionKind())
	if err := r.client.List(ctx, dependents, &client.ListOptions{Namespace: managed.GetNamespace()}); err != nil {
		return reconcile.Result{}, errors.Wrap(IgnoreNotFound(err), "cannot get dependents ")
	}

	if r.DeletionBlockRule.CheckIsDeletionAllowed(*managed, *dependents) {
		log.Info("We can delete the finalizer. Removing finalizer")
		controllerutil.RemoveFinalizer(managed, key.DeletionBlockerFinalizerName)
		// Finally remove the finalizer
		if err := r.client.Update(ctx, managed); err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
	}

	return ctrl.Result{}, nil
}

// IgnoreNotFound returns nil on NotFound errors.
// All other values that are not NotFound errors or nil are returned unmodified.
func IgnoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
