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
	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
)

var (
	GetMatchingClusters          = (*ClusterFeatureReconciler).getMatchingClusters
	UpdateClusterSummaries       = (*ClusterFeatureReconciler).updateClusterSummaries
	CreateClusterSummary         = (*ClusterFeatureReconciler).createClusterSummary
	UpdateClusterSummary         = (*ClusterFeatureReconciler).updateClusterSummary
	DeleteClusterSummary         = (*ClusterFeatureReconciler).deleteClusterSummary
	GetMachinesForCluster        = (*ClusterFeatureReconciler).getMachinesForCluster
	IsClusterReadyToBeConfigured = (*ClusterFeatureReconciler).isClusterReadyToBeConfigured

	RequeueClusterFeatureForCluster = (*ClusterFeatureReconciler).requeueClusterFeatureForCluster
	RequeueClusterFeatureForMachine = (*ClusterFeatureReconciler).requeueClusterFeatureForMachine
)

var (
	GetSecretData       = getSecretData
	GetKubernetesClient = getKubernetesClient
	GetRoleName         = getRoleName

	DeployNamespacedWorkloadRole = deployNamespacedWorkloadRole
	DeployClusterWorkloadRole    = deployClusterWorkloadRole

	WorkloadRoleHash = workloadRoleHash
)

var (
	IsFeatureDeployed   = (*ClusterSummaryReconciler).isFeatureDeployed
	GetHash             = (*ClusterSummaryReconciler).getHash
	UpdateFeatureStatus = (*ClusterSummaryReconciler).updateFeatureStatus
	DeployFeature       = (*ClusterSummaryReconciler).deployFeature

	RequeueClusterSummaryForWorkloadRole = (*ClusterSummaryReconciler).requeueClusterSummaryForWorkloadRole
)

var (
	GetClusterFeatureOwner = getClusterFeatureOwner
)

var (
	Insert = (*Set).insert
	Erase  = (*Set).erase
	Len    = (*Set).len
	Items  = (*Set).items
	Has    = (*Set).has
)

func GetFeature(featureID configv1alpha1.FeatureID, getHash getCurrentHash,
	deploy deployer.RequestHandler) *feature {
	return &feature{
		id:          featureID,
		currentHash: getHash,
		deploy:      deploy,
	}
}
