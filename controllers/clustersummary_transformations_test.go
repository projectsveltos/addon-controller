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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

var _ = Describe("ClustersummaryTransformations map functions", func() {
	var namespace string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = "map-function" + util.RandomString(5)
	})

	It("requeueClusterFeatureForCluster returns matching ClusterFeatures", func() {
		workloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
		}

		clusterSummary0 := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      upstreamClusterNamePrefix + util.RandomString(5),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					WorkloadRoleRefs: []corev1.ObjectReference{
						{Name: workloadRole.Name},
					},
				},
			},
		}

		clusterSummary1 := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      upstreamClusterNamePrefix + util.RandomString(5),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					WorkloadRoleRefs: []corev1.ObjectReference{
						{Name: util.RandomString(5)},
					},
				},
			},
		}

		initObjects := []client.Object{
			workloadRole,
			clusterSummary0,
			clusterSummary1,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		set := controllers.Set{}
		controllers.Insert(&set, clusterSummary0.Name)
		key := controllers.GetEntryKey(controllers.WorkloadRole, "", workloadRole.Name)
		reconciler.ReferenceMap[key] = &set

		requests := controllers.RequeueClusterSummaryForWorkloadRole(reconciler, workloadRole)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal(clusterSummary0.Name))

		controllers.Insert(&set, clusterSummary1.Name)
		reconciler.ReferenceMap[key] = &set

		requests = controllers.RequeueClusterSummaryForWorkloadRole(reconciler, workloadRole)
		Expect(requests).To(HaveLen(2))
		Expect(requests).To(ContainElement(reconcile.Request{NamespacedName: types.NamespacedName{Name: clusterSummary0.Name}}))
		Expect(requests).To(ContainElement(reconcile.Request{NamespacedName: types.NamespacedName{Name: clusterSummary1.Name}}))
	})
})
