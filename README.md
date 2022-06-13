# deletion-blocker-operator
A helper operator to block deletion of k8s objects by managing finalizers based on some defined rules

## Why is it necessary?
Unfortunately all operators don't take advantage of `finalizers`. When you delete some CRs, they stop working for 
some other CRs. You need to ensure that you don't delete CRs who have some dependents. This operator allows you to 
define those dependencies via a Custom Resource so that you can block deletion of necessary CRs until some 
conditions met.

## How to use?

Current implementation only supports `IfNotExits` queries. If there is one dependent who matches the `query`, 
deletion is blocked with a unique finalizer for the CR.  

```yaml
apiVersion: core.giantswarm.io/v1alpha1
kind: DeletionBlock
metadata:
  name: blocker-for-kubeadm-config-template
spec:
  rule:
    type: IfNotExists
    query: '{{ eq .dependent.spec.template.spec.bootstrap.configRef.name .managed.metadata.name }}'
    managed:
      group: bootstrap.cluster.x-k8s.io
      version: v1beta1
      kind: KubeadmConfigTemplate
    dependent:
      group: cluster.x-k8s.io
      version: v1beta1
      kind: MachineSet
```
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

