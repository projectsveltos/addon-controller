[![CI](https://github.com/projectsveltos/addon-controller/actions/workflows/main.yaml/badge.svg)](https://github.com/projectsveltos/addon-controller/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/projectsveltos/addon-controller)](https://goreportcard.com/report/github.com/projectsveltos/addon-controller)
[![Slack](https://img.shields.io/badge/join%20slack-%23projectsveltos-brighteen)](https://join.slack.com/t/projectsveltos/shared_invite/zt-1hraownbr-W8NTs6LTimxLPB8Erj8Q6Q)
[![License](https://img.shields.io/badge/license-Apache-blue.svg)](LICENSE)
[![Twitter Follow](https://img.shields.io/twitter/follow/projectsveltos?style=social)](https://twitter.com/projectsveltos)

# Sveltos

<img src="https://raw.githubusercontent.com/projectsveltos/sveltos/main/docs/assets/logo.png" width="200">

Please refere to sveltos [documentation](https://projectsveltos.github.io/sveltos/).

## Addon deployment: how it works

![sveltos logo](./doc/sveltos.png)

The idea is simple:
1. from the management cluster, selects one or more `clusters` with a Kubernetes [label selector](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors);
2. lists which `addons` need to be deployed on such clusters.

where term:
1. `clusters` represents both [CAPI cluster](https://github.com/kubernetes-sigs/cluster-api/blob/main/api/v1beta1/cluster_types.go) or any other Kubernetes cluster registered with Sveltos;
2. `addons` represents either an [helm release](https://helm.sh), Kubernetes resource YAMLs or [kustomize](https://github.com/kubernetes-sigs/kustomize) resources.

Here is an example of how to require that any CAPI Cluster with label *env: prod* has following features deployed:
1. Kyverno helm chart (version v2.6.0)
2. kubernetes resource(s) contained in the referenced Secret: *default/storage-class*
3. kubernetes resource(s) contained in the referenced ConfigMap: *default/contour*.

```yaml
apiVersion: config.projectsveltos.io/v1alpha1
kind: ClusterProfile
metadata:
  name: deploy-kyverno
spec:
  clusterSelector: env=prod
  syncMode: Continuous
  helmCharts:
  - repositoryURL:    https://kyverno.github.io/kyverno/
    repositoryName:   kyverno
    chartName:        kyverno/kyverno
    chartVersion:     v2.6.0
    releaseName:      kyverno-latest
    releaseNamespace: kyverno
    helmChartAction:  Install
    values: |
      replicaCount: "{{ .Cluster.spec.topology.controlPlane.replicas }}"
  policyRefs:
  - name: storage-class
    namespace: default
    kind: Secret
  - name: contour-gateway
    namespace: default
    kind: ConfigMap
```

As soon as a cluster is a match for above ClusterProfile instance, all referenced features are automatically deployed in such cluster.

Here is an example using Kustomize:

```yaml
apiVersion: config.projectsveltos.io/v1alpha1
kind: ClusterProfile
metadata:
  name: flux-system
spec:
  clusterSelector: env=fv
  syncMode: Continuous
  kustomizationRefs:
  - namespace: flux-system
    name: flux-system
    kind: GitRepository
    path: ./helloWorld/
    targetNamespace: eng
```

where GitRepository synced with Flux contains following resources:

```bash
├── deployment.yaml
├── kustomization.yaml
└── service.yaml
└── configmap.yaml
```

Refer to [examples](./examples/) for more complex examples.

## Sveltos in action

![Sveltos in action](doc/SveltosOverview.gif)

To see the full demo, have a look at this [youtube video](https://youtu.be/Ai5Mr9haWKM)

## Contributing 

❤️ Your contributions are always welcome! If you want to contribute, have questions, noticed any bug or want to get the latest project news, you can connect with us in the following ways:

1. Read contributing [guidelines](CONTRIBUTING.md)
2. Open a bug/feature enhancement on github [![contributions welcome](https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat)](https://github.com/projectsveltos/addon-controller/issues)
3. Chat with us on the Slack in the #projectsveltos channel [![Slack](https://img.shields.io/badge/join%20slack-%23projectsveltos-brighteen)](https://join.slack.com/t/projectsveltos/shared_invite/zt-1hraownbr-W8NTs6LTimxLPB8Erj8Q6Q)
4. [Contact Us](mailto:support@projectsveltos.io)

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
