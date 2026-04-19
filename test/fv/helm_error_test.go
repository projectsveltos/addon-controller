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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

var (
	certManagerValues = `apiVersion: v1
kind: ConfigMap
metadata:
  name: cert-manager-values
  namespace: default
data:
  values: |
    crds:
      enabled: true`

	certManagerIncorrectValues = `apiVersion: v1
kind: ConfigMap
metadata:
  name: cert-manager-values
  namespace: default
data:
  values: |
    crds:
      enabled: true
      replicas: 2`
)

var _ = Describe("Feature", Serial, func() {
	const (
		namePrefix  = "helm-error-"
		certManager = "cert-manager"
	)

	It("An error in helm values does not remove helm chart", Label("FV", "PULLMODE"), func() {
		Byf("Create a configMap with valid helm values")
		configMap, err := k8s_utils.GetUnstructured([]byte(certManagerValues))
		Expect(err).To(BeNil())
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.GetNamespace(),
			kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to deploy helm charts and referencing ConfigMap in ValuesFrom %s/%s",
			clusterProfile.Name, configMap.GetNamespace(), configMap.GetName())

		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name},
				currentClusterProfile)).To(Succeed())

			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://charts.jetstack.io",
					RepositoryName:   "jetstack",
					ChartName:        "jetstack/cert-manager",
					ChartVersion:     "v1.19.4",
					ReleaseName:      "cert-manager",
					ReleaseNamespace: "cert-manager",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					ValuesFrom: []configv1beta1.ValueFrom{
						{
							Namespace: configMap.GetNamespace(),
							Name:      configMap.GetName(),
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
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

		Byf("Verifying cert-manager deployment is created in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: certManager, Name: certManager}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		By("Change ConfigMap with HelmValues to contain incorrect values")
		incorrectValuesConfigMap, err := k8s_utils.GetUnstructured([]byte(certManagerIncorrectValues))
		Expect(err).To(BeNil())

		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.GetNamespace(), Name: configMap.GetName()},
			currentConfigMap)).To(Succeed())
		incorrectValuesConfigMap.SetResourceVersion(currentConfigMap.GetResourceVersion())
		Expect(k8sClient.Update(context.TODO(), incorrectValuesConfigMap))

		Byf("Verifying ClusterSummary reports the error")
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
					return currentClusterSummary.Status.FeatureSummaries[i].FailureMessage != nil
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying cert-manager deployment is still present in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: certManager, Name: certManager}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		By("Change ConfigMap with HelmValues to contain correct values")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.GetNamespace(), Name: configMap.GetName()},
			currentConfigMap)).To(Succeed())
		configMap.SetResourceVersion(currentConfigMap.GetResourceVersion())
		Expect(k8sClient.Update(context.TODO(), configMap))

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Verifying cert-manager deployment is still present in the workload cluster")
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: certManager, Name: certManager}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		deleteClusterProfile(clusterProfile)

		Byf("Verifying cert-manager deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: certManager, Name: certManager}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.GetNamespace(), Name: configMap.GetName()},
			currentConfigMap)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentConfigMap))
	})
})
