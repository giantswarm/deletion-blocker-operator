//go:build integration

package controllers

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/giantswarm/deletion-blocker-operator/pkg/rules"
)

// These tests run a real manager, so they cover SetupWithManager: the
// legacyFinalizer wiring and the watch built from an unstructured For(). Both
// have real failure modes that a direct Reconcile call cannot reach.
//
// They are deliberately few. Assertions here need Eventually or Consistently, so
// they are slower and have more flake surface than the direct tests.

const (
	eventuallyTimeout  = 30 * time.Second
	eventuallyInterval = 250 * time.Millisecond
)

// startManager runs a manager for one rule and returns the reconciler it
// registered. The manager stops when the test finishes.
func startManager(t *testing.T, rule rules.DeletionBlock) *RuleReconciler {
	t.Helper()
	g := NewWithT(t)

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: testScheme,
		// "0" disables the listener. Without this, concurrent tests fight over the
		// default ports.
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// controller-runtime derives the controller name from
		// strings.ToLower(managedKind) and keeps the set of used names in a package
		// global that is never reset. Two managers in one test binary that watch the
		// same managed Kind would collide. TestSetupWithManager_RejectsDuplicate
		// asserts that collision on purpose, so it does not set this.
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	g.Expect(err).NotTo(HaveOccurred())

	r := &RuleReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		DeletionBlockRule: rule,
	}
	g.Expect(r.SetupWithManager(mgr)).To(Succeed())

	// SetupWithManager owns the legacy finalizer wiring. If a refactor drops that
	// line, legacy finalizers silently stop being cleaned up in production.
	g.Expect(r.legacyFinalizer).NotTo(BeEmpty())
	g.Expect(r.legacyFinalizer).To(Equal(buildUniqueFinalizer(rule)))

	ctx, cancel := context.WithCancel(context.Background())

	// A buffered channel, not a shared variable: -race flags the latter as a data
	// race between the manager goroutine and the test.
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()

	t.Cleanup(func() {
		cancel()
		NewWithT(t).Eventually(errCh, eventuallyTimeout).Should(Receive(BeNil()))
	})

	g.Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

	return r
}

// TestManager_AddsFinalizerViaWatch proves that For() on a bare unstructured
// object produces a working watch for a runtime supplied GVK.
func TestManager_AddsFinalizerViaWatch(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	ns := newNamespace(t)

	startManager(t, rule)

	create(t, newObj(rule.Managed, ns, "kct-a", nil))

	g.Eventually(func() []string {
		got, err := get(rule.Managed, ns, "kct-a")
		if err != nil {
			return nil
		}
		return got.GetFinalizers()
	}, eventuallyTimeout, eventuallyInterval).Should(ConsistOf(Finalizer))
}

// TestManager_DependentRemovalDoesNotTriggerReconcile asserts a real
// characteristic of this operator rather than a bug in the test.
//
// SetupWithManager registers For() on the managed kind only. There is no Watches()
// on the dependent kind, so deleting the blocking dependent produces no event. The
// managed object stays in Terminating until the one minute RequeueAfter fires.
//
// The second half nudges the object with an annotation patch, which does produce
// an event. That is what keeps this a five second test instead of a 65 second one.
// The API server forbids adding finalizers to a terminating object, but it allows
// other metadata edits.
func TestManager_DependentRemovalDoesNotTriggerReconcile(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	ns := newNamespace(t)

	startManager(t, rule)

	create(t, newObj(rule.Managed, ns, "kct-a", nil))
	g.Eventually(func() []string {
		got, err := get(rule.Managed, ns, "kct-a")
		if err != nil {
			return nil
		}
		return got.GetFinalizers()
	}, eventuallyTimeout, eventuallyInterval).Should(ConsistOf(Finalizer))

	dependent := machineSetWithBootstrapRef(rule, ns, "ms-a", "kct-a")
	create(t, dependent)

	g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

	// Wait for the blocked state to settle before removing the dependent.
	//
	// Deleting the managed object only sets a deletionTimestamp, which happens
	// server side and immediately. That is not evidence that the controller has
	// reconciled. Holding for three seconds is: a reconcile against envtest takes
	// milliseconds, so an object that still carries its finalizer after that has
	// been seen, judged blocked, and requeued for a minute. Without this wait, the
	// dependent removal below races an in-flight reconcile that would list zero
	// dependents and allow the deletion.
	g.Consistently(func() []string {
		got, err := get(rule.Managed, ns, "kct-a")
		if err != nil {
			return nil
		}
		g.Expect(got.GetDeletionTimestamp().IsZero()).To(BeFalse())
		return got.GetFinalizers()
	}, 3*time.Second, 500*time.Millisecond).Should(ConsistOf(Finalizer))

	g.Expect(k8sClient.Delete(context.Background(), dependent)).To(Succeed())

	// Removing the blocker produces no event, so nothing happens.
	g.Consistently(func() error {
		_, err := get(rule.Managed, ns, "kct-a")
		return err
	}, 5*time.Second, 500*time.Millisecond).Should(Succeed(),
		"the dependent kind is not watched, so its removal must not trigger a reconcile")

	// Any event on the managed object unblocks it.
	patch := []byte(`{"metadata":{"annotations":{"test.giantswarm.io/nudge":"1"}}}`)
	g.Expect(k8sClient.Patch(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"),
		client.RawPatch(types.MergePatchType, patch))).To(Succeed())

	g.Eventually(func() bool {
		_, err := get(rule.Managed, ns, "kct-a")
		return apierrors.IsNotFound(err)
	}, eventuallyTimeout, eventuallyInterval).Should(BeTrue())
}

// TestSetupWithManager_RejectsDuplicateManagedKind documents a real limitation.
//
// controller-runtime derives the controller name from strings.ToLower(Kind) and
// enforces global uniqueness. Two rules that manage the same kind, for example to
// block one kind on two different dependents, make main() fail at startup and
// exit 1. This test needs no running manager.
//
// It deliberately leaves SkipNameValidation unset, and it uses the
// OpenStackMachineTemplate kind so that it does not poison the never-reset global
// name set for the KubeadmConfigTemplate tests above.
func TestSetupWithManager_RejectsDuplicateManagedKind(t *testing.T) {
	g := NewWithT(t)
	rule := openStackMachineTemplateRule(t)

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 testScheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	g.Expect(err).NotTo(HaveOccurred())

	first := &RuleReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), DeletionBlockRule: rule}
	g.Expect(first.SetupWithManager(mgr)).To(Succeed())

	// A second rule for the same managed kind, differing only in its query.
	second := rule
	second.Query = `{{ eq .dependent.spec.template.spec.infrastructureRef.name .managed.metadata.name }} `

	r := &RuleReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), DeletionBlockRule: second}
	err = r.SetupWithManager(mgr)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("already exists"))
}
