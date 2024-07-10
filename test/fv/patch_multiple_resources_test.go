/*
Copyright 2024. projectsveltos.io. All rights reserved.

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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Patch with multiple resources in ConfigMap", func() {
	const (
		namePrefix = "patches-"
	)

	It("Deploy and patches multiple resources contained in a referenced ConfigMap", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		// install-flux ConfigMap is created by Makefile. Contains all Flux resources
		cmNamespace := "default"
		cmName := "install-flux"

		cm := &corev1.ConfigMap{}
		Byf("Verifying ConfigMap %s/%s exists", cmNamespace, cmName)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: cmNamespace, Name: cmName}, cm)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, cmNamespace, cmName)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: cm.Namespace,
				Name:      cm.Name,
			},
		}
		currentClusterProfile.Spec.Patches = []libsveltosv1beta1.Patch{
			{
				Target: &libsveltosv1beta1.PatchSelector{
					Kind: "Namespace",
					Name: ".*",
				},
				Patch: `- op: add
  path: /metadata/labels/type
  value: operator`,
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

		Byf("Verifying proper Namespace is created in the workload cluster")
		Eventually(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: "flux-system"}, currentNamespace)
			if err != nil {
				return false
			}

			if currentNamespace.Labels == nil {
				return false
			}

			v := currentNamespace.Labels["type"]
			return v == "operator"
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureResources)

		Byf("Changing clusterprofile to not reference configmap/secret anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying  Namespace is removed in the workload cluster")
		Eventually(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: "flux-system"}, currentNamespace)
			if err != nil {
				return err != nil && apierrors.IsNotFound(err)
			}
			return !currentNamespace.DeletionTimestamp.IsZero()
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
