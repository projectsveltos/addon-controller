/*
Copyright 2022. projectsveltos.io. All rights reserved.

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
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Helm with conflicts", func() {
	const (
		namePrefix = "helm-conflict-"
	)

	It("Two ClusterProfiles managing same helm chart on same cluster", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())
		Byf("Created ClusterProfile %s", clusterProfile.Name)

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		sparkVersion := "9.3.0"
		addSparkHelmChart(clusterProfile.Name, sparkVersion)

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying Spark statefuleset is created in the workload cluster")
		Eventually(func() error {
			statefulSet := &appsv1.StatefulSet{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "spark", Name: "spark-master"}, statefulSet)
		}, timeout, pollingInterval).Should(BeNil())

		if !isPullMode() {
			charts := []configv1beta1.Chart{
				{ReleaseName: "spark", ChartVersion: sparkVersion, Namespace: "spark"},
			}
			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)
		}

		By("Creating a second ClusterProfile which conflicts with first ClusterProfile")
		clusterProfile2 := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile2.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile2)).To(Succeed())
		Byf("Created ClusterProfile %s", clusterProfile2.Name)

		addSparkHelmChart(clusterProfile2.Name, sparkVersion)

		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile2.Name}, currentClusterProfile)).To(Succeed())
		clusterSummary = verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Deleting clusterProfile %s", clusterProfile.Name)
		deleteClusterProfile(clusterProfile)

		if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
			// In pull-mode handing over to old conflicting profile is a best effort try.
			// So verify StatefulSet eventually is there
			Byf("Verifying spark statefulset is still in the workload cluster")
			Eventually(func() error {
				statefulSet := &appsv1.StatefulSet{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "spark", Name: "spark-master"}, statefulSet)
			}, timeout/2, pollingInterval).Should(BeNil())
		} else {
			// Since second ClusterProfile is waiting to manage same helm chart, it should not be ever
			// uninstalled when first ClusterProfile is deleted
			Byf("Verifying spark statefulset is still in the workload cluster")
			Consistently(func() error {
				statefulSet := &appsv1.StatefulSet{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "spark", Name: "spark-master"}, statefulSet)
			}, timeout/2, pollingInterval).Should(BeNil())
		}

		if !isPullMode() {
			charts := []configv1beta1.Chart{
				{ReleaseName: "spark", ChartVersion: sparkVersion, Namespace: "spark"},
			}
			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile2.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)
		}

		Byf("Deleting clusterProfile %s", clusterProfile2.Name)
		deleteClusterProfile(clusterProfile2)

		Byf("Verifying spark deployment is removed from workload cluster")
		Eventually(func() bool {
			statefulSet := &appsv1.StatefulSet{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "spark", Name: "spark-master"}, statefulSet)
			if err == nil {
				return !statefulSet.DeletionTimestamp.IsZero()
			}
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func addSparkHelmChart(clusterProfileName, version string) {
	Byf("Update ClusterProfile %s to deploy spark helm charts", clusterProfileName)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfileName}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://charts.bitnami.com/bitnami",
				RepositoryName:   "bitnami",
				ChartName:        "bitnami/spark",
				ChartVersion:     version,
				ReleaseName:      "spark",
				ReleaseNamespace: "spark",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}

		return k8sClient.Update(context.TODO(), currentClusterProfile)
	})

	Expect(err).To(BeNil())
}
