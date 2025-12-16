/*
Copyright 2024. projectsveltos.io. All rights reserved.

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
	"math"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func upgradeDriftDetection(ctx context.Context, shardKey string, logger logr.Logger) {
	if getAgentInMgmtCluster() {
		logger.V(logs.LogInfo).Info("upgrade drift detection on management cluster")
		upgradeDriftDetectionDeploymentsInMgmtCluster(ctx, logger)
		return
	}

	logger.V(logs.LogInfo).Info("upgrade drift detection on managed clusters")
	upgradeDriftDetectionDeploymentsInManagedClusters(ctx, shardKey, logger)
}

func upgradeDriftDetectionDeploymentsInManagedClusters(ctx context.Context, shardKey string, logger logr.Logger) {
	const two = 2
	const maxRetryAfter = two * time.Minute // Maximum sleep duration
	retryAfter := 5 * time.Second

	mgmtClient := getManagementClusterClient()
	for {
		globalPatches, err := getGlobalDriftDetectionManagerPatches(ctx, mgmtClient, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect drift detection patches: %v", err))
			time.Sleep(retryAfter)
			// Double the sleep time, capped at maxRetryAfter
			retryAfter = time.Duration(math.Min(float64(retryAfter*two), float64(maxRetryAfter)))
			continue
		}

		clusters, err := clusterproxy.GetListOfClustersForShardKey(ctx, mgmtClient, "", capiOnboardAnnotation,
			shardKey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
			time.Sleep(retryAfter)
			// Double the sleep time, capped at maxRetryAfter
			retryAfter = time.Duration(math.Min(float64(retryAfter*two), float64(maxRetryAfter)))
			continue
		}

		// featureID is not important in this context. Drift detection is not tracked anyway. One is required
		// so setting this.
		featureID := string(libsveltosv1beta1.FeatureKustomize)

		allProcessed := true
		for i := range clusters {
			clusterType := clusterproxy.GetClusterType(&clusters[i])

			cluster, err := clusterproxy.GetCluster(ctx, mgmtClient, clusters[i].Namespace, clusters[i].Name,
				clusterType)
			if err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				allProcessed = false
				continue
			}

			if !cluster.GetDeletionTimestamp().IsZero() {
				continue
			}

			skipUpgrading, err := skipUpgrading(ctx, mgmtClient, cluster, logger)
			if err != nil {
				allProcessed = false
				continue
			}

			if skipUpgrading {
				continue
			}

			patches, err := getPerClusterDriftDetectionManagerPatches(ctx, mgmtClient,
				clusters[i].Namespace, clusters[i].Name, clusterType, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(
					fmt.Sprintf("cluster %s %s/%s failed to get per cluster driftDetection patches: %v",
						clusterType, clusters[i].Namespace, clusters[i].Name, err))
				allProcessed = false
			} else if patches == nil {
				patches = globalPatches
			}

			err = deployDriftDetectionManagerInManagedCluster(ctx, clusters[i].Namespace, clusters[i].Name,
				"sveltos-upgrade", featureID,
				"do-not-send-updates", clusterType, patches, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(
					fmt.Sprintf("cluster %s %s/%s failed to upgrade driftDetection deployment: %v",
						clusterType, clusters[i].Namespace, clusters[i].Name, err))
				allProcessed = false
			}
		}

		if allProcessed {
			break
		}

		// If not all clusters were processed successfully (allProcessed is false),
		// we sleep and increase the backoff timer.
		logger.V(logs.LogInfo).Info(fmt.Sprintf("not all clusters processed successfully. Retrying after %v", retryAfter))
		time.Sleep(retryAfter)
		time.Sleep(retryAfter)
	}
}

func upgradeDriftDetectionDeploymentsInMgmtCluster(ctx context.Context, logger logr.Logger) {
	config := getManagementClusterConfig()
	mgmtClient := getManagementClusterClient()
	const retryAfter = 5 * time.Second

	for {
		globalPatches, err := getGlobalDriftDetectionManagerPatches(ctx, mgmtClient, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect drift detection patches: %v", err))
			time.Sleep(retryAfter)
			continue
		}

		listOptions := []client.ListOption{
			client.MatchingLabels{
				driftDetectionFeatureLabelKey: driftDetectionFeatureLabelValue,
			},
		}

		driftDetectionDeployments := &appsv1.DeploymentList{}
		err = mgmtClient.List(ctx, driftDetectionDeployments, listOptions...)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect driftDetection deployment: %v", err))
			time.Sleep(retryAfter)
			continue
		}

		allProcessed := true
		for i := range driftDetectionDeployments.Items {
			// Ignore deleted drift detection deployment
			if !driftDetectionDeployments.Items[i].DeletionTimestamp.IsZero() {
				continue
			}

			var patches []libsveltosv1beta1.Patch
			clusterNamespace, clusterName, clusterType, found :=
				getClusterDataFromDriftDetectionManagerDeployment(&driftDetectionDeployments.Items[i])
			if found {
				patches, err = getPerClusterDriftDetectionManagerPatches(ctx, mgmtClient,
					clusterNamespace, clusterName, clusterType, logger)
				if err != nil {
					logger.V(logs.LogInfo).Info(
						fmt.Sprintf("cluster %s %s/%s failed to get per cluster driftDetection patches: %v",
							clusterType, clusterNamespace, clusterName, err))
					allProcessed = false
				} else if patches == nil {
					patches = globalPatches
				}
			}

			err = upgradeDriftDetectionDeployment(ctx, config, mgmtClient, &driftDetectionDeployments.Items[i],
				patches, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to upgrade driftDetection deployment: %v", err))
				allProcessed = false
			}
		}

		if allProcessed {
			break
		}

		time.Sleep(retryAfter)
	}
}

func upgradeDriftDetectionDeployment(ctx context.Context, config *rest.Config, c client.Client,
	depl *appsv1.Deployment, patches []libsveltosv1beta1.Patch, logger logr.Logger) error {

	exist, clusterNs, clusterName, clusterType := deplAssociatedClusterExist(ctx, c, depl, logger)
	if exist {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("Upgrade drift detection deployment for cluster %s %s/%s",
			clusterType, clusterNs, clusterName))
		return deployDriftDetectionManagerInManagementCluster(ctx, config, clusterNs, clusterName,
			"do-not-send-updates", clusterType, patches, logger)
	}

	return nil
}

func skipUpgrading(ctx context.Context, c client.Client, cluster client.Object,
	logger logr.Logger) (bool, error) {

	clusterRef := &corev1.ObjectReference{
		Namespace: cluster.GetNamespace(),
		Name:      cluster.GetName(),
	}

	if cluster.GetObjectKind().GroupVersionKind().Kind == libsveltosv1beta1.SveltosClusterKind {
		clusterRef.Kind = libsveltosv1beta1.SveltosClusterKind
		clusterRef.APIVersion = libsveltosv1beta1.GroupVersion.String()
	} else {
		clusterRef.Kind = clusterKind
		clusterRef.APIVersion = clusterv1.GroupVersion.String()
	}

	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return true, err
	}

	if !ready {
		return true, nil
	}

	paused, err := clusterproxy.IsClusterPaused(ctx, c, cluster.GetNamespace(), cluster.GetName(),
		clusterproxy.GetClusterType(clusterRef))
	if err != nil {
		logger.V(logs.LogDebug).Error(err, "failed to verify if cluster is paused")
		return true, err
	}

	if paused {
		return true, nil
	}

	cacheMgr := clustercache.GetManager()
	managedClient, err := cacheMgr.GetKubernetesClient(ctx, c, cluster.GetNamespace(), cluster.GetName(),
		"", "", clusterproxy.GetClusterType(clusterRef), logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get managed client")
		return true, err
	}

	// Verify if ResourceSummary CRD is present. Th
	resourceCRDPresent, err := isResourceSummaryCRDPresent(ctx, managedClient, logger)
	if err != nil {
		return true, err
	}

	if !resourceCRDPresent {
		return true, nil
	}

	return false, nil
}

func isResourceSummaryCRDPresent(ctx context.Context, c client.Client, logger logr.Logger) (bool, error) {
	resourceSummaryCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "resourcesummaries.lib.projectsveltos.io"},
		resourceSummaryCRD)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		logger.V(logs.LogInfo).Error(err, "failed to verify presence of ResourceSummary CRD")
		return false, err
	}

	return true, nil
}
