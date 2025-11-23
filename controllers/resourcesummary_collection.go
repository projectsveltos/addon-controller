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
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
			continue
		}

		clustersWithDD, err := getListOfClusterWithDriftDetectionDeployed(ctx, c)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect clusters with drift detection: %v", err))
			continue
		}

		for i := range clusterList {
			cluster := &clusterList[i]
			if _, ok := clustersWithDD[*cluster]; !ok {
				continue
			}

			err = collectResourceSummariesFromCluster(ctx, c, cluster, version, logger)
			if err != nil {
				if !strings.Contains(err.Error(), "unable to retrieve the complete list of server APIs") {
					logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect ResourceSummaries from cluster: %s/%s %v",
						cluster.Namespace, cluster.Name, err))
				}
			}
		}
	}
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

func collectResourceSummariesFromCluster(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	version string, logger logr.Logger) error {

	skipCollecting, err := skipCollecting(ctx, c, cluster, logger)
	if err != nil {
		return err
	}

	if skipCollecting {
		return nil
	}

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))
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
		installed, err = isResourceSummaryInstalled(ctx, clusterClient)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to verify if ResourceSummary is installed %v", err))
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
			logger.V(logs.LogInfo).Info("clusterSummary is marked for deletion. Nothing to do.")
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
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update ClusterSummary status: %v", err))
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
