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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Helm", func() {
	const (
		namePrefix = "helm-"

		luaEvaluateDeploymentHealth = `function evaluate()
	hs = {}
	hs.healthy = false
	hs.message = "available replicas not matching requested replicas"
	if obj.status ~= nil then
		if obj.status.availableReplicas ~= nil then
			if obj.status.availableReplicas == obj.spec.replicas then
				hs.healthy = true
			end
		end
	end
	return hs
	end`
	)

	It("Deploy and updates helm charts correctly", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
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
					RepositoryURL:    "https://kyverno.github.io/kyverno/",
					RepositoryName:   "kyverno",
					ChartName:        "kyverno/kyverno",
					ChartVersion:     "v3.3.4",
					ReleaseName:      "kyverno-latest",
					ReleaseNamespace: "kyverno",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    "https://docs.wildfly.org/wildfly-charts/",
					RepositoryName:   "wildfly",
					ChartName:        "wildfly/wildfly",
					ChartVersion:     "2.4.0",
					ReleaseName:      "wildfly",
					ReleaseNamespace: "wildfly",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}

			currentClusterProfile.Spec.ValidateHealths = []libsveltosv1beta1.ValidateHealth{
				{
					Name:      "kyverno-deployment-health",
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Namespace: "kyverno",
					Group:     "apps",
					Version:   "v1",
					Kind:      "Deployment",
					Script:    luaEvaluateDeploymentHealth,
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

		Byf("Verifying kyverno deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying kyverno deployment has proper labels/annotations")
		depl := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl))
		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())
		var u unstructured.Unstructured
		u.SetUnstructuredContent(content)

		Byf("Verifying wildfly deployment is created in the workload cluster")
		Eventually(func() error {
			depl = &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "wildfly", Name: "wildfly"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		if !isPullMode() {
			charts := []configv1beta1.Chart{
				{ReleaseName: "kyverno-latest", ChartVersion: "3.3.4", Namespace: "kyverno"},
				{ReleaseName: "wildfly", ChartVersion: "2.4.0", Namespace: "wildfly"},
			}

			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)
		}

		Byf("Changing ClusterProfile requiring different chart version for kyverno and update extra labels")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.3.3",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "kyverno",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
			{
				RepositoryURL:    "https://docs.wildfly.org/wildfly-charts/",
				RepositoryName:   "wildfly",
				ChartName:        "wildfly/wildfly",
				ChartVersion:     "2.4.0",
				ReleaseName:      "wildfly",
				ReleaseNamespace: "wildfly",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}

		extraLabelKey := randomString()
		extraLabelValue := randomString()

		currentClusterProfile.Spec.Patches = []libsveltosv1beta1.Patch{
			{
				Target: &libsveltosv1beta1.PatchSelector{
					Kind: "Deployment",
					Name: ".*",
				},
				Patch: fmt.Sprintf(`- op: add
  path: /metadata/labels/%s
  value: %s`, extraLabelKey, extraLabelValue),
			},
		}

		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying kyverno deployment is still present in the workload cluster")
		Eventually(func() error {
			depl = &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying wildfly deployment is still in the workload cluster")
		Eventually(func() error {
			depl = &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "wildfly", Name: "wildfly"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		if !isPullMode() {
			charts := []configv1beta1.Chart{
				{ReleaseName: "kyverno-latest", ChartVersion: "3.3.3", Namespace: "kyverno"},
				{ReleaseName: "wildfly", ChartVersion: "2.4.0", Namespace: "wildfly"},
			}

			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)
		}

		Byf("Verifying kyverno deployment has proper labels/annotations")
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl))
		content, err = runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())
		u.SetUnstructuredContent(content)
		lbls := u.GetLabels()
		Expect(lbls).ToNot(BeNil())
		v := lbls[extraLabelKey]
		Expect(v).To(Equal(extraLabelValue))

		Byf("Changing ClusterProfile to not require wildfly anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.3.3",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "kyverno",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying kyverno deployment is still present in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying wildfly deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "wildfly", Name: "wildfly"}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		if !isPullMode() {
			charts := []configv1beta1.Chart{
				{ReleaseName: "kyverno-latest", ChartVersion: "3.3.3", Namespace: "kyverno"},
			}

			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)
		}

		deleteClusterProfile(clusterProfile)

		Byf("Verifying kyverno deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-latest"}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
