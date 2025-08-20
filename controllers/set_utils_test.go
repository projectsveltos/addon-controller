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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Set utils", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
	})

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

		// Only ClusterAPI are used, so we can pass clusterSet.Status.MatchingClusterRefs
		controllers.SelectMoreClusters(setScope, clusterSet.Status.MatchingClusterRefs)
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

		// Only ClusterAPI are used, so we can pass clusterSet.Status.MatchingClusterRefs
		controllers.SelectMoreClusters(setScope, clusterSet.Status.MatchingClusterRefs)
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

		// Only ClusterAPI are used, so we can pass clusterSet.Status.MatchingClusterRefs
		controllers.SelectMoreClusters(setScope, setScope.GetStatus().MatchingClusterRefs)
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

		Expect(controllers.SelectClusters(context.TODO(), nil, setScope, logger)).To(Succeed())
		Expect(len(clusterSet.Status.SelectedClusterRefs)).To(Equal(clusterSet.Spec.MaxReplicas))
	})

	It("pruneConnectionDownClusters remove SveltosCluster with connectionStatus set to Down", func() {
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
			Status: libsveltosv1beta1.Status{},
		}

		initObjects := []client.Object{}

		clusterSet.Status.MatchingClusterRefs = make([]corev1.ObjectReference, 0)
		// Add 3 SveltosCluster with Status.ConnectionStatus set to Down
		for i := 0; i < 3; i++ {
			sveltosCluster := libsveltosv1beta1.SveltosCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: randomString(),
					Name:      randomString(),
				},
				Status: libsveltosv1beta1.SveltosClusterStatus{
					ConnectionStatus: libsveltosv1beta1.ConnectionDown,
					Ready:            true,
				},
			}

			initObjects = append(initObjects, &sveltosCluster)

			clusterSet.Status.MatchingClusterRefs = append(clusterSet.Status.MatchingClusterRefs,
				corev1.ObjectReference{
					Kind:       string(libsveltosv1beta1.ClusterTypeSveltos),
					APIVersion: libsveltosv1beta1.GroupVersion.String(),
					Namespace:  sveltosCluster.Namespace,
					Name:       sveltosCluster.Name,
				})
		}

		healthySveltosClusters := make(map[corev1.ObjectReference]bool)
		// Add 3 SveltosCluster with Status.ConnectionStatus set to Healthy
		for i := 0; i < 3; i++ {
			sveltosCluster := libsveltosv1beta1.SveltosCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: randomString(),
					Name:      randomString(),
				},
				Status: libsveltosv1beta1.SveltosClusterStatus{
					ConnectionStatus: libsveltosv1beta1.ConnectionHealthy,
					Ready:            true,
				},
			}

			initObjects = append(initObjects, &sveltosCluster)

			objectRef := &corev1.ObjectReference{
				Kind:       string(libsveltosv1beta1.ClusterTypeSveltos),
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
				Namespace:  sveltosCluster.Namespace,
				Name:       sveltosCluster.Name,
			}

			clusterSet.Status.MatchingClusterRefs = append(clusterSet.Status.MatchingClusterRefs, *objectRef)

			healthySveltosClusters[*objectRef] = true
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		Expect(addTypeInformationToObject(scheme, &clusterSet)).To(Succeed())

		setScope := &scope.SetScope{
			Set: &clusterSet,
		}

		result, err := controllers.PruneConnectionDownClusters(context.TODO(), c, clusterSet.Status.MatchingClusterRefs, logger)
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(3))
		for i := range result {
			v := healthySveltosClusters[result[i]]
			Expect(v).To(BeTrue())
		}

		Expect(controllers.SelectClusters(context.TODO(), c, setScope, logger)).To(Succeed())
		Expect(len(clusterSet.Status.SelectedClusterRefs)).To(Equal(clusterSet.Spec.MaxReplicas))
		// Verify only SveltosCluster with connectionStatus set to Healthy are picked
		for i := range clusterSet.Status.SelectedClusterRefs {
			clusterRef := clusterSet.Status.SelectedClusterRefs[i]
			v := healthySveltosClusters[clusterRef]
			Expect(v).To(BeTrue())
		}
	})
})
