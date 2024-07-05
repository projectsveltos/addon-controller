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
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Conversion", func() {
	const (
		namePrefix = "conversion-"
	)

	It("Post a ClusterProfile.v1alpha1 and verify all is deployed", Label("FV", "EXTENDED"), func() {
		selector := fmt.Sprintf("%s=%s", key, value)

		Byf("Creating ClusterProfile.v1alpha1")
		v1alpha1ClusterProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
			},
			Spec: configv1alpha1.Spec{
				ClusterSelector: libsveltosv1alpha1.Selector(selector),
			},
		}

		v1alpha1ClusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), v1alpha1ClusterProfile)).To(Succeed())

		Byf("Getting ClusterProfile.v1beta1 %s", v1alpha1ClusterProfile.Name)
		v1beta1ClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: v1alpha1ClusterProfile.Name},
			v1beta1ClusterProfile)).To(Succeed())

		verifyClusterProfileMatches(v1beta1ClusterProfile)

		v1beta1Spec, err := convertV1Alpha1SpecToV1Beta1(&v1alpha1ClusterProfile.Spec)
		Expect(err).To(BeNil())

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			v1alpha1ClusterProfile.Name, &v1beta1Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create a configMap with a Namespace")
		deployedNamespaceName := randomString()
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
			fmt.Sprintf(namespace, deployedNamespaceName))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile.v1alpha1 %s to reference ConfigMap %s/%s", v1alpha1ClusterProfile.Name, configMap.Namespace, configMap.Name)
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: v1alpha1ClusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		v1beta1Spec, err = convertV1Alpha1SpecToV1Beta1(&currentClusterProfile.Spec)
		Expect(err).To(BeNil())

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &v1beta1Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Namespace is created in the workload cluster")
		Eventually(func() error {
			currentNs := &corev1.Namespace{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: deployedNamespaceName}, currentNs)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureResources)

		policies := []policy{
			{kind: "Namespace", name: deployedNamespaceName, namespace: "", group: ""},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, v1alpha1ClusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureResources,
			policies, nil)

		Byf("Changing clusterprofile.v1alpha1 to not reference configmap anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: v1alpha1ClusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1alpha1.PolicyRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		v1beta1Spec, err = convertV1Alpha1SpecToV1Beta1(&currentClusterProfile.Spec)
		Expect(err).To(BeNil())

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &v1beta1Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying Namespace is removed in the workload cluster")
		Eventually(func() bool {
			currentNs := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: deployedNamespaceName}, currentNs)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(v1beta1ClusterProfile)

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})

func convertV1Alpha1SpecToV1Beta1(srcSpec *configv1alpha1.Spec) (configv1beta1.Spec, error) {
	dstSpec := configv1beta1.Spec{}

	dstSpec.ClusterRefs = srcSpec.ClusterRefs
	dstSpec.ContinueOnConflict = srcSpec.ContinueOnConflict
	dstSpec.DependsOn = srcSpec.DependsOn
	dstSpec.ExtraAnnotations = srcSpec.ExtraAnnotations
	dstSpec.ExtraLabels = srcSpec.ExtraLabels

	jsonData, err := json.Marshal(srcSpec.HelmCharts) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.HelmCharts: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.HelmCharts) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	jsonData, err = json.Marshal(srcSpec.KustomizationRefs) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.KustomizationRefs: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.KustomizationRefs) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	jsonData, err = json.Marshal(srcSpec.PolicyRefs) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.PolicyRefs: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.PolicyRefs) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	dstSpec.Reloader = srcSpec.Reloader
	dstSpec.SetRefs = srcSpec.SetRefs

	labelSelector, err := metav1.ParseToLabelSelector(string(srcSpec.ClusterSelector))
	if err != nil {
		return dstSpec, fmt.Errorf("error converting labels.Selector to metav1.Selector: %w", err)
	}
	dstSpec.ClusterSelector = libsveltosv1beta1.Selector{LabelSelector: *labelSelector}

	return dstSpec, nil
}
