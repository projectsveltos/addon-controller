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

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const clusterProfileNamePrefix = "scope-"

var _ = Describe("ClusterProfileScope", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
	var c client.Client

	BeforeEach(func() {
		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
		}
		scheme := setupScheme()
		initObjects := []client.Object{clusterProfile}
		c = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()
	})

	It("Return nil,error if ClusterProfile is not specified", func() {
		params := scope.ClusterProfileScopeParams{
			Client: c,
			Logger: klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		params := scope.ClusterProfileScopeParams{
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("IsContinuousSync returns false when SyncMode is OneTime", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeFalse())
	})

	It("IsContinuousSync returns true when SyncMode is Continuous", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeTrue())
	})

	It("IsContinuousSync returns true when SyncMode is ContinuousWithDriftDetection", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuousWithDriftDetection
		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeTrue())
	})

	It("Name returns ClusterProfile Name", func() {
		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.Name()).To(Equal(clusterProfile.Name))
	})

	It("GetSelector returns ClusterProfile ClusterSelector", func() {
		clusterProfile.Spec.ClusterSelector = libsveltosv1alpha1.Selector("zone=east")
		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.GetSelector()).To(Equal(string(clusterProfile.Spec.ClusterSelector)))
	})

	It("SetMatchingClusters sets ClusterProfile.Status.MatchingCluster", func() {
		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		matchingClusters := []corev1.ObjectReference{
			{
				Namespace: "t-" + randomString(),
				Name:      "c-" + randomString(),
			},
		}
		scope.SetMatchingClusterRefs(matchingClusters)
		Expect(reflect.DeepEqual(clusterProfile.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
	})

	It("Close updates ClusterProfile", func() {
		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		clusterProfile.Labels = map[string]string{"clusters": "hr"}
		Expect(scope.Close(context.TODO())).To(Succeed())

		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		Expect(currentClusterProfile.Labels).ToNot(BeNil())
		Expect(len(currentClusterProfile.Labels)).To(Equal(1))
		v, ok := currentClusterProfile.Labels["clusters"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("hr"))
	})

	It("IsContinuousSync returns true when mode is Continuous", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeTrue())

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(scope.IsContinuousSync()).To(BeFalse())
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		Expect(scope.IsContinuousSync()).To(BeFalse())
	})

	It("IsOneTimeSync returns true when mode is OneTime", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime

		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsOneTimeSync()).To(BeTrue())

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(scope.IsOneTimeSync()).To(BeFalse())
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(scope.IsOneTimeSync()).To(BeFalse())
	})

	It("IsDryRunSync returns true when mode is DryRun", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun

		params := scope.ClusterProfileScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterProfileScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsDryRunSync()).To(BeTrue())

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(scope.IsDryRunSync()).To(BeFalse())
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		Expect(scope.IsDryRunSync()).To(BeFalse())
	})
})
