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
	"context"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
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
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					PolicyRefs: []configv1alpha1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name,
							Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
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
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					PolicyRefs: []configv1alpha1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      randomString(),
							Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		set := libsveltosset.Set{}
		key := corev1.ObjectReference{APIVersion: configMap.APIVersion,
			Kind: string(libsveltosv1alpha1.ConfigMapReferencedResourceKind), Namespace: configMap.Namespace, Name: configMap.Name}

		set.Insert(&corev1.ObjectReference{APIVersion: configv1alpha1.GroupVersion.String(),
			Kind: configv1alpha1.ClusterSummaryKind, Namespace: clusterSummary0.Namespace, Name: clusterSummary0.Name})
		reconciler.ReferenceMap[key] = &set

		requests := controllers.RequeueClusterSummaryForReference(reconciler, context.TODO(), configMap)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal(clusterSummary0.Name))

		set.Insert(&corev1.ObjectReference{APIVersion: configv1alpha1.GroupVersion.String(),
			Kind: configv1alpha1.ClusterSummaryKind, Namespace: clusterSummary1.Namespace, Name: clusterSummary1.Name})
		reconciler.ReferenceMap[key] = &set

		requests = controllers.RequeueClusterSummaryForReference(reconciler, context.TODO(), configMap)
		Expect(requests).To(HaveLen(2))
		Expect(requests).To(ContainElement(reconcile.Request{NamespacedName: types.NamespacedName{Name: clusterSummary0.Name}}))
		Expect(requests).To(ContainElement(reconcile.Request{NamespacedName: types.NamespacedName{Name: clusterSummary1.Name}}))
	})
})
