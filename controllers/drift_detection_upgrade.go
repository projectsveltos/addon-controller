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
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func upgradeDriftDetection(ctx context.Context, logger logr.Logger) {
	if getAgentInMgmtCluster() {
		logger.V(logs.LogInfo).Info("upgrade drift detection on management cluster")
		upgradeDriftDetectionDeploymentsInMgmtCluster(ctx, logger)
		return
	}

	logger.V(logs.LogInfo).Info("upgrade drift detection on managed clusters")
	upgradeDriftDetectionDeploymentsInManagedClusters(ctx, logger)
}

func upgradeDriftDetectionDeploymentsInManagedClusters(ctx context.Context, logger logr.Logger) {
	const retryAfter = 5 * time.Second

	mgmtClient := getManagementClusterClient()
	for {
		patches, err := getDriftDetectionManagerPatches(ctx, mgmtClient, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect drift detection patches: %v", err))
			time.Sleep(retryAfter)
			continue
		}

		clusters, err := getListOfClustersWithDriftDetection(ctx, mgmtClient, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect drift detection patches: %v", err))
			time.Sleep(retryAfter)
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
				if !cluster.GetDeletionTimestamp().IsZero() {
					continue
				}
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

		time.Sleep(retryAfter)
	}
}

func upgradeDriftDetectionDeploymentsInMgmtCluster(ctx context.Context, logger logr.Logger) {
	config := getManagementClusterConfig()
	c := getManagementClusterClient()
	const retryAfter = 5 * time.Second

	for {
		patches, err := getDriftDetectionManagerPatches(ctx, c, logger)
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
		err = c.List(ctx, driftDetectionDeployments, listOptions...)
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

			err = upgradeDriftDetectionDeployment(ctx, config, c, &driftDetectionDeployments.Items[i],
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

func getListOfClustersWithDriftDetection(ctx context.Context, c client.Client,
	logger logr.Logger) ([]corev1.ObjectReference, error) {

	clusterSummaries := &configv1beta1.ClusterSummaryList{}

	err := c.List(ctx, clusterSummaries)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch clusterSummary instances: %v", err))
		return nil, err
	}

	clusters := []corev1.ObjectReference{}
	for i := range clusterSummaries.Items {
		cs := &clusterSummaries.Items[i]
		if cs.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
			kind := libsveltosv1beta1.SveltosClusterKind
			apiVersion := libsveltosv1beta1.GroupVersion.String()
			if cs.Spec.ClusterType == libsveltosv1beta1.ClusterTypeCapi {
				kind = clusterv1.ClusterKind
				apiVersion = clusterv1.GroupVersion.String()
			}

			clusters = append(clusters, corev1.ObjectReference{
				Namespace:  cs.Spec.ClusterNamespace,
				Name:       cs.Spec.ClusterName,
				Kind:       kind,
				APIVersion: apiVersion,
			})
		}
	}

	return clusters, nil
}
