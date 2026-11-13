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
	"sync"
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

// maxInitWorkers is the upper bound on the number of ClusterSummary instances processed
// concurrently by the init container. Every worker issues several calls to the management
// cluster API server and the client is rate limited (kube-api-qps), so more workers than this
// would mostly end up waiting on the rate limiter.
const (
	maxInitWorkers = 10
)

func Initialization(ctx context.Context, config *rest.Config,
	scheme *runtime.Scheme, shardKey string, logger logr.Logger) {

	logger.V(logs.LogInfo).Info("perform init work")
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get client")
		return
	}

	updateHelmHashes(ctx, NewInitCachingClient(c), shardKey, logger)
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

	runConcurrently(ctx, len(clusterSummaries.Items), func(i int) {
		cs := &clusterSummaries.Items[i]
		l := logger.WithValues("clusterSummary", fmt.Sprintf("%s/%s", cs.Namespace, cs.Name))
		err := updateClusterSummaryHelmHashes(ctx, directClient, shardKey, cs, l)
		if err != nil {
			l.V(logs.LogInfo).Error(err, "failed to update helm and helm value hashes")
		} else {
			l.V(logs.LogInfo).Info("updated helm and helm value hashes")
		}
	})
}

// getInitWorkerCount returns the number of workers to use to process numItems instances:
// one worker per instance, up to maxInitWorkers.
func getInitWorkerCount(numItems int) int {
	if numItems < maxInitWorkers {
		return numItems
	}
	return maxInitWorkers
}

// runConcurrently calls process once for each index in [0, count), using up to
// getInitWorkerCount(count) goroutines. It returns when all calls in progress are done.
// If ctx is canceled, indexes not yet handed to a worker are skipped.
func runConcurrently(ctx context.Context, count int, process func(index int)) {
	indexes := make(chan int)

	var wg sync.WaitGroup
	for w := 0; w < getInitWorkerCount(count); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indexes {
				process(i)
			}
		}()
	}

	sendIndexes(ctx, count, indexes)
	close(indexes)
	wg.Wait()
}

// sendIndexes sends [0, count) to indexes, stopping early if ctx is canceled.
func sendIndexes(ctx context.Context, count int, indexes chan<- int) {
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return
		case indexes <- i:
		}
	}
}

func updateClusterSummaryHelmHashes(ctx context.Context, directClient client.Client,
	shardKey string, clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	if len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts) == 0 {
		return nil
	}

	// The management cluster objects read to compute the hashes (the Cluster, the ConfigMaps and
	// Secrets referenced by the charts, the templateResourceRefs resources) are fetched once per
	// ClusterSummary: they are memoized in hashCtx. ctx is used instead for the ClusterSummary itself,
	// which must always be read fresh, as the update below can conflict and be retried.
	hashCtx := withInitPassCache(ctx)

	isMatch, err := isClusterAShardMatch(hashCtx, directClient, shardKey, clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to verify is shard is a match")
		isMatch = true
	}
	if !isMatch {
		return nil
	}

	mgmtResources, err := collectTemplateResourceRefs(hashCtx, clusterSummary)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to collect templateResourceRefs")
		return err
	}

	clusterObjects, err := fetchClusterObjects(hashCtx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to fetch resources")
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

		helmHash, err := helmHash(hashCtx, directClient, currentClusterSummary, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get helm configuration hash")
			return err
		}
		setHelmHash(currentClusterSummary, helmHash)

		// PatchesHash is one value for the whole ClusterProfileSpec.Patches list, shared across
		// every chart (same as buildReferencedHelmReleaseSummaries), so it's computed once here,
		// not per chart.
		patchesHash, err := getHelmChartPatchesHash(hashCtx, currentClusterSummary, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get helm patches hash")
			return err
		}
		setHelmChartsPatchesHash(currentClusterSummary, patchesHash)

		for i := range currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			helmChart := &currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			innerDCtx := &deploymentContext{
				clusterSummary: currentClusterSummary,
				clusterObjects: clusterObjects,
				mgmtResources:  mgmtResources,
			}
			instantiatedChart, err := getInstantiatedChartIdentity(hashCtx, innerDCtx, helmChart, logger)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to get instantiated chart")
				return err
			}
			helmChartValueHash, err := getHelmChartValuesHash(hashCtx, directClient, instantiatedChart,
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

// setHelmChartsPatchesHash stores patchesHash on every HelmReleaseSummaries entry, mirroring
// buildReferencedHelmReleaseSummaries: PatchesHash is one value for the whole ClusterProfileSpec.Patches
// list, not per chart, so every chart gets the same value.
func setHelmChartsPatchesHash(clusterSummary *configv1beta1.ClusterSummary, patchesHash []byte) {
	for i := range clusterSummary.Status.HelmReleaseSummaries {
		clusterSummary.Status.HelmReleaseSummaries[i].PatchesHash = patchesHash
	}
}
