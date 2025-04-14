/*
Copyright 2025. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fv_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
)

const (
	values = `      grafana:
          enabled: true
      alertmanager:
          enabled: false
      kubeEtcd:
          service: {}
          serviceMonitor: {}
      prometheus:
          prometheusSpec:
              serviceMonitorSelectorNilUsesHelmValues: false
              serviceMonitorNamespaceSelector:
                  matchExpressions:
                      - key: kubernetes.io/metadata.name
                        operator: Exists
          additionalServiceMonitors:
              - name: k0s
                selector:
                  matchLabels:
                      app: k0s-observability
                      component: pushgateway
                      k0s.k0sproject.io/stack: metrics
                namespaceSelector:
                  matchNames:
                      - k0s-system
                endpoints:
                  - port: http
                    metricRelabelings:
                      - sourceLabels:
                          - exported_job
                        targetLabel: job
      kubelet:
          serviceMonitor:
              cAdvisor: true`
)

var _ = Describe("Feature", func() {
	const (
		namePrefix    = "auto-deploy-dependencies-"
		deplNamespace = "kube-prometheus-stack"
		deplName      = "kube-prometheus-stack-operator"
	)

	// This Helm release contains corev1.List. Verify those are expanded so drift-detection can watch for configuration drift
	It("Expand corev1List in helm charts", Label("NEW-FV"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		Byf("Update ClusterProfile %s to deploy kube-prometheus-stack helm chart", clusterProfile.Name)
		clusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://prometheus-community.github.io/helm-charts",
				RepositoryName:   "prometheus-community",
				ChartName:        "prometheus-community/kube-prometheus-stack",
				ChartVersion:     "70.3.0",
				ReleaseName:      "kube-prometheus-stack",
				ReleaseNamespace: "kube-prometheus-stack",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
				Values:           values,
			},
		}
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection

		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying kube-prometheus-stack-operator deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deplNamespace, Name: deplName}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		charts := []configv1beta1.Chart{
			{ReleaseName: "kube-prometheus-stack", ChartVersion: "70.3.0", Namespace: "kube-prometheus-stack"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureHelm,
			nil, charts)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureHelm)

		deleteClusterProfile(clusterProfile)

		Byf("Verifying kube-prometheus-stack deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deplNamespace, Name: deplName}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
