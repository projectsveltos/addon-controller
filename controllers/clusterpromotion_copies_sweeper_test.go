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

package controllers_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	copyClusterProfileLabel = "config.projectsveltos.io/clusterprofilename"
	copyNamespace           = "default"
)

var _ = Describe("ClusterPromotionCopiesSweeper", func() {
	// getCopyConfigMap returns a ConfigMap as created by ClusterPromotion for a stage
	getCopyConfigMap := func(clusterProfileName string, age time.Duration) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         copyNamespace,
				Name:              randomString(),
				CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
				Labels:            map[string]string{copyClusterProfileLabel: clusterProfileName},
				Annotations:       map[string]string{copyClusterProfileLabel: clusterProfileName},
			},
		}
	}

	getCopySecret := func(clusterProfileName string, age time.Duration) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         copyNamespace,
				Name:              randomString(),
				CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
				Labels:            map[string]string{copyClusterProfileLabel: clusterProfileName},
				Annotations:       map[string]string{copyClusterProfileLabel: clusterProfileName},
			},
		}
	}

	exists := func(c client.Client, obj client.Object) bool {
		err := c.Get(context.TODO(), client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object))
		if apierrors.IsNotFound(err) {
			return false
		}
		Expect(err).To(BeNil())
		return true
	}

	It("removes resources whose ClusterProfile does not exist, keeping the others", func() {
		const oldAge = time.Hour

		missingProfile := randomString()
		configMapOfMissingProfile := getCopyConfigMap(missingProfile, oldAge)
		secretOfMissingProfile := getCopySecret(missingProfile, oldAge)
		// Younger than the grace period: ClusterPromotion creates it just before the ClusterProfile
		youngConfigMap := getCopyConfigMap(missingProfile, time.Minute)
		// Has the label but not the annotation ClusterPromotion sets: not created by ClusterPromotion
		notOurs := getCopyConfigMap(missingProfile, oldAge)
		notOurs.Annotations = nil

		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
		}
		configMapOfExistingProfile := getCopyConfigMap(clusterProfile.Name, oldAge)
		secretOfExistingProfile := getCopySecret(clusterProfile.Name, oldAge)
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: copyNamespace, Name: configMapOfExistingProfile.Name,
				Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}
		clusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				ValuesFrom: []configv1beta1.ValueFrom{
					{
						Namespace: copyNamespace, Name: secretOfExistingProfile.Name,
						Kind: string(libsveltosv1beta1.SecretReferencedResourceKind),
					},
				},
			},
		}

		// The ClusterProfile exists but does not reference the ConfigMap anymore
		configMapNotReferenced := getCopyConfigMap(clusterProfile.Name, oldAge)

		initObjects := []client.Object{
			configMapOfMissingProfile, secretOfMissingProfile, youngConfigMap, notOurs,
			clusterProfile, configMapOfExistingProfile, secretOfExistingProfile, configMapNotReferenced,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		sweeper := controllers.NewClusterPromotionCopiesSweeper(c, logr.Discard())
		controllers.SweepClusterPromotionCopies(sweeper, context.TODO())

		Expect(exists(c, configMapOfMissingProfile)).To(BeFalse())
		Expect(exists(c, secretOfMissingProfile)).To(BeFalse())
		Expect(exists(c, configMapNotReferenced)).To(BeFalse())

		Expect(exists(c, youngConfigMap)).To(BeTrue())
		Expect(exists(c, notOurs)).To(BeTrue())
		Expect(exists(c, configMapOfExistingProfile)).To(BeTrue())
		Expect(exists(c, secretOfExistingProfile)).To(BeTrue())
	})
})
