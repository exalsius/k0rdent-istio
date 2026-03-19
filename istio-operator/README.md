# k0rdent Istio Operator

## Description

The Istio Operator automatically generates Kubernetes credentials secrets and Istio certificates on each cluster labeled with `k0rdent.mirantis.com/istio-role`. This enables Istio endpoints discovery for all clusters in the Istio mesh.

## Getting Started

### Prerequisites

- go version v1.24.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### Build and Deploy on the cluster for Development

From the repo root makefile

```sh
make dev-istio-deploy
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

### To Uninstall

**Delete the k0rdent-istio helm chart from the cluster:**

```sh
helm del k0rdent-istio -n istio-system
```

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
