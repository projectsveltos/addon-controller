/*
Copyright 2024. projectsveltos.io. All rights reserved.

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
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
)

var _ = Describe("Helm", Serial, func() {
	const (
		namePrefix = "tier-"
	)

	It("Use tier to solve conflicts", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://kyverno.github.io/kyverno/",
					RepositoryName:   "kyverno",
					ChartName:        "kyverno/kyverno",
					ChartVersion:     "v3.2.6",
					ReleaseName:      "kyverno-latest",
					ReleaseNamespace: "kyverno",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    "https://prometheus-community.github.io/helm-charts",
					RepositoryName:   "prometheus-community",
					ChartName:        "prometheus-community/prometheus",
					ChartVersion:     "25.24.0",
					ReleaseName:      "prometheus",
					ReleaseNamespace: "prometheus",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    "https://grafana.github.io/helm-charts",
					RepositoryName:   "grafana",
					ChartName:        "grafana/grafana",
					ChartVersion:     "8.3.4",
					ReleaseName:      "grafana",
					ReleaseNamespace: "grafana",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			currentClusterProfile.Spec.Tier = 90
			currentClusterProfile.Spec.ContinueOnConflict = true

			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying kyverno deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureHelm)

		charts := []configv1beta1.Chart{
			{ReleaseName: "kyverno-latest", ChartVersion: "3.2.6", Namespace: "kyverno"},
			{ReleaseName: "grafana", ChartVersion: "8.3.4", Namespace: "grafana"},
			{ReleaseName: "prometheus", ChartVersion: "25.24.0", Namespace: "prometheus"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureHelm,
			nil, charts)

		Byf("Creating another ClusterProfile matching the cluster")
		newClusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		newClusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), newClusterProfile)).To(Succeed())

		verifyClusterProfileMatches(newClusterProfile)

		Byf("configuring the new clusterProfile to deploy Kyverno")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: newClusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.2.5",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "kyverno",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		newClusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying new ClusterSummary reports a conflict")
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: newClusterSummary.Namespace, Name: newClusterSummary.Name}, currentClusterSummary)
			if err != nil {
				return false
			}
			if len(currentClusterSummary.Status.HelmReleaseSummaries) != 1 {
				return false
			}
			return currentClusterSummary.Status.HelmReleaseSummaries[0].Status == configv1beta1.HelmChartStatusConflict
		}, timeout, pollingInterval).Should(BeTrue())

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureHelm,
			nil, charts)

		Byf("Changing ClusterProfile %s tier", newClusterProfile.Name)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: newClusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.Tier = 50
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying new ClusterSummary does not report a conflict")
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: newClusterSummary.Namespace, Name: newClusterSummary.Name}, currentClusterSummary)
			if err != nil {
				return false
			}
			if len(currentClusterSummary.Status.HelmReleaseSummaries) != 1 {
				return false
			}
			return currentClusterSummary.Status.HelmReleaseSummaries[0].Status == configv1beta1.HelmChartStatusManaging
		}, timeout, pollingInterval).Should(BeTrue())

		charts = []configv1beta1.Chart{
			{ReleaseName: "grafana", ChartVersion: "8.3.4", Namespace: "grafana"},
			{ReleaseName: "prometheus", ChartVersion: "25.24.0", Namespace: "prometheus"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureHelm,
			nil, charts)

		charts = []configv1beta1.Chart{
			{ReleaseName: "kyverno-latest", ChartVersion: "3.2.5", Namespace: "kyverno"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, newClusterProfile.Name,
			newClusterSummary.Spec.ClusterNamespace, newClusterSummary.Spec.ClusterName, configv1beta1.FeatureHelm,
			nil, charts)

		deleteClusterProfile(clusterProfile)
		deleteClusterProfile(newClusterProfile)

		Byf("Verifying kyverno deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-latest"}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
