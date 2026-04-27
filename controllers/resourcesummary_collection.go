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

package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

var (
	// resourceSummaryInstalledCache records clusters where the ResourceSummary CRD is
	// confirmed installed. Only positive results are cached: once drift-detection is
	// deployed it is never removed while the controller is running, so there is no need
	// to re-check. Negative results are not cached so we keep checking until installation
	// is complete.
	resourceSummaryInstalledCacheMu sync.RWMutex
	resourceSummaryInstalledCache   = make(map[corev1.ObjectReference]bool)
)

// Periodically collects ResourceSummaries from each CAPI/Sveltos cluster.
func collectAndProcessResourceSummaries(ctx context.Context, c client.Client, shardkey, version string,
	logger logr.Logger) {

	const interval = 10 * time.Second

	for {
		time.Sleep(interval)

		logger.V(logs.LogVerbose).Info("collecting ResourceSummaries")
		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", getCAPIOnboardAnnotation(),
			shardkey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to get clusters")
			continue
		}

		clustersWithDD, err := getListOfClusterWithDriftDetectionDeployed(ctx, c)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to collect clusters with drift detection")
			continue
		}

		// In agentless mode all ResourceSummaries live in the management cluster.
		// A single List call covers every cluster, avoiding N per-cluster API calls.
		// skipCollecting is also only invoked for clusters that actually have changes.
		if getAgentInMgmtCluster() {
			if err := collectAndProcessAllResourceSummaries(ctx, c, clusterList, clustersWithDD, logger); err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to collect ResourceSummaries")
			}
			continue
		}

		// Intersect clusterList (shard-filtered) with clustersWithDD. clustersWithDD is
		// built from all ClusterProfiles/Profiles without shard awareness, so it can
		// contain clusters owned by other addon-controller shards. Only process clusters
		// that belong to this shard AND have drift detection deployed.
		var shardLocalDDClusters []corev1.ObjectReference
		for i := range clusterList {
			if _, ok := clustersWithDD[clusterList[i]]; ok {
				shardLocalDDClusters = append(shardLocalDDClusters, clusterList[i])
			}
		}

		if err = processShardLocalDDClusters(ctx, c, shardLocalDDClusters, version, logger); err != nil {
			return
		}
	}
}

// collectAndProcessAllResourceSummaries is used in agentless mode. It fetches all
// ResourceSummaries from the management cluster in a single List call, groups the
// changed ones by cluster, and processes only those clusters — skipping the N
// per-cluster List calls that the standard path would otherwise make.
func collectAndProcessAllResourceSummaries(ctx context.Context, c client.Client,
	clusterList []corev1.ObjectReference, clustersWithDD map[corev1.ObjectReference]bool,
	logger logr.Logger) error {

	// Build a lookup of shard-local clusters that have drift detection deployed,
	// keyed by namespace/name/clusterType to correctly distinguish a SveltosCluster
	// from a CAPI Cluster that share the same namespace and name.
	type clusterKey struct{ ns, name, clusterType string }
	localClustersWithDD := make(map[clusterKey]corev1.ObjectReference, len(clusterList))
	for i := range clusterList {
		ref := clusterList[i]
		if _, ok := clustersWithDD[ref]; ok {
			ct := strings.ToLower(string(clusterproxy.GetClusterType(&ref)))
			localClustersWithDD[clusterKey{ref.Namespace, ref.Name, ct}] = ref
		}
	}

	rsList := libsveltosv1beta1.ResourceSummaryList{}
	if err := getManagementClusterClient().List(ctx, &rsList); err != nil {
		return err
	}

	// Group changed ResourceSummaries by cluster.
	changedByCluster := make(map[clusterKey][]*libsveltosv1beta1.ResourceSummary)
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !rs.DeletionTimestamp.IsZero() {
			continue
		}
		if !rs.Status.ResourcesChanged && !rs.Status.HelmResourcesChanged && !rs.Status.KustomizeResourcesChanged {
			continue
		}
		clusterNamespace, ok := getClusterSummaryNamespaceFromResourceSummary(rs, logger)
		if !ok {
			continue
		}
		if rs.Labels == nil {
			continue
		}
		clusterName := rs.Labels[sveltos_upgrade.ClusterNameLabel]
		clusterType := rs.Labels[sveltos_upgrade.ClusterTypeLabel]
		if clusterName == "" || clusterType == "" {
			continue
		}
		key := clusterKey{clusterNamespace, clusterName, clusterType}
		if _, ok := localClustersWithDD[key]; !ok {
			continue
		}
		changedByCluster[key] = append(changedByCluster[key], rs)
	}

	mgmtClient := getManagementClusterClient()
	for key, changed := range changedByCluster {
		ref := localClustersWithDD[key]
		l := logger.WithValues("cluster", fmt.Sprintf("%s/%s/%s", key.ns, key.name, key.clusterType))

		skip, err := skipCollecting(ctx, c, &ref, l)
		if err != nil || skip {
			continue
		}

		for _, rs := range changed {
			if err := processResourceSummary(ctx, mgmtClient, rs, l); err != nil {
				l.V(logs.LogInfo).Error(err, fmt.Sprintf("failed to process ResourceSummary %s/%s",
					rs.Namespace, rs.Name))
			}
		}
	}

	return nil
}

func skipCollecting(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	logger logr.Logger) (bool, error) {

	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}
	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return true, err
	}

	if !ready {
		return true, nil
	}

	paused, err := clusterproxy.IsClusterPaused(ctx, c, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster))
	if err != nil {
		logger.V(logs.LogDebug).Error(err, "failed to verify if cluster is paused")
		return true, err
	}

	if paused {
		return true, nil
	}

	return false, nil
}

const (
	// clustersPerWorker controls how many clusters each worker goroutine is responsible for.
	clustersPerWorker = 50
	// maxCollectionWorkers caps the number of concurrent goroutines collecting ResourceSummaries.
	maxCollectionWorkers = 5
)

// processShardLocalDDClusters collects and processes ResourceSummaries for the given
// clusters. It runs sequentially when there are fewer than clustersPerWorker clusters,
// and spawns up to maxCollectionWorkers goroutines otherwise.
func processShardLocalDDClusters(ctx context.Context, c client.Client,
	clusters []corev1.ObjectReference, version string, logger logr.Logger) error {

	logErr := func(cluster *corev1.ObjectReference, err error) {
		if !strings.Contains(err.Error(), "unable to retrieve the complete list of server APIs") {
			logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name)).
				V(logs.LogInfo).Info(fmt.Sprintf("failed to collect ResourceSummaries: %v", err))
		}
	}

	numWorkers := len(clusters) / clustersPerWorker
	if numWorkers > maxCollectionWorkers {
		numWorkers = maxCollectionWorkers
	}

	if numWorkers == 0 {
		// Sequential: fewer than clustersPerWorker clusters, goroutine overhead not justified.
		for i := range clusters {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			cluster := clusters[i]
			l := logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))
			if err := collectResourceSummariesFromCluster(ctx, c, &cluster, version, l); err != nil {
				logErr(&cluster, err)
			}
		}
		return nil
	}

	// Parallel: one worker per clustersPerWorker clusters, capped at maxCollectionWorkers.
	work := make(chan corev1.ObjectReference, len(clusters))
	for _, cluster := range clusters {
		work <- cluster
	}
	close(work)

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cluster := range work {
				select {
				case <-ctx.Done():
					return
				default:
				}
				l := logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))
				if err := collectResourceSummariesFromCluster(ctx, c, &cluster, version, l); err != nil {
					logErr(&cluster, err)
				}
			}
		}()
	}
	wg.Wait()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func collectResourceSummariesFromCluster(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	version string, logger logr.Logger) error {

	skipCollecting, err := skipCollecting(ctx, c, cluster, logger)
	if err != nil {
		return err
	}

	if skipCollecting {
		return nil
	}

	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}

	//  ResourceSummary location depends on drift-detection-manager: management cluster if it's running there,
	// otherwise managed cluster.
	clusterClient, err := getResourceSummaryClient(ctx, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), logger)
	if err != nil {
		return err
	}

	// if cluster is in pull mode, the agent in the managed cluster will create/update resourceSummaries (only metadata and status)
	// to report drift detections. In case of Cluster in pull mode skip verification on resourceSummary installed and compatibility checks
	if clusterClient != nil {
		// not in pull mode
		var installed bool
		installed, err = isResourceSummaryInstalledCached(ctx, clusterClient, cluster)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to verify if ResourceSummary is installed")
			return err
		}

		if !installed {
			return nil
		}

		if !sveltos_upgrade.IsDriftDetectionVersionCompatible(ctx, getManagementClusterClient(), version, cluster.Namespace,
			cluster.Name, clusterproxy.GetClusterType(clusterRef), getAgentInMgmtCluster(), logger) {

			msg := "compatibility checks failed"
			logger.V(logs.LogDebug).Info(msg)
			return errors.New(msg)
		}
	} else {
		logger.V(logs.LogVerbose).Info(fmt.Sprintf("SveltosCluster %s/%s is in pull mode", cluster.Namespace, cluster.Name))
		clusterClient = getManagementClusterClient()
	}

	logger.V(logs.LogVerbose).Info("collecting ResourceSummaries from cluster")
	listOptions := []client.ListOption{}
	if getAgentInMgmtCluster() || clusterClient == nil {
		listOptions = []client.ListOption{
			client.MatchingLabels{
				sveltos_upgrade.ClusterNameLabel: cluster.Name,
			},
			client.InNamespace(cluster.Namespace),
		}
	}

	rsList := libsveltosv1beta1.ResourceSummaryList{}

	err = clusterClient.List(ctx, &rsList, listOptions...)
	if err != nil {
		return err
	}

	l := logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))

	for i := range rsList.Items {
		rs := &rsList.Items[i]

		ns, ok := getClusterSummaryNamespaceFromResourceSummary(rs, logger)
		if !ok {
			continue
		}
		if ns != cluster.Namespace {
			continue
		}

		if !rs.DeletionTimestamp.IsZero() {
			// ignore deleted ResourceSummary
			continue
		}
		if rs.Status.ResourcesChanged || rs.Status.HelmResourcesChanged || rs.Status.KustomizeResourcesChanged {
			// process resourceSummary
			err = processResourceSummary(ctx, clusterClient, rs, l)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// isResourceSummaryInstalledCached returns true if the ResourceSummary CRD is installed
// in the managed cluster identified by cluster. Positive results are cached for the
// lifetime of the process: drift-detection is never removed from a cluster while the
// controller is running, so a confirmed installation never needs to be re-checked.
func isResourceSummaryInstalledCached(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference) (bool, error) {

	resourceSummaryInstalledCacheMu.RLock()
	cached := resourceSummaryInstalledCache[*cluster]
	resourceSummaryInstalledCacheMu.RUnlock()
	if cached {
		return true, nil
	}

	installed, err := isResourceSummaryInstalled(ctx, c)
	if err != nil {
		return false, err
	}
	if installed {
		resourceSummaryInstalledCacheMu.Lock()
		resourceSummaryInstalledCache[*cluster] = true
		resourceSummaryInstalledCacheMu.Unlock()
	}
	return installed, nil
}

// isResourceSummaryInstalled returns true if ResourceSummary CRD is installed, false otherwise
func isResourceSummaryInstalled(ctx context.Context, c client.Client) (bool, error) {
	clusterCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "resourcesummaries.lib.projectsveltos.io"}, clusterCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func getClusterSummaryNameFromResourceSummary(rs *libsveltosv1beta1.ResourceSummary, logger logr.Logger,
) (string, bool) {

	// ResourceSummary previously used labels for ClusterSummary Namespace and Name.
	// This information is now stored as annotations. For backward compatibility, we'll
	// check annotations first, then labels.

	var clusterSummaryName string
	ok := false

	if rs.Annotations != nil {
		clusterSummaryName, ok = rs.Annotations[libsveltosv1beta1.ClusterSummaryNameAnnotation]
	}

	if !ok { // If not found in annotations, try labels
		if rs.Labels != nil {
			//nolint: staticcheck // for backbaward compatibility
			clusterSummaryName, ok = rs.Labels[libsveltosv1beta1.ClusterSummaryNameLabel]
		}
	}

	if !ok { // If still not found after checking both
		logger.V(logs.LogInfo).Info("neither ClusterSummary name annotation nor label are set. Cannot process it.")
		return "", false
	}

	return clusterSummaryName, true
}

func getClusterSummaryNamespaceFromResourceSummary(rs *libsveltosv1beta1.ResourceSummary, logger logr.Logger,
) (string, bool) {

	// ResourceSummary previously used labels for ClusterSummary Namespace and Name.
	// This information is now stored as annotations. For backward compatibility, we'll
	// check annotations first, then labels.

	// Get ClusterSummary Namespace
	var clusterSummaryNamespace string
	ok := false

	if rs.Annotations != nil {
		clusterSummaryNamespace, ok = rs.Annotations[libsveltosv1beta1.ClusterSummaryNamespaceAnnotation]
	}

	if !ok { // If not found in annotations, try labels
		if rs.Labels != nil {
			//nolint: staticcheck // for backbaward compatibility
			clusterSummaryNamespace, ok = rs.Labels[libsveltosv1beta1.ClusterSummaryNamespaceLabel]
		}
	}

	if !ok { // If still not found after checking both
		logger.V(logs.LogInfo).Info("neither ClusterSummary namespace annotation nor label are set. Cannot process it.")
		return "", false
	}

	return clusterSummaryNamespace, true
}

// clusterClient points to the cluster where ResourceSummary is. That can be
// the management cluster if running in agentless mode or the managed cluster
// otherwise
func processResourceSummary(ctx context.Context, clusterClient client.Client,
	rs *libsveltosv1beta1.ResourceSummary, logger logr.Logger) error {

	// Get ClusterSummary
	clusterSummaryName, ok := getClusterSummaryNameFromResourceSummary(rs, logger)
	if !ok {
		logger.V(logs.LogInfo).Info("clusterSummary name label not set. Cannot process it")
		return nil
	}

	var clusterSummaryNamespace string
	clusterSummaryNamespace, ok = getClusterSummaryNamespaceFromResourceSummary(rs, logger)
	if !ok {
		logger.V(logs.LogInfo).Info("clusterSummary namespace label not set. Cannot process it")
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		clusterSummary := &configv1beta1.ClusterSummary{}
		err := getManagementClusterClient().Get(ctx,
			types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName}, clusterSummary)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info("clusterSummary not found. Nothing to do.")
				return nil
			}
			return err
		}

		if !clusterSummary.DeletionTimestamp.IsZero() {
			logger.V(logs.LogDebug).Info("clusterSummary is marked for deletion. Nothing to do.")
			return nil
		}

		l := logger.WithValues("clusterSummary", clusterSummary.Name)
		for i := range clusterSummary.Status.FeatureSummaries {
			switch clusterSummary.Status.FeatureSummaries[i].FeatureID {
			case libsveltosv1beta1.FeatureHelm:
				if rs.Status.HelmResourcesChanged {
					l.V(logs.LogDebug).Info("redeploy helm")
					clusterSummary.Status.FeatureSummaries[i].Hash = nil
					clusterSummary.Status.FeatureSummaries[i].Status = libsveltosv1beta1.FeatureStatusProvisioning
					trackDrifts(clusterSummaryNamespace, clusterSummary.Spec.ClusterName, string(clusterSummary.Status.FeatureSummaries[i].FeatureID),
						string(clusterSummary.Spec.ClusterType), logger)
				}
			case libsveltosv1beta1.FeatureResources:
				if rs.Status.ResourcesChanged {
					l.V(logs.LogDebug).Info("redeploy resources")
					clusterSummary.Status.FeatureSummaries[i].Hash = nil
					clusterSummary.Status.FeatureSummaries[i].Status = libsveltosv1beta1.FeatureStatusProvisioning
					trackDrifts(clusterSummaryNamespace, clusterSummary.Spec.ClusterName, string(clusterSummary.Status.FeatureSummaries[i].FeatureID),
						string(clusterSummary.Spec.ClusterType), logger)
				}
			case libsveltosv1beta1.FeatureKustomize:
				if rs.Status.KustomizeResourcesChanged {
					l.V(logs.LogDebug).Info("redeploy kustomization resources")
					clusterSummary.Status.FeatureSummaries[i].Hash = nil
					clusterSummary.Status.FeatureSummaries[i].Status = libsveltosv1beta1.FeatureStatusProvisioning
					trackDrifts(clusterSummaryNamespace, clusterSummary.Spec.ClusterName, string(clusterSummary.Status.FeatureSummaries[i].FeatureID),
						string(clusterSummary.Spec.ClusterType), logger)
				}
			}
		}

		err = getManagementClusterClient().Status().Update(ctx, clusterSummary)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to update ClusterSummary status")
			return err
		}
		return nil
	})

	if err != nil {
		return err
	}

	return resetResourceSummaryStatus(ctx, clusterClient, rs, logger)
}

func resetResourceSummaryStatus(ctx context.Context, remoteClient client.Client,
	rs *libsveltosv1beta1.ResourceSummary, logger logr.Logger) error {

	resourceSummary := &libsveltosv1beta1.ResourceSummary{}

	err := remoteClient.Get(ctx, types.NamespacedName{Namespace: rs.Namespace, Name: rs.Name}, resourceSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.V(logs.LogDebug).Info("reset resourceSummary status")
	resourceSummary.Status.ResourcesChanged = false
	resourceSummary.Status.KustomizeResourcesChanged = false
	resourceSummary.Status.HelmResourcesChanged = false
	return remoteClient.Status().Update(ctx, resourceSummary)
}

func getListOfClusterWithDriftDetectionDeployed(ctx context.Context, c client.Client,
) (map[corev1.ObjectReference]bool, error) {

	clustersWithDriftDetection := make(map[corev1.ObjectReference]bool, 0)

	clusterProfiles := &configv1beta1.ClusterProfileList{}
	err := c.List(ctx, clusterProfiles)
	if err != nil {
		return nil, err
	}

	for i := range clusterProfiles.Items {
		clusterProfile := &clusterProfiles.Items[i]
		if clusterProfile.Spec.SyncMode != configv1beta1.SyncModeContinuousWithDriftDetection {
			continue
		}

		clustersWithDriftDetection = addMatchingClusters(clusterProfile.Status.MatchingClusterRefs,
			clustersWithDriftDetection)
	}

	profiles := &configv1beta1.ProfileList{}
	err = c.List(ctx, profiles)
	if err != nil {
		return nil, err
	}

	for i := range profiles.Items {
		profile := &profiles.Items[i]
		if profile.Spec.SyncMode != configv1beta1.SyncModeContinuousWithDriftDetection {
			continue
		}

		clustersWithDriftDetection = addMatchingClusters(profile.Status.MatchingClusterRefs,
			clustersWithDriftDetection)
	}

	return clustersWithDriftDetection, nil
}

func addMatchingClusters(matchingClusterRefs []corev1.ObjectReference,
	clustersWithDriftDetection map[corev1.ObjectReference]bool) map[corev1.ObjectReference]bool {

	for i := range matchingClusterRefs {
		cluster := &matchingClusterRefs[i]
		clustersWithDriftDetection[*cluster] = true
	}

	return clustersWithDriftDetection
}
