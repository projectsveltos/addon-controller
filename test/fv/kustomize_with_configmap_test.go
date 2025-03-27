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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

/*
* This test assumes Flux is installed and GitRepoistory flux-system/flux-system is referencing
* main branch of  ssh://git@github.com/gianlucam76/kustomize
* This test is not run as part of CI.
 */
var _ = Describe("Kustomize with ConfigMap", func() {
	const (
		namePrefix             = "kustomize-cm-"
		kustomizeConfigMapName = "kustomize"
	)

	It("Deploy Kustomize resources with ConfigMap", Label("FV", "EXTENDED"), func() {
		Byf("Verifying ConfigMap kustomize exists. It is created by Makefile")
		kustomizeConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: defaultNamespace, Name: kustomizeConfigMapName},
			kustomizeConfigMap)).To(Succeed())

		Expect("Verifying ConfigMap kustomize contains kustomize.tar.gz")
		Expect(kustomizeConfigMap.BinaryData).ToNot(BeNil())
		_, ok := kustomizeConfigMap.BinaryData["kustomize.tar.gz"]
		Expect(ok).To(BeTrue())

		configMapNamespace := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNamespace,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns))

		Byf("Creating ConfigMap to hold labels controller values (with template annotation)")
		labelsConfigMap := createConfigMapWithPolicy(configMapNamespace, randomString(),
			fmt.Sprintf(labelsValues, clusterKey))
		labelsConfigMap.Annotations = map[string]string{
			libsveltosv1beta1.PolicyTemplateAnnotation: "ok",
		}
		Expect(k8sClient.Create(context.TODO(), labelsConfigMap)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		targetNamespace := randomString()

		Byf("Update ClusterProfile %s to reference ConfigMap kustomize", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.KustomizationRefs = []configv1beta1.KustomizationRef{
				{
					Kind:            string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace:       defaultNamespace,
					Name:            kustomizeConfigMapName, // this is created by Makefile and contains kustomize files
					Path:            "./overlays/production/",
					TargetNamespace: targetNamespace,
					ValuesFrom: []configv1beta1.ValueFrom{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: labelsConfigMap.Namespace,
							Name:      labelsConfigMap.Name,
						},
					},
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Service is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "production-the-service"}, currentService)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper Deployment is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "production-the-deployment"}, currentDeployment)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper ConfigMap is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "production-the-map"}, currentConfigMap)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for kustomize", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureKustomize)

		currentConfigMap := &corev1.ConfigMap{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "production-the-map"}, currentConfigMap)).To(Succeed())

		currentService := &corev1.Service{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "production-the-service"}, currentService)).To(Succeed())

		currentDeployment := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "production-the-deployment"}, currentDeployment)).To(Succeed())

		policies := []policy{
			{kind: "Service", name: currentService.Name, namespace: targetNamespace, group: ""},
			{kind: "ConfigMap", name: currentConfigMap.Name, namespace: targetNamespace, group: ""},
			{kind: "Deployment", name: currentDeployment.Name, namespace: targetNamespace, group: "apps"},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureKustomize,
			policies, nil)

		Byf("Changing clusterprofile to not reference ConfigMap anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.KustomizationRefs = []configv1beta1.KustomizationRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying Service is removed from the workload cluster")
		Eventually(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "production-the-service"}, currentService)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Deployment is removed from the workload cluster")
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "production-the-deployment"}, currentDeployment)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ConfigMap is removed from the workload cluster")
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "production-the-map"}, currentConfigMap)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
