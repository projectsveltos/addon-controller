/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Stage Promotions", func() {
	const (
		namePrefix = "promotion-"
	)

	var (
		contourValues = `global:
  security:
    allowInsecureImages: true
contour:
  image:
    registry: ghcr.io
    repository: projectcontour/contour
    tag: v1.33.0
envoy:
    image:
      registry: docker.io
      repository: envoyproxy/envoy
      tag: distroless-v1.35.2`
	)

	It("Deploy ClusterPromotion with multiple stages", Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
		clusterLabels := map[string]string{key: value}
		const two = 2
		stage1 := configv1beta1.Stage{
			Name: "staging",
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: clusterLabels,
				},
			},
			Trigger: &configv1beta1.Trigger{
				Auto: &configv1beta1.AutoTrigger{
					Delay: &metav1.Duration{Duration: two * time.Minute},
				},
			},
		}

		stage2 := configv1beta1.Stage{
			Name: "production",
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "production"},
				},
			},
		}

		Byf("Create a ClusterPromotion")
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
			},
			Spec: configv1beta1.ClusterPromotionSpec{
				ProfileSpec: configv1beta1.ProfileSpec{
					Tier: 90,
					HelmCharts: []configv1beta1.HelmChart{
						{
							RepositoryURL:    "https://charts.bitnami.com/bitnami",
							RepositoryName:   "bitnami",
							ChartName:        "bitnami/contour",
							ChartVersion:     "21.1.4",
							ReleaseName:      "contour",
							ReleaseNamespace: "contour",
							Values:           contourValues,
						},
					},
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: randomString(),
							Name:      randomString(),
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Optional:  true,
						},
					},
				},
				Stages: []configv1beta1.Stage{stage1, stage2},
			},
		}

		Expect(k8sClient.Create(context.TODO(), clusterPromotion)).To(Succeed())

		listOptions := []client.ListOption{
			client.MatchingLabels{
				"config.projectsveltos.io/promotionname": clusterPromotion.Name,
			},
		}
		Byf("Verify ClusterProfile for stage %s is created", stage1.Name)
		Eventually(func() bool {
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			err := k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfileList.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verify ClusterProfile for stage %s is correct", stage1.Name)
		clusterProfileList := &configv1beta1.ClusterProfileList{}
		Expect(k8sClient.List(context.TODO(), clusterProfileList, listOptions...)).To(Succeed())
		Expect(len(clusterProfileList.Items)).To(Equal(1))
		Expect(reflect.DeepEqual(clusterProfileList.Items[0].Spec.HelmCharts,
			clusterPromotion.Spec.ProfileSpec.HelmCharts)).To(BeTrue())
		Expect(reflect.DeepEqual(clusterProfileList.Items[0].Spec.PolicyRefs,
			clusterPromotion.Spec.ProfileSpec.PolicyRefs)).To(BeTrue())
		Expect(clusterProfileList.Items[0].Spec.Tier).To(Equal(clusterPromotion.Spec.ProfileSpec.Tier))
		Expect(reflect.DeepEqual(clusterProfileList.Items[0].Spec.ClusterSelector,
			stage1.ClusterSelector)).To(BeTrue())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfileList.Items[0].Name, &clusterProfileList.Items[0].Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		// Stage1 has a Delay set to two minutes. Verify no new ClusterProfile is created
		Consistently(func() bool {
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			err := k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfileList.Items) == 1
		}, time.Minute, pollingInterval).Should(BeTrue())

		time.Sleep(time.Minute)

		Byf("Verifying second clusterProfile is created for stage %s", stage2.Name)
		Eventually(func() bool {
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			err := k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfileList.Items) == 2
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(k8sClient.List(context.TODO(), clusterProfileList, listOptions...)).To(Succeed())
		Expect(len(clusterProfileList.Items)).To(Equal(2))
		for i := range clusterProfileList.Items {
			Expect(reflect.DeepEqual(clusterProfileList.Items[i].Spec.HelmCharts,
				clusterPromotion.Spec.ProfileSpec.HelmCharts)).To(BeTrue())
			Expect(reflect.DeepEqual(clusterProfileList.Items[i].Spec.PolicyRefs,
				clusterPromotion.Spec.ProfileSpec.PolicyRefs)).To(BeTrue())
			Expect(clusterProfileList.Items[i].Spec.Tier).To(Equal(clusterPromotion.Spec.ProfileSpec.Tier))
			if strings.Contains(clusterProfileList.Items[i].Name, stage1.Name) {
				Expect(reflect.DeepEqual(clusterProfileList.Items[i].Spec.ClusterSelector,
					stage1.ClusterSelector)).To(BeTrue())
			} else {
				Expect(reflect.DeepEqual(clusterProfileList.Items[i].Spec.ClusterSelector,
					stage2.ClusterSelector)).To(BeTrue())
			}
		}

		By("Verifying ClusterPromotion Status")
		Eventually(func() bool {
			currentClusterPromotion := &configv1beta1.ClusterPromotion{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterPromotion.Name},
				currentClusterPromotion)
			if err != nil {
				return false
			}
			if len(currentClusterPromotion.Status.Stages) != 2 {
				return false
			}
			for i := range currentClusterPromotion.Status.Stages {
				if currentClusterPromotion.Status.Stages[i].LastSuccessfulAppliedTime == nil {
					return false
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())

		currentClusterPromotion := &configv1beta1.ClusterPromotion{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterPromotion.Name},
			currentClusterPromotion)).To(Succeed())

		Byf("Deleting ClusterPromotion %s", clusterPromotion.Name)
		Expect(k8sClient.Delete(context.TODO(), currentClusterPromotion)).To(Succeed())

		Byf("Verifying clusterProfiles are deleted")
		Eventually(func() bool {
			clusterProfileList := &configv1beta1.ClusterProfileList{}
			err := k8sClient.List(context.TODO(), clusterProfileList, listOptions...)
			if err != nil {
				return false
			}
			return len(clusterProfileList.Items) == 0
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
