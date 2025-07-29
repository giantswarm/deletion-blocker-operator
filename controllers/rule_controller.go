package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/ghodss/yaml"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/giantswarm/microerror"

	"github.com/giantswarm/deletion-blocker-operator/pkg/rules"
)

type RuleReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	DeletionBlockRule rules.DeletionBlock
	Finalizer         string
}

const finalizerPrefix = "deletion-blocker-operator.finalizers.giantswarm.io"

func (r *RuleReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("name", req.Name, "namespace", req.Namespace)
	logger.Info("Reconciling")

	managed := &unstructured.Unstructured{}
	managed.SetGroupVersionKind(r.DeletionBlockRule.Managed.GetSchemaGroupVersionKind())
	if err := r.Get(ctx, req.NamespacedName, managed); err != nil {
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
		if err := r.Update(ctx, managed); err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
	}

	// nothing other than adding finalizer
	return reconcile.Result{}, nil
}

func (r *RuleReconciler) reconcileDelete(ctx context.Context, logger logr.Logger, managed *unstructured.Unstructured) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(managed, r.Finalizer) {
		return ctrl.Result{}, nil
	}

	dependents := &unstructured.UnstructuredList{}
	dependents.SetGroupVersionKind(r.DeletionBlockRule.Dependent.GetSchemaGroupVersionKind())
	if err := r.List(ctx, dependents, &client.ListOptions{Namespace: managed.GetNamespace()}); err != nil {
		return reconcile.Result{}, microerror.Mask(IgnoreNotFound(err))
	}

	allowed, err := r.DeletionBlockRule.CheckIsDeletionAllowed(*managed, *dependents)
	if err != nil {
		return reconcile.Result{}, microerror.Mask(IgnoreNotFound(err))
	}
	if allowed {
		logger.Info("Deletion is allowed. Removing the finalizer.")
		controllerutil.RemoveFinalizer(managed, r.Finalizer)
		if err := r.Update(ctx, managed); err != nil {
			return reconcile.Result{}, microerror.Mask(err)
		}
		return ctrl.Result{}, nil
	} else {
		logger.Info("Deletion is not allowed.")
		return ctrl.Result{RequeueAfter: time.Minute * 1}, nil
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

// SetupWithManager sets up the controller with the Manager.
func (r *RuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Finalizer = buildUniqueFinalizer(r.DeletionBlockRule)
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(r.DeletionBlockRule.Managed.GetSchemaGroupVersionKind())
	return ctrl.NewControllerManagedBy(mgr).
		For(u).
		Complete(r)
}

func buildUniqueFinalizer(rule rules.DeletionBlock) string {
	ruleAsYaml, _ := yaml.Marshal(rule)
	hash := sha256.Sum256(ruleAsYaml)
	suffix := string(hash[0:4])
	return fmt.Sprintf("%s.%x", finalizerPrefix, suffix)
}
