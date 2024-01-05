/*
Copyright 2023. projectsveltos.io. All rights reserved.

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
	"sync"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("Profile Controller", func() {
	It("limitReferencesToNamespace resets any reference to be within namespace", func() {
		namespace := randomString()
		profile := &configv1alpha1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1alpha1.Spec{
				ClusterRefs: []corev1.ObjectReference{
					{
						Namespace: randomString(),
						Name:      randomString(),
					},
					{
						Namespace: namespace,
						Name:      randomString(),
					},
				},
				PolicyRefs: []configv1alpha1.PolicyRef{
					{
						Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
						Namespace: randomString(),
						Name:      randomString(),
					},
					{
						Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
						Namespace: randomString(),
						Name:      randomString(),
					},
				},
				KustomizationRefs: []configv1alpha1.KustomizationRef{
					{
						Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
						Namespace: randomString(),
						Name:      randomString(),
					},
					{
						Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
						Namespace: randomString(),
						Name:      randomString(),
					},
					{
						Kind:      sourcev1.GitRepositoryKind,
						Namespace: randomString(),
						Name:      randomString(),
					},
				},
			},
		}

		initObjects := []client.Object{
			profile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ProfileReconciler{
			Client:        c,
			Scheme:        scheme,
			ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			ProfileMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		controllers.LimitReferencesToNamespace(reconciler, profile)

		for i := range profile.Spec.ClusterRefs {
			Expect(profile.Spec.ClusterRefs[i].Namespace).To(Equal(namespace))
		}

		for i := range profile.Spec.PolicyRefs {
			Expect(profile.Spec.PolicyRefs[i].Namespace).To(Equal(namespace))
		}

		for i := range profile.Spec.KustomizationRefs {
			Expect(profile.Spec.KustomizationRefs[i].Namespace).To(Equal(namespace))
		}
	})
})
