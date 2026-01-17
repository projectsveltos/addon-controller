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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("Profile Transformations", func() {
	var namespace string

	BeforeEach(func() {
		namespace = randomString()
	})

	It("requeueProfileForCluster returns matching ClusterProfiles", func() {
		key := randomString()
		value := randomString()

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					key: value,
				},
			},
		}

		Expect(addTypeInformationToObject(scheme, cluster)).To(BeNil())

		matchingProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterProfileNamePrefix + randomString(),
				Namespace: cluster.Namespace,
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							key: value,
						},
					},
				},
			},
		}

		nonMatchingProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterProfileNamePrefix + randomString(),
				Namespace: cluster.Namespace,
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): value,
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			matchingProfile,
			nonMatchingProfile,
			cluster,
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

		By("Setting ProfileReconciler internal structures")
		matchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: configv1beta1.ProfileKind, Namespace: matchingProfile.Namespace, Name: matchingProfile.Name}

		nonMatchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: configv1beta1.ProfileKind, Namespace: nonMatchingProfile.Namespace, Name: nonMatchingProfile.Name}

		// ClusterMap contains, per ClusterName, list of ClusterProfiles matching it.
		profileSet := &libsveltosset.Set{}
		profileSet.Insert(&matchingInfo)
		clusterInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: cluster.Kind, Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = profileSet

		By("Expect only matchingProfile to be requeued")
		requests := controllers.RequeueProfileForCluster(reconciler, context.TODO(), cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: matchingProfile.Namespace, Name: matchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing Profile ClusterSelector again to have two ClusterProfiles match")
		nonMatchingProfile.Spec.ClusterSelector = matchingProfile.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingProfile)).To(Succeed())

		profileSet.Insert(&nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = profileSet

		requests = controllers.RequeueProfileForCluster(reconciler, context.TODO(), cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Namespace: matchingProfile.Namespace, Name: matchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Namespace: nonMatchingProfile.Namespace, Name: nonMatchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))
	})

	It("requeueProfileForCluster returns matching Profiles", func() {
		key := randomString()
		value := randomString()

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					key: value,
				},
			},
		}

		matchingProfile := &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterProfileNamePrefix + randomString(),
				Namespace: cluster.Namespace,
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							key: value,
						},
					},
				},
			},
		}

		nonMatchingProfile1 := &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterProfileNamePrefix + randomString(),
				Namespace: cluster.Namespace,
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): value,
						},
					},
				},
			},
		}

		nonMatchingProfile2 := &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterProfileNamePrefix + randomString(),
				Namespace: randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							key: value,
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			matchingProfile,
			nonMatchingProfile1,
			nonMatchingProfile2,
			cluster,
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

		By("Setting ProfileReconciler internal structures")
		matchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: configv1beta1.ProfileKind,
			Namespace: matchingProfile.Namespace, Name: matchingProfile.Name}
		reconciler.Profiles[matchingInfo] = matchingProfile.Spec.ClusterSelector

		nonMatchingInfo1 := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: configv1beta1.ProfileKind,
			Namespace: nonMatchingProfile1.Namespace, Name: nonMatchingProfile1.Name}
		reconciler.Profiles[nonMatchingInfo1] = nonMatchingProfile1.Spec.ClusterSelector

		nonMatchingInfo2 := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: configv1beta1.ProfileKind,
			Namespace: nonMatchingProfile2.Namespace, Name: nonMatchingProfile2.Name}
		reconciler.Profiles[nonMatchingInfo2] = nonMatchingProfile2.Spec.ClusterSelector

		// ClusterMap contains, per ClusterName, list of Profiles matching it.
		profileSet := &libsveltosset.Set{}
		profileSet.Insert(&matchingInfo)
		clusterInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: cluster.Kind, Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = profileSet

		By("Expect only matchingProfile to be requeued")
		requests := controllers.RequeueProfileForCluster(reconciler, context.TODO(), cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: matchingProfile.Namespace, Name: matchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing Profile ClusterSelector again to have two ClusterProfiles match")
		nonMatchingProfile1.Spec.ClusterSelector = matchingProfile.Spec.ClusterSelector
		// Even though selector is a match, namespace is not for nonMatchingProfile2
		nonMatchingProfile2.Spec.ClusterSelector = matchingProfile.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingProfile1)).To(Succeed())
		Expect(c.Update(context.TODO(), nonMatchingProfile2)).To(Succeed())

		reconciler.Profiles[nonMatchingInfo1] = nonMatchingProfile1.Spec.ClusterSelector
		reconciler.Profiles[nonMatchingInfo2] = nonMatchingProfile2.Spec.ClusterSelector

		profileSet.Insert(&nonMatchingInfo1)
		reconciler.ClusterMap[clusterInfo] = profileSet

		requests = controllers.RequeueProfileForCluster(reconciler, context.TODO(), cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: matchingProfile.Namespace, Name: matchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: nonMatchingProfile1.Namespace, Name: nonMatchingProfile1.Name}}
		Expect(requests).To(ContainElement(expected))
	})
})
