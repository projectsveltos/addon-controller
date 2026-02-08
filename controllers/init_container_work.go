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
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func Initialization(ctx context.Context, config *rest.Config,
	scheme *runtime.Scheme, shardKey string, logger logr.Logger) {

	logger.V(logs.LogInfo).Info("perform init work")
	directClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get client")
		return
	}

	updateHelmHashes(ctx, directClient, shardKey, logger)
}

func updateHelmHashes(ctx context.Context, directClient client.Client,
	shardKey string, logger logr.Logger) {

	clusterSummaries := &configv1beta1.ClusterSummaryList{}
	for {
		err := directClient.List(ctx, clusterSummaries)
		if err == nil {
			break
		}

		logger.V(logs.LogInfo).Error(err, "failed to get clusterSummary instances, retrying...")

		select {
		case <-ctx.Done():
			logger.Info("stopping updateHelmHashes: context canceled during retry")
			return
		case <-time.After(time.Second):
			continue
		}
	}

	for i := range clusterSummaries.Items {
		cs := &clusterSummaries.Items[i]
		l := logger.WithValues("clusterSummary", fmt.Sprintf("%s/%s", cs.Namespace, cs.Name))
		err := updateClusterSummaryHelmHashes(ctx, directClient, shardKey, cs, l)
		if err != nil {
			l.V(logs.LogInfo).Error(err, "failed to update helm and helm value hashes")
		} else {
			l.V(logs.LogInfo).Info("updated helm and helm value hashes")
		}
	}
}

func updateClusterSummaryHelmHashes(ctx context.Context, directClient client.Client,
	shardKey string, clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	if len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts) == 0 {
		return nil
	}

	isMatch, err := isClusterAShardMatch(ctx, directClient, shardKey, clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to verify is shard is a match")
		isMatch = true
	}
	if !isMatch {
		return nil
	}

	mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to collect templateResourceRefs")
		return err
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err := directClient.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)
		if err != nil {
			return err
		}

		helmHash, err := helmHash(ctx, directClient, currentClusterSummary, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get helm configuration hash")
			return err
		}
		setHelmHash(currentClusterSummary, helmHash)

		for i := range currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			helmChart := &currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			instantiatedChart, err := getInstantiatedChart(ctx, currentClusterSummary, helmChart,
				mgmtResources, logger)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to get instantiated chart")
				return err
			}
			helmChartValueHash, err := getHelmChartValuesHash(ctx, directClient, instantiatedChart,
				currentClusterSummary, mgmtResources, logger)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to get helm chart value hash")
				return err
			}
			setHelmChartValueHash(currentClusterSummary, instantiatedChart, helmChartValueHash)
		}

		return directClient.Status().Update(ctx, currentClusterSummary)
	})

	return err
}

func setHelmHash(clusterSummary *configv1beta1.ClusterSummary, helmHash []byte) {
	for i := range clusterSummary.Status.FeatureSummaries {
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
			clusterSummary.Status.FeatureSummaries[i].Hash = helmHash
		}
	}
}

func setHelmChartValueHash(clusterSummary *configv1beta1.ClusterSummary, helmChart *configv1beta1.HelmChart,
	helmChartValueHash []byte) {

	for i := range clusterSummary.Status.HelmReleaseSummaries {
		rs := &clusterSummary.Status.HelmReleaseSummaries[i]
		if rs.ReleaseName == helmChart.ReleaseName &&
			rs.ReleaseNamespace == helmChart.ReleaseNamespace {

			rs.ValuesHash = helmChartValueHash
		}
	}
}
