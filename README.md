# deletion-blocker-operator
A helper operator to block deletion of k8s objects by managing finalizers based on some defined rules

## Why is it necessary?
Unfortunately all operators don't take advantage of `finalizers`. When you delete some CRs, they stop working for
some other CRs. You need to ensure that you don't delete CRs who have some dependents. This operator allows you to
define those dependencies via some rules so that you can block deletion of necessary CRs until some
conditions met.

## How does it work?

The helm chart requires `rules`. The chart creates a configmap on which the operator is mounted and also the chart
creates necessary RBACs for the operator.
```
rules:
  - query: '{{ eq .dependent.spec.template.spec.bootstrap.configRef.name .managed.metadata.name }}'
    managed:
      group: bootstrap.cluster.x-k8s.io
      version: v1beta1
      kind: KubeadmConfigTemplate
      resource: kubeadmconfigtemplates
    dependent:
      group: cluster.x-k8s.io
      version: v1beta1
      kind: MachineSet
      resource: machinesets
```

## Development

### Unit tests

```
make test
```

This runs `go test ./...` across the module. It needs no cluster and no extra
tools. This is what CI runs.

### Integration tests

The integration tests run the reconciler against a real Kubernetes API server.
[envtest](https://book.kubebuilder.io/reference/envtest.html) starts a local
`kube-apiserver` and `etcd`. This is necessary because the operator's whole job is
finalizer and deletion semantics, and only a real API server reaps an object the
instant its last finalizer is removed.

```
make test-integration
```

The tests are **local only**. Every file in the suite carries a
`//go:build integration` tag. CI runs a plain `go test ./...` with no `-tags`
flag, so it never compiles or runs them.

The first run downloads about 150 MB into the gitignored `bin/` directory and is
therefore slow. Later runs reuse it:

- `bin/setup-envtest-<version>` is the asset manager. It is installed with
  `go install`, which never touches `go.mod`.
- `bin/k8s/` holds the `kube-apiserver`, `etcd` and `kubectl` binaries.

Prerequisites:

- Go, at the version in the `go` directive in `go.mod`.
- Network access on the first run only, to `raw.githubusercontent.com` and to the
  GitHub release assets of `kubernetes-sigs/controller-tools`.
- **No Kubernetes cluster and no `kind`.** envtest starts its own API server and
  etcd. There is no kubelet, no scheduler and no controller-manager, so pods never
  run, garbage collection never happens, and a deleted namespace stays
  `Terminating` forever.

Useful targets and variables:

```
make envtest                 # install setup-envtest into ./bin
make envtest-list            # list the control-plane versions published upstream
make lint-integration        # lint the tagged tests, which `make lint` skips
make clean-envtest           # delete ./bin/k8s and the setup-envtest binary

# run one test, verbosely
make test-integration GOTESTFLAGS='-run TestReconcile_AddsFinalizerOnCreate -v'

# with the race detector
make test-integration GOTESTFLAGS='-race'

# a different control plane version, which must be published
make test-integration ENVTEST_K8S_VERSION=1.36.0
```

### Test fixtures

The operator reconciles CRDs that other projects own, and it resolves their group,
version and kind at runtime from its rules file. envtest starts with no CRDs, so
the suite loads minimal stubs from `test/crd/`. Their GVKs match
`config/examples/rules.yaml`, which means the shipped example rules are executable
documentation: a typo in a GVK or a field path breaks the tests. See
`test/crd/README.md` before you change them.

`ENVTEST_K8S_VERSION` in `Makefile.custom.mk` tracks the minor version of the
`k8s.io/*` modules in `go.mod` (`v0.36.x` maps to `1.36.x`). `ENVTEST_VERSION`
tracks `sigs.k8s.io/controller-runtime`. Bump both when those dependencies move to
a new minor. Not every Kubernetes patch release gets an envtest asset, so run
`make envtest-list` to see what exists.

### Troubleshooting

- `no versions matching ...` usually means an old `setup-envtest` that still
  queries the retired `storage.googleapis.com/kubebuilder-tools` bucket. Run
  `make clean-envtest && make envtest`.
- `make: gitsemver: No such file or directory` is harmless and unrelated to the
  tests. The generated `Makefile.gen.go.mk` stamps a version into the binary and
  expects `gitsemver` on `$PATH`. The integration targets do not use it.

## License

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
