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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

const (
	oneTimeNamespace = `apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    name: fv`

	modifiedOneTimeNamespace = `apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    name: fv
	env: prod`
)

var _ = Describe("SyncMode one time", func() {
	const (
		namePrefix = "one-time-"
	)

	It("ClusterProfile with SyncMode oneTime. Policies are deployed only once", Label("FV"), func() {
		oneTimeNamespaceName := randomString()

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create a configMap with a Namespace")
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), fmt.Sprintf(oneTimeNamespace, oneTimeNamespaceName))

		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Kind:      string(configv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Namespace is created in the workload cluster")
		Eventually(func() error {
			currentNamespace := &corev1.Namespace{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: oneTimeNamespaceName}, currentNamespace)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1alpha1.FeatureResources)

		policies := []policy{
			{kind: "Namespace", name: oneTimeNamespaceName, namespace: "", group: ""},
		}
		verifyClusterConfiguration(clusterProfile.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureResources, policies, nil)

		By("Updating content of policy in ConfigMap")
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap, fmt.Sprintf(modifiedOneTimeNamespace, oneTimeNamespaceName))
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		Byf("Verifying Namespace is not updated in the workload cluster")
		Consistently(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: oneTimeNamespaceName}, currentNamespace)
			if err != nil ||
				currentNamespace.Labels == nil {
				return false
			}
			// env is the new label added in the policy contained in the
			// referenced ConfigMap. Since SyncMode is OneTime this change should not be reflected in the
			// CAPI Cluster
			_, ok := currentNamespace.Labels["env"]
			return !ok
		}, timeout/2, pollingInterval).Should(BeTrue())

		Byf("Changing clusterprofile to not reference configmap anymore")
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		// Since SyncMode is OneTime ClusterProfile's changes are not propagated to already existing ClusterSummary.
		Byf("Verifying ClusterSummary still references the ConfigMap")
		currentClusterSummary, err := getClusterSummary(context.TODO(),
			clusterProfile.Name, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		Expect(err).To(BeNil())
		Expect(currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs).ToNot(BeNil())
		Expect(len(currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs)).To(Equal(1))

		deleteClusterProfile(clusterProfile)

		Byf("Verifying Namespace is removed in the workload cluster")
		Eventually(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: oneTimeNamespaceName}, currentNamespace)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
