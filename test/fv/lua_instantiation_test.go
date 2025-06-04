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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	luaCode = `function evaluate()
  local namespace = getResource(resources, "Namespace")
  local hs = {}
  local result = [[
apiVersion: v1
kind: Namespace
metadata:
  name: ]] .. namespace.metadata.name .. [[

  labels:
    env: ]] .. getLabel(namespace, "env")
  hs.resources = result
  return hs
end`
)

var _ = Describe("ConfigMap with Lua", func() {
	const (
		namePrefix = "lua-"
	)

	It("Instantiate Lua code and deploy it", Label("FV", "EXTENDED"), func() {
		envKey := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
				Labels: map[string]string{
					"env": envKey,
				},
			},
		}
		Byf("Create namespace %s in the management cluster", ns.Name)
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, ns)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Create a configMap with a Lua code (with annotation projectsveltos.io/lua)")
		configMap := createConfigMapWithPolicy(ns.Name, namePrefix+randomString(), luaCode)
		configMap.Annotations = map[string]string{
			libsveltosv1beta1.PolicyLuaAnnotation: "ok",
		}
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
		Byf("Update ClusterProfile %s to fetch Namespace %s", clusterProfile.Name, ns.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
				{
					Identifier: "Namespace", // this is used in luaCode. Must match
					Resource: corev1.ObjectReference{
						Kind:       ns.Kind,
						Name:       ns.Name,
						APIVersion: "v1",
					},
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

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name,
			configv1beta1.FeatureResources)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Namespace %s is created in the workload cluster", ns.Name)
		Eventually(func() error {
			currentNamespace := &corev1.Namespace{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, currentNamespace)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying Namespace %s has label env: %s", ns.Name, envKey)
		currentNamespace := &corev1.Namespace{}
		Expect(workloadClient.Get(context.TODO(), types.NamespacedName{Name: ns.Name},
			currentNamespace)).To(Succeed())
		Expect(currentNamespace.Labels).ToNot(BeNil())
		v, ok := currentNamespace.Labels["env"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(envKey))

		deleteClusterProfile(clusterProfile)

		Byf("Verifying proper Namespace %s is removed from the workload cluster", ns.Name)
		Eventually(func() bool {
			currentNamespace := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, currentNamespace)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		err = k8sClient.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, currentNamespace)
		Expect(err).To(BeNil())
		Expect(k8sClient.Delete(context.TODO(), currentNamespace)).To(Succeed())
	})
})
