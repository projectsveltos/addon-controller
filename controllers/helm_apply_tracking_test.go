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
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"k8s.io/utils/ptr"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	applyTrackingMetadata = "metadata"
	applyTrackingDataKey  = "key"
	applyTrackingOther    = "other"
)

func newConfigMap(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion":          "v1",
		"kind":                testKindConfigMap,
		applyTrackingMetadata: map[string]interface{}{"name": name, "namespace": copyNamespace},
		"data":                map[string]interface{}{applyTrackingDataKey: name},
	}}
}

var _ = Describe("Helm apply tracking in pull mode", func() {
	var chart *configv1beta1.HelmChart
	var clusterSummary *configv1beta1.ClusterSummary

	BeforeEach(func() {
		chart = &configv1beta1.HelmChart{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}
		clusterSummary = &configv1beta1.ClusterSummary{
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode: configv1beta1.SyncModeContinuousWithDriftDetection,
					Tier:     100,
				},
			},
		}
	})

	Context("getChartApplyHash", func() {
		applyHash := func(resources []*unstructured.Unstructured) []byte {
			hash, err := controllers.GetChartApplyHash(clusterSummary, chart, "1.0.0", resources)
			Expect(err).To(BeNil())
			return hash
		}

		It("does not depend on the order of the resources", func() {
			a, b := newConfigMap("a"), newConfigMap("b")
			Expect(applyHash([]*unstructured.Unstructured{a, b})).To(Equal(applyHash([]*unstructured.Unstructured{b, a})))
		})

		It("changes when a resource changes", func() {
			before := applyHash([]*unstructured.Unstructured{newConfigMap("a")})
			changed := newConfigMap("a")
			changed.Object["data"] = map[string]interface{}{applyTrackingDataKey: applyTrackingOther}
			Expect(applyHash([]*unstructured.Unstructured{changed})).ToNot(Equal(before))
		})

		It("changes with every setting that changes how the chart is applied", func() {
			resources := []*unstructured.Unstructured{newConfigMap("a")}
			baseline := applyHash(resources)

			By("tier: it is stamped on the deployed resources")
			clusterSummary.Spec.ClusterProfileSpec.Tier = 50
			Expect(applyHash(resources)).ToNot(Equal(baseline))
			clusterSummary.Spec.ClusterProfileSpec.Tier = 100
			Expect(applyHash(resources)).To(Equal(baseline))

			By("timeout")
			chart.Options = &configv1beta1.HelmOptions{Timeout: &metav1.Duration{Duration: time.Hour}}
			Expect(applyHash(resources)).ToNot(Equal(baseline))

			By("namespace creation")
			chart.Options = &configv1beta1.HelmOptions{
				InstallOptions: configv1beta1.HelmInstallOptions{CreateNamespace: ptr.To(false)},
			}
			Expect(applyHash(resources)).ToNot(Equal(baseline))
			chart.Options = nil

			By("chart version")
			other, err := controllers.GetChartApplyHash(clusterSummary, chart, "2.0.0", resources)
			Expect(err).To(BeNil())
			Expect(other).ToNot(Equal(baseline))
		})
	})

	Context("isApplyTracked", func() {
		It("tracks a deploy", func() {
			Expect(controllers.IsApplyTracked(clusterSummary, chart, false)).To(BeTrue())
		})

		It("does not track an uninstall", func() {
			Expect(controllers.IsApplyTracked(clusterSummary, chart, true)).To(BeFalse())

			chart.HelmChartAction = configv1beta1.HelmChartActionUninstall
			Expect(controllers.IsApplyTracked(clusterSummary, chart, false)).To(BeFalse())
		})

		It("does not track DryRun, where nothing is applied", func() {
			clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeDryRun
			Expect(controllers.IsApplyTracked(clusterSummary, chart, false)).To(BeFalse())
		})

		It("does not track a ClusterSummary being deleted", func() {
			now := metav1.Now()
			clusterSummary.DeletionTimestamp = &now
			Expect(controllers.IsApplyTracked(clusterSummary, chart, false)).To(BeFalse())
		})
	})

	Context("shouldSkipApply", func() {
		hash := []byte(randomString())

		withSummary := func(summary configv1beta1.HelmChartSummary) {
			summary.ReleaseName = chart.ReleaseName
			summary.ReleaseNamespace = chart.ReleaseNamespace
			clusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{summary}
		}

		It("skips a chart confirmed as applied with the same content, not drifted and nothing pending", func() {
			withSummary(configv1beta1.HelmChartSummary{AppliedContentHash: hash})
			Expect(controllers.ShouldSkipApply(clusterSummary, chart, hash)).To(BeTrue())
		})

		It("applies a chart with no recorded state, the first time and after a controller upgrade", func() {
			Expect(controllers.ShouldSkipApply(clusterSummary, chart, hash)).To(BeFalse())

			withSummary(configv1beta1.HelmChartSummary{})
			Expect(controllers.ShouldSkipApply(clusterSummary, chart, hash)).To(BeFalse())
		})

		It("applies a chart whose content, or an apply setting, changed", func() {
			withSummary(configv1beta1.HelmChartSummary{AppliedContentHash: []byte(randomString())})
			Expect(controllers.ShouldSkipApply(clusterSummary, chart, hash)).To(BeFalse())
		})

		It("applies a chart drift-detection reported", func() {
			withSummary(configv1beta1.HelmChartSummary{AppliedContentHash: hash, NeedsRedeploy: true})
			Expect(controllers.ShouldSkipApply(clusterSummary, chart, hash)).To(BeFalse())
		})

		It("applies a chart with a staged apply not yet confirmed, so a failed apply is retried", func() {
			withSummary(configv1beta1.HelmChartSummary{AppliedContentHash: hash, StagedContentHash: hash})
			Expect(controllers.ShouldSkipApply(clusterSummary, chart, hash)).To(BeFalse())
		})

		It("looks only at the chart's own entry", func() {
			clusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{
				{ReleaseName: randomString(), ReleaseNamespace: randomString(), AppliedContentHash: hash},
			}
			Expect(controllers.ShouldSkipApply(clusterSummary, chart, hash)).To(BeFalse())
		})
	})

	Context("recordHelmChartApply", func() {
		It("clears the drift flag after a push mode deploy", func() {
			summary := &configv1beta1.HelmChartSummary{NeedsRedeploy: true}

			controllers.RecordHelmChartApply(summary, nil)

			Expect(summary.NeedsRedeploy).To(BeFalse())
			Expect(summary.StagedContentHash).To(BeEmpty())
		})

		It("records what is staged and clears the drift flag when the chart is applied in pull mode", func() {
			hash := []byte(randomString())
			summary := &configv1beta1.HelmChartSummary{NeedsRedeploy: true}

			controllers.RecordHelmChartApply(summary, controllers.NewReleaseInfoForApply(hash, false))

			Expect(summary.NeedsRedeploy).To(BeFalse())
			Expect(summary.StagedContentHash).To(Equal(hash))
		})

		It("keeps the drift flag and records nothing when the chart is skipped in pull mode", func() {
			summary := &configv1beta1.HelmChartSummary{NeedsRedeploy: true}

			controllers.RecordHelmChartApply(summary,
				controllers.NewReleaseInfoForApply([]byte(randomString()), true))

			Expect(summary.NeedsRedeploy).To(BeTrue())
			Expect(summary.StagedContentHash).To(BeEmpty())
		})

		It("records nothing to confirm when the chart is not tracked, such as in DryRun", func() {
			summary := &configv1beta1.HelmChartSummary{NeedsRedeploy: true}

			controllers.RecordHelmChartApply(summary, controllers.NewReleaseInfoForApply(nil, false))

			Expect(summary.NeedsRedeploy).To(BeFalse())
			Expect(summary.StagedContentHash).To(BeEmpty())
		})
	})

	Context("confirmAppliedHelmCharts", func() {
		It("turns what was staged into what is applied, for the charts that were staged only", func() {
			cluster := prepareCluster()

			staged := []byte(randomString())
			previouslyApplied := []byte(randomString())
			skippedApplied := []byte(randomString())

			clusterSummary = &configv1beta1.ClusterSummary{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: cluster.Namespace,
					Name:      clusterProfileNamePrefix + randomString(),
				},
				Spec: configv1beta1.ClusterSummarySpec{
					ClusterNamespace: cluster.Namespace,
					ClusterName:      cluster.Name,
					ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				},
			}
			Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

			skipped := configv1beta1.HelmChartSummary{
				ReleaseName: randomString(), ReleaseNamespace: randomString(),
				Status: configv1beta1.HelmChartStatusManaging, AppliedContentHash: skippedApplied,
			}
			applied := configv1beta1.HelmChartSummary{
				ReleaseName: randomString(), ReleaseNamespace: randomString(),
				Status: configv1beta1.HelmChartStatusManaging, AppliedContentHash: previouslyApplied,
				StagedContentHash: staged,
			}
			clusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{skipped, applied}
			Expect(testEnv.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

			// confirmAppliedHelmCharts reads through the cache: wait for it to see the staged state
			Eventually(func() bool {
				current := &configv1beta1.ClusterSummary{}
				err := testEnv.Get(context.TODO(),
					types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, current)
				return err == nil && len(current.Status.HelmReleaseSummaries) == 2
			}, timeout, pollingInterval).Should(BeTrue())

			Expect(controllers.ConfirmAppliedHelmCharts(context.TODO(), testEnv.Client, clusterSummary,
				textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

			current := readClusterSummaryUncached(clusterSummary)

			confirmed := findHelmChartSummary(current, applied.ReleaseNamespace, applied.ReleaseName)
			Expect(confirmed).ToNot(BeNil())
			Expect(confirmed.AppliedContentHash).To(Equal(staged))
			Expect(confirmed.StagedContentHash).To(BeEmpty())

			untouched := findHelmChartSummary(current, skipped.ReleaseNamespace, skipped.ReleaseName)
			Expect(untouched).ToNot(BeNil())
			Expect(untouched.AppliedContentHash).To(Equal(skippedApplied))
			Expect(untouched.StagedContentHash).To(BeEmpty())
		})

		It("does nothing when the ClusterSummary is gone", func() {
			clusterSummary.Namespace = randomString()
			clusterSummary.Name = randomString()

			Expect(controllers.ConfirmAppliedHelmCharts(context.TODO(), testEnv.Client, clusterSummary,
				textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())
		})
	})
})
