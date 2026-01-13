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
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("HelmOptions", func() {
	const (
		namePrefix = "pre-delete-"
	)

	var (
		luaEvaluateDNSEndpointHealth = `function evaluate()
    hs = {}
    hs.status = "Healthy"
    hs.message = "No endpoints remaining"

    -- Check if the spec and endpoints array exist
    if obj.spec ~= nil and obj.spec.endpoints ~= nil then
        -- If the table/array has more than 0 elements, it's not "empty"
        if #obj.spec.endpoints > 0 then
            hs.status = "Progressing"
            hs.message = "Waiting for endpoints to be cleared; " .. #obj.spec.endpoints .. " remaining"
            return hs
        end
    end

    return hs
end`

		dnsEndpoint = `apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: %s
spec:
  endpoints:
  - dnsName: "api.example.com"
    recordTTL: 300
    recordType: A
    targets:
    - "1.2.3.4"
  - dnsName: "docs.example.com"
    recordType: CNAME
    targets:
    - "gh-pages.github.com"`
	)

	// TODO: Add to NEW-PULLMODE as well once sveltos-applier is taken care of
	It("Pre Delete checks blocks an uninstall", Label("NEW-FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.GetNamespace(),
			kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to deploy helm charts with a pre delete checks", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://kubernetes-sigs.github.io/external-dns/",
					RepositoryName:   "external-dns",
					ChartName:        "external-dns/external-dns",
					ChartVersion:     "1.20.0",
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
			currentClusterProfile.Spec.PreDeleteChecks = []libsveltosv1beta1.ValidateHealth{
				{
					Name:      "dns-endpoint-gone",
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Group:     "externaldns.k8s.io",
					Version:   "v1alpha1",
					Kind:      "DNSEndpoint",
					Script:    luaEvaluateDNSEndpointHealth,
				},
			}

			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

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

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create a configMap with a DNSEndpoint")
		dnsEndpointName := randomString()
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
			fmt.Sprintf(dnsEndpoint, dnsEndpointName))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary = verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		Byf("Verifying proper DNSEndpoint is created in the workload cluster")
		remoteConfig, err := getKindWorkloadClusterRestConfig()
		Expect(err).To(BeNil())
		Expect(remoteConfig).ToNot(BeNil())

		dynamicClient, err := dynamic.NewForConfig(remoteConfig)
		Expect(err).To(BeNil())
		Expect(dynamicClient).ToNot(BeNil())

		gvr := schema.GroupVersionResource{
			Group:    "externaldns.k8s.io",
			Version:  "v1alpha1",
			Resource: "dnsendpoints",
		}
		dnsEndpointList, err := dynamicClient.Resource(gvr).Namespace("").List(context.TODO(), metav1.ListOptions{})
		Expect(err).To(BeNil())
		Expect(len(dnsEndpointList.Items)).To(Equal(1))

		Byf("Update ClusterProfile %s to not reference the helm charts", clusterProfile.Name)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Veryfing helm chart is not uninstalled")
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm &&
					currentClusterSummary.Status.FeatureSummaries[i].Status == libsveltosv1beta1.FeatureStatusFailed &&
					currentClusterSummary.Status.FeatureSummaries[i].FailureMessage != nil &&
					strings.Contains(*currentClusterSummary.Status.FeatureSummaries[i].FailureMessage, "Waiting for endpoints to be cleared") {

					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Update ClusterProfile %s to not reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{}
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

		clusterSummary = verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		deleteClusterProfile(clusterProfile)
	})
})
