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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

const (
	updateClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: configmap-updater
rules:
- apiGroups: [""]
  #
  # at the HTTP level, the name of the resource for accessing ConfigMap
  # objects is "configmaps"
  resources: ["configmaps"]
  resourceNames: ["my-configmap"]
  verbs: ["update", "get"]`

	allClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: configmap-all
rules:
- apiGroups: [""]
  #
  # at the HTTP level, the name of the resource for accessing ConfigMap
  # objects is "configmaps"
  resources: ["configmaps"]
  resourceNames: ["my-configmap"]
  verbs: ["*"]`
)

var _ = Describe("Feature", func() {
	const (
		namePrefix = "feature"
	)

	It("Deploy and updates resources referenced in ResourceRefs correctly", Label("FV"), func() {
		Byf("Create a ClusterFeature matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterFeature := getClusterfeature(namePrefix, map[string]string{key: value})
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterFeature)).To(Succeed())

		verifyClusterFeatureMatches(clusterFeature)

		verifyClusterSummary(clusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Create a configMap with a ClusterRole")
		configMap := createConfigMapWithPolicy("default", namePrefix+randomString(), updateClusterRole)

		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Update ClusterFeature %s to reference ConfigMap %s/%s", clusterFeature.Name, configMap.Namespace, configMap.Name)
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.PolicyRefs = []corev1.ObjectReference{
			{Kind: configMap.Kind, Namespace: configMap.Namespace, Name: configMap.Name},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		clusterSummary := verifyClusterSummary(currentClusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper ClusterRole is created in the workload cluster")
		Eventually(func() error {
			currentClusterRole := &rbacv1.ClusterRole{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: "configmap-updater"}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatus(clusterSummary.Name, configv1alpha1.FeatureResources, configv1alpha1.FeatureStatusProvisioned)

		policies := []policy{
			{kind: "ClusterRole", name: "configmap-updater", namespace: "", group: "rbac.authorization.k8s.io"},
		}
		verifyClusterConfiguration(clusterFeature.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureResources, policies, nil)

		By("Updating ConfigMap to reference new ClusterRole")
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap, allClusterRole)
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		Byf("Verifying new clusterrole is deployed in the workload cluster")
		Eventually(func() error {
			currentClusterRole := &rbacv1.ClusterRole{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: "configmap-all"}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying old clusterrole is removed from the workload cluster")
		Eventually(func() bool {
			currentClusterRole := &rbacv1.ClusterRole{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: "configmap-updater"}, currentClusterRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		policies = []policy{
			{kind: "ClusterRole", name: "configmap-all", namespace: "", group: "rbac.authorization.k8s.io"},
		}
		verifyClusterConfiguration(clusterFeature.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureResources, policies, nil)

		Byf("Changing clusterfeature to not reference configmap anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.PolicyRefs = []corev1.ObjectReference{}
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		verifyClusterSummary(currentClusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying proper role is removed in the workload cluster")
		Eventually(func() bool {
			currentClusterRole := &rbacv1.ClusterRole{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: "configmap-updater"}, currentClusterRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterFeature(clusterFeature)
	})
})
