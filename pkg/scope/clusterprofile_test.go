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
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const clusterProfileNamePrefix = "scope-cp-"
const profileNamePrefix = "scope-p-"

var _ = Describe("ProfileScope/ClusterProfileScope", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
	var profile *configv1alpha1.Profile
	var c client.Client

	BeforeEach(func() {
		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
		}
		profile = &configv1alpha1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      profileNamePrefix + randomString(),
				Namespace: randomString(),
			},
		}
		scheme := setupScheme()
		addTypeInformationToObject(scheme, clusterProfile)
		addTypeInformationToObject(scheme, profile)
		initObjects := []client.Object{clusterProfile, profile}
		c = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()
	})

	It("Return nil,error if Profile/ClusterProfile is not specified", func() {
		cpParams := scope.ProfileScopeParams{
			Client: c,
			Logger: textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
		}

		cpScope, err := scope.NewProfileScope(cpParams)
		Expect(err).To(HaveOccurred())
		Expect(cpScope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		cpParams := scope.ProfileScopeParams{
			Profile: clusterProfile,
			Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
		}

		cpScope, err := scope.NewProfileScope(cpParams)
		Expect(err).To(HaveOccurred())
		Expect(cpScope).To(BeNil())
	})

	It("Return nil,error if any resource but Profile/ClusterProfile is passed", func() {
		cpParams := scope.ProfileScopeParams{
			Client:  c,
			Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			Profile: &corev1.Node{},
		}

		cpScope, err := scope.NewProfileScope(cpParams)
		Expect(err).To(HaveOccurred())
		Expect(cpScope).To(BeNil())
	})

	It("IsContinuousSync returns false when SyncMode is OneTime", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		profile.Spec.SyncMode = configv1alpha1.SyncModeOneTime

		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsContinuousSync()).To(BeFalse())
		}
	})

	It("IsContinuousSync returns true when SyncMode is Continuous", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		profile.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsContinuousSync()).To(BeTrue())
		}
	})

	It("IsContinuousSync returns true when SyncMode is ContinuousWithDriftDetection", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuousWithDriftDetection
		profile.Spec.SyncMode = configv1alpha1.SyncModeContinuousWithDriftDetection

		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsContinuousSync()).To(BeTrue())
		}
	})

	It("Name returns ClusterProfile Name", func() {
		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.Name()).To(Equal(objects[i].GetName()))
		}
	})

	It("GetSelector returns ClusterProfile ClusterSelector", func() {
		clusterProfile.Spec.ClusterSelector = libsveltosv1alpha1.Selector("zone=east")
		profile.Spec.ClusterSelector = clusterProfile.Spec.ClusterSelector

		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.GetSelector()).To(Equal(string(clusterProfile.Spec.ClusterSelector)))
		}
	})

	It("SetMatchingClusters sets ClusterProfile.Status.MatchingCluster", func() {
		matchingClusters := []corev1.ObjectReference{
			{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}
		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			scope.SetMatchingClusterRefs(matchingClusters)
		}
		Expect(reflect.DeepEqual(clusterProfile.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
		Expect(reflect.DeepEqual(profile.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
	})

	It("Close updates ClusterProfile", func() {
		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			objects[i].SetLabels(map[string]string{"clusters": "hr"})
			Expect(scope.Close(context.TODO())).To(Succeed())
		}
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		Expect(currentClusterProfile.Labels).ToNot(BeNil())
		Expect(len(currentClusterProfile.Labels)).To(Equal(1))
		v, ok := currentClusterProfile.Labels["clusters"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("hr"))

		currentProfile := &configv1alpha1.Profile{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Name: profile.Name, Namespace: profile.Namespace}, currentProfile)).To(Succeed())
		Expect(currentProfile.Labels).ToNot(BeNil())
		Expect(len(currentProfile.Labels)).To(Equal(1))
		v, ok = currentProfile.Labels["clusters"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("hr"))
	})

	It("IsContinuousSync returns true when mode is Continuous", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		profile.Spec.SyncMode = clusterProfile.Spec.SyncMode

		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsContinuousSync()).To(BeTrue())
		}

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		profile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		objects = []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsContinuousSync()).To(BeFalse())
		}
	})

	It("IsOneTimeSync returns true when mode is OneTime", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		profile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsOneTimeSync()).To(BeFalse())
		}

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		profile.Spec.SyncMode = clusterProfile.Spec.SyncMode
		objects = []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsOneTimeSync()).To(BeTrue())
		}
	})

	It("IsDryRunSync returns true when mode is DryRun", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		profile.Spec.SyncMode = clusterProfile.Spec.SyncMode

		objects := []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsDryRunSync()).To(BeTrue())
		}

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		profile.Spec.SyncMode = configv1alpha1.SyncModeOneTime

		objects = []client.Object{clusterProfile, profile}
		for i := range objects {
			params := scope.ProfileScopeParams{
				Client:  c,
				Profile: objects[i],
				Logger:  textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			}

			scope, err := scope.NewProfileScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.IsDryRunSync()).To(BeFalse())
			Expect(scope.IsDryRunSync()).To(BeFalse())
		}
	})
})
