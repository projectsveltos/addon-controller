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

package controllers

import (
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// metricNamespace prefixes every custom metric in this file, purely to avoid name collisions with
	// unrelated tools scraped by the same Prometheus instance. Deliberately a fixed literal, not derived
	// from getSveltosNamespace() (the Kubernetes namespace this component happens to be deployed into,
	// which is user-configurable per install): a metric name prefix should stay stable across every
	// install so dashboards/alerts built against it work the same way everywhere, regardless of which
	// Kubernetes namespace a given cluster chose to deploy addon-controller into.
	metricNamespace = "projectsveltos"

	metricClusterNameLabel      = "cluster_name"
	metricClusterNamespaceLabel = "cluster_namespace"
	metricClusterTypeLabel      = "cluster_type"
	metricFeatureLabel          = "feature"
	metricStatusLabel           = "status"
	metricProfileKindLabel      = "profile_kind"
	metricProfileNamespaceLabel = "profile_namespace"
	metricProfileNameLabel      = "profile_name"

	metricChartNameLabel        = "chart_name"
	metricReleaseNamespaceLabel = "release_namespace"
	metricReleaseNameLabel      = "release_name"

	statusSuccess = "success"
	statusFailure = "failure"
)

var (
	// reconcileDurationHistogram tracks how long it takes to program a feature (Resources, Helm,
	// Kustomize) on a workload cluster. Labeled by cluster and feature so it can be filtered down to
	// a single cluster, aggregated to a namespace, or averaged fleet-wide, and so each feature type
	// is distinguishable rather than collapsed into a "resources vs everything else" split.
	//
	// Not labeled by profile: this is observed from programDuration, which is invoked as a
	// libsveltos/lib/deployer.MetricHandler callback — a type shared with several other components
	// (healthcheck-manager, classifier, event-manager, etc.). That callback only carries cluster
	// identity and featureID, not the ClusterSummary/profile that requested the work, so adding a
	// profile label here would require changing the shared MetricHandler signature across all of
	// them. reconcileOutcomeCounter and driftCounter below get profile labels instead, since both
	// are recorded from addon-controller-local code that already has the full ClusterSummary object.
	reconcileDurationHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_duration_seconds",
			Help:      "Duration distribution of programming a feature (Resources, Helm, Kustomize) on a workload cluster",
			Buckets:   []float64{0.5, 1, 1.5, 2, 3, 5, 10, 30, 60, 90, 120, 180, 300, 600},
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricFeatureLabel},
	)

	// reconciliationCounter is not labeled by profile for the same reason as reconcileDurationHistogram
	// above: it is incremented from the same shared MetricHandler callback.
	reconciliationCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_operations_total",
			Help:      "Total number of reconcile operations for Helm, Resources, and Kustomization",
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricFeatureLabel},
	)

	// reconcileOutcomeCounter tracks terminal reconcile outcomes (success/failure), unlike
	// reconciliationCounter above which counts every attempt regardless of outcome. Incremented from
	// updateFeatureStatus, the one place that already knows whether a feature ended up Provisioned/Removed
	// (success) or Failed/FailedNonRetriable (failure), and has the full ClusterSummary object needed
	// to resolve the owning ClusterProfile/Profile.
	reconcileOutcomeCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_outcome_total",
			Help:      "Total number of terminal reconcile outcomes for Helm, Resources, and Kustomization, by outcome",
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricFeatureLabel,
			metricStatusLabel, metricProfileKindLabel, metricProfileNamespaceLabel, metricProfileNameLabel},
	)

	driftCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "total_drifts",
			Help:      "Total number of drifts for a given cluster indexed via type, namespace/name and feature id",
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricFeatureLabel,
			metricProfileKindLabel, metricProfileNamespaceLabel, metricProfileNameLabel},
	)

	// matchingClustersGauge tracks how many clusters currently match a given ClusterProfile/Profile's
	// selector. Set from ClusterProfileReconciler/ProfileReconciler right after they compute the
	// matching set, independent of any per-cluster reconcile activity.
	matchingClustersGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "matching_clusters",
			Help:      "Number of clusters currently matching a ClusterProfile/Profile's selector",
		},
		[]string{metricProfileKindLabel, metricProfileNamespaceLabel, metricProfileNameLabel},
	)

	// reconcileConsecutiveFailuresGauge mirrors ClusterSummary.status.featureSummaries[].consecutiveFailures.
	// A gauge, not a counter: it resets to 0 on the next success, so it answers "is this stuck failing right
	// now" directly, unlike reconcileOutcomeCounter which only accumulates and never reflects recovery.
	reconcileConsecutiveFailuresGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_consecutive_failures",
			Help:      "Number of consecutive terminal failures for a feature on a cluster; reset to 0 on success",
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricFeatureLabel,
			metricProfileKindLabel, metricProfileNamespaceLabel, metricProfileNameLabel},
	)

	// reconcileLastSuccessTimestampGauge records when a feature last reached a successful terminal state
	// (Provisioned/Removed). Unlike reconcileConsecutiveFailuresGauge, this only ever moves forward on
	// success and is left untouched on failure, so it answers "how long has it been since this last
	// worked" even for something that fails intermittently rather than in a tight consecutive streak.
	reconcileLastSuccessTimestampGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "reconcile_last_success_timestamp_seconds",
			Help:      "Unix timestamp of the last successful (Provisioned/Removed) terminal outcome for a feature on a cluster",
		},
		[]string{metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel, metricFeatureLabel,
			metricProfileKindLabel, metricProfileNamespaceLabel, metricProfileNameLabel},
	)

	// outdatedHelmChartGauge is 1 when a ClusterSummary's managed HelmChart release is behind
	// the latest version published upstream. Reset() once per checkOutdatedHelmCharts pass and
	// re-Set(1) only for entries found outdated that pass, so stale series (charts that became
	// up to date, or are no longer referenced) don't linger. Version strings are deliberately
	// not labels here (high cardinality/churn) — they live only on HelmChartSummary.
	outdatedHelmChartGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "outdated_helm_chart",
			Help:      "1 if a ClusterSummary's managed HelmChart release is behind the latest published upstream version",
		},
		[]string{metricProfileKindLabel, metricProfileNamespaceLabel, metricProfileNameLabel,
			metricClusterTypeLabel, metricClusterNamespaceLabel, metricClusterNameLabel,
			metricChartNameLabel, metricReleaseNamespaceLabel, metricReleaseNameLabel},
	)

	// outdatedHelmChartCheckLastRunTimestampGauge records when checkOutdatedHelmCharts last
	// completed a full pass, regardless of whether any chart was found outdated. Unlike
	// outdatedHelmChartGauge (which describes chart state), this describes the checker's own
	// health: if it stops advancing, the checker is stuck or has stopped running, which is
	// indistinguishable from "everything is up to date" by looking at outdatedHelmChartGauge
	// alone.
	outdatedHelmChartCheckLastRunTimestampGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Name:      "outdated_helm_chart_check_last_run_timestamp_seconds",
			Help:      "Unix timestamp of the last completed pass of the periodic outdated-Helm-chart checker",
		},
	)

	// outdatedHelmChartCheckFailuresCounter counts chart keys skipped in a
	// checkOutdatedHelmCharts pass because their upstream repository/registry could not be
	// queried (unreachable, auth failure, timeout, chart not found, etc). Not labeled by chart:
	// this is meant as a coarse "is the checker healthy" signal, not a per-chart diagnostic —
	// per-chart failures are logged instead.
	outdatedHelmChartCheckFailuresCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "outdated_helm_chart_check_failures_total",
			Help:      "Total number of chart keys skipped by the outdated-Helm-chart checker due to a fetch failure or timeout",
		},
	)
)

//nolint:gochecknoinits // forced pattern, can't workaround
func init() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(reconcileDurationHistogram, reconciliationCounter, reconcileOutcomeCounter,
		driftCounter, matchingClustersGauge, reconcileConsecutiveFailuresGauge, reconcileLastSuccessTimestampGauge,
		outdatedHelmChartGauge, outdatedHelmChartCheckLastRunTimestampGauge, outdatedHelmChartCheckFailuresCounter)
}

func programDuration(elapsed time.Duration, clusterNamespace, clusterName, featureID string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) {

	reconcileDurationHistogram.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricFeatureLabel:          featureID,
	}).Observe(elapsed.Seconds())

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("recorded duration for %s/%s %s: %s",
		clusterNamespace, clusterName, featureID, elapsed))
}

func programDeployMetrics(elapsed time.Duration, clusterNamespace, clusterName, featureID string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) {

	programDuration(elapsed, clusterNamespace, clusterName, featureID, clusterType, logger)
	trackReconciliation(clusterNamespace, clusterName, featureID, clusterType, logger)
}

func trackReconciliation(clusterNamespace, clusterName, featureID string, clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) {
	reconciliationCounter.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricFeatureLabel:          featureID,
	}).Inc()

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking reconciliation for %s %s/%s %s",
		clusterType, clusterNamespace, clusterName, featureID))
}

// getProfileLabels resolves the ClusterProfile/Profile owning clusterSummary into the three profile
// labels shared by reconcileOutcomeCounter, driftCounter, and matchingClustersGauge. Returns empty
// strings if the owner can't be resolved (e.g. a transient state before OwnerReferences are set) rather
// than failing metric recording.
func getProfileLabels(clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) (profileKind, profileNamespace, profileName string) {
	ref, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		logger.V(logs.LogVerbose).Info(fmt.Sprintf("failed to get profile owner reference: %s", err))
		return "", "", ""
	}

	if ref.Kind == configv1beta1.ProfileKind {
		// Profile is namespaced; an owner reference is always same-namespace as the owned object.
		return ref.Kind, clusterSummary.Namespace, ref.Name
	}

	return ref.Kind, "", ref.Name
}

// trackReconcileOutcome records a terminal reconcile outcome for a feature. success is true for
// FeatureStatusProvisioned/FeatureStatusRemoved, false for FeatureStatusFailed/FeatureStatusFailedNonRetriable.
// Non-terminal statuses (Provisioning, Removing, AgentRemoving) must not call this.
func trackReconcileOutcome(clusterNamespace, clusterName, featureID string, clusterType libsveltosv1beta1.ClusterType,
	success bool, profileKind, profileNamespace, profileName string, logger logr.Logger) {

	status := statusFailure
	if success {
		status = statusSuccess
	}

	reconcileOutcomeCounter.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricFeatureLabel:          featureID,
		metricStatusLabel:           status,
		metricProfileKindLabel:      profileKind,
		metricProfileNamespaceLabel: profileNamespace,
		metricProfileNameLabel:      profileName,
	}).Inc()

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking reconcile outcome for %s %s/%s %s: %s",
		clusterType, clusterNamespace, clusterName, featureID, status))
}

func trackDrifts(clusterNamespace, clusterName, featureID, clusterType, profileKind, profileNamespace, profileName string,
	logger logr.Logger) {

	driftCounter.With(prometheus.Labels{
		metricClusterTypeLabel:      clusterType,
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricFeatureLabel:          featureID,
		metricProfileKindLabel:      profileKind,
		metricProfileNamespaceLabel: profileNamespace,
		metricProfileNameLabel:      profileName,
	}).Inc()

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking drifts for %s %s/%s %s",
		clusterType, clusterNamespace, clusterName, featureID))
}

// trackMatchingClusters records how many clusters currently match a ClusterProfile/Profile's selector.
func trackMatchingClusters(profileKind, profileNamespace, profileName string, count int, logger logr.Logger) {
	matchingClustersGauge.With(prometheus.Labels{
		metricProfileKindLabel:      profileKind,
		metricProfileNamespaceLabel: profileNamespace,
		metricProfileNameLabel:      profileName,
	}).Set(float64(count))

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking matching clusters for %s %s/%s: %d",
		profileKind, profileNamespace, profileName, count))
}

// trackConsecutiveFailures records the current consecutive-failure streak for a feature on a cluster,
// read from ClusterSummary.status.featureSummaries[].consecutiveFailures after SetFeatureStatus has
// updated it. Called on every terminal outcome (success resets it to 0, failure increments it).
func trackConsecutiveFailures(clusterNamespace, clusterName, featureID string, clusterType libsveltosv1beta1.ClusterType,
	consecutiveFailures uint, profileKind, profileNamespace, profileName string, logger logr.Logger) {

	reconcileConsecutiveFailuresGauge.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricFeatureLabel:          featureID,
		metricProfileKindLabel:      profileKind,
		metricProfileNamespaceLabel: profileNamespace,
		metricProfileNameLabel:      profileName,
	}).Set(float64(consecutiveFailures))

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking consecutive failures for %s %s/%s %s: %d",
		clusterType, clusterNamespace, clusterName, featureID, consecutiveFailures))
}

// trackOutdatedHelmChart records that a ClusterSummary's managed HelmChart release currently
// has a newer version published upstream than its deployed ChartVersion. Only called when an
// outdated version was actually found. Pairs with clearOutdatedHelmChart, which
// checkOutdatedHelmCharts calls instead of resetting the whole gauge, so a chart's exposed
// state only ever changes when the real answer changes (see reconcileOutdatedHelmChartMetric).
func trackOutdatedHelmChart(profileKind, profileNamespace, profileName, clusterType, clusterNamespace, clusterName,
	chartName, releaseNamespace, releaseName string, logger logr.Logger) {

	outdatedHelmChartGauge.With(prometheus.Labels{
		metricProfileKindLabel:      profileKind,
		metricProfileNamespaceLabel: profileNamespace,
		metricProfileNameLabel:      profileName,
		metricClusterTypeLabel:      clusterType,
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricChartNameLabel:        chartName,
		metricReleaseNamespaceLabel: releaseNamespace,
		metricReleaseNameLabel:      releaseName,
	}).Set(1)

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("tracking outdated helm chart %s (release %s/%s)",
		chartName, releaseNamespace, releaseName))
}

// clearOutdatedHelmChart removes the gauge series for a chart confirmed, this pass, to no
// longer be outdated (or no longer referenced). Deletes the series rather than Set(0), matching
// outdatedHelmChartGauge's "absent means not outdated" convention.
func clearOutdatedHelmChart(profileKind, profileNamespace, profileName, clusterType, clusterNamespace, clusterName,
	chartName, releaseNamespace, releaseName string) {

	outdatedHelmChartGauge.Delete(prometheus.Labels{
		metricProfileKindLabel:      profileKind,
		metricProfileNamespaceLabel: profileNamespace,
		metricProfileNameLabel:      profileName,
		metricClusterTypeLabel:      clusterType,
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricChartNameLabel:        chartName,
		metricReleaseNamespaceLabel: releaseNamespace,
		metricReleaseNameLabel:      releaseName,
	})
}

// trackHelmChartCheckCompleted records that a checkOutdatedHelmCharts pass just finished,
// regardless of whether any individual chart fetch failed. Call once per pass.
func trackHelmChartCheckCompleted(logger logr.Logger) {
	outdatedHelmChartCheckLastRunTimestampGauge.Set(float64(time.Now().Unix()))

	logger.V(logs.LogVerbose).Info("recorded completed outdated helm chart check pass")
}

// recordHelmChartCheckFailure increments the coarse checker-health failure counter. Call once
// per chart key skipped due to a fetch failure or timeout, not once per affected ClusterSummary.
func recordHelmChartCheckFailure() {
	outdatedHelmChartCheckFailuresCounter.Inc()
}

// trackLastSuccess records the timestamp of the most recent successful (Provisioned/Removed) terminal
// outcome for a feature on a cluster. Only called from the success branches of updateFeatureStatus.
func trackLastSuccess(clusterNamespace, clusterName, featureID string, clusterType libsveltosv1beta1.ClusterType,
	timestamp time.Time, profileKind, profileNamespace, profileName string, logger logr.Logger) {

	reconcileLastSuccessTimestampGauge.With(prometheus.Labels{
		metricClusterTypeLabel:      string(clusterType),
		metricClusterNamespaceLabel: clusterNamespace,
		metricClusterNameLabel:      clusterName,
		metricFeatureLabel:          featureID,
		metricProfileKindLabel:      profileKind,
		metricProfileNamespaceLabel: profileNamespace,
		metricProfileNameLabel:      profileName,
	}).Set(float64(timestamp.Unix()))

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking last success for %s %s/%s %s: %s",
		clusterType, clusterNamespace, clusterName, featureID, timestamp))
}
