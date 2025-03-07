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

package dependencymanager_test

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2/textlogger"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/dependencymanager"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Dependency manager", func() {
	var profile *configv1beta1.Profile
	var objRef *corev1.ObjectReference
	var logger logr.Logger

	BeforeEach(func() {
		profile = getProfile(randomString())

		objRef = &corev1.ObjectReference{
			Namespace:  profile.Namespace,
			Name:       profile.Name,
			Kind:       configv1beta1.ProfileKind,
			APIVersion: configv1beta1.GroupVersion.String(),
		}

		logger = textlogger.NewLogger(textlogger.NewConfig())
	})

	It("updateConfigMap tracks prerequisites", func() {
		manager, err := dependencymanager.GetManagerInstance()
		Expect(err).To(BeNil())

		dependencies := getDependencies(profile)

		manager.UpdateDependencies(objRef, profile.Status.MatchingClusterRefs, dependencies, logger)

		profiles := manager.GetTrackedProfiles()
		Expect(len(profiles.Profiles)).ToNot(BeZero())
		for i := range dependencies {
			v, ok := profiles.Profiles[dependencies[i]]
			Expect(ok).To(BeTrue())
			Expect(len(v.Clusters)).To(Equal(len(profile.Status.MatchingClusterRefs)))
			for j := range v.Clusters {
				Expect(len(v.Clusters[j].Dependents)).To(Equal(1))
			}
		}

		profile.Status.MatchingClusterRefs = append(profile.Status.MatchingClusterRefs, corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		})
		manager.UpdateDependencies(objRef, profile.Status.MatchingClusterRefs, dependencies, logger)

		profiles = manager.GetTrackedProfiles()
		Expect(len(profiles.Profiles)).ToNot(BeZero())
		for i := range dependencies {
			v, ok := profiles.Profiles[dependencies[i]]
			Expect(ok).To(BeTrue())
			Expect(len(v.Clusters)).To(Equal(len(profile.Status.MatchingClusterRefs)))
			for j := range v.Clusters {
				Expect(len(v.Clusters[j].Dependents)).To(Equal(1))
			}
		}
	})

	It("updateConfigMap same prerequisite for multiple dependents", func() {
		manager, err := dependencymanager.GetManagerInstance()
		Expect(err).To(BeNil())

		dependencies := getDependencies(profile)

		manager.UpdateDependencies(objRef, profile.Status.MatchingClusterRefs, dependencies, logger)

		profiles := manager.GetTrackedProfiles()
		Expect(len(profiles.Profiles)).ToNot(BeZero())
		for i := range dependencies {
			v, ok := profiles.Profiles[dependencies[i]]
			Expect(ok).To(BeTrue())
			Expect(len(v.Clusters)).To(Equal(len(profile.Status.MatchingClusterRefs)))
			for j := range v.Clusters {
				Expect(len(v.Clusters[j].Dependents)).To(Equal(1))
			}
		}

		profile1 := getProfile(profile.Namespace)
		profile1.Namespace = profile.Namespace
		profile1.Spec.DependsOn = profile.Spec.DependsOn

		objRef = &corev1.ObjectReference{
			Namespace:  profile1.Namespace,
			Name:       profile1.Name,
			Kind:       configv1beta1.ProfileKind,
			APIVersion: configv1beta1.GroupVersion.String(),
		}

		manager.UpdateDependencies(objRef, profile1.Status.MatchingClusterRefs, dependencies, logger)

		profiles = manager.GetTrackedProfiles()
		Expect(len(profiles.Profiles)).ToNot(BeZero())
		for i := range dependencies {
			v, ok := profiles.Profiles[dependencies[i]]
			Expect(ok).To(BeTrue())
			Expect(len(v.Clusters)).To(Equal(4)) // 2 from profile and 2 from profile1
			for j := range v.Clusters {
				Expect(len(v.Clusters[j].Dependents)).To(Equal(1))
			}
		}

		profile1.Status.MatchingClusterRefs = append(profile1.Status.MatchingClusterRefs, profile.Status.MatchingClusterRefs...)

		manager.UpdateDependencies(objRef, profile1.Status.MatchingClusterRefs, dependencies, logger)

		profiles = manager.GetTrackedProfiles()
		Expect(len(profiles.Profiles)).ToNot(BeZero())
		for i := range dependencies {
			v, ok := profiles.Profiles[dependencies[i]]
			Expect(ok).To(BeTrue())
			Expect(len(v.Clusters)).To(Equal(4)) // 2 from profile and 2 from profile1  (profile1 is deployed on 4 clusters, but 2 are same as profile)
		}
	})

	It("updateConfigMap cleans prerequisites when dependent's matching clusters change", func() {
		manager, err := dependencymanager.GetManagerInstance()
		Expect(err).To(BeNil())

		dependencies := getDependencies(profile)

		manager.UpdateDependencies(objRef, profile.Status.MatchingClusterRefs, dependencies, logger)

		profiles := manager.GetTrackedProfiles()
		Expect(len(profiles.Profiles)).ToNot(BeZero())
		for i := range dependencies {
			v, ok := profiles.Profiles[dependencies[i]]
			Expect(ok).To(BeTrue())
			Expect(len(v.Clusters)).To(Equal(len(profile.Status.MatchingClusterRefs)))
			for j := range v.Clusters {
				Expect(len(v.Clusters[j].Dependents)).To(Equal(1))
			}
		}

		// Dependent is not deployed to any cluster. So prerequisite is not tracked for deployment anymore
		profile.Status.MatchingClusterRefs = nil
		manager.UpdateDependencies(objRef, profile.Status.MatchingClusterRefs, dependencies, logger)

		verifyNoDeployments(dependencies)
	})

	It("updateConfigMap cleans prerequisites when dependent's dependsOn change", func() {
		manager, err := dependencymanager.GetManagerInstance()
		Expect(err).To(BeNil())

		dependencies := getDependencies(profile)

		manager.UpdateDependencies(objRef, profile.Status.MatchingClusterRefs, dependencies, logger)

		profiles := manager.GetTrackedProfiles()
		Expect(len(profiles.Profiles)).ToNot(BeZero())
		for i := range dependencies {
			v, ok := profiles.Profiles[dependencies[i]]
			Expect(ok).To(BeTrue())
			Expect(len(v.Clusters)).To(Equal(len(profile.Status.MatchingClusterRefs)))
			for j := range v.Clusters {
				Expect(len(v.Clusters[j].Dependents)).To(Equal(1))
			}
		}

		profile.Spec.DependsOn = nil
		manager.UpdateDependencies(objRef, profile.Status.MatchingClusterRefs, nil, logger)

		verifyNoDeployments(dependencies)
	})
})

func setupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	Expect(configv1beta1.AddToScheme(s)).To(Succeed())
	Expect(clientgoscheme.AddToScheme(s)).To(Succeed())
	return s
}

func getProfile(namespace string) *configv1beta1.Profile {
	return &configv1beta1.Profile{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
		},
		Spec: configv1beta1.Spec{
			DependsOn: []string{randomString(), randomString()},
		},
		Status: configv1beta1.Status{
			MatchingClusterRefs: []corev1.ObjectReference{
				{
					Namespace:  namespace,
					Name:       randomString(),
					Kind:       libsveltosv1beta1.SveltosClusterKind,
					APIVersion: libsveltosv1beta1.GroupVersion.String(),
				},
				{
					Namespace:  namespace,
					Name:       randomString(),
					Kind:       libsveltosv1beta1.SveltosClusterKind,
					APIVersion: libsveltosv1beta1.GroupVersion.String(),
				},
			},
		},
	}
}

func getDependencies(profile *configv1beta1.Profile) []corev1.ObjectReference {
	dependencies := make([]corev1.ObjectReference, len(profile.Spec.DependsOn))
	for i := range profile.Spec.DependsOn {
		dependencies[i] = corev1.ObjectReference{
			Namespace:  profile.Namespace,
			Name:       profile.Spec.DependsOn[i],
			Kind:       configv1beta1.ProfileKind,
			APIVersion: configv1beta1.GroupVersion.String(),
		}
	}
	return dependencies
}

func verifyNoDeployments(dependencies []corev1.ObjectReference) {
	manager, err := dependencymanager.GetManagerInstance()
	Expect(err).To(BeNil())
	profiles := manager.GetTrackedProfiles()
	Expect(len(profiles.Profiles)).ToNot(BeZero())
	for i := range dependencies {
		_, ok := profiles.Profiles[dependencies[i]]
		Expect(ok).To(BeFalse())
	}
}
