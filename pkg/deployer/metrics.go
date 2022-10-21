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

package deployer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
)

var (
	programResourceDurationHistogram = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "projectsveltos",
			Name:      "program_resources_time_seconds",
			Help:      "Program Resources on a workload cluster duration distribution",
			Buckets:   []float64{1, 10, 30, 60, 120, 180, 240},
		},
	)

	programChartDurationHistogram = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "projectsveltos",
			Name:      "program_charts_time_seconds",
			Help:      "Program Helm charts on a workload cluster duration distribution",
			Buckets:   []float64{1, 10, 30, 60, 120, 180, 240},
		},
	)
)

//nolint:gochecknoinits // forced pattern, can't workaround
func init() {
	// Register custom metrics with the global prometheus registry
	metrics.Registry.MustRegister(programResourceDurationHistogram, programChartDurationHistogram)
}

func newResourceHistogram(clusterNamespace, clusterName string,
	logger logr.Logger) prometheus.Histogram {

	clusterInfo := strings.ReplaceAll(fmt.Sprintf("%s_%s", clusterNamespace, clusterName), "-", "_")
	histogram := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: clusterInfo,
			Name:      "program_resources_time_seconds",
			Help:      "Program Resources on a workload cluster duration distribution",
			Buckets:   []float64{1, 10, 30, 60, 120, 180, 240},
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

func newChartHistogram(clusterNamespace, clusterName string,
	logger logr.Logger) prometheus.Histogram {

	clusterInfo := strings.ReplaceAll(fmt.Sprintf("%s_%s", clusterNamespace, clusterName), "-", "_")
	histogram := prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: clusterInfo,
			Name:      "program_resources_time_seconds",
			Help:      "Program Helm Charts on a workload cluster duration distribution",
			Buckets:   []float64{1, 10, 30, 60, 120, 180, 240},
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
	logger.V(logs.LogInfo).Info(fmt.Sprint("failed to register collector: %w", err))
}
