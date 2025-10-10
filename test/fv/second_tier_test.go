/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	resource = `apiVersion: v1
kind: ServiceAccount
metadata:
  namespace: %s
  name: %s
  labels:
    %s: %s`
)

var _ = Describe("PolicyRef Tier", func() {
	const (
		namePrefix = "second-tier-"
	)

	// TODO: Fix sveltos-applier and enable this test for pullmode as well
	It("ClusterProfile referencing two ConfigMaps both containing same resource", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		saNamespace := randomString()
		saName := randomString()

		firstConfigMapLabelKey := randomString()
		firstConfigMapLabelValue := randomString()

		secondConfigMapLabelKey := randomString()
		secondConfigMapLabelValue := randomString()

		Byf("Create a first ConfigMap with a ServiceAccount")
		firstConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
			fmt.Sprintf(resource, saNamespace, saName, firstConfigMapLabelKey, firstConfigMapLabelValue))
		Expect(k8sClient.Create(context.TODO(), firstConfigMap)).To(Succeed())

		Byf("Create a second ConfigMap with same ServiceAccount")
		secondConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
			fmt.Sprintf(resource, saNamespace, saName, secondConfigMapLabelKey, secondConfigMapLabelValue))
		Expect(k8sClient.Create(context.TODO(), secondConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s",
			clusterProfile.Name, firstConfigMap.Namespace, firstConfigMap.Name)
		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s",
			clusterProfile.Name, secondConfigMap.Namespace, secondConfigMap.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: firstConfigMap.Namespace,
					Name:      firstConfigMap.Name,
				},
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: secondConfigMap.Namespace,
					Name:      secondConfigMap.Name,
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

		Byf("Verifying proper ServiceAccount is created in the workload cluster")
		Eventually(func() error {
			currentServiceAccount := &corev1.ServiceAccount{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: saNamespace, Name: saName},
				currentServiceAccount)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ServicdAccount has proper labels")
		currentServiceAccount := &corev1.ServiceAccount{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: saNamespace, Name: saName},
			currentServiceAccount)).To(Succeed())
		Expect(currentServiceAccount.Labels).ToNot(BeNil())
		v, ok := currentServiceAccount.Labels[firstConfigMapLabelKey]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(firstConfigMapLabelValue))

		Byf("Verifying ClusterSummary %s status reports conflict for Resources feature", clusterSummary.Name)
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureResources {
					if currentClusterSummary.Status.FeatureSummaries[i].FailureMessage != nil {
						return strings.Contains(*currentClusterSummary.Status.FeatureSummaries[i].FailureMessage,
							"A conflict was detected while deploying resource")
					}
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		By("Updating second ConfigMap tier")
		const lowerTier = 90
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: firstConfigMap.Namespace,
					Name:      firstConfigMap.Name,
				},
				{
					Tier:      lowerTier,
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: secondConfigMap.Namespace,
					Name:      secondConfigMap.Name,
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		Byf("Verifying proper ServiceAccount is still present in the workload cluster with correct labels")
		Eventually(func() bool {
			currentServiceAccount := &corev1.ServiceAccount{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: saNamespace, Name: saName},
				currentServiceAccount)
			if err != nil {
				return false
			}

			if currentServiceAccount.Labels == nil {
				return false
			}
			v, ok = currentServiceAccount.Labels[secondConfigMapLabelKey]
			return ok && v == secondConfigMapLabelValue
		}, timeout, pollingInterval).Should(BeTrue())

		policies := []policy{
			{kind: "ServiceAccount", name: saName, namespace: saNamespace, group: ""},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureResources,
			policies, nil)

		deleteClusterProfile(clusterProfile)

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())

		Byf("Verifying proper ServiceAccount is removed from the workload cluster")
		Eventually(func() bool {
			currentServiceAccount := &corev1.ServiceAccount{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: saNamespace, Name: saName},
				currentServiceAccount)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
