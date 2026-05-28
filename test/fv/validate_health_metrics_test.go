/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"fmt"
	"net"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// prometheusHelmValues configures the prometheus-community/prometheus chart for FV tests:
//   - only the Prometheus server component is enabled (alertmanager, pushgateway,
//     kube-state-metrics and nodeExporter are disabled to save resources)
//   - the server service is exposed as NodePort 30090 so the management cluster can
//     reach it in push mode (both clusters share the same Docker bridge network)
//   - a static scrape job targets podinfo on port 9898 inside the managed cluster
const prometheusHelmValues = `
alertmanager:
  enabled: false
prometheus-pushgateway:
  enabled: false
kube-state-metrics:
  enabled: false
nodeExporter:
  enabled: false
configmapReload:
  prometheus:
    enabled: false
server:
  persistentVolume:
    enabled: false
  service:
    type: NodePort
    nodePort: 30090
extraScrapeConfigs: |
  - job_name: podinfo
    static_configs:
    - targets:
      - podinfo.monitoring.svc.cluster.local:9898
`

// luaDeploymentHealthScript evaluates whether a Deployment has all requested replicas available.
const luaDeploymentHealthScript = `
function evaluate(obj)
  if obj.status ~= nil and obj.status.availableReplicas ~= nil
      and obj.status.availableReplicas == obj.spec.replicas then
    return {healthy=true, message=""}
  end
  return {healthy=false, message="available replicas not matching requested replicas"}
end`

// luaMetricUpScript evaluates the "up" metric returned for a scrape job.
// A value of 1 means the target is being scraped successfully.
const luaMetricUpScript = `
function evaluate()
  if metrics["up"] < 1 then
    return {healthy=false, message="target not yet scraped: up=" .. tostring(metrics["up"])}
  end
  return {healthy=true, message=""}
end`

var _ = Describe("ValidateHealth metric-based checks", func() {
	const (
		namePrefix        = "metric-health-"
		monitoringNs      = "monitoring"
		prometheusRelease = "prometheus-server"
		podinfoRelease    = "podinfo"
	)

	// Runs in push mode only (CAPI): in pull mode the agent would need to implement
	// metric checks, which is a separate change.
	It("holds a ClusterProfile as not provisioned until Prometheus confirms the app is scraped",
		Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {

			Byf("Determining Prometheus URL for cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			prometheusURL, err := getPrometheusURL()
			Expect(err).To(BeNil())
			Byf("Prometheus URL: %s", prometheusURL)

			// ── ClusterProfile 1: Prometheus
			Byf("Creating ClusterProfile %s to deploy Prometheus in namespace %s",
				namePrefix+"prometheus", monitoringNs)
			cpPrometheus := getClusterProfile(namePrefix+"prometheus-", map[string]string{key: value})
			cpPrometheus.Spec.SyncMode = configv1beta1.SyncModeContinuous
			cpPrometheus.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://prometheus-community.github.io/helm-charts",
					RepositoryName:   "prometheus-community",
					ChartName:        "prometheus-community/prometheus",
					ChartVersion:     "25.27.0",
					ReleaseName:      prometheusRelease,
					ReleaseNamespace: monitoringNs,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values:           prometheusHelmValues,
				},
			}
			cpPrometheus.Spec.ValidateHealths = []libsveltosv1beta1.ValidateHealth{
				{
					Name:      "prometheus-server-ready",
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Group:     "apps",
					Version:   "v1",
					Kind:      "Deployment",
					Namespace: monitoringNs,
					Script:    luaDeploymentHealthScript,
				},
			}
			Expect(k8sClient.Create(context.TODO(), cpPrometheus)).To(Succeed())

			verifyClusterProfileMatches(cpPrometheus)
			clusterSummaryPrometheus := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				cpPrometheus.Name, &cpPrometheus.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			// Deploying Prometheus takes time.
			Byf("Waiting for ClusterSummary %s to provision Prometheus", clusterSummaryPrometheus.Name)
			const two = 2
			Eventually(func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: clusterSummaryPrometheus.Namespace, Name: clusterSummaryPrometheus.Name},
					currentClusterSummary)
				if err != nil {
					return false
				}
				for i := range currentClusterSummary.Status.FeatureSummaries {
					if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
						if currentClusterSummary.Status.FeatureSummaries[i].Status == libsveltosv1beta1.FeatureStatusProvisioned &&
							currentClusterSummary.Status.FeatureSummaries[i].FailureMessage == nil {

							return true
						}
					}
				}
				return false
			}, two*timeout, pollingInterval).Should(BeTrue())

			// ── ClusterProfile 2: podinfo
			// DependsOn ensures podinfo is only deployed after Prometheus is provisioned.
			// The validateHealth metric check holds the profile as not provisioned until
			// Prometheus reports up{job="podinfo"} == 1 (i.e. podinfo is being scraped).
			Byf("Creating ClusterProfile %s to deploy podinfo in namespace %s",
				namePrefix+"podinfo", monitoringNs)
			cpPodinfo := getClusterProfile(namePrefix+"podinfo-", map[string]string{key: value})
			cpPodinfo.Spec.SyncMode = configv1beta1.SyncModeContinuous
			cpPodinfo.Spec.DependsOn = []string{cpPrometheus.Name}
			cpPodinfo.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://stefanprodan.github.io/podinfo",
					RepositoryName:   "podinfo",
					ChartName:        "podinfo/podinfo",
					ChartVersion:     "6.7.1",
					ReleaseName:      podinfoRelease,
					ReleaseNamespace: monitoringNs,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values:           "image:\n  repository: docker.io/stefanprodan/podinfo\n",
				},
			}
			cpPodinfo.Spec.ValidateHealths = []libsveltosv1beta1.ValidateHealth{
				{
					Name:      "podinfo-scraped",
					FeatureID: libsveltosv1beta1.FeatureHelm,
					MetricSource: &libsveltosv1beta1.MetricSource{
						URL: prometheusURL,
					},
					MetricQueries: []libsveltosv1beta1.MetricQuery{
						{
							Name:  "up",
							Query: `up{job="podinfo"}`,
						},
					},
					Script: luaMetricUpScript,
				},
			}
			Expect(k8sClient.Create(context.TODO(), cpPodinfo)).To(Succeed())

			verifyClusterProfileMatches(cpPodinfo)
			clusterSummaryPodinfo := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				cpPodinfo.Name, &cpPodinfo.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Waiting for ClusterSummary %s to provision podinfo (metric check must pass)",
				clusterSummaryPodinfo.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(),
				clusterSummaryPodinfo.Name, libsveltosv1beta1.FeatureHelm)

			// ── Cleanup ───────────────────────────────────────────────────────────────
			Byf("Deleting ClusterProfile %s (podinfo)", cpPodinfo.Name)
			deleteClusterProfile(cpPodinfo)

			Byf("Deleting ClusterProfile %s (Prometheus)", cpPrometheus.Name)
			// Update prometheus profile to remove the finalizer dependency before deleting
			currentCP := &configv1beta1.ClusterProfile{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: cpPrometheus.Name}, currentCP)).To(Succeed())
			deleteClusterProfile(cpPrometheus)
		})
})

// getPrometheusURL returns the URL to use in the validateHealth metricSource.
// In pull mode (SveltosCluster) the agent runs inside the managed cluster and can
// reach the service via its DNS name.
// In push mode (CAPI) the addon-controller runs on the management cluster; both clusters
// share the same Docker bridge network, so the NodePort (30090) is reachable at the
// InternalIP of any workload cluster node.
func getPrometheusURL() (string, error) {
	if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
		return "http://prometheus-server-server.monitoring.svc:80", nil
	}

	// Push mode: the workload kubeconfig server URL may be 0.0.0.0 (the local bind
	// address used when creating the kind cluster), so we get the real Docker bridge IP
	// by listing the workload cluster's nodes directly.
	workloadClient, err := getKindWorkloadClusterKubeconfig()
	if err != nil {
		return "", fmt.Errorf("getting workload cluster client: %w", err)
	}

	nodeList := &corev1.NodeList{}
	if err := workloadClient.List(context.TODO(), nodeList, &client.ListOptions{}); err != nil {
		return "", fmt.Errorf("listing workload cluster nodes: %w", err)
	}
	if len(nodeList.Items) == 0 {
		return "", fmt.Errorf("no nodes found in workload cluster")
	}

	for i := range nodeList.Items {
		for _, addr := range nodeList.Items[i].Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				return "http://" + net.JoinHostPort(addr.Address, "30090"), nil
			}
		}
	}

	return "", fmt.Errorf("no InternalIP address found on any workload cluster node")
}
