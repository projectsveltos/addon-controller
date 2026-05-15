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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Missing TemplateResourceRef", func() {
	const (
		namePrefix = "missing-tmpl-ref-"
	)

	It("Reports failure when a required templateResourceRef is missing, recovers when the resource is created",
		Label("FV", "PULLMODE", "EXTENDED"), func() {

			Byf("Create a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyClusterProfileMatches(clusterProfile)

			verifyClusterSummary(clusterops.ClusterProfileLabelName,
				clusterProfile.Name, &clusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			// Create the ConfigMap that will be referenced by PolicyRefs (so FeatureResources is tracked)
			configMapNs := randomString()
			Byf("Create configMap namespace %s in the management cluster", configMapNs)
			cmNsObj := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: configMapNs},
			}
			Expect(k8sClient.Create(context.TODO(), cmNsObj)).To(Succeed())

			namespaceName := randomString()
			configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
				fmt.Sprintf(namespace, namespaceName))
			Byf("Creating ConfigMap %s/%s to be deployed via PolicyRefs", configMapNs, configMap.Name)
			Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

			// A Secret that will be referenced as a required templateResourceRef, but does not exist yet
			secretNsName := randomString()
			secretName := randomString()

			Byf("Updating ClusterProfile %s with templateResourceRefs pointing to missing Secret %s/%s",
				clusterProfile.Name, secretNsName, secretName)
			currentClusterProfile := &configv1beta1.ClusterProfile{}
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
					{
						Resource: corev1.ObjectReference{
							Kind:       "Secret",
							APIVersion: "v1",
							Namespace:  secretNsName,
							Name:       secretName,
						},
						Identifier: "MySecret",
						Optional:   false,
					},
				}
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

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			By("Verify ClusterSummary reports failure for missing required templateResourceRef")
			Eventually(func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
					currentClusterSummary)
				if err != nil {
					return false
				}
				for i := range currentClusterSummary.Status.FeatureSummaries {
					fs := currentClusterSummary.Status.FeatureSummaries[i]
					if fs.FeatureID == libsveltosv1beta1.FeatureResources &&
						fs.Status == libsveltosv1beta1.FeatureStatusFailedNonRetriable &&
						fs.FailureMessage != nil &&
						strings.Contains(*fs.FailureMessage, secretName) {

						return true
					}
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue())

			// Create the namespace and Secret so the templateResourceRef can be resolved
			Byf("Creating namespace %s in the management cluster for the Secret", secretNsName)
			secretNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: secretNsName},
			}
			Expect(k8sClient.Create(context.TODO(), secretNs)).To(Succeed())

			Byf("Creating Secret %s/%s in the management cluster", secretNsName, secretName)
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: secretNsName,
					Name:      secretName,
				},
				Data: map[string][]byte{"key": []byte("value")},
			}
			Expect(k8sClient.Create(context.TODO(), secret)).To(Succeed())

			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			Byf("Verifying Namespace %s is created in the workload cluster after Secret is available", namespaceName)
			Eventually(func() error {
				currentNamespace := &corev1.Namespace{}
				return workloadClient.Get(context.TODO(), types.NamespacedName{Name: namespaceName}, currentNamespace)
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying ClusterSummary %s status is set to Provisioned for Resources feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

			deleteClusterProfile(clusterProfile)

			Byf("Cleaning up namespaces created in the management cluster")
			currentNs := &corev1.Namespace{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: secretNsName}, currentNs)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
		})
})
