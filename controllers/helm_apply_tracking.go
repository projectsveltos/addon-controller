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

package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// In pull mode every Helm chart of a ClusterSummary is rendered and staged on every reconcile,
// and sveltos-applier applies every bundle in the ConfigurationGroup. So a change to one chart
// (or drift on one chart) would reapply all of them.
//
// Push mode only upgrades a chart if it changed or drifted. Pull mode gets the same behavior by
// telling sveltos-applier, per chart, not to apply a bundle (ConfigurationBundle.Spec.SkipApply)
// when the chart is known to be deployed already. A chart is known to be deployed when:
//   - sveltos-applier confirmed applying exactly the same content and settings before
//     (HelmChartSummary.AppliedContentHash matches the hash about to be staged);
//   - drift-detection has not reported the chart (HelmChartSummary.NeedsRedeploy);
//   - nothing staged earlier is still waiting for confirmation (HelmChartSummary.StagedContentHash).
//
// In every other case, including when nothing is recorded yet (first reconciliation, controller
// upgrade), the chart is applied. Skipping is only ever granted with proof.

// chartApplyInputs is everything that decides what sveltos-applier does when it applies a chart.
// Two applies with the same inputs leave the managed cluster in the same state, so a change in
// any of them must not be skipped.
type chartApplyInputs struct {
	// ResourcesHash is the hash of the rendered resources
	ResourcesHash string
	// Tier is stamped on the deployed resources and drives conflict resolution
	Tier int32
	// Timeout and SkipNamespaceCreation are the bundle settings the applier honors
	Timeout               time.Duration
	SkipNamespaceCreation bool
	ChartVersion          string
}

// getChartApplyHash returns the hash of everything that decides what applying the chart does.
func getChartApplyHash(clusterSummary *configv1beta1.ClusterSummary, chart *configv1beta1.HelmChart,
	chartVersion string, resources []*unstructured.Unstructured) ([]byte, error) {

	resourcesHash, err := getResourcesHash(resources)
	if err != nil {
		return nil, err
	}

	inputs := chartApplyInputs{
		ResourcesHash:         resourcesHash,
		Tier:                  clusterSummary.Spec.ClusterProfileSpec.Tier,
		Timeout:               getTimeoutValue(chart.Options).Duration,
		SkipNamespaceCreation: !getCreateNamespaceHelmValue(chart.Options),
		ChartVersion:          chartVersion,
	}

	data, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}

	hash := sha256.Sum256(data)
	return hash[:], nil
}

// getResourcesHash hashes the resources independently of their order.
func getResourcesHash(resources []*unstructured.Unstructured) (string, error) {
	hashes := make([]string, len(resources))
	for i := range resources {
		data, err := json.Marshal(resources[i].Object)
		if err != nil {
			return "", err
		}
		hash := sha256.Sum256(data)
		hashes[i] = hex.EncodeToString(hash[:])
	}
	sort.Strings(hashes)

	overall := sha256.New()
	for i := range hashes {
		overall.Write([]byte(hashes[i]))
	}
	return hex.EncodeToString(overall.Sum(nil)), nil
}

// isApplyTracked returns true if a chart being staged is a deploy whose confirmed state can be
// recorded. DryRun applies nothing, and an uninstall is not a deploy.
func isApplyTracked(clusterSummary *configv1beta1.ClusterSummary, chart *configv1beta1.HelmChart,
	isUninstall bool) bool {

	return !isUninstall &&
		chart.HelmChartAction != configv1beta1.HelmChartActionUninstall &&
		clusterSummary.DeletionTimestamp.IsZero() &&
		clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun
}

// shouldSkipApply returns true if sveltos-applier can be told not to apply the chart.
// applyHash comes from getChartApplyHash.
func shouldSkipApply(clusterSummary *configv1beta1.ClusterSummary, chart *configv1beta1.HelmChart,
	applyHash []byte) bool {

	summary := getHelmChartSummary(chart, clusterSummary)
	if summary == nil {
		return false
	}

	if summary.NeedsRedeploy || len(summary.StagedContentHash) != 0 || len(summary.AppliedContentHash) == 0 {
		return false
	}

	return bytes.Equal(summary.AppliedContentHash, applyHash)
}

// getHelmChartSummary returns the entry of clusterSummary.Status.HelmReleaseSummaries for the
// chart, or nil if there is none.
func getHelmChartSummary(chart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary) *configv1beta1.HelmChartSummary {

	for i := range clusterSummary.Status.HelmReleaseSummaries {
		summary := &clusterSummary.Status.HelmReleaseSummaries[i]
		if summary.ReleaseName == chart.ReleaseName && summary.ReleaseNamespace == chart.ReleaseNamespace {
			return summary
		}
	}

	return nil
}

// recordHelmChartApply updates the chart summary right after the chart was deployed (push mode) or
// staged for sveltos-applier (pull mode).
//
// In push mode a successful deploy is the end of the story, so a drift flag is cleared.
// In pull mode staging is not an apply, sveltos-applier confirms later. So what was staged is
// recorded to be confirmed, and a drift flag is cleared only if the chart is being applied: a chart
// skipped in this pass keeps it, as drift may have been reported after the decision was taken.
func recordHelmChartApply(summary *configv1beta1.HelmChartSummary, release *releaseInfo) {
	if release != nil && release.skipApply {
		return
	}

	summary.NeedsRedeploy = false

	if release != nil && len(release.applyHash) != 0 {
		summary.StagedContentHash = release.applyHash
	}
}

// confirmAppliedHelmCharts is called once sveltos-applier reports the Helm ConfigurationGroup as
// provisioned. Everything staged for the charts has been applied, so it becomes the applied state.
func confirmAppliedHelmCharts(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) error {

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err := c.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		changed := false
		for i := range currentClusterSummary.Status.HelmReleaseSummaries {
			summary := &currentClusterSummary.Status.HelmReleaseSummaries[i]
			if len(summary.StagedContentHash) == 0 {
				continue
			}
			summary.AppliedContentHash = summary.StagedContentHash
			summary.StagedContentHash = nil
			changed = true
		}

		if !changed {
			return nil
		}

		logger.V(logs.LogDebug).Info("recording helm charts confirmed as applied")
		return c.Status().Update(ctx, currentClusterSummary)
	})
}
