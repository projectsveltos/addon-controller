/*
Copyright 2023. projectsveltos.io. All rights reserved.

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
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
)

var _ = Describe("Dependencies", func() {
	const (
		namePrefix = "dependencies-"
	)

	It("ClusterProfile with dependencies is deployed after dependencies are provisioned", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfileDependency := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfileDependency.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfileDependency)).To(Succeed())
		verifyClusterProfileMatches(clusterProfileDependency)
		verifyClusterSummary(controllers.ClusterProfileLabelName, clusterProfileDependency.Name,
			&clusterProfileDependency.Spec, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Byf("Set ClusterProfile %s as dependency for ClusterProfile %s", clusterProfileDependency.Name, clusterProfile.Name)
		clusterProfile.Spec.DependsOn = []string{clusterProfileDependency.Name}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())
		verifyClusterProfileMatches(clusterProfile)
		verifyClusterSummary(controllers.ClusterProfileLabelName, clusterProfile.Name,
			&clusterProfile.Spec, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfileDependency.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfileDependency.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://airflow.apache.org",
				RepositoryName:   "apache-airflow",
				ChartName:        "apache-airflow/airflow",
				ChartVersion:     "1.15.0",
				ReleaseName:      "airflow",
				ReleaseNamespace: "airflow",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
				Values: `createUserJob:
  useHelmHooks: false
  applyCustomEnv: false
migrateDatabaseJob:
  useHelmHooks: false
  applyCustomEnv: false`,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummaryDependency := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://charts.bitnami.com/bitnami",
				RepositoryName:   "bitnami",
				ChartName:        "bitnami/flink",
				ChartVersion:     "1.4.0",
				ReleaseName:      "flink",
				ReleaseNamespace: "flink",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}

		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())
		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		By("Verifying clusterSummary is not deployed till the dependencies are provisioned")
		Eventually(func() bool {
			currentClusterSummaryDependecy := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummaryDependency.Namespace, Name: clusterSummaryDependency.Name},
				currentClusterSummaryDependecy)
			if err != nil {
				return false
			}
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}

			// currentClusterSummary depends on currentClusterSummaryDependecy so expects
			// currentClusterSummary.Status.FeatureSummaries to be nil till all helm charts
			// in currentClusterSummaryDependecy not provisioned
			if currentClusterSummaryDependecy.Status.FeatureSummaries == nil {
				return currentClusterSummary.Status.FeatureSummaries == nil
			}

			for i := range currentClusterSummaryDependecy.Status.FeatureSummaries {
				if currentClusterSummaryDependecy.Status.FeatureSummaries[i].Status != configv1beta1.FeatureStatusProvisioned {
					return currentClusterSummary.Status.FeatureSummaries == nil
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummaryDependency.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummaryDependency.Name, configv1beta1.FeatureHelm)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureHelm)

		deleteClusterProfile(clusterProfileDependency)

		deleteClusterProfile(clusterProfile)
	})
})
