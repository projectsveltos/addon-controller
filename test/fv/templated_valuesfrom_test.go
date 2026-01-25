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
	"fmt"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Helm", func() {
	const (
		namePrefix = "template-valuesfrom-"
	)

	var (
		authMethod = `defaultAuthMethod:
  allowedNamespaces:
  - fraud-dev
  gcp:
    role: "{{ index .Cluster.metadata.annotations.cluster_vso_role }}"
    workloadIdentityServiceAccount: "{{ index .Cluster.metadata.annotations.cluster_vso_wif }}"`
	)

	It("Express ValuesFrom as Template. When instantiated value changes, Sveltos redeploys.",
		Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
			vsoRoleTagKey := "cluster_vso_role"
			vsoRoleTagValue := randomString()

			vsoWifTagKey := "cluster_vso_wif"
			vsoWifTagValue := randomString()

			// Annotation is used to instantiate ConfigMap with labelsValues used in ValuesFrom
			Byf("Add annotation %s: %s on cluster %s/%s",
				vsoRoleTagKey, vsoRoleTagValue, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			setAnnotationOnCluster(vsoRoleTagKey, vsoRoleTagValue)

			// Annotation is used to instantiate ConfigMap with labelsValues used in ValuesFrom
			Byf("Add annotation %s: %s on cluster %s/%s",
				vsoWifTagKey, vsoWifTagValue, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			setAnnotationOnCluster(vsoWifTagKey, vsoWifTagValue)

			namespace := randomString()
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(context.TODO(), ns))

			Byf("Creating ConfigMap to hold helm values")
			authMethodConfigMap := createConfigMapWithPolicy(namespace, fmt.Sprintf("%s-vso-ns-values", kindWorkloadCluster.GetName()),
				authMethod)
			authMethodConfigMap.Annotations = map[string]string{
				libsveltosv1beta1.PolicyTemplateAnnotation: "ok",
			}
			Expect(k8sClient.Create(context.TODO(), authMethodConfigMap)).To(Succeed())

			Byf("Create a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
			Byf("Update ClusterProfile %s to reference cluster in templateResourceRefs", clusterProfile.Name)
			clusterProfile.Spec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
				{
					Resource: corev1.ObjectReference{
						Kind:       kindWorkloadCluster.GetKind(),
						APIVersion: kindWorkloadCluster.GetAPIVersion(),
						Name:       "{{ .Cluster.metadata.name }}",
						Namespace:  "{{ .Cluster.metadata.namespace }}",
					},
					IgnoreStatusChanges: true,
				},
			}
			clusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					ChartName:        "hashicorp/vault-secrets-operator",
					ChartVersion:     "1.2.0",
					ReleaseName:      "vso",
					ReleaseNamespace: randomString(),
					RepositoryName:   "hashicorp",
					RepositoryURL:    "https://helm.releases.hashicorp.com",
					ValuesFrom: []configv1beta1.ValueFrom{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Name:      `{{ .Cluster.metadata.name }}-vso-ns-values`,
							Namespace: namespace,
						},
					},
				},
			}

			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyClusterProfileMatches(clusterProfile)

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				clusterProfile.Name, &clusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

			// Get Helm Value Hash
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)).To(Succeed())
			Expect(len(currentClusterSummary.Status.HelmReleaseSummaries)).To(Equal(1))
			valueHash := currentClusterSummary.Status.HelmReleaseSummaries[0].ValuesHash

			vsoWifTagValue = randomString()
			Byf("Update annotation %s: %s on cluster %s/%s",
				vsoWifTagKey, vsoWifTagValue, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			setAnnotationOnCluster(vsoWifTagKey, vsoWifTagValue)

			Byf("Verifying Helm Chart is redeployed")
			Eventually(func() bool {
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
					currentClusterSummary)
				if err != nil {
					return false
				}
				if len(currentClusterSummary.Status.HelmReleaseSummaries) != 1 {
					return false
				}
				return !reflect.DeepEqual(currentClusterSummary.Status.HelmReleaseSummaries[0].ValuesHash,
					valueHash)
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

			deleteClusterProfile(clusterProfile)

			currentNs := &corev1.Namespace{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
		})
})
