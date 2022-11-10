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
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

var _ = Describe("Helm with conflicts", Serial, func() {
	const (
		namePrefix = "helm-conflict-"
	)

	BeforeEach(func() {
		// Set Cluster unpaused
		currentCluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name},
			currentCluster)).To(Succeed())

		currentCluster.Spec.Paused = false

		Expect(k8sClient.Update(context.TODO(), currentCluster)).To(Succeed())
	})

	AfterEach(func() {
		// Set Cluster unpaused and reset labels

		currentCluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name},
			currentCluster)).To(Succeed())

		currentLabels := currentCluster.Labels
		if currentLabels == nil {
			currentLabels = make(map[string]string)
		}
		currentLabels[key] = value
		currentCluster.Labels = currentLabels
		currentCluster.Spec.Paused = false

		Expect(k8sClient.Update(context.TODO(), currentCluster)).To(Succeed())
	})

	It("Two ClusterProfiles managing same helm chart on same cluster", Label("FV"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())
		Byf("Created ClusterProfile %s", clusterProfile.Name)

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		influxDBVersion := "5.4.5"
		addInfluxdbHelmChart(clusterProfile.Name, influxDBVersion)

		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying Influxdb deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "influxdb", Name: "influxdb"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		charts := []configv1alpha1.Chart{
			{ReleaseName: "influxdb", ChartVersion: influxDBVersion, Namespace: "influxdb"},
		}
		verifyClusterConfiguration(clusterProfile.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureHelm, nil, charts)

		By("Creating a second ClusterProfile which conflicts with first ClusterProfile")
		newValue := value + randomString()
		clusterProfile2 := getClusterProfile(namePrefix, map[string]string{key: newValue})
		clusterProfile2.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile2)).To(Succeed())
		Byf("Created ClusterProfile %s", clusterProfile2.Name)

		newInfluxDBVersion := "5.4.6"
		addInfluxdbHelmChart(clusterProfile2.Name, newInfluxDBVersion)

		// Change cluster label
		Byf("Change Cluster labels so to stop matching ClusterProfile %s and start matching second ClusterProfile %s",
			clusterProfile.Name, clusterProfile2.Name)
		currentCluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name},
			currentCluster)).To(Succeed())

		currentLabels := currentCluster.Labels
		if currentLabels == nil {
			currentLabels = make(map[string]string)
		}
		currentLabels[key] = newValue
		Expect(k8sClient.Update(context.TODO(), currentCluster)).To(Succeed())

		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile2.Name}, currentClusterProfile)).To(Succeed())
		clusterSummary = verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying influxdb deployment is still in the workload cluster")
		Consistently(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "influxdb", Name: "influxdb"}, depl)
		}, timeout/2, pollingInterval).Should(BeNil())

		charts = []configv1alpha1.Chart{
			{ReleaseName: "influxdb", ChartVersion: newInfluxDBVersion, Namespace: "influxdb"},
		}
		verifyClusterConfiguration(clusterProfile2.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureHelm, nil, charts)

		Byf("Deleting clusterProfile %s", clusterProfile.Name)
		deleteClusterProfile(clusterProfile)

		Byf("Deleting clusterProfile %s", clusterProfile2.Name)
		deleteClusterProfile(clusterProfile2)

		Byf("Verifying influxdb deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "influxdb", Name: "influxdb"}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func addInfluxdbHelmChart(clusterProfileName, version string) {
	Byf("Update ClusterProfile %s to deploy influxdb helm charts", clusterProfileName)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfileName}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1alpha1.HelmChart{
			{
				RepositoryURL:    "https://charts.bitnami.com/bitnami",
				RepositoryName:   "bitnami",
				ChartName:        "bitnami/influxdb",
				ChartVersion:     version,
				ReleaseName:      "influxdb",
				ReleaseNamespace: "influxdb",
				HelmChartAction:  configv1alpha1.HelmChartActionInstall,
			},
		}

		return k8sClient.Update(context.TODO(), currentClusterProfile)
	})

	Expect(err).To(BeNil())
}
