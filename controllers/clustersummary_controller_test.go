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
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	fakedeployer "github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer/fake"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

var _ = Describe("ClustersummaryController", func() {
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var clusterName string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

		clusterName = randomString()
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, namespace, clusterName)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
			},
		}

		prepareForDeployment(clusterFeature, clusterSummary, cluster)
	})

	It("isPaused returns true if CAPI Cluster has Spec.Paused set", func() {
		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          nil,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.IsPaused(reconciler, context.TODO(), clusterSummary)).To(BeFalse())

		cluster.Spec.Paused = true
		Expect(c.Update(context.TODO(), cluster)).To(Succeed())

		Expect(controllers.IsPaused(reconciler, context.TODO(), clusterSummary)).To(BeTrue())
	})

	It("shouldReconcile returns true when mode is Continuous", func() {
		clusterSummary.Spec.ClusterFeatureSpec.SyncMode = configv1alpha1.SyncModeContinuous

		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          nil,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("shouldReconcile returns true when mode is OneTime but not all policies are deployed", func() {
		clusterSummary.Spec.ClusterFeatureSpec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterSummary.Spec.ClusterFeatureSpec.PolicyRefs = []corev1.ObjectReference{
			{Namespace: randomString(), Name: randomString()},
		}
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioning},
		}

		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          nil,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("shouldReconcile returns true when mode is OneTime but not all helm charts are deployed", func() {
		clusterSummary.Spec.ClusterFeatureSpec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterSummary.Spec.ClusterFeatureSpec.HelmCharts = []configv1alpha1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(), ReleaseName: randomString()},
		}
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusProvisioning},
		}

		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          nil,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("shouldReconcile returns false when mode is OneTime and policies and helm charts are deployed", func() {
		clusterSummary.Spec.ClusterFeatureSpec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterSummary.Spec.ClusterFeatureSpec.HelmCharts = []configv1alpha1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(), ReleaseName: randomString()},
		}
		clusterSummary.Spec.ClusterFeatureSpec.PolicyRefs = []corev1.ObjectReference{
			{Namespace: randomString(), Name: randomString()},
		}
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusProvisioned},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioned},
		}

		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          nil,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeFalse())
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
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

	It("Reconciliation of deleted ClusterSummary removes finalizer only when all features are removed", func() {
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusRemoving},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusRemoved},
		}

		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now
		controllerutil.AddFinalizer(clusterSummary, configv1alpha1.ClusterSummaryFinalizer)

		initObjects := []client.Object{
			clusterSummary,
			clusterFeature,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		addOwnerReference(context.TODO(), c, clusterSummary, clusterFeature)

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			PolicyMux:         sync.Mutex{},
		}

		clusterSummaryName := client.ObjectKey{
			Name: clusterSummary.Name,
		}

		// Since FeatureHelm is still marked to be removed, reconciliation won't
		// remove finalizer

		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err = c.Get(context.TODO(), clusterSummaryName, currentClusterSummary)
		Expect(err).ToNot(HaveOccurred())

		// Mark all features as removed
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusRemoved},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusRemoved},
		}

		Expect(c.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Since all features are now marked as removed, reconciliation will
		// remove finalizer

		_, err = reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), clusterSummaryName, currentClusterSummary)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("ClusterSummaryReconciler: requeue methods", func() {
	var referencingClusterSummary *configv1alpha1.ClusterSummary
	var nonReferencingClusterSummary *configv1alpha1.ClusterSummary
	var configMap *corev1.ConfigMap
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		namespace = "reconcile" + randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
		}

		referencingClusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					PolicyRefs: []corev1.ObjectReference{
						{Namespace: configMap.Namespace, Name: configMap.Name},
					},
					SyncMode: configv1alpha1.SyncModeContinuous,
				},
			},
		}

		nonReferencingClusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					PolicyRefs: []corev1.ObjectReference{
						{Namespace: configMap.Namespace, Name: configMap.Name + randomString()},
					},
					SyncMode: configv1alpha1.SyncModeContinuous,
				},
			},
		}
	})

	AfterEach(func() {
		err := testEnv.Client.Delete(context.TODO(), referencingClusterSummary)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		err = testEnv.Client.Delete(context.TODO(), nonReferencingClusterSummary)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		Expect(testEnv.Client.Delete(context.TODO(), cluster)).To(Succeed())
	})

	It("requeueClusterSummaryForConfigMap returns correct ClusterSummary for a ConfigMap", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())

		configMap := createConfigMapWithPolicy(namespace, randomString(), fmt.Sprintf(editClusterRole, randomString()))
		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())
		referencingClusterSummary.Spec.ClusterFeatureSpec.PolicyRefs = []corev1.ObjectReference{
			{Namespace: namespace, Name: configMap.Name},
		}

		Expect(testEnv.Client.Create(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonReferencingClusterSummary)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, nonReferencingClusterSummary)).To(Succeed())

		clusterSummaryName := client.ObjectKey{
			Name: referencingClusterSummary.Name,
		}

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), testEnv.Client)
		Expect(dep.RegisterFeatureID(string(configv1alpha1.FeatureResources))).To(Succeed())
		clusterSummaryReconciler := getClusterSummaryReconciler(testEnv.Client, dep)

		_, err := clusterSummaryReconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterSummaryList := controllers.RequeueClusterSummaryForConfigMap(clusterSummaryReconciler, configMap)
			result := reconcile.Request{NamespacedName: types.NamespacedName{Name: referencingClusterSummary.Name}}
			for i := range clusterSummaryList {
				if clusterSummaryList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Client.Delete(context.TODO(), ns)).To(Succeed())
	})

	It("requeueClusterSummaryForCluster returns correct ClusterSummary for a CAPI Cluster", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonReferencingClusterSummary)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, nonReferencingClusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		clusterSummaryName := client.ObjectKey{
			Name: referencingClusterSummary.Name,
		}

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), testEnv.Client)
		Expect(dep.RegisterFeatureID(string(configv1alpha1.FeatureResources))).To(Succeed())
		clusterSummaryReconciler := getClusterSummaryReconciler(testEnv.Client, dep)

		_, err := clusterSummaryReconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterSummaryList := controllers.RequeueClusterSummaryForCluster(clusterSummaryReconciler, cluster)
			result := reconcile.Request{NamespacedName: types.NamespacedName{Name: referencingClusterSummary.Name}}
			for i := range clusterSummaryList {
				if clusterSummaryList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Client.Delete(context.TODO(), ns)).To(Succeed())
	})
})
