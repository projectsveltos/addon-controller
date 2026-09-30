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

package controllers_test

import (
	"context"
	"fmt"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	ClusterSelectorField = "ClusterSelector"
)

var _ = Describe("ClusterPromotionController", func() {
	It("ClusterPromotion exposes all ClusterProfile.Spec fields", func() {
		// With the exception of ClusterSelector,ClusterRefs and SetRefs
		// ClusterPromotion exposes all other fields defined in ClusterProfile.Spec

		// 1. Get the reflect.Type for the source struct
		profileSpecType := reflect.TypeOf(configv1beta1.Spec{})

		// 2. Get the reflect.Type for the embedded struct within ClusterPromotionSpec
		clusterPromotionProfileSpecField := reflect.TypeOf(configv1beta1.ProfileSpec{})

		for i := 0; i < profileSpecType.NumField(); i++ {
			sourceField := profileSpecType.Field(i)

			// Those fields are only present in ClusterProfile.Spec
			if sourceField.Name == ClusterSelectorField || sourceField.Name == clusterRefsField ||
				sourceField.Name == setRefsField {

				_, found := clusterPromotionProfileSpecField.FieldByName(sourceField.Name)
				if found {
					By(fmt.Sprintf("field %s must not be present in ClusterPromotion Spec", sourceField.Name))
				}
				Expect(found).To(BeFalse())
				continue
			}

			// Those fields are deprecated in ClusterProfile.Spec
			if sourceField.Name == "ExtraLabels" || sourceField.Name == "ExtraAnnotations" {
				_, found := clusterPromotionProfileSpecField.FieldByName(sourceField.Name)
				if found {
					By(fmt.Sprintf("field %s must not be present in ClusterPromotion Spec", sourceField.Name))
				}
				Expect(found).To(BeFalse())
				continue
			}

			targetField, found := clusterPromotionProfileSpecField.FieldByName(sourceField.Name)

			if !found {
				By(fmt.Sprintf("field %s is missing in ClusterPromotion Spec", sourceField.Name))
			}
			Expect(found).To(BeTrue())

			// Verify that the data type of the field matches
			if sourceField.Type != targetField.Type {
				By(fmt.Sprintf("Field %s.%s has a type mismatch: Expected %v, Got %v.",
					profileSpecType.Name(), sourceField.Name, sourceField.Type, targetField.Type))
			}
			Expect(sourceField.Type).To(Equal(targetField.Type))
		}
	})

	It("cleanClusterProfiles deletes all ClusterProfile instances created by a ClusterPromotion", func() {
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterProfile1 := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   randomString(),
				Labels: controllers.GetMainDeploymentClusterProfileLabels(clusterPromotion)},
		}
		clusterProfile2 := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   randomString(),
				Labels: controllers.GetMainDeploymentClusterProfileLabels(clusterPromotion)},
		}
		clusterProfile3 := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   randomString(),
				Labels: controllers.GetMainDeploymentClusterProfileLabels(clusterPromotion)},
		}

		initObjects := []client.Object{
			clusterProfile1,
			clusterProfile2,
			clusterProfile3,
			clusterPromotion,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := controllers.ClusterPromotionReconciler{Client: c}
		Expect(controllers.CleanClusterProfiles(&reconciler, context.TODO(), clusterPromotion)).To(Succeed())

		listOptions := []client.ListOption{
			client.MatchingLabels(controllers.GetMainDeploymentClusterProfileLabels(clusterPromotion)),
		}
		clusterProfiles := &configv1beta1.ClusterProfileList{}
		Expect(c.List(ctx, clusterProfiles, listOptions...)).To(Succeed())
		Expect(len(clusterProfiles.Items)).To(BeZero())
	})

	It("requeueClusterPromotionForReference enqueues the ClusterPromotions referencing the ConfigMap/Secret", func() {
		namespace := randomString()
		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: randomString()}}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: randomString()}}

		policyRefPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: configv1beta1.ClusterPromotionSpec{ProfileSpec: configv1beta1.ProfileSpec{
				PolicyRefs: []configv1beta1.PolicyRef{
					{Namespace: namespace, Name: configMap.Name, Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind)},
				},
			}},
		}
		valuesFromPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: configv1beta1.ClusterPromotionSpec{ProfileSpec: configv1beta1.ProfileSpec{
				HelmCharts: []configv1beta1.HelmChart{{
					ValuesFrom: []configv1beta1.ValueFrom{
						{Namespace: namespace, Name: secret.Name, Kind: string(libsveltosv1beta1.SecretReferencedResourceKind)},
					},
				}},
			}},
		}
		// Same name as the ConfigMap but it is a Secret: must not match
		otherKindPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: configv1beta1.ClusterPromotionSpec{ProfileSpec: configv1beta1.ProfileSpec{
				PatchesFrom: []configv1beta1.ValueFrom{
					{Namespace: namespace, Name: configMap.Name, Kind: string(libsveltosv1beta1.SecretReferencedResourceKind)},
				},
			}},
		}

		reconciler := controllers.ClusterPromotionReconciler{}
		controllers.ClusterPromotionUpdateMaps(&reconciler, policyRefPromotion)
		controllers.ClusterPromotionUpdateMaps(&reconciler, valuesFromPromotion)
		controllers.ClusterPromotionUpdateMaps(&reconciler, otherKindPromotion)

		requests := controllers.RequeueClusterPromotionForReference(&reconciler, context.TODO(), configMap)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal(policyRefPromotion.Name))

		requests = controllers.RequeueClusterPromotionForReference(&reconciler, context.TODO(), secret)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal(valuesFromPromotion.Name))

		By("updating the ClusterPromotion so it does not reference the ConfigMap anymore")
		policyRefPromotion.Spec.ProfileSpec.PolicyRefs = nil
		controllers.ClusterPromotionUpdateMaps(&reconciler, policyRefPromotion)
		Expect(controllers.RequeueClusterPromotionForReference(&reconciler, context.TODO(), configMap)).To(BeEmpty())

		By("cleaning the maps for a deleted ClusterPromotion")
		controllers.ClusterPromotionCleanMaps(&reconciler, valuesFromPromotion)
		Expect(controllers.RequeueClusterPromotionForReference(&reconciler, context.TODO(), secret)).To(BeEmpty())
	})
})
