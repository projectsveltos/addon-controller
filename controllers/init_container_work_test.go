/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"bytes"
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Init container work", func() {
	It("updateClusterSummaryHelmHashes refreshes ValuesHash and PatchesHash without triggering a redeploy", func() {
		clusterNamespace := randomString()

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		requestChart := configv1beta1.HelmChart{
			RepositoryURL:    testRepoURLNginxStable,
			RepositoryName:   testRepoNameNginxStable,
			ChartName:        testChartNameNginxIngress,
			ReleaseName:      testReleaseNameNginxLatest,
			ReleaseNamespace: testNginxRepo,
			ChartVersion:     testChartVersion100,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode:   configv1beta1.SyncModeContinuousWithDriftDetection,
					HelmCharts: []configv1beta1.HelmChart{requestChart},
					Patches: []libsveltosv1beta1.Patch{
						{
							Patch: testManagedAnnotationPatch,
							Target: &libsveltosv1beta1.PatchSelector{
								Kind: testKindDeployment,
							},
						},
					},
				},
			},
		}
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())

		// Simulate the state right before an addon-controller upgrade: the feature is
		// already Provisioned, and the stored hashes are stale/wrong (as if computed by an
		// older, different hash implementation).
		currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				Hash:      []byte(randomString()),
			},
		}
		currentClusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{
			{
				ReleaseName:      requestChart.ReleaseName,
				ReleaseNamespace: requestChart.ReleaseNamespace,
				Status:           configv1beta1.HelmChartStatusManaging,
				ValuesHash:       []byte("stale-values-hash"),
				PatchesHash:      []byte("stale-patches-hash"),
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())
		Eventually(func() error {
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
		}, timeout, pollingInterval).Should(Succeed())

		logger := textlogger.NewLogger(textlogger.NewConfig())

		expectedPatchesHash, err := controllers.GetHelmChartPatchesHash(context.TODO(), currentClusterSummary, logger)
		Expect(err).To(BeNil())
		Expect(expectedPatchesHash).ToNot(Equal([]byte("stale-patches-hash")))

		Expect(controllers.UpdateClusterSummaryHelmHashes(context.TODO(), testEnv.Client, "",
			currentClusterSummary, logger)).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			if len(currentClusterSummary.Status.HelmReleaseSummaries) != 1 {
				return false
			}
			rs := currentClusterSummary.Status.HelmReleaseSummaries[0]
			return bytes.Equal(rs.PatchesHash, expectedPatchesHash) &&
				string(rs.ValuesHash) != "stale-values-hash"
		}, timeout, pollingInterval).Should(BeTrue())

		// Refreshing the stored hashes must not, by itself, mark the feature for redeploy:
		// no reset of Status/Hash, that decision belongs to the main reconcile loop, not to
		// this startup pre-warm step.
		for i := range currentClusterSummary.Status.FeatureSummaries {
			if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
				Expect(currentClusterSummary.Status.FeatureSummaries[i].Status).To(
					Equal(libsveltosv1beta1.FeatureStatusProvisioned))
			}
		}
	})
})
