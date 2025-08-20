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
	"context"
	"sync"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("Profile Controller", func() {
	var profile *configv1beta1.Profile
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())

		namespace := randomString()
		profile = &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, profile)).To(Succeed())
	})

	It("limitReferencesToNamespace resets any reference to be within namespace", func() {
		profile.Spec = configv1beta1.Spec{
			ClusterRefs: []corev1.ObjectReference{
				{
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Namespace: profile.Namespace,
					Name:      randomString(),
				},
			},
			PolicyRefs: []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
			},
			KustomizationRefs: []configv1beta1.KustomizationRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Kind:      sourcev1.GitRepositoryKind,
					Namespace: randomString(),
					Name:      randomString(),
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
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		controllers.LimitReferencesToNamespace(reconciler, profile)

		for i := range profile.Spec.ClusterRefs {
			Expect(profile.Spec.ClusterRefs[i].Namespace).To(Equal(profile.Namespace))
		}

		for i := range profile.Spec.PolicyRefs {
			Expect(profile.Spec.PolicyRefs[i].Namespace).To(Equal(profile.Namespace))
		}

		for i := range profile.Spec.KustomizationRefs {
			Expect(profile.Spec.KustomizationRefs[i].Namespace).To(Equal(profile.Namespace))
		}
	})

	It("getClustersFromClusterSets gets cluster selected by referenced sets", func() {
		set1 := &libsveltosv1beta1.Set{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: profile.Namespace,
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: clusterKind, APIVersion: clusterv1.GroupVersion.String(),
					},
				},
			},
		}

		set2 := &libsveltosv1beta1.Set{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: profile.Namespace,
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		profile.Spec.SetRefs = []string{
			set1.Name,
			set2.Name,
		}

		initObjects := []client.Object{
			set1,
			set2,
			profile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.ProfileReconciler{
			Client:        c,
			Scheme:        scheme,
			SetMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		clusters, err := controllers.GetClustersFromSets(reconciler, context.TODO(),
			profile.Namespace, profile.Spec.SetRefs, logger)
		Expect(err).To(BeNil())
		Expect(clusters).ToNot(BeNil())
		for i := range set1.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  set1.Status.SelectedClusterRefs[i].Namespace,
				Name:       set1.Status.SelectedClusterRefs[i].Name,
				Kind:       set1.Status.SelectedClusterRefs[i].Kind,
				APIVersion: set1.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
		for i := range set2.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  set2.Status.SelectedClusterRefs[i].Namespace,
				Name:       set2.Status.SelectedClusterRefs[i].Name,
				Kind:       set2.Status.SelectedClusterRefs[i].Kind,
				APIVersion: set2.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
	})
})
