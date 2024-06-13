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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Set utils", func() {
	It("selectMoreClusters does nothing when selected clusters matches MaxReplicas", func() {
		selectedCluster := []corev1.ObjectReference{
			{
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
				Namespace:  randomString(),
				Name:       randomString(),
			},
			{
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
				Namespace:  randomString(),
				Name:       randomString(),
			},
		}
		clusterSet := libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.Spec{
				MaxReplicas: 2,
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
			Status: libsveltosv1beta1.Status{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind:       clusterKind,
						APIVersion: clusterv1.GroupVersion.String(),
						Namespace:  randomString(),
						Name:       randomString(),
					},
				},
			},
		}

		clusterSet.Status.MatchingClusterRefs = append(clusterSet.Status.MatchingClusterRefs, selectedCluster...)
		clusterSet.Status.SelectedClusterRefs = selectedCluster
		Expect(addTypeInformationToObject(scheme, &clusterSet)).To(Succeed())

		setScope := &scope.SetScope{
			Set: &clusterSet,
		}

		controllers.SelectMoreClusters(setScope)
		Expect(len(clusterSet.Status.SelectedClusterRefs)).To(Equal(len(selectedCluster)))
		for i := range selectedCluster {
			Expect(clusterSet.Status.SelectedClusterRefs).To(ContainElement(selectedCluster[i]))
		}
	})

	It("selectMoreClusters adds new clusters till it matches MaxReplicas", func() {
		clusterSet := libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.Spec{
				MaxReplicas: 2,
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
			Status: libsveltosv1beta1.Status{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind:       clusterKind,
						APIVersion: clusterv1.GroupVersion.String(),
						Namespace:  randomString(),
						Name:       randomString(),
					},
					{
						Kind:       clusterKind,
						APIVersion: clusterv1.GroupVersion.String(),
						Namespace:  randomString(),
						Name:       randomString(),
					},
					{
						Kind:       clusterKind,
						APIVersion: clusterv1.GroupVersion.String(),
						Namespace:  randomString(),
						Name:       randomString(),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, &clusterSet)).To(Succeed())

		setScope := &scope.SetScope{
			Set: &clusterSet,
		}

		controllers.SelectMoreClusters(setScope)
		Expect(len(clusterSet.Status.SelectedClusterRefs)).To(Equal(clusterSet.Spec.MaxReplicas))
	})

	It("selectMoreClusters adds as many clusters as available up to MaxReplicas", func() {
		// MaxReplicas is 4 but there are only 3 matching clusters.
		// All 3 will be selected
		clusterSet := libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.Spec{
				MaxReplicas: 4,
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
			Status: libsveltosv1beta1.Status{
				MatchingClusterRefs: []corev1.ObjectReference{
					{
						Kind:       clusterKind,
						APIVersion: clusterv1.GroupVersion.String(),
						Namespace:  randomString(),
						Name:       randomString(),
					},
					{
						Kind:       clusterKind,
						APIVersion: clusterv1.GroupVersion.String(),
						Namespace:  randomString(),
						Name:       randomString(),
					},
					{
						Kind:       clusterKind,
						APIVersion: clusterv1.GroupVersion.String(),
						Namespace:  randomString(),
						Name:       randomString(),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, &clusterSet)).To(Succeed())

		setScope := &scope.SetScope{
			Set: &clusterSet,
		}

		controllers.SelectMoreClusters(setScope)
		Expect(len(clusterSet.Status.SelectedClusterRefs)).To(Equal(len(clusterSet.Status.MatchingClusterRefs)))
	})

	It("selectClusters selects MaxReplicas clusters from matching clusters", func() {
		clusterSet := libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1beta1.Spec{
				MaxReplicas: 4,
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
			Status: libsveltosv1beta1.Status{},
		}

		clusterSet.Status.MatchingClusterRefs = make([]corev1.ObjectReference, 0)
		for i := 0; i < 10; i++ {
			clusterSet.Status.MatchingClusterRefs = append(clusterSet.Status.MatchingClusterRefs,
				corev1.ObjectReference{
					Kind:       clusterKind,
					APIVersion: clusterv1.GroupVersion.String(),
					Namespace:  randomString(),
					Name:       randomString(),
				})
		}
		Expect(addTypeInformationToObject(scheme, &clusterSet)).To(Succeed())

		setScope := &scope.SetScope{
			Set: &clusterSet,
		}

		controllers.SelectClusters(setScope)
		Expect(len(clusterSet.Status.SelectedClusterRefs)).To(Equal(clusterSet.Spec.MaxReplicas))
	})
})
