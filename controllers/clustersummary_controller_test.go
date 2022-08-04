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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	fakedeployer "github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer/fake"
)

var _ = Describe("ClustersummaryController", func() {
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var namespace string
	var clusterName string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = "reconcile" + util.RandomString(5)

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
		}

		clusterName = util.RandomString(7)
		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, namespace, clusterName)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      clusterName,
			},
		}
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		clusterSummaryName := client.ObjectKey{
			Name: clusterSummary.Name,
		}
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err = c.Get(context.TODO(), clusterSummaryName, currentClusterSummary)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentClusterSummary,
				configv1alpha1.ClusterSummaryFinalizer,
			),
		).Should(BeTrue())
	})
})

var _ = Describe("ClusterSummaryReconciler: requeue methods", func() {
	var referencingClusterSummary *configv1alpha1.ClusterSummary
	var nonReferencingClusterSummary *configv1alpha1.ClusterSummary
	var workloadRole *configv1alpha1.WorkloadRole

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		workloadRole = &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeNamespaced,
			},
		}

		referencingClusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					WorkloadRoleRefs: []corev1.ObjectReference{
						{Name: workloadRole.Name},
					},
				},
			},
		}

		nonReferencingClusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					WorkloadRoleRefs: []corev1.ObjectReference{
						{Name: workloadRole.Name + util.RandomString(5)},
					},
				},
			},
		}
	})

	AfterEach(func() {
		Expect(testEnv.Client.Delete(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), nonReferencingClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), workloadRole)).To(Succeed())
	})

	It("requeueClusterSummaryForWorkloadRole returns correct ClusterSummary for a WorkloadRole", func() {
		Expect(testEnv.Client.Create(context.TODO(), workloadRole)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonReferencingClusterSummary)).To(Succeed())

		clusterSummaryName := client.ObjectKey{
			Name: referencingClusterSummary.Name,
		}
		_, err := clusterSummaryReconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterSummaryList := controllers.RequeueClusterSummaryForWorkloadRole(clusterSummaryReconciler, workloadRole)
			result := reconcile.Request{NamespacedName: types.NamespacedName{Name: referencingClusterSummary.Name}}
			for i := range clusterSummaryList {
				if clusterSummaryList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
