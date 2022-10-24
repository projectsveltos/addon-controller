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
	"encoding/base64"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
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

	demoPod = `apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    environment: production
    app: nginx
spec:
  containers:
  - name: nginx
    image: nginx:1.14.2`
)

var _ = Describe("Feature", func() {
	const (
		namePrefix = "feature"
	)

	It("Deploy and updates resources referenced in ResourceRefs correctly", Label("FV"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterprofile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create a configMap with a ClusterRole")
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), updateClusterRole)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		podName := "demo" + randomString()
		Byf("Create a secret with a Pod")
		secret := createSecretWithPolicy(configMapNs, namePrefix+randomString(), fmt.Sprintf(demoPod, podName))
		Expect(k8sClient.Create(context.TODO(), secret)).To(Succeed())
		currentSecret := &corev1.Secret{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, currentSecret)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
		Byf("Update ClusterProfile %s to reference Secret %s/%s", clusterProfile.Name, secret.Namespace, secret.Name)
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Kind:      string(configv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
			{
				Kind:      string(configv1alpha1.SecretReferencedResourceKind),
				Namespace: secret.Namespace,
				Name:      secret.Name,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper ClusterRole is created in the workload cluster")
		Eventually(func() error {
			currentClusterRole := &rbacv1.ClusterRole{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: "configmap-updater"}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying proper Pod is created in the workload cluster")
		Eventually(func() error {
			currentPod := &corev1.Pod{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "default", Name: podName}, currentPod)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1alpha1.FeatureResources)

		policies := []policy{
			{kind: "ClusterRole", name: "configmap-updater", namespace: "", group: "rbac.authorization.k8s.io"},
			{kind: "Pod", name: podName, namespace: "default", group: ""},
		}
		verifyClusterConfiguration(clusterProfile.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureResources, policies, nil)

		By("Updating ConfigMap to reference new ClusterRole")
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
			{kind: "Pod", name: podName, namespace: "default", group: ""},
		}
		verifyClusterConfiguration(clusterProfile.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureResources, policies, nil)

		By("Updating Secret to reference new Pod")
		newPodName := "prod" + randomString()
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, currentSecret)).To(Succeed())
		currentSecret.Data["policy0.yaml"] = []byte(base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(demoPod, newPodName))))
		Expect(k8sClient.Update(context.TODO(), currentSecret)).To(Succeed())

		Byf("Verifying new Pod is deployed in the workload cluster")
		Eventually(func() error {
			currentPod := &corev1.Pod{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "default", Name: newPodName}, currentPod)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying old clusterrole is removed from the workload cluster")
		Eventually(func() bool {
			currentPod := &corev1.Pod{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "default", Name: podName}, currentPod)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		policies = []policy{
			{kind: "ClusterRole", name: "configmap-all", namespace: "", group: "rbac.authorization.k8s.io"},
			{kind: "Pod", name: newPodName, namespace: "default", group: ""},
		}
		verifyClusterConfiguration(clusterProfile.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureResources, policies, nil)

		Byf("Changing clusterprofile to not reference configmap/secret anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying proper ClusterRole is removed in the workload cluster")
		Eventually(func() bool {
			currentClusterRole := &rbacv1.ClusterRole{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: "configmap-updater"}, currentClusterRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper Pod is removed in the workload cluster")
		Eventually(func() bool {
			currentPod := &corev1.Pod{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "default", Name: newPodName}, currentPod)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
