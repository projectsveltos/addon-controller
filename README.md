[![CI](https://github.com/projectsveltos/addon-controller/actions/workflows/main.yaml/badge.svg)](https://github.com/projectsveltos/addon-controller/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/projectsveltos/addon-controller)](https://goreportcard.com/report/github.com/projectsveltos/addon-controller)
[![Docker Pulls](https://img.shields.io/docker/pulls/projectsveltos/addon-controller.svg)](https://store.docker.com/community/images/projectsveltos/addon-controller)
[![Release](https://img.shields.io/github/v/release/projectsveltos/addon-controller)](https://github.com/projectsveltos/addon-controller/releases)
[![Slack](https://img.shields.io/badge/join%20slack-%23projectsveltos-brighteen)](https://join.slack.com/t/projectsveltos/shared_invite/zt-1hraownbr-W8NTs6LTimxLPB8Erj8Q6Q)
[![License](https://img.shields.io/badge/license-Apache-blue.svg)](LICENSE)
[![Twitter Follow](https://img.shields.io/twitter/follow/projectsveltos?style=social)](https://twitter.com/projectsveltos)

# Sveltos: Kubernetes add-on controller

<img src="https://raw.githubusercontent.com/projectsveltos/sveltos/main/docs/assets/logo.png" width="200">

# What is the Projectsveltos?
Sveltos is a Kubernetes add-on controller that simplifies the deployment and management of add-ons and applications across multiple clusters. It runs in the management cluster and can programmatically deploy and manage add-ons and applications on any cluster in the fleet, including the management cluster itself. Sveltos supports a variety of add-on formats, including Helm charts, raw YAML, Kustomize, Carvel ytt, and Jsonnet.

<p align="center">
  <img alt="Sveltos Kubernetes add-ons management across clusters" src="https://projectsveltos.github.io/sveltos/assets/multi-clusters.png" width="600"/>
</p>

Sveltos allows you to represent add-ons and applications as templates. Before deploying to managed clusters, Sveltos instantiates these templates. Sveltos can gather the information required to instantiate the templates from either the management cluster or the managed clusters themselves. This enables you to use the same add-on configuration across all of your clusters, while still allowing for some variation, such as different add-on configuration values. In other words, Sveltos lets you define add-ons and applications in a reusable way. You can then deploy these definitions to multiple clusters, with minor adjustments as needed. This can save you a lot of time and effort, especially if you manage a large number of clusters.

Sveltos provides precise control over add-on deployment order. Add-ons within a Profile/ClusterProfile are deployed in the exact order they appear, ensuring a predictable and controlled rollout. Furthermore, ClusterProfiles can depend on others, guaranteeing that dependent add-ons only deploy after their dependencies are fully operational. Finally Sveltos' event-driven framework offers additional flexibility. This framework allows for deploying add-ons and applications in response to specific events, enabling dynamic and adaptable deployments based on your needs.

👉 To get updates [⭐️ star](https://github.com/projectsveltos/addon-controller/stargazers) this repository.

## Profiles vs. ClusterProfiles

Projectsveltos offers two powerful tools for managing cluster configurations: **Profiles** and **ClusterProfiles**. Understanding their distinctions is crucial for efficient setup and administration.

1. ClusterProfiles: Apply across all clusters in any namespace. Ideal for platform admins maintaining global consistency and managing settings like networking, security, and resource allocation.
2. Profiles: Limited to a specific namespace, granting granular control to tenant admins. This isolation ensures teams manage, from the management cluster, their managed clusters independently without impacting others.

<p align="center">
  <img alt="Sveltos Profile vs ClusterProfile" src="https://projectsveltos.github.io/sveltos/assets/profile_vs_clusterprofile.png" width="600"/>
</p>

## Addon deployment: how it works

The idea is simple:
1. from the management cluster, selects one or more `clusters` with a Kubernetes [label selector](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors);
2. lists which `addons` need to be deployed on such clusters.

where term:
1. `clusters` represents both [CAPI cluster](https://github.com/kubernetes-sigs/cluster-api/blob/main/api/v1beta1/cluster_types.go) or any other Kubernetes cluster registered with Sveltos;
2. `addons` represents either an [helm release](https://helm.sh), Kubernetes resource YAMLs or [kustomize](https://github.com/kubernetes-sigs/kustomize) resources.

Here is an example of how to require that any CAPI Cluster with label *env: prod* has following features deployed:
1. Kyverno helm chart (version v3.0.1)
2. kubernetes resource(s) contained in the referenced Secret: *default/storage-class*
3. kubernetes resource(s) contained in the referenced ConfigMap: *default/contour*.

```yaml
apiVersion: config.projectsveltos.io/v1beta1
kind: ClusterProfile
metadata:
  name: deploy-kyverno
spec:
  clusterSelector:
    matchLabels:
      env: prod
  syncMode: Continuous
  helmCharts:
  - repositoryURL:    https://kyverno.github.io/kyverno/
    repositoryName:   kyverno
    chartName:        kyverno/kyverno
    chartVersion:     v3.0.1
    releaseName:      kyverno-latest
    releaseNamespace: kyverno
    helmChartAction:  Install
    values: |
      admissionController:
        replicas: 3
  policyRefs:
  - name: storage-class
    namespace: default
    kind: Secret
  - name: contour-gateway
    namespace: default
    kind: ConfigMap
```

As soon as a cluster is a match for above ClusterProfile instance, all referenced features are automatically deployed in such cluster.

![Kubernetes add-on deployment](https://github.com/projectsveltos/sveltos/blob/main/docs/assets/addons_deployment.gif)

Here is an example using Kustomize:

```yaml
apiVersion: config.projectsveltos.io/v1beta1
kind: ClusterProfile
metadata:
  name: flux-system
spec:
  clusterSelector:
    matchLabels:
      env: fv
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

## Different SyncMode

- *OneTime*: This mode is designed for bootstrapping critical components during the initial cluster setup. Think of it as a one-shot configuration injection:
    1. Deploying essential infrastructure components like CNI plugins, cloud controllers, or the workload cluster's package manager itself;
    2. Simplifies initial cluster setup;
    3. Hands over management to the workload cluster's own tools, promoting modularity and potentially simplifying ongoing maintenance. 
- *Continuous*: This mode continuously monitors ClusterProfiles or Profiles for changes and automatically applies them to matching clusters. It ensures ongoing consistency between your desired configuration and the actual cluster state: 
    1. Centralized control over deployments across multiple clusters for consistency and compliance;
    2. Simplifies management of configurations across multiple clusters.
- *ContinuousWithDriftDetection*: Detects and automatically corrects configuration drifts in managed clusters, ensuring they remain aligned with the desired state defined in the management cluster.

## Configuration Drift Detection

Sveltos can automatically detect drift between the desired state, defined in the management cluster, and actual state of your clusters and recover from it.

<p align="center">
  <img alt="Configuration drift recovery" src="https://github.com/projectsveltos/demos/blob/main/configuration_drift/reconcile_configuration_drift.gif" width="600"/>
</p>

[![Projectsveltos intro](https://img.youtube.com/vi/FRYYHAWr0MQ/0.jpg)](https://www.youtube.com/watch?v=FRYYHAWr0MQ)
[![Projectsveltos intro](https://img.youtube.com/vi/A5Y0XTnoS7k/0.jpg)](https://www.youtube.com/watch?v=A5Y0XTnoS7k)

## Give projectsveltos a try

If you want to try projectsveltos with a test cluster:

1. git clone https://github.com/projectsveltos/addon-controller
2. make quickstart

will create a management cluster using Kind, deploy clusterAPI and projectsveltos, create a workload cluster powered by clusterAPI.

## Sveltos in action

![Sveltos in action](doc/SveltosOverview.gif)

To see the full demo, have a look at this [youtube video](https://youtu.be/Ai5Mr9haWKM)

# Useful links

- Projectsveltos [documentation](https://projectsveltos.github.io/sveltos/)
- [Quick Start](https://projectsveltos.github.io/sveltos/install/quick_start/)

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
