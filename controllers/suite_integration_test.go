//go:build integration

// This file and its siblings are LOCAL ONLY. The `integration` build tag keeps
// them out of the plain `go test ./...` that CI runs. They need to fork an etcd
// and a kube-apiserver, so they cannot run in the CI container.
//
// Run them with `make test-integration`.
//
// The suite lives in package controllers, not controllers_test, because
// legacyFinalizer and buildUniqueFinalizer are unexported. SetupWithManager is
// the only production code path that populates legacyFinalizer, so a test that
// calls Reconcile directly has to set that field itself.
package controllers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/giantswarm/deletion-blocker-operator/pkg/rules"
)

// Field names that the shipped rule queries traverse.
const (
	fieldSpec     = "spec"
	fieldTemplate = "template"
)

var (
	testEnv *envtest.Environment
	restCfg *rest.Config

	// k8sClient is deliberately uncached and independent of any manager, so
	// assertions read what the API server actually stored.
	k8sClient client.Client

	// testScheme mirrors main.go: client-go types only. The Cluster API kinds are
	// handled purely as unstructured, exactly as in production.
	testScheme *k8sruntime.Scheme

	// namespaceCounter names namespaces. The control plane is fresh for every run,
	// so a counter is enough to keep the names unique.
	namespaceCounter atomic.Uint64
)

func TestMain(m *testing.M) {
	// os.Exit skips deferred calls, so the real body lives in a helper. Without
	// this, testEnv.Stop() never runs and the etcd and kube-apiserver child
	// processes leak for the lifetime of the shell.
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	// Without a logger, controller-runtime writes a "log.SetLogger(...) was never
	// called" stack trace into the test output 30 seconds in.
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr)))

	testScheme = k8sruntime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))

	testEnv = &envtest.Environment{
		Scheme:            testScheme,
		CRDDirectoryPaths: []string{filepath.Join(repoRoot(), "test", "crd")},
		// Fail loudly if the stub CRDs move, rather than silently running every
		// test against a control plane that has no Cluster API kinds.
		ErrorIfCRDPathMissing: true,
	}

	// Note: DownloadBinaryAssets is deliberately left false. When it is true
	// envtest takes a download branch that ignores KUBEBUILDER_ASSETS, which is
	// what `make test-integration` sets.
	var err error
	restCfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"cannot start the envtest control plane: %v\n\n"+
				"This suite is local only. It needs to fork etcd and kube-apiserver, "+
				"bind loopback ports, and write to TMPDIR. Run it with "+
				"`make test-integration`, which provisions the binaries and sets "+
				"KUBEBUILDER_ASSETS.\n", err)
		return 1
	}
	defer func() {
		if err := testEnv.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "cannot stop the envtest control plane: %v\n", err)
		}
	}()

	k8sClient, err = client.New(restCfg, client.Options{Scheme: testScheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot build the test client: %v\n", err)
		return 1
	}

	return m.Run()
}

// repoRoot resolves the module root from this file's own location. Using
// runtime.Caller instead of the working directory means ErrorIfCRDPathMissing
// does not misfire if the suite ever moves to another package.
func repoRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot resolve the path of suite_integration_test.go")
	}
	return filepath.Dir(filepath.Dir(thisFile))
}

// newNamespace creates a namespace unique to the calling test.
//
// envtest runs no kube-controller-manager, so there is no namespace controller.
// A deleted namespace stays Terminating forever and its contents are never
// removed. Namespaces are therefore never deleted; they die with the API server
// at the end of the suite. Giving every test its own namespace is what keeps the
// tests isolated.
func newNamespace(t *testing.T) string {
	t.Helper()
	g := NewWithT(t)

	name := fmt.Sprintf("test-ns-%d", namespaceCounter.Add(1))
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	g.Expect(k8sClient.Create(context.Background(), ns)).To(Succeed())

	return name
}

// exampleRules parses config/examples/rules.yaml, so the suite runs against the
// rules the project actually ships and documents. A typo in a GVK or a field path
// in that file breaks these tests.
//
// It repeats the body of main.readRulesFromFile because that function is
// unexported in package main.
func exampleRules(t *testing.T) []rules.DeletionBlock {
	t.Helper()
	g := NewWithT(t)

	data, err := os.ReadFile(filepath.Join(repoRoot(), "config", "examples", "rules.yaml"))
	g.Expect(err).NotTo(HaveOccurred())

	var out []rules.DeletionBlock
	g.Expect(yaml.Unmarshal(data, &out)).To(Succeed())
	g.Expect(out).To(HaveLen(2))

	return out
}

// kubeadmConfigTemplateRule is the first shipped rule: a KubeadmConfigTemplate is
// blocked while a MachineSet references it through
// spec.template.spec.bootstrap.configRef.name.
func kubeadmConfigTemplateRule(t *testing.T) rules.DeletionBlock {
	t.Helper()
	return exampleRules(t)[0]
}

// openStackMachineTemplateRule is the second shipped rule: an
// OpenStackMachineTemplate is blocked while a MachineSet references it through
// spec.template.spec.infrastructureRef.name.
func openStackMachineTemplateRule(t *testing.T) rules.DeletionBlock {
	t.Helper()
	return exampleRules(t)[1]
}

// newReconciler wires legacyFinalizer by hand. In production SetupWithManager
// does this. Tests that call Reconcile directly must do it themselves, which is
// why this suite is in package controllers.
func newReconciler(rule rules.DeletionBlock) *RuleReconciler {
	return &RuleReconciler{
		Client:            k8sClient,
		Scheme:            testScheme,
		DeletionBlockRule: rule,
		legacyFinalizer:   buildUniqueFinalizer(rule),
	}
}

func newObj(gvk rules.GroupVersionKind, namespace, name string, spec map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{fieldSpec: spec}}
	u.SetGroupVersionKind(gvk.GetSchemaGroupVersionKind())
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

// machineSetWithBootstrapRef builds a MachineSet that the first shipped rule
// treats as a dependent of the named KubeadmConfigTemplate.
func machineSetWithBootstrapRef(rule rules.DeletionBlock, namespace, name, configRefName string) *unstructured.Unstructured {
	return newObj(rule.Dependent, namespace, name, map[string]interface{}{
		fieldTemplate: map[string]interface{}{
			fieldSpec: map[string]interface{}{
				"bootstrap": map[string]interface{}{
					"configRef": map[string]interface{}{"name": configRefName},
				},
			},
		},
	})
}

// machineSetWithInfrastructureRef builds a MachineSet that the second shipped
// rule treats as a dependent of the named OpenStackMachineTemplate.
func machineSetWithInfrastructureRef(rule rules.DeletionBlock, namespace, name, infraRefName string) *unstructured.Unstructured {
	return newObj(rule.Dependent, namespace, name, map[string]interface{}{
		fieldTemplate: map[string]interface{}{
			fieldSpec: map[string]interface{}{
				"infrastructureRef": map[string]interface{}{"name": infraRefName},
			},
		},
	})
}

func create(t *testing.T, obj *unstructured.Unstructured) {
	t.Helper()
	NewWithT(t).Expect(k8sClient.Create(context.Background(), obj)).To(Succeed())
}

// get reads an object of the given kind. The returned error is left for the
// caller to inspect, because several tests assert on apierrors.IsNotFound.
func get(gvk rules.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk.GetSchemaGroupVersionKind())
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, u)
	return u, err
}

func mustGet(t *testing.T, gvk rules.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	t.Helper()
	u, err := get(gvk, namespace, name)
	NewWithT(t).Expect(err).NotTo(HaveOccurred())
	return u
}

// reconcileOnce drives a single Reconcile for the given object.
func reconcileOnce(r *RuleReconciler, namespace, name string) (ctrl.Result, error) {
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: name},
	})
}
