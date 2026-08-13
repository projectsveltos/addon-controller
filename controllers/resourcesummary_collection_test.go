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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

var _ = Describe("ResourceSummary Collection", func() {
	It("collectResourceSummariesFromCluster collects and processes ResourceSummaries from clusters", func() {
		cluster := prepareCluster()

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      clusterProfileNamePrefix + randomString(),
				Labels:    map[string]string{clusterops.ClusterProfileLabelName: randomString()},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterType: libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		// Updates clusterSummary Status to indicate Helm is deployed
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				Hash:      []byte(randomString()),
			},
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				Hash:      []byte(randomString()),
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Create a ResourceSummary whose status indicates configuration drift for
		// helm resources

		// In managed cluster this is the namespace where ResourceSummaries
		// are created
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: sveltosNamespace,
			},
		}
		err := testEnv.Create(context.TODO(), ns)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		resourceSummary := getResourceSummary()
		resourceSummary.Annotations = map[string]string{
			libsveltosv1beta1.ClusterSummaryNameAnnotation:      clusterSummary.Name,
			libsveltosv1beta1.ClusterSummaryNamespaceAnnotation: clusterSummary.Namespace,
		}
		Expect(testEnv.Create(context.TODO(), resourceSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resourceSummary)).To(Succeed())

		currentResourceSummary := &libsveltosv1beta1.ResourceSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: sveltosNamespace, Name: resourceSummary.Name},
			currentResourceSummary)).To(Succeed())
		currentResourceSummary.Status.HelmResourcesChanged = true
		Expect(testEnv.Status().Update(context.TODO(), currentResourceSummary))

		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosNamespace, Name: resourceSummary.Name},
				currentResourceSummary)
			return err == nil && currentResourceSummary.Status.HelmResourcesChanged
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: sveltosNamespace, Name: resourceSummary.Name},
			currentResourceSummary)).To(Succeed())
		Expect(currentResourceSummary.Status.HelmResourcesChanged).To(BeTrue())

		// Given current state:
		// - ClusterSummary.Status.FeatureSummaries marked as provisioned for helm
		// - ResourceSummary.Status indicating a configuration drift for helm
		// CollectResourceSummariesFromCluster will:
		// - reset ClusterSummary.Status.FeatureSummaries hash for helm (indicating new reconciliation is needed)
		// - reset ResourceSummary.Status
		Expect(controllers.CollectResourceSummariesFromCluster(context.TODO(), testEnv.Client, getClusterRef(cluster),
			version, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
					return currentClusterSummary.Status.FeatureSummaries[i].Hash == nil
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosNamespace, Name: resourceSummary.Name},
				currentResourceSummary)
			return err == nil && !currentResourceSummary.Status.HelmResourcesChanged
		}, timeout, pollingInterval).Should(BeTrue())
	})
	It("processResourceSummary scopes NeedsRedeploy to only the drifted chart", func() {
		cluster := prepareCluster()

		chartA := configv1beta1.HelmChartSummary{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			Status:           configv1beta1.HelmChartStatusManaging,
		}
		chartB := configv1beta1.HelmChartSummary{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			Status:           configv1beta1.HelmChartStatusManaging,
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      clusterProfileNamePrefix + randomString(),
				Labels:    map[string]string{clusterops.ClusterProfileLabelName: randomString()},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterType: libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				Hash:      []byte(randomString()),
			},
		}
		currentClusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{chartA, chartB}
		Expect(testEnv.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		resourceSummary := getResourceSummary()
		resourceSummary.Annotations = map[string]string{
			libsveltosv1beta1.ClusterSummaryNameAnnotation:      clusterSummary.Name,
			libsveltosv1beta1.ClusterSummaryNamespaceAnnotation: clusterSummary.Namespace,
		}
		Expect(testEnv.Create(context.TODO(), resourceSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resourceSummary)).To(Succeed())

		currentResourceSummary := &libsveltosv1beta1.ResourceSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceSummary.Namespace, Name: resourceSummary.Name},
			currentResourceSummary)).To(Succeed())
		currentResourceSummary.Status.HelmResourcesChanged = true
		// Only chartA drifted. chartB must be left alone.
		currentResourceSummary.Status.DriftedHelmCharts = []libsveltosv1beta1.HelmChartRef{
			{
				ChartName:        randomString(),
				ReleaseName:      chartA.ReleaseName,
				ReleaseNamespace: chartA.ReleaseNamespace,
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentResourceSummary)).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: resourceSummary.Namespace, Name: resourceSummary.Name},
				currentResourceSummary)
			return err == nil && currentResourceSummary.Status.HelmResourcesChanged
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.ProcessResourceSummary(context.TODO(), testEnv.Client, currentResourceSummary,
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.HelmReleaseSummaries {
				rs := &currentClusterSummary.Status.HelmReleaseSummaries[i]
				if rs.ReleaseName == chartA.ReleaseName {
					if !rs.NeedsRedeploy {
						return false
					}
				} else if rs.NeedsRedeploy {
					return false
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("markDriftedHelmCharts marks every chart when no chart-scoped drift info is available", func() {
		clusterSummary := &configv1beta1.ClusterSummary{
			Status: configv1beta1.ClusterSummaryStatus{
				HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
					{ReleaseName: randomString(), ReleaseNamespace: randomString()},
					{ReleaseName: randomString(), ReleaseNamespace: randomString()},
				},
			},
		}

		controllers.MarkDriftedHelmCharts(clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))

		for i := range clusterSummary.Status.HelmReleaseSummaries {
			Expect(clusterSummary.Status.HelmReleaseSummaries[i].NeedsRedeploy).To(BeTrue())
		}
	})

	It("isResourceSummaryInstalledCached caches positive result", func() {
		controllers.ResetResourceSummaryInstalledCache()

		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: testResourceSummaryCRDName,
			},
		}
		withCRD := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
		withoutCRD := fake.NewClientBuilder().WithScheme(scheme).Build()

		cluster := &corev1.ObjectReference{Namespace: randomString(), Name: randomString()}

		installed, err := controllers.IsResourceSummaryInstalledCached(context.TODO(), withCRD, cluster)
		Expect(err).To(BeNil())
		Expect(installed).To(BeTrue())

		// CRD is gone from the client but the cache must still return true.
		installed, err = controllers.IsResourceSummaryInstalledCached(context.TODO(), withoutCRD, cluster)
		Expect(err).To(BeNil())
		Expect(installed).To(BeTrue())
	})

	It("isResourceSummaryInstalledCached does not cache negative result", func() {
		controllers.ResetResourceSummaryInstalledCache()

		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: testResourceSummaryCRDName,
			},
		}
		withCRD := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
		withoutCRD := fake.NewClientBuilder().WithScheme(scheme).Build()

		cluster := &corev1.ObjectReference{Namespace: randomString(), Name: randomString()}

		// First call: CRD absent — must return false and not cache the result.
		installed, err := controllers.IsResourceSummaryInstalledCached(context.TODO(), withoutCRD, cluster)
		Expect(err).To(BeNil())
		Expect(installed).To(BeFalse())

		// Second call with CRD present: must return true (false was not cached).
		installed, err = controllers.IsResourceSummaryInstalledCached(context.TODO(), withCRD, cluster)
		Expect(err).To(BeNil())
		Expect(installed).To(BeTrue())
	})

	It("collectAndProcessAllResourceSummaries distinguishes CAPI and Sveltos clusters with same namespace and name", func() {
		logger := textlogger.NewLogger(textlogger.NewConfig())

		// prepareCluster creates a ready CAPI cluster in testEnv so that
		// skipCollecting passes when we process ResourceSummaries for it.
		capiCluster := prepareCluster()

		// ClusterSummary for the CAPI cluster.
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: capiCluster.Namespace,
				Name:      clusterProfileNamePrefix + randomString(),
				Labels:    map[string]string{clusterops.ClusterProfileLabelName: randomString()},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterType: libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				Hash:      []byte(randomString()),
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// RS labeled as CAPI cluster — drift detected.
		capiRS := getResourceSummary()
		capiRS.Namespace = capiCluster.Namespace
		capiRS.Annotations = map[string]string{
			libsveltosv1beta1.ClusterSummaryNameAnnotation:      clusterSummary.Name,
			libsveltosv1beta1.ClusterSummaryNamespaceAnnotation: capiCluster.Namespace,
		}
		capiRS.Labels = map[string]string{
			sveltos_upgrade.ClusterNameLabel: capiCluster.Name,
			sveltos_upgrade.ClusterTypeLabel: strings.ToLower(string(libsveltosv1beta1.ClusterTypeCapi)),
		}
		Expect(testEnv.Create(context.TODO(), capiRS)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, capiRS)).To(Succeed())
		currentCapiRS := &libsveltosv1beta1.ResourceSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: capiRS.Namespace, Name: capiRS.Name}, currentCapiRS)).To(Succeed())
		currentCapiRS.Status.HelmResourcesChanged = true
		Expect(testEnv.Status().Update(context.TODO(), currentCapiRS)).To(Succeed())

		// RS labeled as Sveltos cluster — same namespace/name as the CAPI cluster, also with drift.
		// This must NOT be processed since its cluster type does not match the CAPI entry in clustersWithDD.
		sveltosRS := getResourceSummary()
		sveltosRS.Namespace = capiCluster.Namespace
		sveltosRS.Annotations = map[string]string{
			libsveltosv1beta1.ClusterSummaryNameAnnotation:      clusterSummary.Name,
			libsveltosv1beta1.ClusterSummaryNamespaceAnnotation: capiCluster.Namespace,
		}
		sveltosRS.Labels = map[string]string{
			sveltos_upgrade.ClusterNameLabel: capiCluster.Name,
			sveltos_upgrade.ClusterTypeLabel: strings.ToLower(string(libsveltosv1beta1.ClusterTypeSveltos)),
		}
		Expect(testEnv.Create(context.TODO(), sveltosRS)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, sveltosRS)).To(Succeed())
		currentSveltosRS := &libsveltosv1beta1.ResourceSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: sveltosRS.Namespace, Name: sveltosRS.Name}, currentSveltosRS)).To(Succeed())
		currentSveltosRS.Status.HelmResourcesChanged = true
		Expect(testEnv.Status().Update(context.TODO(), currentSveltosRS)).To(Succeed())

		// Only the CAPI cluster is in clustersWithDD.
		capiClusterRef := corev1.ObjectReference{
			Namespace:  capiCluster.Namespace,
			Name:       capiCluster.Name,
			Kind:       clusterKind,
			APIVersion: clusterv1.GroupVersion.String(),
		}
		clustersWithDD := map[corev1.ObjectReference]bool{capiClusterRef: true}

		Expect(controllers.CollectAndProcessAllResourceSummaries(context.TODO(), testEnv.Client,
			[]corev1.ObjectReference{capiClusterRef}, clustersWithDD, logger)).To(Succeed())

		// The CAPI ClusterSummary hash must be cleared — drift was processed.
		Eventually(func() bool {
			if err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary); err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
					return currentClusterSummary.Status.FeatureSummaries[i].Hash == nil
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		// The Sveltos RS must not have been reset — its cluster type was not in clustersWithDD.
		Consistently(func() bool {
			currentSveltosRS := &libsveltosv1beta1.ResourceSummary{}
			if err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: sveltosRS.Namespace, Name: sveltosRS.Name},
				currentSveltosRS); err != nil {
				return false
			}
			return currentSveltosRS.Status.HelmResourcesChanged
		}, "2s", pollingInterval).Should(BeTrue())
	})
})

// getResourceSummary returns a bare ResourceSummary. None of the tests in this file need it
// pre-populated with Spec.Resources/ChartResources, they set Status directly instead.
func getResourceSummary() *libsveltosv1beta1.ResourceSummary {
	return &libsveltosv1beta1.ResourceSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      randomString(),
			Namespace: sveltosNamespace,
		},
	}
}
