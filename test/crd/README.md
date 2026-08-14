# envtest CRD stubs

## Never apply these to a real cluster

These files are test fixtures. They are loaded by the integration suite through
`envtest.Environment.CRDDirectoryPaths`. They are **not** deployment manifests.

Each file is a minimal stub of a CRD that another project owns:

| File | Owner |
| --- | --- |
| `bootstrap.cluster.x-k8s.io_kubeadmconfigtemplates.yaml` | Cluster API Bootstrap Provider Kubeadm |
| `cluster.x-k8s.io_machinesets.yaml` | Cluster API |
| `infrastructure.cluster.x-k8s.io_openstackmachinetemplates.yaml` | Cluster API Provider OpenStack |

Applying a stub to a management cluster would replace the real CRD with a
schemaless version. That destroys the validation and the stored schema for every
existing object of that kind.

## Why the stubs are schemaless

Each stub declares `type: object` with
`x-kubernetes-preserve-unknown-fields: true` at the root. This is a valid
structural schema for `apiextensions.k8s.io/v1`.

The operator is generic. It resolves the group, version and kind at runtime from
its rules file and treats every object as `unstructured`. It reads only
`metadata.name`, `metadata.finalizers` and whichever nested `spec` fields the
rule query names. It never creates or validates these objects. Therefore a stub
needs to supply the identity of the resource and nothing else.

The consequence is that test fixtures are **not validated**. A MachineSet fixture
that omits `spec.clusterName` and `spec.selector` is accepted here, and a real
cluster would reject it. This is an accepted trade. Vendoring the real CRDs would
add about 100 kB of schema per kind and force every fixture to carry 40 lines of
Cluster API boilerplate that tests nothing about this operator.

## Which kinds belong here

The group, version and kind of every stub must match an entry in
`config/examples/rules.yaml`. The integration suite reads that file directly, so
the shipped example rules are executable documentation. If you change a GVK in
the example rules, add or update the matching stub here.
