/*
Copyright 2022-23. projectsveltos.io. All rights reserved.

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

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("ClustersummaryController", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var clusterName string

	BeforeEach(func() {
		namespace = randomString()

		initialized := true
		clusterName = randomString()
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
			Status: clusterv1.ClusterStatus{
				ControlPlaneReady: initialized,
			},
		}

		clusterProfile = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
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

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, clusterName, false)
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		prepareForDeployment(clusterProfile, clusterSummary, cluster)

		cluster.Status.ControlPlaneReady = initialized

		// Get ClusterSummary so OwnerReference is set
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, clusterSummary)).To(Succeed())
	})

	It("isReady returns true if CAPI Cluster has Initialization.ControlPlaneInitialized set to true", func() {
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       testEnv.Client,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.IsReady(reconciler, context.TODO(), clusterSummary, logr.Logger{})).To(BeTrue())

		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name},
			cluster)).To(Succeed())
		initialized := false
		cluster.Status.ControlPlaneReady = initialized
		Expect(testEnv.Status().Update(context.TODO(), cluster)).To(Succeed())

		Eventually(func() bool {
			currentCluster := clusterv1.Cluster{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name},
				&currentCluster)
			if err != nil {
				return false
			}
			return !currentCluster.Status.ControlPlaneReady
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.IsReady(reconciler, context.TODO(), clusterSummary, logr.Logger{})).To(BeFalse())
	})

	It("isPaused returns true if CAPI Cluster has Spec.Paused set", func() {
		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.IsPaused(reconciler, context.TODO(), clusterSummary)).To(BeFalse())

		paused := true
		cluster.Spec.Paused = paused
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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.IsPaused(reconciler, context.TODO(), clusterSummary)).To(BeFalse())
	})

	It("shouldReconcile returns true when mode is Continuous", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuous

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("updateChartMap updates chartMap always but in DryRun mode", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuous
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
				ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.UpdateChartMap(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		Expect(manager.CanManageChart(clusterSummary, &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0])).To(BeTrue())

		// set mode to dryRun
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeDryRun
		Expect(c.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Add an extra helm chart
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts = append(currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts,
			configv1beta1.HelmChart{
				RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
				ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
			})
		Expect(c.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		// Verify chart registrations have not changed
		Expect(manager.IsChartManaged(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType,
			&currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[0])).To(BeTrue())
		Expect(manager.IsChartManaged(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType,
			&currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[1])).To(BeFalse())
	})

	It("shouldReconcile returns true when mode is OneTime but not all policies are deployed", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeOneTime
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioning},
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope, textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("setFailureMessage set failure message for every features in ClusterSummary", func() {
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    randomString(),
				RepositoryName:   randomString(),
				ChartName:        randomString(),
				ChartVersion:     randomString(),
				ReleaseName:      randomString(),
				ReleaseNamespace: randomString(),
			},
		}

		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs = []configv1beta1.KustomizationRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
				Path:      randomString(),
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		failureMsg := randomString()
		controllers.SetFailureMessage(reconciler, clusterSummaryScope, failureMsg)

		featureHelmVerified := false
		featureResourcesVerified := false
		featureKustomizeVerified := false
		for i := range clusterSummary.Status.FeatureSummaries {
			if clusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
				Expect(clusterSummary.Status.FeatureSummaries[i].FailureMessage).ToNot(BeNil())
				Expect(*clusterSummary.Status.FeatureSummaries[i].FailureMessage).To(Equal(failureMsg))
				featureHelmVerified = true
			}
			if clusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureResources {
				Expect(clusterSummary.Status.FeatureSummaries[i].FailureMessage).ToNot(BeNil())
				Expect(*clusterSummary.Status.FeatureSummaries[i].FailureMessage).To(Equal(failureMsg))
				featureResourcesVerified = true
			}
			if clusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureKustomize {
				Expect(clusterSummary.Status.FeatureSummaries[i].FailureMessage).ToNot(BeNil())
				Expect(*clusterSummary.Status.FeatureSummaries[i].FailureMessage).To(Equal(failureMsg))
				featureKustomizeVerified = true
			}
		}

		Expect(featureHelmVerified).To(BeTrue())
		Expect(featureResourcesVerified).To(BeTrue())
		Expect(featureKustomizeVerified).To(BeTrue())
	})

	It("resetFeatureStatus set failure message for every features in ClusterSummary", func() {
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    randomString(),
				RepositoryName:   randomString(),
				ChartName:        randomString(),
				ChartVersion:     randomString(),
				ReleaseName:      randomString(),
				ReleaseNamespace: randomString(),
			},
		}

		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs = []configv1beta1.KustomizationRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
				Path:      randomString(),
			},
		}

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioned, Hash: []byte(randomString())},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioned, Hash: []byte(randomString())},
			{FeatureID: libsveltosv1beta1.FeatureKustomize, Status: libsveltosv1beta1.FeatureStatusProvisioned, Hash: []byte(randomString())},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		controllers.ResetFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureStatusFailed)

		featureHelmVerified := false
		featureResourcesVerified := false
		featureKustomizeVerified := false
		for i := range clusterSummary.Status.FeatureSummaries {
			if clusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
				Expect(clusterSummary.Status.FeatureSummaries[i].Status).To(Equal(libsveltosv1beta1.FeatureStatusFailed))
				Expect(clusterSummary.Status.FeatureSummaries[i].Hash).To(BeNil())
				featureHelmVerified = true
			}
			if clusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureResources {
				Expect(clusterSummary.Status.FeatureSummaries[i].Status).To(Equal(libsveltosv1beta1.FeatureStatusFailed))
				Expect(clusterSummary.Status.FeatureSummaries[i].Hash).To(BeNil())
				featureResourcesVerified = true
			}
			if clusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureKustomize {
				Expect(clusterSummary.Status.FeatureSummaries[i].Status).To(Equal(libsveltosv1beta1.FeatureStatusFailed))
				Expect(clusterSummary.Status.FeatureSummaries[i].Hash).To(BeNil())
				featureKustomizeVerified = true
			}
		}

		Expect(featureHelmVerified).To(BeTrue())
		Expect(featureResourcesVerified).To(BeTrue())
		Expect(featureKustomizeVerified).To(BeTrue())
	})

	It("shouldReconcile returns true when mode is OneTime but not all helm charts are deployed", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeOneTime
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1beta1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(), ReleaseName: randomString()},
		}
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioning},
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("shouldReconcile returns false when mode is OneTime and policies and helm charts are deployed", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeOneTime
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1beta1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(), ReleaseName: randomString()},
		}
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind)},
		}
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioned},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioned},
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     nil,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		Expect(controllers.ShouldReconcile(reconciler, clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     deployer,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		clusterSummaryName := client.ObjectKey{
			Name:      clusterSummary.Name,
			Namespace: clusterSummary.Namespace,
		}
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err = c.Get(context.TODO(), clusterSummaryName, currentClusterSummary)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentClusterSummary,
				configv1beta1.ClusterSummaryFinalizer,
			),
		).Should(BeTrue())
	})

	It("shouldRedeploy returns true in DryRun mode", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeDryRun
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioned},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioned},
		}
		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     deployer,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		// In SyncMode DryRun even if config is same (input for ShouldRedeploy) result is redeploy
		redeploy := controllers.ShouldRedeploy(reconciler, context.TODO(), clusterSummaryScope, f, true,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(redeploy).To(BeTrue())

		clusterSummaryName := client.ObjectKey{
			Name:      clusterSummary.Name,
			Namespace: clusterSummary.Namespace,
		}

		// Update SyncMode to Continuous
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err = c.Get(context.TODO(), clusterSummaryName, currentClusterSummary)
		Expect(err).ToNot(HaveOccurred())
		currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(c.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		clusterSummaryScope.ClusterSummary = currentClusterSummary
		// In SyncMode != DryRun and if config is same (input for ShouldRedeploy) result is do not redeploy
		redeploy = controllers.ShouldRedeploy(reconciler, context.TODO(), clusterSummaryScope, f, true,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(redeploy).To(BeFalse())
	})

	It("canRemoveFinalizer in DryRun returns true when ClusterSummary and ClusterProfile are deleted", func() {
		controllerutil.AddFinalizer(clusterSummary, configv1beta1.ClusterSummaryFinalizer)
		controllerutil.AddFinalizer(clusterProfile, configv1beta1.ClusterProfileFinalizer)

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeDryRun
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeDryRun
		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     deployer,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		// ClusterSummary not marked for deletion. So cannot remove finalizer
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())

		// Mark ClusterSummary for deletion
		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now
		clusterSummaryScope.ClusterSummary = clusterSummary

		// ClusterProfile is not marked for deletion. So cannot remove finalizer
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())

		// Mark ClusterProfile for deletion
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		Expect(c.Delete(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummaryScope.Profile = currentClusterProfile

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("canRemoveFinalizer in not DryRun returns true when ClusterSummary is deleted and features removed", func() {
		controllerutil.AddFinalizer(clusterSummary, configv1beta1.ClusterSummaryFinalizer)

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuous
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusRemoved},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusRemoving},
		}

		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(),
			textlogger.NewLogger(textlogger.NewConfig()), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     deployer,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())

		// Mark ClusterSummary for deletion
		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now
		clusterSummaryScope.ClusterSummary = clusterSummary

		clusterSummaryScope.ClusterSummary = clusterSummary

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())

		// Mark all features as removed
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusRemoved},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusRemoved},
		}

		clusterSummaryScope.ClusterSummary = clusterSummary

		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("canRemoveFinalizer returns true when Cluster is gone", func() {
		controllerutil.AddFinalizer(clusterSummary, configv1beta1.ClusterSummaryFinalizer)

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuous
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusRemoved},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusRemoving},
		}

		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     deployer,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		// Since ClusterSummary is not yet marked for deletion, finalizer cannot be removed
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())

		// Mark ClusterSummary for deletion
		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now
		clusterSummaryScope.ClusterSummary = clusterSummary

		// Because CAPI cluster does not exist and ClusterSummary is marked for deletion, finalizer can be removed
		Expect(controllers.CanRemoveFinalizer(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("getCurrentReferences collects all ClusterSummary referenced objects", func() {
		referencedResourceNamespace := randomString()

		kustomizeValueFrom := configv1beta1.ValueFrom{
			Namespace: referencedResourceNamespace,
			Name:      randomString(),
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		helmValueFrom := configv1beta1.ValueFrom{
			Namespace: referencedResourceNamespace,
			Name:      randomString(),
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		cmName := randomString()
		gitRepositoryName := randomString()
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: referencedResourceNamespace,
				Name:      cmName,
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
			{
				Namespace: referencedResourceNamespace,
				Name:      gitRepositoryName,
				Kind:      sourcev1.GitRepositoryKind,
			},
		}
		clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs = []configv1beta1.KustomizationRef{
			{
				Namespace:  referencedResourceNamespace,
				Name:       randomString(),
				Kind:       string(libsveltosv1beta1.SecretReferencedResourceKind),
				ValuesFrom: []configv1beta1.ValueFrom{kustomizeValueFrom},
			},
		}
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1beta1.HelmChart{
			{
				ValuesFrom: []configv1beta1.ValueFrom{helmValueFrom},
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummaryScope := getClusterSummaryScope(c,
			textlogger.NewLogger(textlogger.NewConfig()), clusterProfile, clusterSummary)
		reconciler := getClusterSummaryReconciler(nil, nil)
		set, err := controllers.GetCurrentReferences(reconciler, context.TODO(), clusterSummaryScope)
		Expect(err).To(BeNil())
		Expect(set.Len()).To(Equal(5))

		Expect(set.Items()).To(ContainElement(corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			Namespace:  referencedResourceNamespace,
			Name:       cmName,
		}))

		Expect(set.Items()).To(ContainElement(corev1.ObjectReference{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.GitRepositoryKind,
			Namespace:  referencedResourceNamespace,
			Name:       gitRepositoryName,
		}))
	})

	It("getCurrentReferences collects all ClusterSummary referenced objects using cluster namespace when not set", func() {
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{Namespace: "", Name: randomString(), Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind)},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummaryScope := getClusterSummaryScope(c,
			textlogger.NewLogger(textlogger.NewConfig()), clusterProfile, clusterSummary)
		reconciler := getClusterSummaryReconciler(nil, nil)
		set, err := controllers.GetCurrentReferences(reconciler, context.TODO(), clusterSummaryScope)
		Expect(err).To(BeNil())
		Expect(set.Len()).To(Equal(1))
		items := set.Items()
		Expect(items[0].Namespace).To(Equal(clusterSummary.Namespace))
	})

	It("reconcileDelete successfully returns when cluster is not found", func() {
		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = []configv1beta1.HelmChart{
			{RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(), ReleaseName: randomString()},
		}
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind)},
		}
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioned},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioned},
		}

		// No cluster.
		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)
		clusterSummaryReconciler := getClusterSummaryReconciler(c, dep)

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		var result reconcile.Result
		result, err = controllers.ReconcileDelete(clusterSummaryReconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result.RequeueAfter).To(BeZero())
	})

	It("areDependenciesDeployed returns true when all dependencies are deployed", func() {
		clusterProfileAName := randomString()
		clusterSummaryAName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfileAName, clusterName, false)
		By(fmt.Sprintf("Create a ClusterProfile %s (and ClusterSummary %s) used as dependency", clusterProfileAName, clusterSummaryAName))
		clusterSummaryA := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryAName,
				Namespace: namespace,
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfileAName,
					configv1beta1.ClusterNameLabel:     clusterName,
					configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						{
							RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
							ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
						},
					},
				},
			},
		}

		clusterProfileBName := randomString()
		clusterSummaryBName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfileBName, clusterName, false)
		By(fmt.Sprintf("Create a ClusterProfile %s (and ClusterSummary %s) used as dependency", clusterProfileBName, clusterSummaryBName))
		clusterSummaryB := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryBName,
				Namespace: namespace,
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfileBName,
					configv1beta1.ClusterNameLabel:     clusterName,
					configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						{
							RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
							ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
						},
					},
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
					},
				},
			},
		}

		By(fmt.Sprintf("setting %s and %s as dependencies", clusterProfileAName, clusterProfileBName))
		// Set dependencyA and dependencyB as dependendecies for clustersummary
		clusterSummary.Spec.ClusterProfileSpec.DependsOn = []string{clusterProfileAName, clusterProfileBName}

		initObjects := []client.Object{
			clusterSummaryA,
			clusterSummaryB,
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		addOwnerReference(context.TODO(), c, clusterSummary, clusterProfile)

		deployer := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     deployer,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		// because dependencies are not provisioned
		deployed, _, err := controllers.AreDependenciesDeployed(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(deployed).To(BeFalse())

		// Mark first deopendency as provisioned
		clusterSummaryA.Status = configv1beta1.ClusterSummaryStatus{
			FeatureSummaries: []configv1beta1.FeatureSummary{
				{
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				},
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummaryA)).To(Succeed())

		// because dependencies are not all provisioned
		deployed, _, err = controllers.AreDependenciesDeployed(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(deployed).To(BeFalse())

		// Mark first deopendency as provisioned
		clusterSummaryB.Status = configv1beta1.ClusterSummaryStatus{
			FeatureSummaries: []configv1beta1.FeatureSummary{
				{
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				},
				{
					FeatureID: libsveltosv1beta1.FeatureResources,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				},
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummaryB)).To(Succeed())
		// because dependencies are  all provisioned
		deployed, _, err = controllers.AreDependenciesDeployed(reconciler, context.TODO(), clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(deployed).To(BeTrue())
	})
})

var _ = Describe("ClusterSummaryReconciler: requeue methods", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var referencingClusterSummary *configv1beta1.ClusterSummary
	var nonReferencingClusterSummary *configv1beta1.ClusterSummary
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

		namespace = randomString()

		clusterProfile = &configv1beta1.ClusterProfile{
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

		referencingClusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name,
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
					},
					SyncMode: configv1beta1.SyncModeContinuous,
				},
			},
		}

		nonReferencingClusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name + randomString(),
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
					},
					SyncMode: configv1beta1.SyncModeContinuous,
				},
			},
		}
	})

	AfterEach(func() {
		err := testEnv.Delete(context.TODO(), referencingClusterSummary)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		err = testEnv.Delete(context.TODO(), nonReferencingClusterSummary)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		Expect(testEnv.Delete(context.TODO(), cluster)).To(Succeed())
	})

	It("requeueClusterSummaryForConfigMap returns correct ClusterSummary for a ConfigMap", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), clusterProfile)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())
		currentCluster := clusterv1.Cluster{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name},
			&currentCluster)).To(Succeed())
		initialized := true
		currentCluster.Status.ControlPlaneReady = initialized
		Expect(testEnv.Status().Update(ctx, &currentCluster)).To(Succeed())

		configMap := createConfigMapWithPolicy(namespace, randomString(), fmt.Sprintf(editClusterRole, randomString()))
		By(fmt.Sprintf("Creating %s %s/%s", configMap.Kind, configMap.Namespace, configMap.Name))
		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, configMap)).To(Succeed())

		By(fmt.Sprintf("Configuring ClusterSummary %s reference %s %s/%s",
			referencingClusterSummary.Name, configMap.Kind, configMap.Namespace, configMap.Name))
		referencingClusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: namespace,
				Name:      configMap.Name,
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}

		Expect(testEnv.Create(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, referencingClusterSummary)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, referencingClusterSummary, clusterProfile)

		Expect(testEnv.Create(context.TODO(), nonReferencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, nonReferencingClusterSummary)).To(Succeed())

		clusterSummaryName := client.ObjectKey{
			Name:      referencingClusterSummary.Name,
			Namespace: referencingClusterSummary.Namespace,
		}

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), testEnv.Client)
		Expect(dep.RegisterFeatureID(string(libsveltosv1beta1.FeatureResources))).To(Succeed())
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
			clusterSummaryList := controllers.RequeueClusterSummaryForReference(clusterSummaryReconciler,
				context.TODO(), configMap)
			result := reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: referencingClusterSummary.Namespace, Name: referencingClusterSummary.Name}}
			for i := range clusterSummaryList {
				if clusterSummaryList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Delete(context.TODO(), ns)).To(Succeed())
	})

	It("requeueClusterSummaryForCluster returns correct ClusterSummary for a CAPI Cluster", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), clusterProfile)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())
		currentCluster := clusterv1.Cluster{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name},
			&currentCluster)).To(Succeed())
		initialized := true
		currentCluster.Status.ControlPlaneReady = initialized
		Expect(testEnv.Status().Update(ctx, &currentCluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), referencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, referencingClusterSummary)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, referencingClusterSummary, clusterProfile)

		Expect(testEnv.Create(context.TODO(), nonReferencingClusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, nonReferencingClusterSummary)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, nonReferencingClusterSummary, clusterProfile)

		clusterSummaryName := client.ObjectKey{
			Name:      referencingClusterSummary.Name,
			Namespace: referencingClusterSummary.Namespace,
		}

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), testEnv.Client)
		Expect(dep.RegisterFeatureID(string(libsveltosv1beta1.FeatureResources))).To(Succeed())
		clusterSummaryReconciler := getClusterSummaryReconciler(testEnv.Client, dep)

		_, err := clusterSummaryReconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterSummaryName,
		})
		Expect(err).ToNot(HaveOccurred())

		clusterSummaryList := controllers.RequeueClusterSummaryForCluster(clusterSummaryReconciler,
			context.TODO(), cluster)
		result := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: referencingClusterSummary.Namespace,
				Name:      referencingClusterSummary.Name,
			},
		}
		found := false
		for i := range clusterSummaryList {
			if clusterSummaryList[i] == result {
				found = true
			}
		}
		Expect(found).To(BeTrue())

		Expect(testEnv.Delete(context.TODO(), ns)).To(Succeed())
	})
})
