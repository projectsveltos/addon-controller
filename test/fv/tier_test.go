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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Helm", Serial, func() {
	const (
		namePrefix = "tier-"
	)

	It("Use tier to solve conflicts", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix+"first-", map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    kyvernoRepoURL,
					RepositoryName:   kyvernoNamespace,
					ChartName:        kyvernoChartName,
					ChartVersion:     kyvernoVersion372,
					ReleaseName:      kyvernoLatestRelease,
					ReleaseNamespace: kyvernoNamespace,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    prometheusCommunityURL,
					RepositoryName:   prometheusCommunityName,
					ChartName:        prometheusChartName,
					ChartVersion:     prometheusVersion2739,
					ReleaseName:      prometheusRelease,
					ReleaseNamespace: prometheusRelease,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    "https://grafana.github.io/helm-charts",
					RepositoryName:   grafanaRepoName,
					ChartName:        grafanaChartName,
					ChartVersion:     grafanaVersion1000,
					ReleaseName:      grafanaRepoName,
					ReleaseNamespace: grafanaRepoName,
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

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying kyverno deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kyvernoNamespace, Name: admissionControllerDeplName}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		charts := []configv1beta1.Chart{
			{ReleaseName: kyvernoLatestRelease, ChartVersion: kyvernoVersion372S, Namespace: kyvernoNamespace},
			{ReleaseName: grafanaRepoName, ChartVersion: grafanaVersion1000, Namespace: grafanaRepoName},
			{ReleaseName: prometheusRelease, ChartVersion: prometheusVersion2739, Namespace: prometheusRelease},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		Byf("Creating another ClusterProfile matching the cluster")
		newClusterProfile := getClusterProfile(namePrefix+"second-", map[string]string{key: value})
		newClusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), newClusterProfile)).To(Succeed())

		verifyClusterProfileMatches(newClusterProfile)

		Byf("Configuring the new clusterProfile to deploy Kyverno")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: newClusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    kyvernoRepoURL,
				RepositoryName:   kyvernoNamespace,
				ChartName:        kyvernoChartName,
				ChartVersion:     kyvernoVersion371,
				ReleaseName:      kyvernoLatestRelease,
				ReleaseNamespace: kyvernoNamespace,
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		newClusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		// Creation of a second clusterProfile (with higher tier) does not affect first profile with better tier
		Byf("Verifying (first profile) ClusterSummary %s status is still set to Deployed for Helm feature", clusterSummary.Name)
		const five = 5
		for range five {
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
			time.Sleep(five * time.Second)
		}

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

		// Creation of a second clusterProfile (with higher tier) does not affect first profile with better tier
		Byf("Verifying (first profile) ClusterSummary %s status is still set to Deployed for Helm feature", clusterSummary.Name)
		for range five {
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
			time.Sleep(five * time.Second)
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		Byf("Changing ClusterProfile %s tier", newClusterProfile.Name)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: newClusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.Tier = 50
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

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
			{ReleaseName: grafanaRepoName, ChartVersion: grafanaVersion1000, Namespace: grafanaRepoName},
			{ReleaseName: prometheusRelease, ChartVersion: prometheusVersion2739, Namespace: prometheusRelease},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		charts = []configv1beta1.Chart{
			{ReleaseName: kyvernoLatestRelease, ChartVersion: kyvernoVersion371S, Namespace: kyvernoNamespace},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, newClusterProfile.Name,
			newClusterSummary.Spec.ClusterNamespace, newClusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		Byf("Changing ClusterProfile %s tier to 100 (above profile1 tier 90); profile1 should reclaim kyverno", newClusterProfile.Name)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: newClusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.Tier = 100
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterProfile %s ClusterSummary reports conflict for kyverno", newClusterProfile.Name)
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

		Byf("Verifying ClusterSummary %s is Provisioned for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		charts = []configv1beta1.Chart{
			{ReleaseName: kyvernoLatestRelease, ChartVersion: kyvernoVersion372S, Namespace: kyvernoNamespace},
			{ReleaseName: grafanaRepoName, ChartVersion: grafanaVersion1000, Namespace: grafanaRepoName},
			{ReleaseName: prometheusRelease, ChartVersion: prometheusVersion2739, Namespace: prometheusRelease},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		deleteClusterProfile(clusterProfile)
		deleteClusterProfile(newClusterProfile)

		Byf("Verifying kyverno deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kyvernoNamespace, Name: kyvernoLatestRelease}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
