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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	serviceAccountTemplate = `  apiVersion: v1
  kind: ServiceAccount
  metadata:
    namespace: %s
    name: %s`
)

var _ = Describe("Template Instantiation Error", func() {
	const (
		namePrefix = "template-error"
	)

	It("Template Instantiation errors are reported as non retriable", Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
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

		serviceAccount := randomString()

		Byf("Create a configMap with Service")
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
			fmt.Sprintf(serviceAccountTemplate, configMapNs, serviceAccount),
		)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s with an incorrect template in PolicyRefs", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: "{{ nonexistingfunction .Cluster.metadata.namespace }}",
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

		By("Verify error is reported on the ClusterSummary Status")
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			if len(currentClusterSummary.Status.FeatureSummaries) == 0 {
				return false
			}
			if currentClusterSummary.Status.FeatureSummaries[0].Status !=
				libsveltosv1beta1.FeatureStatusFailedNonRetriable {
				return false
			}
			if currentClusterSummary.Status.FeatureSummaries[0].FailureMessage == nil {
				return false
			}
			return strings.Contains(*currentClusterSummary.Status.FeatureSummaries[0].FailureMessage,
				"function \"nonexistingfunction\" not defined")
		}, timeout, pollingInterval).Should(BeTrue())

		By("Correct the ClusterProfile")
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

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying ServiceAccount %s is created in the workload cluster", serviceAccount)
		Eventually(func() error {
			currentServiceAccount := &corev1.ServiceAccount{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: configMapNs, Name: serviceAccount},
				currentServiceAccount)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		deleteClusterProfile(clusterProfile)

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})
