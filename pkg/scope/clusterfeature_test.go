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

package scope_test

import (
	"context"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

const clusterFeatureNamePrefix = "scope-"

var _ = Describe("ClusterFeatureScope", func() {
	var clusterFeature *configv1alpha1.ClusterFeature
	var c client.Client

	BeforeEach(func() {
		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
		}
		scheme := setupScheme()
		initObjects := []client.Object{clusterFeature}
		c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
	})

	It("Return nil,error if ClusterFeature is not specified", func() {
		params := scope.ClusterFeatureScopeParams{
			Client: c,
			Logger: klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		params := scope.ClusterFeatureScopeParams{
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("IsContinuousSync returns false when SyncMode is OneTime", func() {
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeFalse())
	})

	It("IsContinuousSync returns true when SyncMode is Continuous", func() {
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeTrue())
	})

	It("Name returns ClusterFeature Name", func() {
		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.Name()).To(Equal(clusterFeature.Name))
	})

	It("GetSelector returns ClusterFeature ClusterSelector", func() {
		clusterFeature.Spec.ClusterSelector = configv1alpha1.Selector("zone=east")
		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.GetSelector()).To(Equal(string(clusterFeature.Spec.ClusterSelector)))
	})

	It("SetMatchingClusters sets ClusterFeature.Status.MatchingCluster", func() {
		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		matchingClusters := []corev1.ObjectReference{
			{
				Namespace: "t-" + randomString(),
				Name:      "c-" + randomString(),
			},
		}
		scope.SetMatchingClusterRefs(matchingClusters)
		Expect(reflect.DeepEqual(clusterFeature.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
	})

	It("Close updates ClusterFeature", func() {
		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		clusterFeature.Labels = map[string]string{"clusters": "hr"}
		Expect(scope.Close(context.TODO())).To(Succeed())

		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		Expect(currentClusterFeature.Labels).ToNot(BeNil())
		Expect(len(currentClusterFeature.Labels)).To(Equal(1))
		v, ok := currentClusterFeature.Labels["clusters"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("hr"))
	})

	It("IsContinuousSync returns true when mode is Continuous", func() {
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeTrue())

		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(scope.IsContinuousSync()).To(BeFalse())
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		Expect(scope.IsContinuousSync()).To(BeFalse())
	})

	It("IsOneTimeSync returns true when mode is OneTime", func() {
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeOneTime

		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsOneTimeSync()).To(BeTrue())

		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(scope.IsOneTimeSync()).To(BeFalse())
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(scope.IsOneTimeSync()).To(BeFalse())
	})

	It("IsDryRunSync returns true when mode is DryRun", func() {
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeDryRun

		params := scope.ClusterFeatureScopeParams{
			Client:         c,
			ClusterFeature: clusterFeature,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterFeatureScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsDryRunSync()).To(BeTrue())

		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(scope.IsDryRunSync()).To(BeFalse())
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		Expect(scope.IsDryRunSync()).To(BeFalse())
	})
})
