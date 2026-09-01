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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/cli"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
)

var _ = Describe("HandlersHelm", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary

	BeforeEach(func() {
		clusterNamespace := randomString()

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

		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       configv1beta1.ClusterProfileKind,
						Name:       clusterProfile.Name,
						APIVersion: testConfigAPIVersion,
						UID:        types.UID(randomString()),
					},
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
	})

	It("shouldInstall returns false when requested version does not match installed version", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion250,
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeFalse())
	})

	It("shouldInstall returns false when requested version matches installed version",
		func() {
			currentRelease := &controllers.ReleaseInfo{
				Status:       releasecommon.StatusDeployed.String(),
				ChartVersion: testChartVersion253,
			}
			requestChart := &configv1beta1.HelmChart{
				ChartVersion:    testChartVersion253,
				HelmChartAction: configv1beta1.HelmChartActionInstall,
			}
			Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeFalse())
		})

	It("shouldInstall returns true when there is no current installed version", func() {
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(nil, requestChart)).To(BeTrue())
	})

	It("shouldInstall returns false action is uninstall", func() {
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionUninstall,
		}
		Expect(controllers.ShouldInstall(nil, requestChart)).To(BeFalse())
	})

	It("shouldInstall returns false for a failed release even when its recorded version matches the request", func() {
		// A failed upgrade/install attempt still leaves a release record behind, stamped
		// with the version that attempt tried (and failed) to reach. That must not be
		// mistaken for "nothing to do here but install" - it has to be retried as an
		// upgrade instead, never routed through handleInstall's uninstall-on-repeated-
		// failure recovery.
		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusFailed.String(),
			ChartVersion: testChartVersion253,
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeFalse())
	})

	It("shouldInstall returns true when the current release was cleanly uninstalled", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusUninstalled.String(),
			ChartVersion: testChartVersion253,
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeTrue())
	})

	It("shouldUninstall returns false when there is no current release installed", func() {
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionUninstall,
		}
		Expect(controllers.ShouldUninstall(nil, requestChart)).To(BeFalse())
	})

	It("shouldUninstall returns false when action is not Uninstall", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion253,
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldUninstall(currentRelease, requestChart)).To(BeFalse())
	})

	It("shouldUpgrade returns true when installed release is different than requested release", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion250,
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion253,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("desiredValuesAreSubset returns true when desired is a subset of full values", func() {
		full := map[string]interface{}{
			testReplicaCountKey: 2,
			testImageKey: map[string]interface{}{
				testRepositoryKey: testNginxRepo,
				testTagKey:        "1.21",
				"pullPolicy":      "IfNotPresent",
			},
			testServiceKey: map[string]interface{}{
				testPortKey: 80,
			},
		}
		// Desired only sets a subset — tag and pullPolicy come from chart defaults.
		desired := map[string]interface{}{
			testReplicaCountKey: 2,
			testImageKey: map[string]interface{}{
				testRepositoryKey: testNginxRepo,
			},
		}
		Expect(controllers.DesiredValuesAreSubset(desired, full)).To(BeTrue())
	})

	It("desiredValuesAreSubset returns false when a desired value differs from full", func() {
		full := map[string]interface{}{
			testReplicaCountKey: 2,
		}
		desired := map[string]interface{}{
			testReplicaCountKey: 3,
		}
		Expect(controllers.DesiredValuesAreSubset(desired, full)).To(BeFalse())
	})

	It("desiredValuesAreSubset returns false when a desired key is absent from full", func() {
		full := map[string]interface{}{
			testReplicaCountKey: 2,
		}
		desired := map[string]interface{}{
			testReplicaCountKey: 2,
			"extraKey":          testValueKey,
		}
		Expect(controllers.DesiredValuesAreSubset(desired, full)).To(BeFalse())
	})

	It("desiredValuesAreSubset returns false when desired value is a map but full value is a scalar", func() {
		full := map[string]interface{}{
			testImageKey: testNginxLatestImage, // scalar, not a map
		}
		desired := map[string]interface{}{
			testImageKey: map[string]interface{}{
				testRepositoryKey: testNginxRepo,
			},
		}
		Expect(controllers.DesiredValuesAreSubset(desired, full)).To(BeFalse())
	})

	It("desiredValuesAreSubset returns false when desired value is a scalar but full value is a map", func() {
		full := map[string]interface{}{
			testImageKey: map[string]interface{}{
				testRepositoryKey: testNginxRepo,
			},
		}
		desired := map[string]interface{}{
			testImageKey: testNginxLatestImage, // scalar, not a map
		}
		Expect(controllers.DesiredValuesAreSubset(desired, full)).To(BeFalse())
	})

	It("shouldUpgrade returns false when no stored state and desired values are subset of live release", func() {
		// When ClusterSummary has no stored hash (first reconciliation), shouldUpgrade must
		// not trigger a spurious upgrade if the desired values are already reflected in the
		// deployed release's full values (chart defaults + Config).
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		// Live release: deployed, same version, FullValues includes chart defaults.
		// Sveltos desires no values (empty), which is a subset of anything.
		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			Values:       map[string]interface{}{},
			FullValues:   map[string]interface{}{testReplicaCountKey: 1, testServiceKey: map[string]interface{}{testPortKey: 80}},
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion100,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())
	})

	It("shouldUpgrade returns true when no stored state and desired values are not a subset of live release", func() {
		// Same version, but the desired values include a key that differs from what is
		// deployed — upgrade must be triggered even with no stored state.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			FullValues:   map[string]interface{}{testReplicaCountKey: 1},
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion100,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
			Values:          "replicaCount: 3\n", // desired differs from live
		}

		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("shouldUpgrade returns true when no stored state but live release version differs", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			FullValues:   map[string]interface{}{},
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    "v1.1.0",
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("shouldUpgrade returns false on first reconciliation with ContinuousWithDriftDetection when version and values already match", func() {
		// When a new mgmt cluster takes over and the civo cluster already has the correct
		// release deployed (same version, values subset, no patches), Sveltos should skip
		// the helm upgrade even in ContinuousWithDriftDetection mode.
		// ResourceSummary is still deployed by postProcessDeployedHelmCharts so the
		// drift-detection agent is correctly informed.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection

		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			FullValues:   map[string]interface{}{testReplicaCountKey: 1},
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion100,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())
	})

	// shouldUpgradeWithDriftDetection sets up a chart whose stored ValuesHash already matches
	// what's actually deployed (nothing about the desired spec changed), with NeedsRedeploy set
	// as requested, and returns what shouldUpgrade decides for it under ContinuousWithDriftDetection.
	shouldUpgradeWithDriftDetection := func(needsRedeploy bool) bool {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection

		requestChart := &configv1beta1.HelmChart{
			ReleaseName:      testReleaseNameNginxLatest,
			ReleaseNamespace: testNginxRepo,
			ChartVersion:     testChartVersion100,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		storedValuesHash, err := controllers.GetHelmChartValuesHash(context.TODO(), testEnv, requestChart,
			clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		clusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{
			{
				ReleaseName:      testReleaseNameNginxLatest,
				ReleaseNamespace: testNginxRepo,
				ValuesHash:       storedValuesHash,
				Status:           configv1beta1.HelmChartStatusManaging,
				NeedsRedeploy:    needsRedeploy,
			},
		}

		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			FullValues:   map[string]interface{}{testReplicaCountKey: 1},
		}

		return controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))
	}

	It("shouldUpgrade returns false with ContinuousWithDriftDetection when stored state matches and NeedsRedeploy is false", func() {
		// Nothing changed and drift-detection hasn't flagged this chart, so there's nothing to
		// do: shouldUpgrade must not blindly re-upgrade every chart on every reconcile
		// regardless of state.
		Expect(shouldUpgradeWithDriftDetection(false)).To(BeFalse())
	})

	It("shouldUpgrade returns true with ContinuousWithDriftDetection when NeedsRedeploy is set, even if stored state matches", func() {
		// Same as above, nothing about the desired spec changed, but drift-detection flagged
		// this chart's deployed resources as changed out of band. That alone must trigger the
		// upgrade, scoped to this chart.
		Expect(shouldUpgradeWithDriftDetection(true)).To(BeTrue())
	})

	It("shouldUpgrade returns true when patches are added to a chart that previously had none", func() {
		// Regression test: buildReferencedHelmReleaseSummaries used to overwrite PatchesHash
		// with the freshly computed value on every reconcile, before shouldUpgrade ever compared
		// it, so a real patches change was undetectable, old and current were always equal by
		// construction, since both came from the same current state. PatchesHash now behaves
		// like ValuesHash: carried forward until a successful deploy, so a genuine change here
		// (patches added where there were none) must be visible to the comparison.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuous

		requestChart := &configv1beta1.HelmChart{
			ReleaseName:      testReleaseNameNginxLatest,
			ReleaseNamespace: testNginxRepo,
			ChartVersion:     testChartVersion100,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		// Deployed previously with no patches configured.
		noPatchesHash, err := controllers.GetHelmChartPatchesHash(context.TODO(), clusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		storedValuesHash, err := controllers.GetHelmChartValuesHash(context.TODO(), testEnv, requestChart,
			clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		clusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{
			{
				ReleaseName:      testReleaseNameNginxLatest,
				ReleaseNamespace: testNginxRepo,
				ValuesHash:       storedValuesHash,
				PatchesHash:      noPatchesHash,
				Status:           configv1beta1.HelmChartStatusManaging,
			},
		}

		// Patches just got added to the ClusterProfile.
		clusterSummary.Spec.ClusterProfileSpec.Patches = []libsveltosv1beta1.Patch{
			{
				Patch: testManagedAnnotationPatch,
				Target: &libsveltosv1beta1.PatchSelector{
					Kind: testKindDeployment,
				},
			},
		}

		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			FullValues:   map[string]interface{}{testReplicaCountKey: 1},
		}

		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("shouldUpgrade returns true when no stored state with ContinuousWithDriftDetection but patches are configured", func() {
		// This is the nginx case: even though version and values are unchanged, patches are
		// defined in the ClusterProfile. Sveltos cannot infer from the live release whether
		// those patches were applied previously, so it must re-deploy to be safe.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		clusterSummary.Spec.ClusterProfileSpec.Patches = []libsveltosv1beta1.Patch{
			{
				Patch: testManagedAnnotationPatch,
				Target: &libsveltosv1beta1.PatchSelector{
					Kind: testKindDeployment,
				},
			},
		}

		// Same version, desired values (empty) are a subset of live FullValues — the
		// first-reconciliation skip would fire if there were no patches.
		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			FullValues:   map[string]interface{}{testReplicaCountKey: 1},
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    testChartVersion100,
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("getHelmChartPatchesHash returns the same encoding stored by buildReferencedHelmReleaseSummaries", func() {
		// buildReferencedHelmReleaseSummaries stores getPatchesHash's return value directly as
		// []byte(patchesHash), a hex-encoded digest kept as text. getHelmChartPatchesHash must
		// return that same encoding, not hash it again, otherwise the two are never equal and
		// shouldUpgradeForContinuousMode reports "patches changed" on every single reconcile
		// regardless of whether patches actually changed.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.Patches = []libsveltosv1beta1.Patch{
			{
				Patch: testManagedAnnotationPatch,
				Target: &libsveltosv1beta1.PatchSelector{
					Kind: testKindDeployment,
				},
			},
		}

		storedFormat, err := controllers.GetPatchesHash(context.TODO(), clusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(storedFormat).ToNot(BeEmpty())

		comparisonFormat, err := controllers.GetHelmChartPatchesHash(context.TODO(), clusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		Expect(comparisonFormat).To(Equal([]byte(storedFormat)))
	})

	It("shouldUpgrade returns false when patches are configured but unchanged", func() {
		// Regression test: previously getHelmChartPatchesHash hashed the already-hex-encoded
		// stored value a second time, so this always evaluated to true, forcing a redeploy of
		// every chart with patches configured on every reconcile, in any sync mode, regardless
		// of whether anything actually changed.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: clusterSummary.Spec.ClusterNamespace}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuous
		clusterSummary.Spec.ClusterProfileSpec.Patches = []libsveltosv1beta1.Patch{
			{
				Patch: testManagedAnnotationPatch,
				Target: &libsveltosv1beta1.PatchSelector{
					Kind: testKindDeployment,
				},
			},
		}

		requestChart := &configv1beta1.HelmChart{
			ReleaseName:      testReleaseNameNginxLatest,
			ReleaseNamespace: testNginxRepo,
			ChartVersion:     testChartVersion100,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		storedValuesHash, err := controllers.GetHelmChartValuesHash(context.TODO(), testEnv, requestChart,
			clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		storedPatchesHash, err := controllers.GetHelmChartPatchesHash(context.TODO(), clusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		clusterSummary.Status.HelmReleaseSummaries = []configv1beta1.HelmChartSummary{
			{
				ReleaseName:      testReleaseNameNginxLatest,
				ReleaseNamespace: testNginxRepo,
				ValuesHash:       storedValuesHash,
				PatchesHash:      storedPatchesHash,
				Status:           configv1beta1.HelmChartStatusManaging,
			},
		}

		currentRelease := &controllers.ReleaseInfo{
			Status:       releasecommon.StatusDeployed.String(),
			ChartVersion: testChartVersion100,
			FullValues:   map[string]interface{}{testReplicaCountKey: 1},
		}

		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			controllers.NewDeploymentContext(clusterSummary, nil, nil),
			textlogger.NewLogger(textlogger.NewConfig()))).To(BeFalse())
	})

	It("UpdateStatusForeferencedHelmReleases updates ClusterSummary.Status.HelmReleaseSummaries", func() {
		calicoChart := &configv1beta1.HelmChart{
			RepositoryURL:    "https://projectcalico.docs.tigera.io/charts",
			RepositoryName:   "projectcalico",
			ChartName:        "projectcalico/tigera-operator",
			ChartVersion:     "v3.24.1",
			ReleaseName:      testReleaseNameCalico,
			ReleaseNamespace: testReleaseNameCalico,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		kyvernoSummary := configv1beta1.HelmChartSummary{
			ReleaseName:      testReleaseNameKyverno,
			ReleaseNamespace: testReleaseNameKyverno,
			Status:           configv1beta1.HelmChartStatusManaging,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{*calicoChart},
		}

		clusterSummary.Namespace = defaultNamespace
		clusterSummary.Spec.ClusterNamespace = defaultNamespace

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		// List a helm chart non referenced anymore as managed
		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				kyvernoSummary,
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		clusterSummary, conflict, err := controllers.UpdateStatusForReferencedHelmReleases(context.TODO(),
			testEnv.Client, clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())

		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			if currentClusterSummary.Status.HelmReleaseSummaries == nil {
				return false
			}
			if len(currentClusterSummary.Status.HelmReleaseSummaries) != 2 {
				return false
			}
			if currentClusterSummary.Status.HelmReleaseSummaries[0].Status != configv1beta1.HelmChartStatusManaging ||
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName != calicoChart.ReleaseName ||
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace != calicoChart.ReleaseNamespace {
				return false
			}

			// UpdateStatusForeferencedHelmReleases adds status for referenced releases and does not remove any
			// existing entry for non existing releases.
			return currentClusterSummary.Status.HelmReleaseSummaries[1].Status == kyvernoSummary.Status &&
				currentClusterSummary.Status.HelmReleaseSummaries[1].ReleaseName == kyvernoSummary.ReleaseName &&
				currentClusterSummary.Status.HelmReleaseSummaries[1].ReleaseNamespace == kyvernoSummary.ReleaseNamespace
		}, timeout, pollingInterval).Should(BeTrue())

	})

	It("UpdateStatusForReferencedHelmReleases preserves NeedsRedeploy across a rebuild", func() {
		// Regression test: UpdateStatusForReferencedHelmReleases (via buildReferencedHelmReleaseSummaries)
		// rebuilds Status.HelmReleaseSummaries from scratch on every reconcile, before shouldUpgrade
		// ever runs. It already carries ValuesHash forward explicitly; it must do the same for
		// NeedsRedeploy, set by markDriftedHelmCharts when drift-detection reports this chart
		// changed out of band, otherwise the flag is silently wiped before shouldUpgrade can act on
		// it, and a chart with a real, detected drift is never actually redeployed.
		nginxChart := &configv1beta1.HelmChart{
			RepositoryURL:    testRepoURLNginxStable,
			RepositoryName:   testRepoNameNginxStable,
			ChartName:        testChartNameNginxIngress,
			ChartVersion:     testChartVersion100,
			ReleaseName:      testReleaseNameNginxLatest,
			ReleaseNamespace: testNginxRepo,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{*nginxChart},
		}
		clusterSummary.Namespace = defaultNamespace
		clusterSummary.Spec.ClusterNamespace = defaultNamespace

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				{
					ReleaseName:      nginxChart.ReleaseName,
					ReleaseNamespace: nginxChart.ReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusManaging,
					NeedsRedeploy:    true,
				},
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())
		manager.RegisterClusterSummaryForCharts(clusterSummary)

		_, conflict, err := controllers.UpdateStatusForReferencedHelmReleases(context.TODO(),
			testEnv.Client, clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())

		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil || len(currentClusterSummary.Status.HelmReleaseSummaries) != 1 {
				return false
			}
			return currentClusterSummary.Status.HelmReleaseSummaries[0].NeedsRedeploy
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("UpdateStatusForReferencedHelmReleases carries PatchesHash forward instead of recomputing it", func() {
		// Regression test: buildReferencedHelmReleaseSummaries used to store the freshly computed
		// PatchesHash directly, on every reconcile, before shouldUpgrade ever ran. That made a
		// real patches change undetectable: by the time shouldUpgrade compared old vs current,
		// both had already been set to the same fresh value moments earlier in the same
		// reconcile. PatchesHash must instead be carried forward here, the same way ValuesHash
		// already is, and only advanced to the current value on a successful deploy
		// (updateValueHashOnHelmChartSummary), so shouldUpgrade's comparison has something real
		// to compare against.
		nginxChart := &configv1beta1.HelmChart{
			RepositoryURL:    testRepoURLNginxStable,
			RepositoryName:   testRepoNameNginxStable,
			ChartName:        testChartNameNginxIngress,
			ChartVersion:     testChartVersion100,
			ReleaseName:      testReleaseNameNginxLatest,
			ReleaseNamespace: testNginxRepo,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{*nginxChart},
			// Patches configured now. If PatchesHash were recomputed instead of carried
			// forward, this reconcile would store the hash of *this* patch list, matching
			// whatever shouldUpgrade computes as "current" right after, and the change below
			// would never be visible.
			Patches: []libsveltosv1beta1.Patch{
				{
					Patch: testManagedAnnotationPatch,
					Target: &libsveltosv1beta1.PatchSelector{
						Kind: testKindDeployment,
					},
				},
			},
		}
		clusterSummary.Namespace = defaultNamespace
		clusterSummary.Spec.ClusterNamespace = defaultNamespace

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		stalePatchesHash := []byte("stale-patches-hash-from-before-patches-were-added")
		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				{
					ReleaseName:      nginxChart.ReleaseName,
					ReleaseNamespace: nginxChart.ReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusManaging,
					PatchesHash:      stalePatchesHash,
				},
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())
		manager.RegisterClusterSummaryForCharts(clusterSummary)

		// Checked on the function's direct return value, not via a Get against testEnv's cache:
		// the cache can still be showing the pre-call state for a beat after this returns, which
		// would let a broken (fresh-value-writing) implementation slip past an Eventually/Get
		// check by accident, since the stale cache read looks like the pre-existing value too.
		updatedClusterSummary, conflict, err := controllers.UpdateStatusForReferencedHelmReleases(context.TODO(),
			testEnv.Client, clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())
		Expect(updatedClusterSummary.Status.HelmReleaseSummaries).To(HaveLen(1))
		Expect(bytes.Equal(updatedClusterSummary.Status.HelmReleaseSummaries[0].PatchesHash, stalePatchesHash)).To(BeTrue())
	})

	It("updateStatusForeferencedHelmReleases is no-op in DryRun mode", func() {
		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{
				{RepositoryURL: randomString(), RepositoryName: randomString(), ChartName: randomString(), ChartVersion: randomString(),
					ReleaseName: randomString(), ReleaseNamespace: randomString()},
			},
			SyncMode: configv1beta1.SyncModeDryRun,
		}

		// List an helm chart non referenced anymore as managed
		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				{ReleaseName: randomString(), ReleaseNamespace: randomString(), Status: configv1beta1.HelmChartStatusManaging},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummary, conflict, err := controllers.UpdateStatusForReferencedHelmReleases(context.TODO(), c, clusterSummary, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())

		// Cause we are in DryRun mode, clusterSummary Status has not changed
		Expect(len(currentClusterSummary.Status.HelmReleaseSummaries)).To(Equal(1))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName).To(
			Equal(clusterSummary.Status.HelmReleaseSummaries[0].ReleaseName))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace).To(
			Equal(clusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].Status).To(
			Equal(clusterSummary.Status.HelmReleaseSummaries[0].Status))
	})

	It("UpdateStatusForNonReferencedHelmReleases updates ClusterSummary.Status.HelmReleaseSummaries", func() {
		contourChart := &configv1beta1.HelmChart{
			RepositoryURL:    testRepoURLBitnami,
			RepositoryName:   "bitnami/contour",
			ChartName:        testChartNameBitnamiContour,
			ChartVersion:     testChartVersion2114,
			ReleaseName:      testReleaseNameContour,
			ReleaseNamespace: "contour",
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		kyvernoSummary := configv1beta1.HelmChartSummary{
			ReleaseName:      testReleaseNameKyverno,
			ReleaseNamespace: testReleaseNameKyverno,
			Status:           configv1beta1.HelmChartStatusManaging,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{*contourChart},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		// List a helm chart non referenced anymore as managed
		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				kyvernoSummary,
				{
					ReleaseName:      contourChart.ReleaseName,
					ReleaseNamespace: contourChart.ReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusManaging,
				},
			},
		}

		Expect(testEnv.Status().Update(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		Eventually(func() bool {
			currentlClusterSummary := &configv1beta1.ClusterSummary{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterSummary.Name, Namespace: clusterSummary.Namespace},
				currentlClusterSummary)
			if err != nil {
				return false
			}
			return len(currentlClusterSummary.Spec.ClusterProfileSpec.HelmCharts) == 1 &&
				len(currentlClusterSummary.Status.HelmReleaseSummaries) == 2
		}, timeout, pollingInterval).Should(BeTrue())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		clusterObjects, err := controllers.FetchClusterObjects(context.TODO(), testEnv.Config, testEnv.Client,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.ClusterTypeCapi,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		clusterSummary, err = controllers.UpdateStatusForNonReferencedHelmReleases(context.TODO(), testEnv.Client,
			controllers.NewDeploymentContext(clusterSummary, clusterObjects, nil), textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			if len(currentClusterSummary.Status.HelmReleaseSummaries) == 1 &&
				currentClusterSummary.Status.HelmReleaseSummaries[0].Status == configv1beta1.HelmChartStatusManaging &&
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName == contourChart.ReleaseName &&
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace == contourChart.ReleaseNamespace {
				return true
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("UpdateStatusForNonReferencedHelmReleases retries on a status update conflict (regression for #1933)",
		func() {
			// No helm charts referenced and no existing HelmReleaseSummaries: the only side
			// effect left is the unconditional Status().Update() at the end of the function.
			initObjects := []client.Object{
				clusterSummary,
			}

			// Simulate the cached-client race from #1933: a concurrent write (e.g.
			// updateValueHashOnHelmChartSummary) lands between this function's Get and its
			// own Status().Update, so the first attempt loses with a stale-resourceVersion
			// conflict. Only the first status update conflicts, mirroring a real race rather
			// than a permanently broken client.
			conflictCount := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, cl client.Client, subResourceName string,
						obj client.Object, opts ...client.SubResourceUpdateOption) error {

						if subResourceName == testStatusField && conflictCount == 0 {
							conflictCount++
							return apierrors.NewConflict(
								schema.GroupResource{Group: configv1beta1.GroupVersion.Group, Resource: "clustersummaries"},
								obj.GetName(),
								fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again"))
						}
						return cl.SubResource(subResourceName).Update(ctx, obj, opts...)
					},
				}).Build()

			_, err := controllers.UpdateStatusForNonReferencedHelmReleases(context.TODO(), c,
				controllers.NewDeploymentContext(clusterSummary, nil, nil), textlogger.NewLogger(textlogger.NewConfig()))
			Expect(err).To(BeNil())
			Expect(conflictCount).To(Equal(1))
		})

	It("updateChartsInClusterConfiguration updates ClusterConfiguration with deployed helm releases", func() {
		chartDeployed := []configv1beta1.Chart{
			{
				RepoURL:      "https://charts.bitnami.com/bitnami",
				ReleaseName:  testReleaseNameContour,
				ChartVersion: testChartVersion2114,
				Namespace:    "projectcontour",
			},
		}

		clusterConfiguration := &configv1beta1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      controllers.GetClusterConfigurationName(clusterSummary.Spec.ClusterName, libsveltosv1beta1.ClusterTypeCapi),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Status: configv1beta1.ClusterConfigurationStatus{
				ClusterProfileResources: []configv1beta1.ClusterProfileResource{
					{
						ClusterProfileName: clusterProfile.Name},
				},
			},
		}

		initObjects := []client.Object{
			clusterConfiguration,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		Expect(controllers.UpdateChartsInClusterConfiguration(context.TODO(), c, clusterSummary,
			chartDeployed, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		currentClusterConfiguration := &configv1beta1.ClusterConfiguration{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterConfiguration.Namespace,
				Name:      clusterConfiguration.Name,
			},
			currentClusterConfiguration)).To(Succeed())

		Expect(currentClusterConfiguration.Status.ClusterProfileResources).ToNot(BeNil())
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(1))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].ClusterProfileName).To(
			Equal(clusterProfile.Name))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features).ToNot(BeNil())
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources[0].Features)).To(Equal(1))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].FeatureID).To(
			Equal(libsveltosv1beta1.FeatureHelm))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts).ToNot(BeNil())
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts)).To(Equal(1))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts[0].RepoURL).To(
			Equal(chartDeployed[0].RepoURL))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts[0].ReleaseName).To(
			Equal(chartDeployed[0].ReleaseName))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts[0].ChartVersion).To(
			Equal(chartDeployed[0].ChartVersion))
	})

	It("updateChartsInClusterConfiguration stores the live release ChartVersion, not the spec version", func() {
		// chartDeployed is built from currentRelease.ChartVersion (the actually-deployed version),
		// not from the spec. This test verifies that what lands in ClusterConfiguration reflects
		// what the cluster actually has, including multi-chart profiles.
		chartsDeployed := []configv1beta1.Chart{
			{
				RepoURL:      "https://prometheus-community.github.io/helm-charts",
				ReleaseName:  testReleaseNamePrometheus,
				Namespace:    testReleaseNamePrometheus,
				ChartVersion: "26.0.0",
				AppVersion:   "v3.0.0",
			},
			{
				RepoURL:      "https://grafana-community.github.io/helm-charts",
				ReleaseName:  testReleaseNameGrafana,
				Namespace:    testReleaseNameGrafana,
				ChartVersion: "11.3.6",
				AppVersion:   "12.4.2",
			},
		}

		clusterConfiguration := &configv1beta1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      controllers.GetClusterConfigurationName(clusterSummary.Spec.ClusterName, libsveltosv1beta1.ClusterTypeCapi),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Status: configv1beta1.ClusterConfigurationStatus{
				ClusterProfileResources: []configv1beta1.ClusterProfileResource{
					{ClusterProfileName: clusterProfile.Name},
				},
			},
		}

		initObjects := []client.Object{clusterConfiguration}
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		Expect(controllers.UpdateChartsInClusterConfiguration(context.TODO(), c, clusterSummary,
			chartsDeployed, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		current := &configv1beta1.ClusterConfiguration{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterConfiguration.Namespace, Name: clusterConfiguration.Name},
			current)).To(Succeed())

		Expect(current.Status.ClusterProfileResources).To(HaveLen(1))
		charts := current.Status.ClusterProfileResources[0].Features[0].Charts
		Expect(charts).To(HaveLen(2))

		for i, expected := range chartsDeployed {
			Expect(charts[i].ChartVersion).To(Equal(expected.ChartVersion),
				"chart %s: stored version must match the live release version", expected.ReleaseName)
			Expect(charts[i].AppVersion).To(Equal(expected.AppVersion))
			Expect(charts[i].ReleaseName).To(Equal(expected.ReleaseName))
			Expect(charts[i].RepoURL).To(Equal(expected.RepoURL))
		}
	})

	It("createReportForUnmanagedHelmRelease ", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			SyncMode:   configv1beta1.SyncModeDryRun,
			HelmCharts: []configv1beta1.HelmChart{*helmChart},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		report, err := controllers.CreateReportForUnmanagedHelmRelease(context.TODO(), c, clusterSummary,
			helmChart, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(report).ToNot(BeNil())
		Expect(report.Action).To(Equal(string(configv1beta1.InstallHelmAction)))
		Expect(report.ReleaseName).To(Equal(helmChart.ReleaseName))
		Expect(report.ReleaseNamespace).To(Equal(helmChart.ReleaseNamespace))
	})

	It("getInstantiatedChart returns instantiated HelmChart matching passed in chart", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Namespace = defaultNamespace
		clusterSummary.Spec.ClusterNamespace = defaultNamespace

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		clusterObjects, err := controllers.FetchClusterObjects(context.TODO(), testEnv.Config, testEnv.Client,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.ClusterTypeCapi,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		instaniatedChart, err := controllers.GetInstantiatedChart(context.TODO(),
			controllers.NewDeploymentContext(clusterSummary, clusterObjects, nil), helmChart,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(instaniatedChart.ReleaseName).To(Equal(helmChart.ReleaseName))
		Expect(instaniatedChart.ReleaseNamespace).To(Equal(helmChart.ReleaseNamespace))
		Expect(instaniatedChart.ChartName).To(Equal(helmChart.ChartName))
		Expect(instaniatedChart.RepositoryURL).To(Equal(helmChart.RepositoryURL))
		Expect(instaniatedChart.RepositoryName).To(Equal(helmChart.RepositoryName))
		Expect(instaniatedChart.HelmChartAction).To(Equal(helmChart.HelmChartAction))
		Expect(instaniatedChart.ChartVersion).To(Equal(helmChart.ChartVersion))
	})

	It("getInstantiatedChart returns instantiated HelmChart", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), RepositoryURL: randomString(),
			RepositoryName: randomString(), HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		helmChart.ChartVersion = `{{$version := index .Cluster.metadata.labels "k8s-version" }}{{- if eq $version "1.20"}}23.4.0
{{- else if eq $version "1.22"}}24.1.0
{{- else if eq $version "1.25"}}25.0.2
{{- else }}23.4.0
{{- end}}`

		clusterSummary.Namespace = defaultNamespace
		clusterSummary.Spec.ClusterNamespace = defaultNamespace
		clusterSummary.Spec.ClusterType = libsveltosv1beta1.ClusterTypeSveltos

		cluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Labels: map[string]string{
					"k8s-version": "1.25",
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		clusterObjects, err := controllers.FetchClusterObjects(context.TODO(), testEnv.Config, testEnv.Client,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.ClusterTypeSveltos,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		instaniatedChart, err := controllers.GetInstantiatedChart(context.TODO(),
			controllers.NewDeploymentContext(clusterSummary, clusterObjects, nil), helmChart,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(instaniatedChart.ReleaseName).To(Equal(helmChart.ReleaseName))
		Expect(instaniatedChart.ReleaseNamespace).To(Equal(helmChart.ReleaseNamespace))
		Expect(instaniatedChart.ChartName).To(Equal(helmChart.ChartName))
		Expect(instaniatedChart.RepositoryURL).To(Equal(helmChart.RepositoryURL))
		Expect(instaniatedChart.RepositoryName).To(Equal(helmChart.RepositoryName))
		Expect(instaniatedChart.HelmChartAction).To(Equal(helmChart.HelmChartAction))
		Expect(instaniatedChart.ChartVersion).To(Equal("25.0.2"))
	})

	It("updateClusterReportWithHelmReports updates ClusterReports with HelmReports", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			SyncMode:   configv1beta1.SyncModeDryRun,
			HelmCharts: []configv1beta1.HelmChart{*helmChart},
		}

		clusterReport := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterops.GetClusterReportName(configv1beta1.ClusterProfileKind,
					clusterProfile.Name, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Spec: configv1beta1.ClusterReportSpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
			},
			Status: configv1beta1.ClusterReportStatus{
				ResourceReports: []libsveltosv1beta1.ResourceReport{
					{
						Action:   string(libsveltosv1beta1.CreateResourceAction),
						Resource: libsveltosv1beta1.Resource{Name: randomString(), Kind: randomString()}},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		releaseReports := []configv1beta1.ReleaseReport{
			{ReleaseName: helmChart.ReleaseName, ReleaseNamespace: helmChart.ReleaseNamespace, Action: string(configv1beta1.HelmChartActionInstall)},
		}
		err := controllers.UpdateClusterReportWithHelmReports(context.TODO(), c, clusterSummary, releaseReports)
		Expect(err).To(BeNil())

		currentClusterReport := &configv1beta1.ClusterReport{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Name: clusterReport.Name, Namespace: clusterReport.Namespace}, currentClusterReport)).To(Succeed())
		Expect(currentClusterReport.Status.ResourceReports).ToNot(BeNil())
		Expect(len(currentClusterReport.Status.ResourceReports)).To(Equal(1))
		Expect(reflect.DeepEqual(currentClusterReport.Status.ResourceReports, clusterReport.Status.ResourceReports)).To(BeTrue())
		Expect(currentClusterReport.Status.ReleaseReports).To(ContainElement(releaseReports[0]))
	})

	It("handleCharts in DryRun mode updates ClusterReport", func() {
		helmChartInstall := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		helmChartUninstall := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionUninstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			SyncMode: configv1beta1.SyncModeDryRun,
			HelmCharts: []configv1beta1.HelmChart{
				*helmChartInstall, *helmChartUninstall,
			},
		}

		clusterReport := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterops.GetClusterReportName(configv1beta1.ClusterProfileKind,
					clusterProfile.Name, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Spec: configv1beta1.ClusterReportSpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterProfile)).To(Succeed())
		clusterSummary.OwnerReferences = []metav1.OwnerReference{
			{
				Kind:       configv1beta1.ClusterProfileKind,
				Name:       clusterProfile.Name,
				APIVersion: testConfigAPIVersion,
				UID:        clusterProfile.UID,
			},
		}
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, clusterReport)).To(Succeed())

		kubeconfig, closer, err := clusterproxy.CreateKubeconfig(textlogger.NewLogger(textlogger.NewConfig()), testEnv.Kubeconfig)
		Expect(err).To(BeNil())
		defer closer()

		// ClusterSummary in DryRun mode. Nothing registered with chartManager with respect to the two referenced
		// helm chart. So expect action for Install will be install, and the action for Uninstall will be no action as
		// such release has never been installed.
		err = controllers.HandleCharts(context.TODO(), clusterSummary, testEnv.Client, testEnv.Client, kubeconfig,
			false, nil, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())

		var druRunError *configv1beta1.DryRunReconciliationError
		ok := errors.As(err, &druRunError)
		Expect(ok).To(BeTrue())

		currentClusterReport := &configv1beta1.ClusterReport{}
		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterReport.Name, Namespace: clusterReport.Namespace}, currentClusterReport)
			if err != nil {
				return false
			}
			return len(currentClusterReport.Status.ReleaseReports) == 2
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Name: clusterReport.Name, Namespace: clusterReport.Namespace}, currentClusterReport)).To(Succeed())

		// Verify action for helmChartInstall is Install
		found := false
		for i := range currentClusterReport.Status.ReleaseReports {
			report := &currentClusterReport.Status.ReleaseReports[i]
			if report.ReleaseName == helmChartInstall.ReleaseName {
				Expect(report.Action).To(Equal(string(configv1beta1.InstallHelmAction)))
				found = true
			}
		}
		Expect(found).To(BeTrue())

		// Verify action for helmChartUninstall is no action
		found = false
		for i := range currentClusterReport.Status.ReleaseReports {
			report := &currentClusterReport.Status.ReleaseReports[i]
			if report.ReleaseName == helmChartUninstall.ReleaseName {
				Expect(report.Action).To(Equal(string(configv1beta1.NoHelmAction)))
				found = true
			}
		}
		Expect(found).To(BeTrue())
	})
})

var _ = Describe("Hash methods", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
	})

	It("HelmHash returns hash considering all referenced helm charts", func() {
		kyvernoChart := configv1beta1.HelmChart{
			RepositoryURL:    "https://kyverno.github.io/kyverno/",
			RepositoryName:   testReleaseNameKyverno,
			ChartName:        "kyverno/kyverno",
			ChartVersion:     "v3.0.1",
			ReleaseName:      "kyverno-latest",
			ReleaseNamespace: testReleaseNameKyverno,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		nginxChart := configv1beta1.HelmChart{
			RepositoryURL:    testRepoURLNginxStable,
			RepositoryName:   testRepoNameNginxStable,
			ChartName:        testChartNameNginxIngress,
			ChartVersion:     "0.17.1",
			ReleaseName:      testReleaseNameNginxLatest,
			ReleaseNamespace: testNginxRepo,
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		namespace := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Create(ctx, ns)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, ns)).To(Succeed())

		cluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}
		Expect(testEnv.Create(ctx, cluster)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, cluster)).To(Succeed())

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeSveltos,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						kyvernoChart,
						nginxChart,
					},
					Patches: []libsveltosv1beta1.Patch{
						{
							Patch: testEnvLabelPatch,
						},
					},
					Tier: 100,
				},
			},
		}
		Expect(testEnv.Create(ctx, clusterSummary)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, clusterSummary)).To(Succeed())

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         testEnv,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: testControllerNameSummary,
		})
		Expect(err).To(BeNil())

		config := fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode)
		config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Reloader)
		config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Tier)
		config += fmt.Sprintf("%t", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.ContinueOnConflict)

		patchBytes, err := json.Marshal(clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Patches)
		Expect(err).To(BeNil())
		patchHash := sha256.New()
		patchHash.Write(patchBytes)
		config += fmt.Sprintf("%x", patchHash.Sum(nil))

		sort.Sort(controllers.SortedHelmCharts(clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts))
		for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			currentChart := &clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			chartHash, err := controllers.GetHelmChartHash(context.TODO(), testEnv, clusterSummary, currentChart, nil, logger)
			Expect(err).To(BeNil())
			config += chartHash
		}

		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.HelmHash(context.TODO(), testEnv, clusterSummaryScope.ClusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})

	It(`getHelmChartInstantiatedValues returns the instantiated values/valuesFrom`, func() {
		namespace := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Create(ctx, ns)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, ns)).To(Succeed())

		validYamlValue := func() string {
			return fmt.Sprintf("%s: %s", randomString(), randomString())
		}

		templatedHash := `cluster: {{ .Cluster.metadata.name }}`

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				Annotations: map[string]string{
					libsveltosv1beta1.PolicyTemplateAnnotation: testOkValue,
				},
			},
			Data: map[string]string{
				randomString(): validYamlValue(),
				randomString(): validYamlValue(),
				randomString(): validYamlValue(),
				randomString(): validYamlValue(),
				randomString(): templatedHash,
			},
		}
		Expect(testEnv.Create(ctx, configMap)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, configMap)).To(Succeed())

		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
		},
			Data: map[string][]byte{
				randomString(): []byte(validYamlValue()),
				randomString(): []byte(validYamlValue()),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}
		Expect(testEnv.Create(ctx, secret)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, secret)).To(Succeed())

		helmChart := configv1beta1.HelmChart{
			RepositoryURL:    randomString(),
			RepositoryName:   randomString(),
			ChartName:        randomString(),
			ChartVersion:     randomString(),
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			ValuesFrom: []configv1beta1.ValueFrom{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: namespace,
					Name:      configMap.Name,
				},
				{
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
					Namespace: namespace,
					Name:      secret.Name,
				},
			},
		}

		cluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}
		Expect(testEnv.Create(ctx, cluster)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, cluster)).To(Succeed())

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeSveltos,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						helmChart,
					},
				},
			},
		}
		Expect(testEnv.Create(ctx, clusterSummary)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, clusterSummary)).To(Succeed())

		// Hash is calculated using instantiated values
		instantiatedValues, err := controllers.GetHelmChartInstantiatedValues(context.TODO(), clusterSummary, nil, &helmChart, logger)
		Expect(err).To(BeNil())
		yamlValues, err := instantiatedValues.YAML()
		Expect(err).To(BeNil())
		Expect(yamlValues).To(ContainSubstring(fmt.Sprintf(`cluster: %s`, cluster.Name)))
	})

	It("getHelmChartValuesHash returns consistent hash considering Values and ValuesFrom", func() {
		namespace := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Create(ctx, ns)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, ns)).To(Succeed())

		helmValueContent := "region: west"

		helmValuesFromContent := `
global:
  tag: my-tag
replicaCount: 1
resources:
  limits:
    cpu: 100m`

		key := randomString()
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				Annotations: map[string]string{
					libsveltosv1beta1.PolicyTemplateAnnotation: testOkValue,
				},
			},
			Data: map[string]string{
				key: helmValuesFromContent,
			},
		}
		Expect(testEnv.Create(ctx, configMap)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, configMap)).To(Succeed())

		requestedChart := configv1beta1.HelmChart{
			RepositoryURL:    randomString(),
			RepositoryName:   randomString(),
			ChartName:        randomString(),
			ChartVersion:     randomString(),
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			Values:           helmValueContent,
			ValuesFrom: []configv1beta1.ValueFrom{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			},
		}

		cluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
		}
		Expect(testEnv.Create(ctx, cluster)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, cluster)).To(Succeed())

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeSveltos,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						requestedChart,
					},
				},
			},
		}
		Expect(testEnv.Create(ctx, clusterSummary)).To(Succeed())
		Expect(waitForObject(ctx, testEnv, clusterSummary)).To(Succeed())

		expectedHash, err := controllers.GetHelmChartValuesHash(context.TODO(), testEnv, &requestedChart,
			clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		// Must be consistent
		for range 10 {
			hash, err := controllers.GetHelmChartValuesHash(context.TODO(), testEnv, &requestedChart,
				clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
			Expect(err).To(BeNil())
			Expect(reflect.DeepEqual(hash, expectedHash)).To(BeTrue())
		}
	})

	It("getCredentialsAndCAFiles returns files containing credentials and CA", func() {
		credentials := struct {
			Username     string `json:"username"`
			Password     string `json:"password"`
			RefreshToken string `json:"refresh_token"`
			AccessToken  string `json:"access_token"`
		}{
			Username:     randomString(),
			Password:     randomString(),
			RefreshToken: randomString(),
			AccessToken:  randomString(),
		}

		//nolint:gosec // This is dummy data for testing purposes
		credentialsBytes, err := json.Marshal(credentials)
		Expect(err).To(BeNil())

		// getCredentialsAndCAFiles reads these Secrets through getManagementClusterDirectClient(),
		// which is backed by the shared envtest API server (see controllers_suite_test.go), not by
		// the local fake client c built below. So they must be created via testEnv, not just added
		// to initObjects.
		credentialsNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
		Expect(testEnv.Create(context.TODO(), credentialsNamespace)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, credentialsNamespace)).To(Succeed())

		secretCredentials := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: credentialsNamespace.Name,
				Name:      randomString(),
			},
			Data: map[string][]byte{
				// The real API server (this Secret is now created via testEnv, not the local
				// fake client) enforces this exact key for type=kubernetes.io/dockerconfigjson.
				corev1.DockerConfigJsonKey: credentialsBytes,
			},
			Type: corev1.SecretTypeDockerConfigJson,
		}
		Expect(testEnv.Create(context.TODO(), secretCredentials)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secretCredentials)).To(Succeed())

		caByte := []byte(randomString())
		secretCA := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: credentialsNamespace.Name,
				Name:      randomString(),
			},
			Data: map[string][]byte{
				"ca.crt": caByte,
			},
		}
		Expect(testEnv.Create(context.TODO(), secretCA)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secretCA)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       configv1beta1.ClusterProfileKind,
						Name:       randomString(),
						APIVersion: testConfigAPIVersion,
						UID:        types.UID(randomString()),
					},
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		initObjects := []client.Object{
			secretCredentials, secretCA, cluster,
		}

		requestedChart := configv1beta1.HelmChart{
			RegistryCredentialsConfig: &configv1beta1.RegistryCredentialsConfig{
				CredentialsSecretRef: &corev1.SecretReference{
					Namespace: secretCredentials.Namespace,
					Name:      secretCredentials.Name,
				},
				CASecretRef: &corev1.SecretReference{
					Namespace: secretCA.Namespace,
					Name:      secretCA.Name,
				},
				InsecureSkipTLSVerify: false,
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		credentialsPath, caPath, err := controllers.GetCredentialsAndCAFiles(context.TODO(), c,
			clusterSummary, &requestedChart)
		Expect(err).To(BeNil())
		Expect(credentialsPath).ToNot(BeEmpty())
		verifyFileContent(credentialsPath, credentialsBytes)
		Expect(os.Remove(credentialsPath)).To(Succeed())
		Expect(caPath).ToNot(BeEmpty())
		verifyFileContent(caPath, caByte)
		Expect(os.Remove(caPath)).To(Succeed())
	})

	It("getHelmChartValuesFrom returns values to instantiate and the one ready to be used", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		toInstantiate := `customConfig:
  transforms:
    flagger_warning_counter:
      type: log_to_metric
      inputs: ["flagger_filter"]
      metrics:
        - type: counter
          name: flagger_warnings_total
          field: "flagger_warning_count"
          tags:
            namespace: '{{.target_namespace}}'
            pod_name: '{{.target_pod_name}}'
            canary_name: '{{.canary_name}}'
            event_type: '{{.event_type}}''`

		readyToUse := `installOptions:
  createNamespace: true`

		configMap1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: ns.Name,
			},
			Data: map[string]string{
				testValueKey: readyToUse,
			},
		}

		configMap2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: ns.Name,
				Annotations: map[string]string{
					libsveltosv1beta1.PolicyTemplateAnnotation: testOkValue,
				},
			},
			Data: map[string]string{
				testValueKey: toInstantiate,
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: ns.Name,
			},
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: ns.Name,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterName:      cluster.Name,
				ClusterNamespace: cluster.Namespace,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		helmChart := configv1beta1.HelmChart{
			RepositoryURL:    randomString(),
			RepositoryName:   randomString(),
			ChartName:        randomString(),
			ChartVersion:     randomString(),
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			ValuesFrom: []configv1beta1.ValueFrom{
				{
					Namespace: configMap1.Namespace,
					Name:      configMap1.Name,
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				},
				{
					Namespace: configMap2.Namespace,
					Name:      configMap2.Name,
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				},
			},
		}

		initObjects := []client.Object{
			ns, cluster, configMap1, configMap2, clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		valuesToInstantiate, valuesToUse, err := controllers.GetHelmChartValuesFrom(context.TODO(), c, clusterSummary,
			&helmChart, logger)
		Expect(err).To(BeNil())
		Expect(len(valuesToUse)).To(Equal(1))
		Expect(valuesToUse[0]).To(Equal(readyToUse))
		Expect(len(valuesToInstantiate)).To(Equal(1))
		Expect(valuesToInstantiate[0]).To(Equal(toInstantiate))
	})

	It("getHelmActionInPullMode returns upgrade when spec version is newer", func() {
		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())

		cs := newPullModeClusterSummary()
		chart := newChart("1.20.2")
		cached := &configv1beta1.HelmChart{
			ReleaseName:      chart.ReleaseName,
			ReleaseNamespace: chart.ReleaseNamespace,
			ChartVersion:     testChartVersion119,
		}
		manager.RegisterVersionForChart(cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cached)

		action, err := controllers.GetHelmActionInPullMode(context.TODO(), cs, chart)
		Expect(err).To(BeNil())
		Expect(action).To(Equal(controllers.HelmActionUpgrade))
	})

	It("getHelmActionInPullMode wraps spec semver errors with chart context", func() {
		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())

		cs := newPullModeClusterSummary()
		chart := newChart("not-a-version")
		cached := &configv1beta1.HelmChart{
			ReleaseName:      chart.ReleaseName,
			ReleaseNamespace: chart.ReleaseNamespace,
			ChartVersion:     testChartVersion119,
		}
		manager.RegisterVersionForChart(cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cached)

		_, err = controllers.GetHelmActionInPullMode(context.TODO(), cs, chart)
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring(chart.ReleaseName))
		Expect(err.Error()).To(ContainSubstring(chart.ReleaseNamespace))
		Expect(err.Error()).To(ContainSubstring("not-a-version"))
	})
})

func newPullModeClusterSummary() *configv1beta1.ClusterSummary {
	return &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      randomString(),
			Namespace: randomString(),
		},
		Spec: configv1beta1.ClusterSummarySpec{
			ClusterNamespace: randomString(),
			ClusterName:      randomString(),
			ClusterType:      libsveltosv1beta1.ClusterTypeSveltos,
		},
	}
}

func newChart(version string) *configv1beta1.HelmChart {
	return &configv1beta1.HelmChart{
		ReleaseName:      randomString(),
		ReleaseNamespace: randomString(),
		ChartVersion:     version,
	}
}

func verifyFileContent(filePath string, data []byte) {
	content, err := os.ReadFile(filePath)
	Expect(err).To(BeNil())
	Expect(reflect.DeepEqual(content, data)).To(BeTrue())
}

var _ = Describe("selectorMatchesCluster", func() {
	var clusterSummary *configv1beta1.ClusterSummary

	BeforeEach(func() {
		clusterSummary = &configv1beta1.ClusterSummary{
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: randomString(),
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
	})

	It("returns true when cluster labels match the selector", func() {
		matchKey := randomString()
		matchVal := randomString()

		selector := libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{matchKey: matchVal},
			},
		}
		clusterLabels := labels.Set{matchKey: matchVal, randomString(): randomString()}

		Expect(controllers.SelectorMatchesCluster(selector, nil, clusterSummary, clusterLabels)).To(BeTrue())
	})

	It("returns false when cluster labels do not match the selector", func() {
		selector := libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{randomString(): randomString()},
			},
		}
		clusterLabels := labels.Set{randomString(): randomString()}

		Expect(controllers.SelectorMatchesCluster(selector, nil, clusterSummary, clusterLabels)).To(BeFalse())
	})

	It("returns true when cluster is listed in clusterRefs even if selector does not match", func() {
		selector := libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{randomString(): randomString()},
			},
		}
		clusterLabels := labels.Set{randomString(): randomString()}

		refs := []corev1.ObjectReference{
			{
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Name:      clusterSummary.Spec.ClusterName,
			},
		}

		Expect(controllers.SelectorMatchesCluster(selector, refs, clusterSummary, clusterLabels)).To(BeTrue())
	})

	It("returns false when neither selector nor clusterRefs match", func() {
		selector := libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{randomString(): randomString()},
			},
		}
		clusterLabels := labels.Set{randomString(): randomString()}

		refs := []corev1.ObjectReference{
			{Namespace: randomString(), Name: randomString()},
		}

		Expect(controllers.SelectorMatchesCluster(selector, refs, clusterSummary, clusterLabels)).To(BeFalse())
	})
})

var _ = Describe("isProfileFullyProcessed", func() {
	var clusterSummary *configv1beta1.ClusterSummary
	var otherCS *configv1beta1.ClusterSummary
	var c client.Client

	BeforeEach(func() {
		clusterNamespace := randomString()
		clusterName := randomString()

		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		otherCS = nil
		c = nil
	})

	AfterEach(func() {
		if otherCS == nil || c == nil {
			return
		}
		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		otherCS.Spec.ClusterProfileSpec.HelmCharts = nil
		manager.RemoveStaleRegistrations(otherCS)
	})

	It("returns false when no ClusterSummary exists for the profile", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())
		c = fake.NewClientBuilder().WithScheme(s).Build()

		processed, err := controllers.IsProfileFullyProcessed(context.TODO(), c,
			randomString(), clusterops.ClusterProfileLabelName,
			clusterSummary, 1)
		Expect(err).To(BeNil())
		Expect(processed).To(BeFalse())
	})

	It("returns false when ClusterSummary exists but is not registered with the chart manager", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		profileName := randomString()

		otherCS = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: profileName,
					configv1beta1.ClusterNameLabel:     clusterSummary.Spec.ClusterName,
					configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						{
							ReleaseName: randomString(), ReleaseNamespace: randomString(),
							RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
						},
					},
				},
			},
		}
		c = fake.NewClientBuilder().WithScheme(s).WithObjects(otherCS).Build()

		// otherCS is in the fake client but has not been registered with the chart manager
		processed, err := controllers.IsProfileFullyProcessed(context.TODO(), c,
			profileName, clusterops.ClusterProfileLabelName,
			clusterSummary, len(otherCS.Spec.ClusterProfileSpec.HelmCharts))
		Expect(err).To(BeNil())
		Expect(processed).To(BeFalse())
	})

	It("returns true when ClusterSummary has registered all its charts with the chart manager", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		profileName := randomString()

		otherCS = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: profileName,
					configv1beta1.ClusterNameLabel:     clusterSummary.Spec.ClusterName,
					configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						{
							ReleaseName: randomString(), ReleaseNamespace: randomString(),
							RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
						},
					},
				},
			},
		}
		c = fake.NewClientBuilder().WithScheme(s).WithObjects(otherCS).Build()

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		manager.RegisterClusterSummaryForCharts(otherCS)

		processed, err := controllers.IsProfileFullyProcessed(context.TODO(), c,
			profileName, clusterops.ClusterProfileLabelName,
			clusterSummary, len(otherCS.Spec.ClusterProfileSpec.HelmCharts))
		Expect(err).To(BeNil())
		Expect(processed).To(BeTrue())
	})
})

var _ = Describe("allMatchingProfilesProcessed", func() {
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var ownerCPName string

	BeforeEach(func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		ownerCPName = randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: clusterNamespace,
				Labels:    map[string]string{testEnvLabelKey: testProductionValue},
			},
		}

		now := metav1.Now()
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:              randomString(),
				Namespace:         clusterNamespace,
				DeletionTimestamp: &now,
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: ownerCPName,
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
	})

	It("returns true when no other ClusterProfile with HelmCharts matches the cluster", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		// cpNoMatch has HelmCharts but its selector does not match the cluster
		cpNoMatch := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testEnvLabelKey: "staging"},
					},
				},
				HelmCharts: []configv1beta1.HelmChart{
					{ReleaseName: randomString(), ReleaseNamespace: randomString(),
						RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString()},
				},
			},
		}

		c := fake.NewClientBuilder().WithScheme(s).WithObjects(cluster, cpNoMatch).Build()

		processed, err := controllers.AllMatchingProfilesProcessed(context.TODO(), c,
			clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(processed).To(BeTrue())
	})

	It("returns true when the cluster does not exist", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		// no cluster object in the fake client
		c := fake.NewClientBuilder().WithScheme(s).Build()

		processed, err := controllers.AllMatchingProfilesProcessed(context.TODO(), c,
			clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(processed).To(BeTrue())
	})

	It("returns false when a matching ClusterProfile's ClusterSummary has not been registered yet", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		// cpB matches the cluster and has a HelmChart but its ClusterSummary does not exist yet
		cpB := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testEnvLabelKey: testProductionValue},
					},
				},
				HelmCharts: []configv1beta1.HelmChart{
					{ReleaseName: randomString(), ReleaseNamespace: randomString(),
						RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString()},
				},
			},
		}

		c := fake.NewClientBuilder().WithScheme(s).WithObjects(cluster, cpB).Build()

		processed, err := controllers.AllMatchingProfilesProcessed(context.TODO(), c,
			clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(processed).To(BeFalse())
	})

	It("returns true when all matching ClusterProfiles have fully registered ClusterSummaries", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		// cpB matches the cluster and has a HelmChart
		cpB := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testEnvLabelKey: testProductionValue},
					},
				},
				HelmCharts: []configv1beta1.HelmChart{
					{ReleaseName: randomString(), ReleaseNamespace: randomString(),
						RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString()},
				},
			},
		}

		// csB is the ClusterSummary for cpB targeting the same cluster
		csB := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: cpB.Name,
					configv1beta1.ClusterNameLabel:     clusterSummary.Spec.ClusterName,
					configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace:   clusterSummary.Spec.ClusterNamespace,
				ClusterName:        clusterSummary.Spec.ClusterName,
				ClusterType:        libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: cpB.Spec,
			},
		}

		c := fake.NewClientBuilder().WithScheme(s).WithObjects(cluster, cpB, csB).Build()

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		manager.RegisterClusterSummaryForCharts(csB)
		defer func() {
			csB.Spec.ClusterProfileSpec.HelmCharts = nil
			manager.RemoveStaleRegistrations(csB)
		}()

		processed, err := controllers.AllMatchingProfilesProcessed(context.TODO(), c,
			clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(processed).To(BeTrue())
	})

	It("returns true when a matching ClusterProfile is being deleted and its ClusterSummary is gone", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		// cpB matches the cluster and has HelmCharts, but is being deleted — its
		// ClusterSummary was already cleaned up as part of the deletion flow.
		now := metav1.Now()
		cpB := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:              randomString(),
				DeletionTimestamp: &now,
				Finalizers:        []string{"test-finalizer"},
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testEnvLabelKey: testProductionValue},
					},
				},
				HelmCharts: []configv1beta1.HelmChart{
					{ReleaseName: randomString(), ReleaseNamespace: randomString(),
						RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString()},
				},
			},
		}

		// no ClusterSummary for cpB — it was already deleted during profile cleanup
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(cluster, cpB).Build()

		processed, err := controllers.AllMatchingProfilesProcessed(context.TODO(), c,
			clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(processed).To(BeTrue())
	})
})

var _ = Describe("requeueLowerTierChallengers", func() {
	It("requeues a challenger whose tier is now lower than the current manager's tier", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		clusterNamespace := randomString()
		clusterName := randomString()
		releaseName := randomString()
		releaseNamespace := randomString()

		chart := configv1beta1.HelmChart{
			ReleaseName:      releaseName,
			ReleaseNamespace: releaseNamespace,
			RepositoryURL:    randomString(),
			ChartName:        randomString(),
			ChartVersion:     randomString(),
		}

		// manager is the current chart owner; its tier was raised above the challenger's.
		manager := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					Tier:       2147483447,
					HelmCharts: []configv1beta1.HelmChart{chart},
				},
			},
		}

		// challenger has a lower tier (higher priority) but is stuck in FailedNonRetriable.
		challenger := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					Tier:       2147463647,
					HelmCharts: []configv1beta1.HelmChart{chart},
				},
			},
			Status: configv1beta1.ClusterSummaryStatus{
				FeatureSummaries: []configv1beta1.FeatureSummary{
					{
						FeatureID: libsveltosv1beta1.FeatureHelm,
						Status:    libsveltosv1beta1.FeatureStatusFailedNonRetriable,
					},
				},
				HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
					{
						ReleaseName:      releaseName,
						ReleaseNamespace: releaseNamespace,
						Status:           configv1beta1.HelmChartStatusConflict,
					},
				},
			},
		}

		c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(challenger).WithObjects(manager, challenger).Build()

		mgr, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		// Register both: manager at position 0 (wins), challenger at position 1.
		mgr.RegisterClusterSummaryForCharts(manager)
		mgr.RegisterClusterSummaryForCharts(challenger)
		defer func() {
			manager.Spec.ClusterProfileSpec.HelmCharts = nil
			mgr.RemoveStaleRegistrations(manager)
			challenger.Spec.ClusterProfileSpec.HelmCharts = nil
			mgr.RemoveStaleRegistrations(challenger)
		}()

		logger := textlogger.NewLogger(textlogger.NewConfig())
		var requeued bool
		requeued, err = controllers.RequeueLowerTierChallengers(context.TODO(), c, manager, &chart, false, logger)
		Expect(err).To(BeNil())
		Expect(requeued).To(BeTrue())

		// The challenger should have been requeued: its Helm feature status reset to Provisioning.
		requeuedCS := &configv1beta1.ClusterSummary{}
		err = c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: challenger.Name}, requeuedCS)
		Expect(err).To(BeNil())
		var helmFS *configv1beta1.FeatureSummary
		for i := range requeuedCS.Status.FeatureSummaries {
			if requeuedCS.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
				helmFS = &requeuedCS.Status.FeatureSummaries[i]
				break
			}
		}
		Expect(helmFS).NotTo(BeNil())
		Expect(helmFS.Status).To(Equal(libsveltosv1beta1.FeatureStatusProvisioning))
	})

	It("does not requeue a challenger whose tier is higher than the current manager's tier", func() {
		s, err := setupScheme()
		Expect(err).To(BeNil())

		clusterNamespace := randomString()
		clusterName := randomString()
		chart := configv1beta1.HelmChart{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			RepositoryURL:    randomString(),
			ChartName:        randomString(),
			ChartVersion:     randomString(),
		}

		// manager has lower tier (higher priority) — correctly owns the chart.
		manager := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					Tier:       100,
					HelmCharts: []configv1beta1.HelmChart{chart},
				},
			},
		}

		// challenger has a higher tier (lower priority) — it should NOT be requeued.
		challenger := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					Tier:       2147463647,
					HelmCharts: []configv1beta1.HelmChart{chart},
				},
			},
			Status: configv1beta1.ClusterSummaryStatus{
				FeatureSummaries: []configv1beta1.FeatureSummary{
					{
						FeatureID: libsveltosv1beta1.FeatureHelm,
						Status:    libsveltosv1beta1.FeatureStatusFailedNonRetriable,
					},
				},
			},
		}

		c := fake.NewClientBuilder().WithScheme(s).WithObjects(manager, challenger).Build()

		mgr, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		mgr.RegisterClusterSummaryForCharts(manager)
		mgr.RegisterClusterSummaryForCharts(challenger)
		defer func() {
			manager.Spec.ClusterProfileSpec.HelmCharts = nil
			mgr.RemoveStaleRegistrations(manager)
			challenger.Spec.ClusterProfileSpec.HelmCharts = nil
			mgr.RemoveStaleRegistrations(challenger)
		}()

		logger := textlogger.NewLogger(textlogger.NewConfig())
		var requeued bool
		requeued, err = controllers.RequeueLowerTierChallengers(context.TODO(), c, manager, &chart, false, logger)
		Expect(err).To(BeNil())
		Expect(requeued).To(BeFalse())

		// challenger status must be unchanged (still FailedNonRetriable).
		unchanged := &configv1beta1.ClusterSummary{}
		err = c.Get(context.TODO(), types.NamespacedName{Namespace: clusterNamespace, Name: challenger.Name}, unchanged)
		Expect(err).To(BeNil())
		var helmFS *configv1beta1.FeatureSummary
		for i := range unchanged.Status.FeatureSummaries {
			if unchanged.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
				helmFS = &unchanged.Status.FeatureSummaries[i]
				break
			}
		}
		Expect(helmFS).NotTo(BeNil())
		Expect(helmFS.Status).To(Equal(libsveltosv1beta1.FeatureStatusFailedNonRetriable))
	})
})

var _ = Describe("IsCRDEstablished", func() {
	makeCondition := func(condType, status string) map[string]interface{} {
		return map[string]interface{}{"type": condType, "status": status}
	}

	makeCRD := func(conditions []map[string]interface{}) *unstructured.Unstructured {
		raw := []interface{}{}
		for _, c := range conditions {
			raw = append(raw, c)
		}
		obj := &unstructured.Unstructured{}
		obj.SetName("test-crd")
		if len(raw) > 0 {
			_ = unstructured.SetNestedSlice(obj.Object, raw, "status", "conditions")
		}
		return obj
	}

	It("returns false when status conditions are absent", func() {
		obj := &unstructured.Unstructured{}
		obj.SetName("no-status")
		Expect(controllers.IsCRDEstablished(obj)).To(BeFalse())
	})

	It("returns false when only Established is True", func() {
		obj := makeCRD([]map[string]interface{}{
			makeCondition("Established", "True"),
			makeCondition("NamesAccepted", "False"),
		})
		Expect(controllers.IsCRDEstablished(obj)).To(BeFalse())
	})

	It("returns false when only NamesAccepted is True", func() {
		obj := makeCRD([]map[string]interface{}{
			makeCondition("Established", "False"),
			makeCondition("NamesAccepted", "True"),
		})
		Expect(controllers.IsCRDEstablished(obj)).To(BeFalse())
	})

	It("returns true when both Established and NamesAccepted are True", func() {
		obj := makeCRD([]map[string]interface{}{
			makeCondition("Established", "True"),
			makeCondition("NamesAccepted", "True"),
		})
		Expect(controllers.IsCRDEstablished(obj)).To(BeTrue())
	})

	It("returns false when neither condition is present", func() {
		obj := makeCRD([]map[string]interface{}{
			makeCondition("SomeOther", "True"),
		})
		Expect(controllers.IsCRDEstablished(obj)).To(BeFalse())
	})
})

var _ = Describe("getHelmUpgradeClient PostRenderStrategy", func() {
	It("sets PostRenderer and PostRenderStrategy when patches are present", func() {
		requestedChart := &configv1beta1.HelmChart{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			Options: &configv1beta1.HelmOptions{
				PostRenderStrategy: configv1beta1.PostRenderStrategySeparate,
			},
		}
		patches := []libsveltosv1beta1.Patch{
			{Patch: randomString()},
		}

		upgradeClient := controllers.GetHelmUpgradeClient(requestedChart, &action.Configuration{},
			&controllers.RegistryClientOptions{}, patches)

		Expect(upgradeClient.PostRenderer).ToNot(BeNil())
		Expect(upgradeClient.PostRenderStrategy).To(Equal(action.PostRenderStrategySeparate))
	})

	It("leaves PostRenderer and PostRenderStrategy unset when no patches are present", func() {
		requestedChart := &configv1beta1.HelmChart{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			Options: &configv1beta1.HelmOptions{
				PostRenderStrategy: configv1beta1.PostRenderStrategySeparate,
			},
		}

		upgradeClient := controllers.GetHelmUpgradeClient(requestedChart, &action.Configuration{},
			&controllers.RegistryClientOptions{}, nil)

		Expect(upgradeClient.PostRenderer).To(BeNil())
		Expect(upgradeClient.PostRenderStrategy).To(Equal(action.PostRenderStrategyCombined))
	})
})

var _ = Describe("getHelmUpgradeClient Force", func() {
	It("disables server-side apply and force conflicts when force is set", func() {
		requestedChart := &configv1beta1.HelmChart{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			Options: &configv1beta1.HelmOptions{
				UpgradeOptions: configv1beta1.HelmUpgradeOptions{
					Force: true,
				},
			},
		}

		upgradeClient := controllers.GetHelmUpgradeClient(requestedChart, &action.Configuration{},
			&controllers.RegistryClientOptions{}, nil)

		Expect(upgradeClient.ForceReplace).To(BeTrue())
		Expect(upgradeClient.ServerSideApply).To(Equal("false"))
		Expect(upgradeClient.ForceConflicts).To(BeFalse())
	})

	It("defaults to server-side apply with force conflicts when force is not set", func() {
		requestedChart := &configv1beta1.HelmChart{
			ReleaseName:      randomString(),
			ReleaseNamespace: randomString(),
			Options:          &configv1beta1.HelmOptions{},
		}

		upgradeClient := controllers.GetHelmUpgradeClient(requestedChart, &action.Configuration{},
			&controllers.RegistryClientOptions{}, nil)

		Expect(upgradeClient.ForceReplace).To(BeFalse())
		Expect(upgradeClient.ServerSideApply).To(Equal("true"))
		Expect(upgradeClient.ForceConflicts).To(BeTrue())
	})
})

var _ = Describe("locateChartWithTimeout", func() {
	It("returns the result once locateFn returns", func() {
		expectedPath := randomString()
		locateFn := func(chartName string, settings *cli.EnvSettings) (string, error) {
			return expectedPath, nil
		}

		cp, err := controllers.LocateChartWithTimeout(locateFn, randomString(), &cli.EnvSettings{})
		Expect(err).To(BeNil())
		Expect(cp).To(Equal(expectedPath))
	})

	It("propagates the error returned by locateFn", func() {
		expectedErr := errors.New(randomString())
		locateFn := func(chartName string, settings *cli.EnvSettings) (string, error) {
			return "", expectedErr
		}

		_, err := controllers.LocateChartWithTimeout(locateFn, randomString(), &cli.EnvSettings{})
		Expect(err).To(Equal(expectedErr))
	})
})
