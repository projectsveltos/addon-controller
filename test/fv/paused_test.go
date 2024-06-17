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
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	devNamespace = `apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    name: fv`
)

var _ = Describe("Feature", Serial, func() {
	const (
		namePrefix = "paused-"
	)

	It("Pause and unpause cluster. Policies are deployed only when unpaused", Label("FV", "EXTENDED"), func() {
		Byf("Setting Cluster as paused")
		setClusterPausedField(true)

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		devNamespaceName := randomString()
		Byf("Create a configMap with a Namespace")
		configMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(), fmt.Sprintf(devNamespace, devNamespaceName))

		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying namespace is not created in the workload cluster as cluster is paused")
		Consistently(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: devNamespaceName}, currentNamespace)
			return apierrors.IsNotFound(err)
		}, timeout/2, pollingInterval).Should(BeTrue())

		Byf("Setting Cluster as unpaused")
		setClusterPausedField(false)

		Byf("Verifying namespace is created in the workload cluster as cluster is not paused anymore")
		Eventually(func() error {
			currentNamespace := &corev1.Namespace{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: devNamespaceName}, currentNamespace)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureResources)

		Byf("Changing clusterprofile to not reference configmap anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying policy is removed in the workload cluster")
		Eventually(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: devNamespaceName}, currentNamespace)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})

func setClusterPausedField(paused bool) {
	cluster := &clusterv1.Cluster{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name}, cluster)).To(Succeed())
	cluster.Spec.Paused = paused
	Expect(k8sClient.Update(context.TODO(), cluster)).To(Succeed())
}
