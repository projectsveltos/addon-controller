/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
)

/*
* This test assumes Flux is installed and GitRepoistory flux-system/flux-system is referencing
* main branch of  ssh://git@github.com/gianlucam76/kustomize
* This test is not run as part of CI.
 */
var _ = Describe("Kustomize", func() {
	const (
		namePrefix = "kustomize-"
	)

	It("Deploy Kustomize resources", Label("EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		targetNamespace := randomString()

		gitRepositoryNamespace := "flux-system"
		gitRepositoryName := gitRepositoryNamespace

		Byf("Update ClusterProfile %s to reference GitRepository flux-system/flux-system", clusterProfile.Name)
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.KustomizationRefs = []configv1alpha1.KustomizationRef{
			{
				Kind:            sourcev1.GitRepositoryKind,
				Namespace:       gitRepositoryNamespace,
				Name:            gitRepositoryName,
				Path:            "./helloWorld",
				TargetNamespace: targetNamespace,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		Byf("Verifying GitRepository %s/%s exists", gitRepositoryNamespace, gitRepositoryName)
		gitRepository := &sourcev1.GitRepository{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: gitRepositoryNamespace, Name: gitRepositoryName},
			gitRepository)).To(Succeed())

		Byf("Verifying GitRepository %s/%s artifact is set", gitRepositoryNamespace, gitRepositoryName)
		Eventually(func() bool {
			gitRepository := &sourcev1.GitRepository{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: gitRepositoryNamespace, Name: gitRepositoryName},
				gitRepository)
			return err == nil &&
				gitRepository.Status.Artifact != nil
		}, timeout, pollingInterval).Should(BeTrue())

		clusterSummary := verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Service is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-service"}, currentService)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper Deployment is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-deployment"}, currentDeployment)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper ConfigMap is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-map"}, currentConfigMap)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for kustomize", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1alpha1.FeatureKustomize)

		currentConfigMap := &corev1.ConfigMap{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "the-map"}, currentConfigMap)).To(Succeed())

		currentService := &corev1.Service{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "the-service"}, currentService)).To(Succeed())

		currentDeployment := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "the-deployment"}, currentDeployment)).To(Succeed())

		policies := []policy{
			{kind: "Service", name: currentService.Name, namespace: targetNamespace, group: ""},
			{kind: "ConfigMap", name: currentConfigMap.Name, namespace: targetNamespace, group: ""},
			{kind: "Deployment", name: currentDeployment.Name, namespace: targetNamespace, group: "apps"},
		}
		verifyClusterConfiguration(clusterProfile.Name, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1alpha1.FeatureKustomize, policies, nil)

		Byf("Changing clusterprofile to not reference GitRepository anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.KustomizationRefs = []configv1alpha1.KustomizationRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying Service is removed from the workload cluster")
		Eventually(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-service"}, currentService)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Deployment is removed from the workload cluster")
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-deployment"}, currentDeployment)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ConfigMap is removed from the workload cluster")
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-map"}, currentConfigMap)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
