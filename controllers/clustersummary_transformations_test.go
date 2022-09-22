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

package controllers_test

import (
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

var _ = Describe("ClustersummaryTransformations map functions", func() {
	var namespace string

	BeforeEach(func() {
		namespace = "map-function" + randomString()
	})

	It("RequeueClusterSummaryForReference returns matching ClusterSummary", func() {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}

		Expect(addTypeInformationToObject(scheme, configMap)).To(Succeed())

		clusterSummary0 := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      upstreamClusterNamePrefix + randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					PolicyRefs: []configv1alpha1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name,
							Kind:      string(configv1alpha1.ConfigMapReferencedResourceKind),
						},
					},
				},
			},
		}

		clusterSummary1 := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      upstreamClusterNamePrefix + randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					PolicyRefs: []configv1alpha1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      randomString(),
							Kind:      string(configv1alpha1.ConfigMapReferencedResourceKind),
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			configMap,
			clusterSummary0,
			clusterSummary1,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
		}

		set := controllers.Set{}
		controllers.Insert(&set, clusterSummary0.Name)
		key := controllers.GetEntryKey(string(configv1alpha1.ConfigMapReferencedResourceKind), configMap.Namespace, configMap.Name)
		reconciler.ReferenceMap[key] = &set

		requests := controllers.RequeueClusterSummaryForReference(reconciler, configMap)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal(clusterSummary0.Name))

		controllers.Insert(&set, clusterSummary1.Name)
		reconciler.ReferenceMap[key] = &set

		requests = controllers.RequeueClusterSummaryForReference(reconciler, configMap)
		Expect(requests).To(HaveLen(2))
		Expect(requests).To(ContainElement(reconcile.Request{NamespacedName: types.NamespacedName{Name: clusterSummary0.Name}}))
		Expect(requests).To(ContainElement(reconcile.Request{NamespacedName: types.NamespacedName{Name: clusterSummary1.Name}}))
	})
})
