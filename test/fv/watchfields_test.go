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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// watchFieldsPolicy is a template that renders a ConfigMap on the workload cluster
// whose data.watchedValue is taken from the management-cluster resource identified
// as "RefData". Only "data.watched" is listed in WatchFields so changes to other
// keys in that ConfigMap do not affect the hash.
const watchFieldsPolicy = `apiVersion: v1
kind: ConfigMap
metadata:
  name: watchfields-result
  namespace: default
data:
  watchedValue: "{{ (index .MgmtResources "RefData").data.watched }}"`

var _ = Describe("WatchFields", func() {
	const (
		namePrefix = "watchfields-"
	)

	It("Non-watched field change does not trigger redeployment", Label("FV", "PULLMODE", "EXTENDED"), func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
		Byf("Create namespace %s for test resources", ns.Name)
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create reference ConfigMap with a watched and an unwatched field")
		refCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      namePrefix + randomString(),
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"watched":   "value1",
				"unwatched": "extra",
			},
		}
		Expect(k8sClient.Create(context.TODO(), refCM)).To(Succeed())

		Byf("Create policy ConfigMap containing a template that references the watched field")
		policyConfigMap := createConfigMapWithPolicy(ns.Name, namePrefix+randomString(), watchFieldsPolicy)
		policyConfigMap.Annotations = map[string]string{
			libsveltosv1beta1.PolicyTemplateAnnotation: "ok",
		}
		Expect(k8sClient.Create(context.TODO(), policyConfigMap)).To(Succeed())

		Byf("Create ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
			{
				Resource: corev1.ObjectReference{
					APIVersion: "v1",
					Kind:       "ConfigMap",
					Namespace:  refCM.Namespace,
					Name:       refCM.Name,
				},
				Identifier:  "RefData",
				WatchFields: []string{"data.watched"},
			},
		}
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: policyConfigMap.Namespace,
				Name:      policyConfigMap.Name,
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s is Provisioned", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
			libsveltosv1beta1.FeatureResources)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying result ConfigMap is deployed on workload cluster with correct watched value")
		Eventually(func() bool {
			result := &corev1.ConfigMap{}
			err := workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: defaultNamespace, Name: "watchfields-result"}, result)
			return err == nil && result.Data["watchedValue"] == "value1"
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Updating non-watched field in the referenced ConfigMap")
		currentRefCM := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: refCM.Namespace, Name: refCM.Name}, currentRefCM)).To(Succeed())
		currentRefCM.Data["unwatched"] = "changed"
		Expect(k8sClient.Update(context.TODO(), currentRefCM)).To(Succeed())

		Byf("Verifying ClusterSummary %s stays Provisioned — hash must not change", clusterSummary.Name)
		Consistently(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{
					Namespace: kindWorkloadCluster.GetNamespace(),
					Name:      clusterSummary.Name,
				},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureResources {
					return currentClusterSummary.Status.FeatureSummaries[i].Status == libsveltosv1beta1.FeatureStatusProvisioned
				}
			}
			return false
		}, timeout/4, pollingInterval).Should(BeTrue())

		Byf("Verifying workload cluster ConfigMap still shows the original watched value")
		result := &corev1.ConfigMap{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: defaultNamespace, Name: "watchfields-result"}, result)).To(Succeed())
		Expect(result.Data["watchedValue"]).To(Equal("value1"))

		deleteClusterProfile(clusterProfile)
	})
})
