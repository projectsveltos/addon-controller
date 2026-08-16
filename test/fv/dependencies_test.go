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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Dependencies", func() {
	const (
		namePrefix = "dependencies-"

		// Charts for "prerequisite deletion waits for dependent" below. Deliberately not
		// Bitnami (unreliable chart-index downloads in CI) and not cert-manager/kyverno
		// (already used by other dependsOn FV tests).
		metricsServerRepoURL    = "https://kubernetes-sigs.github.io/metrics-server/"
		metricsServerRepoName   = "metrics-server"
		metricsServerChartName  = "metrics-server/metrics-server"
		metricsServerVersion    = "3.13.1"
		metricsServerRelease    = "metrics-server"
		reloaderChartRepoURL    = "https://stakater.github.io/stakater-charts"
		reloaderChartRepoName   = "stakater"
		reloaderHelmChartName   = "stakater/reloader"
		reloaderHelmVersion     = "2.2.16"
		reloaderHelmReleaseName = "stakater-reloader"
	)

	It("ClusterProfile with dependencies is deployed after dependencies are provisioned",
		Label("FV", "PULLMODE", "EXTENDED"), func() {
			Byf("Create a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfileDependency := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfileDependency.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Create(context.TODO(), clusterProfileDependency)).To(Succeed())
			verifyClusterProfileMatches(clusterProfileDependency)
			verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfileDependency.Name,
				&clusterProfileDependency.Spec, kindWorkloadCluster.GetNamespace(),
				kindWorkloadCluster.GetName(), getClusterType())

			Byf("Create a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Byf("Set ClusterProfile %s as dependency for ClusterProfile %s",
				clusterProfileDependency.Name, clusterProfile.Name)
			clusterProfile.Spec.DependsOn = []string{clusterProfileDependency.Name}
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())
			verifyClusterProfileMatches(clusterProfile)
			verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name,
				&clusterProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Update ClusterProfile %s to deploy helm charts", clusterProfileDependency.Name)
			currentClusterProfile := &configv1beta1.ClusterProfile{}

			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfileDependency.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
					{
						RepositoryURL:    "https://airflow.apache.org",
						RepositoryName:   "apache-airflow",
						ChartName:        "apache-airflow/airflow",
						ChartVersion:     "1.15.0",
						ReleaseName:      airflowRelease,
						ReleaseNamespace: airflowRelease,
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
						Values: `createUserJob:
  useHelmHooks: false
  applyCustomEnv: false
migrateDatabaseJob:
  useHelmHooks: false
  applyCustomEnv: false`,
					},
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfileDependency.Name}, currentClusterProfile)).To(Succeed())

			clusterSummaryDependency := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
					{
						RepositoryURL:    bitnamiURL,
						RepositoryName:   bitnamiName,
						ChartName:        "bitnami/flink",
						ChartVersion:     "1.4.0",
						ReleaseName:      flinkRelease,
						ReleaseNamespace: flinkRelease,
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
					},
				}

				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

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
					if currentClusterSummaryDependecy.Status.FeatureSummaries[i].Status != libsveltosv1beta1.FeatureStatusProvisioned {
						return currentClusterSummary.Status.FeatureSummaries == nil
					}
				}
				return true
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummaryDependency.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummaryDependency.Name, libsveltosv1beta1.FeatureHelm)

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

			// dependsOn protects the prerequisite from deletion while its dependent still
			// needs it: the dependent must be removed first.
			deleteClusterProfile(clusterProfile)

			deleteClusterProfile(clusterProfileDependency)
		})

	It("Deleting a prerequisite ClusterProfile waits for its dependent to be removed first",
		Label("FV", "PULLMODE", "EXTENDED"), func() {
			Byf("Create a ClusterProfile matching Cluster %s/%s to be used as a prerequisite",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			prerequisiteProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			prerequisiteProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			prerequisiteProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    metricsServerRepoURL,
					RepositoryName:   metricsServerRepoName,
					ChartName:        metricsServerChartName,
					ChartVersion:     metricsServerVersion,
					ReleaseName:      metricsServerRelease,
					ReleaseNamespace: metricsServerRelease,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			Expect(k8sClient.Create(context.TODO(), prerequisiteProfile)).To(Succeed())
			verifyClusterProfileMatches(prerequisiteProfile)
			prerequisiteSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, prerequisiteProfile.Name,
				&prerequisiteProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Create a ClusterProfile matching Cluster %s/%s, depending on %s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), prerequisiteProfile.Name)
			dependentProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			dependentProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			dependentProfile.Spec.DependsOn = []string{prerequisiteProfile.Name}
			dependentProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    reloaderChartRepoURL,
					RepositoryName:   reloaderChartRepoName,
					ChartName:        reloaderHelmChartName,
					ChartVersion:     reloaderHelmVersion,
					ReleaseName:      reloaderHelmReleaseName,
					ReleaseNamespace: reloaderHelmReleaseName,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			Expect(k8sClient.Create(context.TODO(), dependentProfile)).To(Succeed())
			verifyClusterProfileMatches(dependentProfile)
			dependentSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, dependentProfile.Name,
				&dependentProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", prerequisiteSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), prerequisiteSummary.Name, libsveltosv1beta1.FeatureHelm)

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", dependentSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), dependentSummary.Name, libsveltosv1beta1.FeatureHelm)

			Byf("Deleting prerequisite ClusterProfile %s while dependent ClusterProfile %s is still present",
				prerequisiteProfile.Name, dependentProfile.Name)
			currentPrerequisiteProfile := &configv1beta1.ClusterProfile{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: prerequisiteProfile.Name},
				currentPrerequisiteProfile)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentPrerequisiteProfile)).To(Succeed())

			isBlockedOnDependent := func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: prerequisiteSummary.Namespace, Name: prerequisiteSummary.Name},
					currentClusterSummary)
				if err != nil {
					return false
				}
				return currentClusterSummary.Status.Dependencies != nil &&
					strings.Contains(*currentClusterSummary.Status.Dependencies, dependentSummary.Name)
			}

			Byf("Verifying ClusterSummary %s reports it's waiting on dependent %s",
				prerequisiteSummary.Name, dependentSummary.Name)
			// The reconciler needs at least one pass after the Delete above to observe the
			// dependent and write this status - Eventually here, not Consistently, which would
			// fail on that first, still-stale sample.
			Eventually(isBlockedOnDependent, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying ClusterSummary %s stays present, reporting dependent %s, while %s exists",
				prerequisiteSummary.Name, dependentSummary.Name, dependentProfile.Name)
			Consistently(isBlockedOnDependent, timeout/2, pollingInterval).Should(BeTrue())

			Byf("Deleting dependent ClusterProfile %s", dependentProfile.Name)
			deleteClusterProfile(dependentProfile)

			Byf("Verifying ClusterSummary %s is now gone, freed by dependent %s's removal",
				prerequisiteSummary.Name, dependentProfile.Name)
			Eventually(func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: prerequisiteSummary.Namespace, Name: prerequisiteSummary.Name},
					currentClusterSummary)
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying ClusterProfile %s is now gone", prerequisiteProfile.Name)
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: prerequisiteProfile.Name},
					currentPrerequisiteProfile)
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
