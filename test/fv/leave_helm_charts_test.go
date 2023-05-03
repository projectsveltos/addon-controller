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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	minio = "minio"
)

var _ = Describe("Helm", func() {
	const (
		namePrefix = "leave-helm-charts-"
	)

	It("Deploy helm charts. When Cluster stops matching, policies are left on Cluster", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterProfile.Spec.StopMatchingBehavior = configv1alpha1.LeavePolicies
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1alpha1.HelmChart{
			{
				RepositoryURL:    "https://charts.bitnami.com/bitnami",
				RepositoryName:   "bitnami",
				ChartName:        "bitnami/minio",
				ChartVersion:     "11.10.13",
				ReleaseName:      minio,
				ReleaseNamespace: minio,
				HelmChartAction:  configv1alpha1.HelmChartActionInstall,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying Minio deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "minio", Name: "minio"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1alpha1.FeatureHelm)

		charts := []configv1alpha1.Chart{
			{ReleaseName: minio, ChartVersion: "11.10.13", Namespace: minio},
		}

		verifyClusterConfiguration(clusterProfile.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureHelm, nil, charts)

		Byf("Changing clusterprofile ClusterSelector so Cluster is not a match anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.ClusterSelector = libsveltosv1alpha1.Selector(fmt.Sprintf("%s=%s", key, value+randomString()))
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		Byf("Verifying ClusterSummary is gone")
		Eventually(func() bool {
			_, err = getClusterSummary(context.TODO(),
				clusterProfile.Name, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Minio deployment is still present in the workload cluster")
		depl := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: minio, Name: minio}, depl)).To(Succeed())

		deleteClusterProfile(clusterProfile)
	})
})
