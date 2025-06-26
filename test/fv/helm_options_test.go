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
	"strings"

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

var _ = Describe("HelmOptions", func() {
	const (
		namePrefix = "helm-options-"
	)

	It("Deploy and updates helm charts with options correctly", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.GetNamespace(),
			kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://kubernetes-sigs.github.io/external-dns/",
					RepositoryName:   "external-dns",
					ChartName:        "external-dns/external-dns",
					ChartVersion:     "1.17.0",
					ReleaseName:      "external-dns",
					ReleaseNamespace: "external-dns",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Options: &configv1beta1.HelmOptions{
						DependencyUpdate: true,
						InstallOptions: configv1beta1.HelmInstallOptions{
							Replace: false,
						},
					},
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

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying external-dns deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "external-dns", Name: "external-dns"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying external-dns deployment image")
		depl := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "external-dns", Name: "external-dns"}, depl)).To(Succeed())
		Expect(len(depl.Spec.Template.Spec.Containers)).To(Equal(1))
		Expect(depl.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("v0.17.0"))

		if !isPullMode() {
			charts := []configv1beta1.Chart{
				{ReleaseName: "external-dns", ChartVersion: "1.17.0", Namespace: "external-dns"},
			}

			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)
		}

		Byf("Update ClusterProfile %s to upgrade external-dns helm charts", clusterProfile.Name)

		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://kubernetes-sigs.github.io/external-dns/",
					RepositoryName:   "external-dns",
					ChartName:        "external-dns/external-dns",
					ChartVersion:     "1.14.4",
					ReleaseName:      "external-dns",
					ReleaseNamespace: "external-dns",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Options: &configv1beta1.HelmOptions{
						DependencyUpdate: true,
						UpgradeOptions: configv1beta1.HelmUpgradeOptions{
							ResetValues: false,
							MaxHistory:  5,
						},
					},
				},
			}

			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying external-dns deployment is upgraded in the workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "external-dns", Name: "external-dns"}, depl)
			if err != nil {
				return false
			}
			if len(depl.Spec.Template.Spec.Containers) != 1 {
				return false
			}
			return strings.Contains(depl.Spec.Template.Spec.Containers[0].Image, "v0.14.1")
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Update ClusterProfile %s to uninstall external-dns helm charts", clusterProfile.Name)

		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://kubernetes-sigs.github.io/external-dns/",
					RepositoryName:   "external-dns",
					ChartName:        "external-dns/external-dns",
					ChartVersion:     "1.14.4",
					ReleaseName:      "external-dns",
					ReleaseNamespace: "external-dns",
					HelmChartAction:  configv1beta1.HelmChartActionUninstall,
					Options: &configv1beta1.HelmOptions{
						DependencyUpdate: true,
						UninstallOptions: configv1beta1.HelmUninstallOptions{
							KeepHistory:         true,
							DeletionPropagation: "background",
						},
					},
				},
			}

			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Verifying external-dns deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "external-dns", Name: "external-dns"}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
