//go:build integration

package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
)

// These tests call Reconcile directly rather than through a manager. That makes
// them fully synchronous, so no Eventually is needed, and it makes the returned
// reconcile.Result assertable. The one minute requeue interval is observable only
// from that Result, and it is a first class part of this operator's behaviour.

func TestReconcile_AddsFinalizerOnCreate(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	ns := newNamespace(t)
	r := newReconciler(rule)

	create(t, newObj(rule.Managed, ns, "kct", nil))

	res, err := reconcileOnce(r, ns, "kct")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.IsZero()).To(BeTrue())

	got := mustGet(t, rule.Managed, ns, "kct")
	g.Expect(got.GetFinalizers()).To(ConsistOf(Finalizer))

	// A second pass must not issue another Update. An unchanged resourceVersion is
	// the evidence.
	before := got.GetResourceVersion()
	res, err = reconcileOnce(r, ns, "kct")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.IsZero()).To(BeTrue())
	g.Expect(mustGet(t, rule.Managed, ns, "kct").GetResourceVersion()).To(Equal(before))
}

// TestReconcile_LegacyFinalizerSuppressesNewFinalizer covers the back compatible
// path. An object that already carries the per-rule legacy finalizer must not
// also gain the static one. Creating the object also proves the API server
// accepts the legacy finalizer's generated name.
func TestReconcile_LegacyFinalizerSuppressesNewFinalizer(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	ns := newNamespace(t)
	r := newReconciler(rule)

	obj := newObj(rule.Managed, ns, "kct", nil)
	obj.SetFinalizers([]string{r.legacyFinalizer})
	create(t, obj)

	res, err := reconcileOnce(r, ns, "kct")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.IsZero()).To(BeTrue())

	g.Expect(mustGet(t, rule.Managed, ns, "kct").GetFinalizers()).To(ConsistOf(r.legacyFinalizer))
}

func TestReconcile_ObjectNotFound(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	ns := newNamespace(t)

	res, err := reconcileOnce(newReconciler(rule), ns, "does-not-exist")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.IsZero()).To(BeTrue())
}

// TestReconcile_DeletionBlockedByMatchingDependent is the core blocking case.
func TestReconcile_DeletionBlockedByMatchingDependent(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	ns := newNamespace(t)
	r := newReconciler(rule)

	create(t, newObj(rule.Managed, ns, "kct-a", nil))
	_, err := reconcileOnce(r, ns, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())

	create(t, machineSetWithBootstrapRef(rule, ns, "ms-a", "kct-a"))

	g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

	res, err := reconcileOnce(r, ns, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.RequeueAfter).To(Equal(time.Minute))

	got := mustGet(t, rule.Managed, ns, "kct-a")
	g.Expect(got.GetDeletionTimestamp().IsZero()).To(BeFalse())
	g.Expect(got.GetFinalizers()).To(ConsistOf(Finalizer))
}

// TestReconcile_DeletionAllowed_ObjectDisappears is the most important assertion
// in the suite. The API server reaps the object synchronously inside the Update
// that empties metadata.finalizers. A fake client cannot be trusted to model
// this, which is why the suite uses a real control plane.
func TestReconcile_DeletionAllowed_ObjectDisappears(t *testing.T) {
	rule := kubeadmConfigTemplateRule(t)

	cases := map[string]func(t *testing.T, ns string){
		"no dependents at all": func(*testing.T, string) {},
		"a dependent that references a different object": func(t *testing.T, ns string) {
			create(t, machineSetWithBootstrapRef(rule, ns, "ms-other", "some-other-kct"))
		},
	}

	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			ns := newNamespace(t)
			r := newReconciler(rule)

			create(t, newObj(rule.Managed, ns, "kct-a", nil))
			_, err := reconcileOnce(r, ns, "kct-a")
			g.Expect(err).NotTo(HaveOccurred())

			seed(t, ns)

			g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

			res, err := reconcileOnce(r, ns, "kct-a")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res.IsZero()).To(BeTrue())

			_, err = get(rule.Managed, ns, "kct-a")
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the object must be gone once the finalizer is removed")
		})
	}
}

// TestReconcile_DependentInDifferentNamespaceDoesNotBlock pins the namespace
// scoping in reconcileDelete. The List is restricted to the managed object's
// namespace, which is otherwise invisible and trivially broken by a refactor.
func TestReconcile_DependentInDifferentNamespaceDoesNotBlock(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	nsA := newNamespace(t)
	nsB := newNamespace(t)
	r := newReconciler(rule)

	create(t, newObj(rule.Managed, nsA, "kct-a", nil))
	_, err := reconcileOnce(r, nsA, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())

	// This MachineSet matches the query, but it lives in another namespace.
	create(t, machineSetWithBootstrapRef(rule, nsB, "ms-a", "kct-a"))

	g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, nsA, "kct-a"))).To(Succeed())

	res, err := reconcileOnce(r, nsA, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.IsZero()).To(BeTrue())

	_, err = get(rule.Managed, nsA, "kct-a")
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

// TestReconcile_FinalizerCombinationsRemoved covers the double RemoveFinalizer in
// reconcileDelete. Whichever finalizer an object carries, an allowed deletion
// must clear it.
func TestReconcile_FinalizerCombinationsRemoved(t *testing.T) {
	rule := kubeadmConfigTemplateRule(t)
	legacy := buildUniqueFinalizer(rule)

	cases := map[string][]string{
		"legacy only":     {legacy},
		"static only":     {Finalizer},
		"legacy and both": {legacy, Finalizer},
	}

	for name, finalizers := range cases {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			ns := newNamespace(t)
			r := newReconciler(rule)

			obj := newObj(rule.Managed, ns, "kct-a", nil)
			obj.SetFinalizers(finalizers)
			create(t, obj)

			g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

			res, err := reconcileOnce(r, ns, "kct-a")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res.IsZero()).To(BeTrue())

			_, err = get(rule.Managed, ns, "kct-a")
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	}
}

func TestReconcile_ManyDependents(t *testing.T) {
	rule := kubeadmConfigTemplateRule(t)

	t.Run("one match among many blocks deletion", func(t *testing.T) {
		g := NewWithT(t)
		ns := newNamespace(t)
		r := newReconciler(rule)

		create(t, newObj(rule.Managed, ns, "kct-a", nil))
		_, err := reconcileOnce(r, ns, "kct-a")
		g.Expect(err).NotTo(HaveOccurred())

		for _, ms := range []struct{ name, ref string }{
			{"ms-1", "other-1"},
			{"ms-2", "other-2"},
			{"ms-3", "kct-a"},
			{"ms-4", "other-4"},
			{"ms-5", "other-5"},
		} {
			create(t, machineSetWithBootstrapRef(rule, ns, ms.name, ms.ref))
		}

		g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

		res, err := reconcileOnce(r, ns, "kct-a")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(res.RequeueAfter).To(Equal(time.Minute))
	})

	t.Run("no match among many allows deletion", func(t *testing.T) {
		g := NewWithT(t)
		ns := newNamespace(t)
		r := newReconciler(rule)

		create(t, newObj(rule.Managed, ns, "kct-a", nil))
		_, err := reconcileOnce(r, ns, "kct-a")
		g.Expect(err).NotTo(HaveOccurred())

		for i, ref := range []string{"other-1", "other-2", "other-3", "other-4", "other-5"} {
			create(t, machineSetWithBootstrapRef(rule, ns, fmt.Sprintf("ms-%d", i), ref))
		}

		g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

		res, err := reconcileOnce(r, ns, "kct-a")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(res.IsZero()).To(BeTrue())

		_, err = get(rule.Managed, ns, "kct-a")
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
}

// TestReconcile_OpenStackMachineTemplate exercises the second shipped rule end to
// end. It proves the v1alpha5 stub resolves through the RESTMapper, and that the
// operator is genuinely GVK agnostic.
func TestReconcile_OpenStackMachineTemplate(t *testing.T) {
	rule := openStackMachineTemplateRule(t)

	t.Run("blocked by a matching dependent", func(t *testing.T) {
		g := NewWithT(t)
		ns := newNamespace(t)
		r := newReconciler(rule)

		create(t, newObj(rule.Managed, ns, "osmt-a", nil))
		_, err := reconcileOnce(r, ns, "osmt-a")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(mustGet(t, rule.Managed, ns, "osmt-a").GetFinalizers()).To(ConsistOf(Finalizer))

		create(t, machineSetWithInfrastructureRef(rule, ns, "ms-a", "osmt-a"))

		g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "osmt-a"))).To(Succeed())

		res, err := reconcileOnce(r, ns, "osmt-a")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(res.RequeueAfter).To(Equal(time.Minute))
	})

	t.Run("deleted when no dependent matches", func(t *testing.T) {
		g := NewWithT(t)
		ns := newNamespace(t)
		r := newReconciler(rule)

		create(t, newObj(rule.Managed, ns, "osmt-a", nil))
		_, err := reconcileOnce(r, ns, "osmt-a")
		g.Expect(err).NotTo(HaveOccurred())

		create(t, machineSetWithInfrastructureRef(rule, ns, "ms-a", "osmt-other"))

		g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "osmt-a"))).To(Succeed())

		res, err := reconcileOnce(r, ns, "osmt-a")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(res.IsZero()).To(BeTrue())

		_, err = get(rule.Managed, ns, "osmt-a")
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
}

// TestReconcile_QueryMustRenderExactlyTrue pins the polarity in
// rules.CheckIsDependent, which compares the rendered template against the
// literal string "true".
//
// The consequence is a footgun. A query written as a YAML block scalar
// (`query: |`) gains a trailing newline, renders as "true\n", and becomes a rule
// that blocks nothing. Nothing logs a warning.
func TestReconcile_QueryMustRenderExactlyTrue(t *testing.T) {
	g := NewWithT(t)
	shipped := kubeadmConfigTemplateRule(t)

	rule := shipped
	rule.Query = shipped.Query + "\n"

	ns := newNamespace(t)
	r := newReconciler(rule)

	create(t, newObj(rule.Managed, ns, "kct-a", nil))
	_, err := reconcileOnce(r, ns, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())

	// This MachineSet matches. With the shipped query it would block deletion.
	create(t, machineSetWithBootstrapRef(rule, ns, "ms-a", "kct-a"))

	g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

	res, err := reconcileOnce(r, ns, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.IsZero()).To(BeTrue())

	_, err = get(rule.Managed, ns, "kct-a")
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
		"a query that renders anything other than exactly \"true\" blocks nothing")
}

// TestReconcile_MalformedDependentFailsOpen records what happens when a dependent
// does not carry the fields the query traverses. The behaviour was measured, not
// predicted.
//
// text/template yields nil for a missing map key rather than failing, and `eq`
// compares that nil against the managed object's name as false. So the query
// renders the string "false" with no error, and the dependent is treated as not
// referencing the managed object. Deletion proceeds.
//
// This fails open, which is the safer of the two directions: a malformed
// dependent cannot wedge deletion. The hazard is the other way round. If a
// dependent genuinely references the managed object but stores the reference
// somewhere else, for example because the upstream CRD renamed the field or the
// rule pins the wrong apiVersion, the operator silently stops blocking and logs
// nothing. There is no signal that a rule has quietly become a no-op.
func TestReconcile_MalformedDependentFailsOpen(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)
	ns := newNamespace(t)
	r := newReconciler(rule)

	create(t, newObj(rule.Managed, ns, "kct-a", nil))
	_, err := reconcileOnce(r, ns, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())

	// spec.template.spec exists, but bootstrap does not.
	create(t, newObj(rule.Dependent, ns, "ms-broken", map[string]interface{}{
		fieldTemplate: map[string]interface{}{fieldSpec: map[string]interface{}{}},
	}))

	g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

	res, err := reconcileOnce(r, ns, "kct-a")
	g.Expect(err).NotTo(HaveOccurred(), "a missing map key renders as false rather than failing")
	g.Expect(res.IsZero()).To(BeTrue())

	_, err = get(rule.Managed, ns, "kct-a")
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
		"a dependent that does not carry the queried field does not block deletion")
}

// TestReconcile_UnparseableQuery documents that a typo in the rules file wedges
// deletions. Nothing validates the query at startup.
func TestReconcile_UnparseableQuery(t *testing.T) {
	g := NewWithT(t)
	shipped := kubeadmConfigTemplateRule(t)

	rule := shipped
	rule.Query = "{{"

	ns := newNamespace(t)
	r := newReconciler(rule)

	create(t, newObj(rule.Managed, ns, "kct-a", nil))
	_, err := reconcileOnce(r, ns, "kct-a")
	g.Expect(err).NotTo(HaveOccurred())

	create(t, machineSetWithBootstrapRef(rule, ns, "ms-a", "kct-a"))

	g.Expect(k8sClient.Delete(context.Background(), mustGet(t, rule.Managed, ns, "kct-a"))).To(Succeed())

	_, err = reconcileOnce(r, ns, "kct-a")
	g.Expect(err).To(HaveOccurred())

	g.Expect(mustGet(t, rule.Managed, ns, "kct-a").GetFinalizers()).To(ConsistOf(Finalizer))
}

// TestReconcileDelete_NoFinalizerIsNoop covers a branch that a real API server
// cannot produce. An object with a deletionTimestamp and no finalizers is deleted
// immediately, so it can never be read back in that state.
//
// The Client is nil on purpose. If reconcileDelete were to issue the List, the
// test would panic. Passing is the proof that it returns before touching the API.
func TestReconcileDelete_NoFinalizerIsNoop(t *testing.T) {
	g := NewWithT(t)
	rule := kubeadmConfigTemplateRule(t)

	r := &RuleReconciler{
		Client:            nil,
		Scheme:            testScheme,
		DeletionBlockRule: rule,
		legacyFinalizer:   buildUniqueFinalizer(rule),
	}

	now := metav1.Now()
	managed := &unstructured.Unstructured{}
	managed.SetGroupVersionKind(rule.Managed.GetSchemaGroupVersionKind())
	managed.SetName("kct-a")
	managed.SetNamespace("default")
	managed.SetDeletionTimestamp(&now)
	managed.SetFinalizers(nil)

	res, err := r.reconcileDelete(context.Background(), logr.Discard(), managed)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res).To(Equal(ctrl.Result{}))
}
