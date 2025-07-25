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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

var (
	programResourceDurationHistogram = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "projectsveltos",
			Name:      "program_resources_time_seconds",
			Help:      "Program Resources on a workload cluster duration distribution",
			Buckets:   []float64{0.5, 1, 1.5, 2, 3, 5, 10, 30, 60, 90, 120},
		},
	)

	programChartDurationHistogram = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "projectsveltos",
			Name:      "program_charts_time_seconds",
			Help:      "Program Helm charts on a workload cluster duration distribution",
			Buckets:   []float64{0.5, 1, 1.5, 2, 3, 5, 10, 30, 60, 90, 120},
		},
	)

	reconciliationCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "projectsveltos",
			Name:      "reconcile_operations_total",
			Help:      "Total number of reconcile operations for Helm, Resources, and Kustomization",
		},
		[]string{"cluster_type", "cluster_namespace", "cluster_name", "feature"},
	)

	driftCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "projectsveltos",
			Name:      "total_drifts",
			Help:      "Total number of drifts for a given cluster indexed via type, namespace/name and feature id",
		},
		[]string{"cluster_type", "cluster_namespace", "cluster_name", "feature"},
	)
)

//nolint:gochecknoinits // forced pattern, can't workaround
func init() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(programResourceDurationHistogram, programChartDurationHistogram, reconciliationCounter, driftCounter)
}

func newResourceHistogram(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) prometheus.Histogram {

	clusterInfo := strings.ReplaceAll(fmt.Sprintf("%s_%s_%s", clusterType, clusterNamespace, clusterName), "-", "_")
	histogram := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: clusterInfo,
			Name:      "program_resources_time_seconds",
			Help:      "Program Resources on a workload cluster duration distribution",
			Buckets:   []float64{0.5, 1, 1.5, 2, 3, 5, 10, 30, 60, 90, 120},
		},
	)

	err := metrics.Registry.Register(histogram)
	if err != nil {
		var registrationError *prometheus.AlreadyRegisteredError
		ok := errors.As(err, &registrationError)
		if ok {
			_, ok = registrationError.ExistingCollector.(prometheus.Histogram)
			if ok {
				return registrationError.ExistingCollector.(prometheus.Histogram)
			}
			logCollectorError(err, logger)
			return nil
		}
		logCollectorError(err, logger)
		return nil
	}

	return histogram
}

func newChartHistogram(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) prometheus.Histogram {

	clusterInfo := strings.ReplaceAll(fmt.Sprintf("%s_%s_%s", clusterType, clusterNamespace, clusterName), "-", "_")
	histogram := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: clusterInfo,
			Name:      "program_charts_time_seconds",
			Help:      "Program Helm Charts on a workload cluster duration distribution",
			Buckets:   []float64{0.5, 1, 1.5, 2, 3, 5, 10, 30, 60, 90, 120},
		},
	)

	err := metrics.Registry.Register(histogram)
	if err != nil {
		var registrationError *prometheus.AlreadyRegisteredError
		ok := errors.As(err, &registrationError)
		if ok {
			_, ok = registrationError.ExistingCollector.(prometheus.Histogram)
			if ok {
				return registrationError.ExistingCollector.(prometheus.Histogram)
			}
			logCollectorError(err, logger)
			return nil
		}
		logCollectorError(err, logger)
		return nil
	}

	return histogram
}

func logCollectorError(err error, logger logr.Logger) {
	logger.V(logs.LogVerbose).Info(fmt.Sprintf("failed to register collector: %s", err))
}

func programDuration(elapsed time.Duration, clusterNamespace, clusterName, featureID string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) {

	if featureID == string(libsveltosv1beta1.FeatureResources) {
		programResourceDurationHistogram.Observe(elapsed.Seconds())
		clusterHistogram := newResourceHistogram(clusterNamespace, clusterName, clusterType, logger)
		if clusterHistogram != nil {
			logger.V(logs.LogVerbose).Info(fmt.Sprintf("register data for %s/%s %s",
				clusterNamespace, clusterName, featureID))
			clusterHistogram.Observe(elapsed.Seconds())
		}
	} else {
		programChartDurationHistogram.Observe(elapsed.Seconds())
		clusterHistogram := newChartHistogram(clusterNamespace, clusterName, clusterType, logger)
		if clusterHistogram != nil {
			logger.V(logs.LogVerbose).Info(fmt.Sprintf("register data for %s/%s %s",
				clusterNamespace, clusterName, featureID))
			clusterHistogram.Observe(elapsed.Seconds())
		}
	}
}

func programDeployMetrics(elapsed time.Duration, clusterNamespace, clusterName, featureID string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) {

	programDuration(elapsed, clusterNamespace, clusterName, featureID, clusterType, logger)
	trackReconciliation(clusterNamespace, clusterName, featureID, clusterType, logger)
}

func trackReconciliation(clusterNamespace, clusterName, featureID string, clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) {
	reconciliationCounter.With(prometheus.Labels{
		"cluster_type":      string(clusterType),
		"cluster_namespace": clusterNamespace,
		"cluster_name":      clusterName,
		"feature":           featureID,
	}).Inc()

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking reconciliation for %s %s/%s %s",
		clusterType, clusterNamespace, clusterName, featureID))
}

func trackDrifts(clusterNamespace, clusterName, featureID, clusterType string, logger logr.Logger) {
	driftCounter.With(prometheus.Labels{
		"cluster_type":      clusterType,
		"cluster_namespace": clusterNamespace,
		"cluster_name":      clusterName,
		"feature":           featureID,
	}).Inc()

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("Tracking drifts for %s %s/%s %s",
		clusterType, clusterNamespace, clusterName, featureID))
}
