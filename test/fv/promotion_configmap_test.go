/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"bytes"
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Stage Promotions with referenced ConfigMap", func() {
	const (
		namePrefix = "promotion-cm-"

		// A change to the referenced ConfigMap restarts the promotion. Each stage is checked
		// every couple of minutes so allow for a whole promotion.
		restartTimeout = 10 * time.Minute

		// label ClusterPromotion sets on the ConfigMaps/Secrets it creates for a stage
		clusterProfileNameLabel = "config.projectsveltos.io/clusterprofilename"

		payloadPolicy = `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  version: %s`
	)

	// getStageCopyName returns the name of the ConfigMap ClusterPromotion creates for a stage
	getStageCopyName := func(clusterPromotion *configv1beta1.ClusterPromotion, stage *configv1beta1.Stage,
		configMap *corev1.ConfigMap) string {

		return fmt.Sprintf("%s-%s-%s", clusterPromotion.Name, stage.Name, configMap.Name)
	}

	getClusterPromotion := func(name string) *configv1beta1.ClusterPromotion {
		clusterPromotion := &configv1beta1.ClusterPromotion{}
		ExpectWithOffset(1, k8sClient.Get(context.TODO(), types.NamespacedName{Name: name},
			clusterPromotion)).To(Succeed())
		return clusterPromotion
	}

	// getStageProfile returns the ClusterProfile ClusterPromotion created for a stage
	getStageProfile := func(clusterPromotion *configv1beta1.ClusterPromotion, stage *configv1beta1.Stage,
	) (*configv1beta1.ClusterProfile, error) {

		clusterProfile := &configv1beta1.ClusterProfile{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: fmt.Sprintf("%s-%s", clusterPromotion.Name, stage.Name)}, clusterProfile)
		return clusterProfile, err
	}

	// getCopyContent returns the policy stored in the ConfigMap copy
	getCopyContent := func(namespace, name string) (string, error) {
		copyConfigMap := &corev1.ConfigMap{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, copyConfigMap)
		if err != nil {
			return "", err
		}
		return copyConfigMap.Data["policy0.yaml"], nil
	}

	// getStageAppliedTime returns the time the stage was successfully applied, nil if it was not
	getStageAppliedTime := func(clusterPromotion *configv1beta1.ClusterPromotion, stageName string) *metav1.Time {
		for i := range clusterPromotion.Status.Stages {
			if clusterPromotion.Status.Stages[i].Name == stageName {
				return clusterPromotion.Status.Stages[i].LastSuccessfulAppliedTime
			}
		}
		return nil
	}

	It("Restarts the promotion when the referenced ConfigMap changes", Label("Enterprise"), func() {
		payloadName := namePrefix + randomString()
		payloadNamespace := defaultNamespace

		Byf("Create the ConfigMap ClusterPromotion references")
		configMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
			fmt.Sprintf(payloadPolicy, payloadName, payloadNamespace, "v1"))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		// No trigger: the promotion moves to the next stage as soon as a stage is provisioned
		staging := configv1beta1.Stage{
			Name: stagingValue,
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{key: value}},
			},
		}
		production := configv1beta1.Stage{
			Name: productionValue,
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{key: productionValue}},
			},
		}

		Byf("Create a ClusterPromotion with two stages")
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{Name: namePrefix + randomString()},
			Spec: configv1beta1.ClusterPromotionSpec{
				ProfileSpec: configv1beta1.ProfileSpec{
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name,
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
					},
				},
				Stages: []configv1beta1.Stage{staging, production},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterPromotion)).To(Succeed())

		stages := []*configv1beta1.Stage{&staging, &production}

		Byf("Verify all stages are provisioned")
		Eventually(func() bool {
			current := getClusterPromotion(clusterPromotion.Name)
			for _, stage := range stages {
				if getStageAppliedTime(current, stage.Name) == nil {
					return false
				}
			}
			return true
		}, restartTimeout, pollingInterval).Should(BeTrue())

		Byf("Verify each stage has its own ClusterProfile referencing its own copy of the ConfigMap")
		for _, stage := range stages {
			copyName := getStageCopyName(clusterPromotion, stage, configMap)

			clusterProfile, err := getStageProfile(clusterPromotion, stage)
			Expect(err).To(BeNil())
			Expect(clusterProfile.Spec.PolicyRefs).To(HaveLen(1))
			Expect(clusterProfile.Spec.PolicyRefs[0].Name).To(Equal(copyName))
			Expect(clusterProfile.Spec.PolicyRefs[0].Namespace).To(Equal(configMap.Namespace))

			content, err := getCopyContent(configMap.Namespace, copyName)
			Expect(err).To(BeNil())
			Expect(content).To(Equal(configMap.Data["policy0.yaml"]))

			Byf("Verify the copy is labeled with the name of the ClusterProfile consuming it")
			copyConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: configMap.Namespace, Name: copyName},
				copyConfigMap)).To(Succeed())
			Expect(copyConfigMap.Labels).To(HaveKeyWithValue(clusterProfileNameLabel, clusterProfile.Name))
		}

		Byf("Verify the resource is deployed in the cluster matching stage %s", staging.Name)
		stagingProfile, err := getStageProfile(clusterPromotion, &staging)
		Expect(err).To(BeNil())
		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, stagingProfile.Name,
			&stagingProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
			libsveltosv1beta1.FeatureResources)

		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Eventually(func() string {
			deployed := &corev1.ConfigMap{}
			err := workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: payloadNamespace, Name: payloadName}, deployed)
			if err != nil {
				return ""
			}
			return deployed.Data["version"]
		}, timeout, pollingInterval).Should(Equal("v1"))

		appliedTimes := map[string]metav1.Time{}
		current := getClusterPromotion(clusterPromotion.Name)
		for _, stage := range stages {
			appliedTimes[stage.Name] = *getStageAppliedTime(current, stage.Name)
		}
		profileSpecHash := current.Status.ProfileSpecHash

		Byf("Change the referenced ConfigMap")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: configMap.Namespace,
			Name: configMap.Name}, configMap)).To(Succeed())
		configMap = updateConfigMapWithPolicy(configMap, fmt.Sprintf(payloadPolicy, payloadName, payloadNamespace, "v2"))
		Expect(k8sClient.Update(context.TODO(), configMap)).To(Succeed())

		Byf("Verify the promotion starts over: ClusterPromotion detects the change")
		Eventually(func() bool {
			current := getClusterPromotion(clusterPromotion.Name)
			return !bytes.Equal(current.Status.ProfileSpecHash, profileSpecHash)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verify every stage is provisioned again, in a new run")
		Eventually(func() bool {
			current := getClusterPromotion(clusterPromotion.Name)
			for _, stage := range stages {
				appliedTime := getStageAppliedTime(current, stage.Name)
				if appliedTime == nil || !appliedTime.After(appliedTimes[stage.Name].Time) {
					return false
				}
			}
			return true
		}, restartTimeout, pollingInterval).Should(BeTrue())

		Byf("Verify each stage copy has the new content")
		for _, stage := range stages {
			Eventually(func() string {
				content, err := getCopyContent(configMap.Namespace, getStageCopyName(clusterPromotion, stage, configMap))
				if err != nil {
					return ""
				}
				return content
			}, timeout, pollingInterval).Should(Equal(configMap.Data["policy0.yaml"]))
		}

		Byf("Verify the new content is deployed in the cluster matching stage %s", staging.Name)
		Eventually(func() string {
			deployed := &corev1.ConfigMap{}
			err := workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: payloadNamespace, Name: payloadName}, deployed)
			if err != nil {
				return ""
			}
			return deployed.Data["version"]
		}, timeout, pollingInterval).Should(Equal("v2"))

		Byf("Deleting ClusterPromotion %s", clusterPromotion.Name)
		Expect(k8sClient.Delete(context.TODO(), getClusterPromotion(clusterPromotion.Name))).To(Succeed())

		Byf("Verify ClusterProfiles and the ConfigMap copies are deleted")
		Eventually(func() bool {
			for _, stage := range stages {
				profile, err := getStageProfile(clusterPromotion, stage)
				if err == nil && profile.DeletionTimestamp.IsZero() {
					return false
				}
				if err != nil && !apierrors.IsNotFound(err) {
					return false
				}

				_, err = getCopyContent(configMap.Namespace, getStageCopyName(clusterPromotion, stage, configMap))
				if err == nil || !apierrors.IsNotFound(err) {
					return false
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verify the original ConfigMap is not deleted")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: configMap.Namespace,
			Name: configMap.Name}, &corev1.ConfigMap{})).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), configMap)).To(Succeed())
	})
})
