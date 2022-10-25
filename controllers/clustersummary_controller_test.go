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

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/controllers"
	"github.com/projectsveltos/sveltos-manager/controllers/chartmanager"
	"github.com/projectsveltos/sveltos-manager/pkg/scope"
)

var _ = Describe("ClustersummaryController", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
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

		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: selector,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterProfile.Name, clusterName)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
			},
		}

		prepareForDeployment(clusterProfile, clusterSummary, cluster)

		// Get ClusterSummary so OwnerReference is set
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, clusterSummary)).To(Succeed())
	})

	It("isPaused returns true if CAPI Cluster has Spec.Paused set", func() {
		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          nil,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.IsPaused(reconciler, context.TODO(), clusterSummary)).To(BeFalse())

		cluster.Spec.Paused = true
		Expect(c.Update(context.TODO(), cluster)).To(Succeed())

		Expect(controllers.IsPaused(reconciler, context.TODO(), clusterSummary)).To(BeTrue())
	})

	It("isPaused returns false when Cluster does not exist", func() {
		clusterSummary.Annotations = map[string]string{
			"cluster.x-k8s.io/paused": "ok",
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          nil,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.IsPaused(reconciler, context.TODO(), clusterSummary)).To(BeFalse())
	})

	It("shouldReconcile returns true when mode is Continuous", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous

		initObjects := []client.Object{
			clusterProfile,
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
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("updateChartMap updates chartMap always but in DryRun mode", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1alpha1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
				ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString()},
		}

		initObjects := []client.Object{
			clusterSummary,
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
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.UpdateChartMap(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(Succeed())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		Expect(manager.CanManageChart(clusterSummary, &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0])).To(BeTrue())

		// set mode to dryRun
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(c.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Add an extra helm chart
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts = append(currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts,
			configv1alpha1.HelmChart{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
				ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString()})
		Expect(c.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		// Verify chart registrations have not changed
		Expect(manager.CanManageChart(clusterSummary, &currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[0])).To(BeTrue())
		Expect(manager.CanManageChart(clusterSummary, &currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[1])).To(BeFalse())
	})

	It("shouldReconcile returns true when mode is OneTime but not all policies are deployed", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{Namespace: randomString(), Name: randomString(), Kind: string(configv1alpha1.ConfigMapReferencedResourceKind)},
		}
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioning},
		}

		initObjects := []client.Object{
			clusterProfile,
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
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("shouldReconcile returns true when mode is OneTime but not all helm charts are deployed", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1alpha1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(), ReleaseName: randomString()},
		}
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusProvisioning},
		}

		initObjects := []client.Object{
			clusterProfile,
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
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("shouldReconcile returns false when mode is OneTime and policies and helm charts are deployed", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1alpha1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(), ReleaseName: randomString()},
		}
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{Namespace: randomString(), Name: randomString(), Kind: string(configv1alpha1.ConfigMapReferencedResourceKind)},
		}
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusProvisioned},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioned},
		}

		initObjects := []client.Object{
			clusterProfile,
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
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, klogr.New())).To(BeFalse())
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		clusterSummaryName := client.ObjectKey{
			Name:      clusterSummary.Name,
			Namespace: clusterSummary.Namespace,
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

	It("shouldRedeploy returns true in DryRun mode", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusProvisioned},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioned},
		}
		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		f := controllers.GetHandlersForFeature(configv1alpha1.FeatureResources)

		// In SyncMode DryRun even if config is same (input for ShouldRedeploy) result is redeploy
		Expect(controllers.ShouldRedeploy(reconciler, clusterSummaryScope, f, true, klogr.New())).To(BeTrue())

		clusterSummaryName := client.ObjectKey{
			Name:      clusterSummary.Name,
			Namespace: clusterSummary.Namespace,
		}

		// Update SyncMode to Continuous
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err = c.Get(context.TODO(), clusterSummaryName, currentClusterSummary)
		Expect(err).ToNot(HaveOccurred())
		currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(c.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		clusterSummaryScope.ClusterSummary = currentClusterSummary
		// In SyncMode != DryRun and if config is same (input for ShouldRedeploy) result is do not redeploy
		Expect(controllers.ShouldRedeploy(reconciler, clusterSummaryScope, f, true, klogr.New())).To(BeFalse())
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
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		clusterSummaryName := client.ObjectKey{
			Name:      clusterSummary.Name,
			Namespace: clusterSummary.Namespace,
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

	It("canRemoveFinalizer in DryRun returns true when ClusterSummary and ClusterProfile are deleted", func() {
		controllerutil.AddFinalizer(clusterSummary, configv1alpha1.ClusterSummaryFinalizer)
		controllerutil.AddFinalizer(clusterProfile, configv1alpha1.ClusterProfileFinalizer)

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		// ClusterSummary not marked for deletion. So cannot remove finalizer
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeFalse())

		// Mark ClusterSummary for deletion
		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now
		clusterSummaryScope.ClusterSummary = clusterSummary

		// ClusterProfile is not marked for deletion. So cannot remove finalizer
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeFalse())

		// Mark ClusterProfile for deletion
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		Expect(c.Delete(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummaryScope.ClusterProfile = currentClusterProfile

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("canRemoveFinalizer in not DryRun returns true when ClusterSummary is deleted and features removed", func() {
		controllerutil.AddFinalizer(clusterSummary, configv1alpha1.ClusterSummaryFinalizer)

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusRemoved},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusRemoving},
		}

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeFalse())

		// Mark ClusterSummary for deletion
		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now
		clusterSummaryScope.ClusterSummary = clusterSummary

		clusterSummaryScope.ClusterSummary = clusterSummary

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeFalse())

		// Mark all features as removed
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusRemoved},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusRemoved},
		}

		clusterSummaryScope.ClusterSummary = clusterSummary

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeTrue())
	})

	It("canRemoveFinalizer returns true when Cluster is gone", func() {
		controllerutil.AddFinalizer(clusterSummary, configv1alpha1.ClusterSummaryFinalizer)

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusRemoved},
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusRemoving},
		}

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          deployer,
			ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
			ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
			PolicyMux:         sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		// Since ClusterSummary is not yet marked for deletion, finalizer cannot be removed
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeFalse())

		// Mark ClusterSummary for deletion
		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now
		clusterSummaryScope.ClusterSummary = clusterSummary

		// Because CAPI cluster does not exist and ClusterSummary is marked for deletion, finalizer can be removed
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope, klogr.New())).To(BeTrue())
	})
})

var _ = Describe("ClusterSummaryReconciler: requeue methods", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
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

		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
		}

		referencingClusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					PolicyRefs: []libsveltosv1alpha1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name,
							Kind:      string(configv1alpha1.ConfigMapReferencedResourceKind),
						},
					},
					SyncMode: configv1alpha1.SyncModeContinuous,
				},
			},
		}

		nonReferencingClusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					PolicyRefs: []libsveltosv1alpha1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name + randomString(),
							Kind:      string(configv1alpha1.ConfigMapReferencedResourceKind),
						},
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

		Expect(testEnv.Client.Create(context.TODO(), clusterProfile)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())

		configMap := createConfigMapWithPolicy(namespace, randomString(), fmt.Sprintf(editClusterRole, randomString()))
		By(fmt.Sprintf("Creating %s %s/%s", configMap.Kind, configMap.Namespace, configMap.Name))
		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())

		By(fmt.Sprintf("Configuring ClusterSummary %s reference %s %s/%s",
			referencingClusterSummary.Name, configMap.Kind, configMap.Namespace, configMap.Name))
		referencingClusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Namespace: namespace,
				Name:      configMap.Name,
				Kind:      string(configv1alpha1.ConfigMapReferencedResourceKind),
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, referencingClusterSummary)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, referencingClusterSummary, clusterProfile)

		Expect(testEnv.Client.Create(context.TODO(), nonReferencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, nonReferencingClusterSummary)).To(Succeed())

		clusterSummaryName := client.ObjectKey{
			Name:      referencingClusterSummary.Name,
			Namespace: referencingClusterSummary.Namespace,
		}

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), testEnv.Client)
		Expect(dep.RegisterFeatureID(string(configv1alpha1.FeatureResources))).To(Succeed())
		clusterSummaryReconciler := getClusterSummaryReconciler(testEnv.Client, dep)

		// Reconcile so it is tracked that referencingClusterSummary is referencing configMap
		By(fmt.Sprintf("Reconciling ClusterSummary %s", clusterSummaryName))
		_, err := clusterSummaryReconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		// Eventual loop so testEnv Cache is synced
		By(fmt.Sprintf("Verifying that a change in ConfigMap %s/%s causes ClusterSummary %s reconciliation",
			configMap.Namespace, configMap.Name, referencingClusterSummary.Name))
		Eventually(func() bool {
			clusterSummaryList := controllers.RequeueClusterSummaryForReference(clusterSummaryReconciler, configMap)
			result := reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: referencingClusterSummary.Namespace, Name: referencingClusterSummary.Name}}
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

		Expect(testEnv.Client.Create(context.TODO(), clusterProfile)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, referencingClusterSummary)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, referencingClusterSummary, clusterProfile)

		Expect(testEnv.Client.Create(context.TODO(), nonReferencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, nonReferencingClusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		clusterSummaryName := client.ObjectKey{
			Name:      referencingClusterSummary.Name,
			Namespace: referencingClusterSummary.Namespace,
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
			result := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: referencingClusterSummary.Namespace,
					Name:      referencingClusterSummary.Name,
				},
			}
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
