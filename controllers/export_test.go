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
	UpdateClusterConfiguration   = (*ClusterFeatureReconciler).updateClusterConfiguration
	CleanClusterConfiguration    = (*ClusterFeatureReconciler).cleanClusterConfiguration

	RequeueClusterFeatureForCluster = (*ClusterFeatureReconciler).requeueClusterFeatureForCluster
	RequeueClusterFeatureForMachine = (*ClusterFeatureReconciler).requeueClusterFeatureForMachine
)

var (
	IsFeatureDeployed              = (*ClusterSummaryReconciler).isFeatureDeployed
	GetHash                        = (*ClusterSummaryReconciler).getHash
	UpdateFeatureStatus            = (*ClusterSummaryReconciler).updateFeatureStatus
	DeployFeature                  = (*ClusterSummaryReconciler).deployFeature
	UndeployFeature                = (*ClusterSummaryReconciler).undeployFeature
	UpdateDeployedGroupVersionKind = (*ClusterSummaryReconciler).updateDeployedGroupVersionKind
	GetCurrentReferences           = (*ClusterSummaryReconciler).getCurrentReferences
	IsPaused                       = (*ClusterSummaryReconciler).isPaused

	ConvertResultStatus               = (*ClusterSummaryReconciler).convertResultStatus
	RequeueClusterSummaryForConfigMap = (*ClusterSummaryReconciler).requeueClusterSummaryForConfigMap
	RequeueClusterSummaryForCluster   = (*ClusterSummaryReconciler).requeueClusterSummaryForCluster
)

var (
	CreatFeatureHandlerMaps = creatFeatureHandlerMaps
	GetHandlersForFeature   = getHandlersForFeature
	GenericDeploy           = genericDeploy
	GenericUndeploy         = genericUndeploy

	GetClusterSummary            = getClusterSummary
	GetSecretData                = getSecretData
	GetKubernetesClient          = getKubernetesClient
	AddLabel                     = addLabel
	CreateNamespace              = createNamespace
	GetEntryKey                  = getEntryKey
	DeployContentOfConfigMap     = deployContentOfConfigMap
	GetPolicyName                = getPolicyName
	GetPolicyInfo                = getPolicyInfo
	UndeployStaleResources       = undeployStaleResources
	GetDeployedGroupVersionKinds = getDeployedGroupVersionKinds

	ResourcesHash   = resourcesHash
	GetResourceRefs = getResourceRefs

	DeployKyvernoInWorklaodCluster = deployKyvernoInWorklaodCluster
	IsKyvernoReady                 = isKyvernoReady
	KyvernoHash                    = kyvernoHash
	GetKyvernoRefs                 = getKyvernoRefs
	DeployKyverno                  = deployKyverno

	DeployPrometheusOperator                  = deployPrometheusOperator
	DeployPrometheusOperatorInWorklaodCluster = deployPrometheusOperatorInWorklaodCluster
	IsPrometheusOperatorReady                 = isPrometheusOperatorReady
	DeployKubeStateMetricsInWorklaodCluster   = deployKubeStateMetricsInWorklaodCluster
	IsKubeStateMetricsReady                   = isKubeStateMetricsReady
	DeployKubePrometheusInWorklaodCluster     = deployKubePrometheusInWorklaodCluster
	DeployPrometheus                          = deployPrometheus
	PrometheusHash                            = prometheusHash
	GetPrometheusRefs                         = getPrometheusRefs
	GetPrometheusInstance                     = getPrometheusInstance
	AddStorageConfig                          = addStorageConfig

	DeployGatekeeperInWorklaodCluster = deployGatekeeperInWorklaodCluster
	IsGatekeeperReady                 = isGatekeeperReady
	DeployGatekeeper                  = deployGatekeeper
	GatekeeperHash                    = gatekeeperHash
	HasContraintTemplates             = hasContraintTemplates
	SortConfigMapByConstraintsFirst   = sortConfigMapByConstraintsFirst
	ApplyAuditOptions                 = applyAuditOptions

	IsContourGatewayReady                 = isContourGatewayReady
	IsContourReady                        = isContourReady
	DeployContourGateway                  = deployContourGateway
	DeployRegularContour                  = deployRegularContour
	DeployContour                         = deployContour
	ContourHash                           = contourHash
	DeployContourGatewayInWorklaodCluster = deployContourGatewayInWorklaodCluster
	DeployContourInWorklaodCluster        = deployContourInWorklaodCluster
	ShouldInstallContourGateway           = shouldInstallContourGateway
	ShouldInstallContour                  = shouldInstallContour
)

var (
	GetClusterFeatureOwner = getClusterFeatureOwner
	GetUnstructured        = getUnstructured
	AddOwnerReference      = addOwnerReference
	RemoveOwnerReference   = removeOwnerReference
)

var (
	Insert = (*Set).insert
	Erase  = (*Set).erase
	Len    = (*Set).len
	Items  = (*Set).items
	Has    = (*Set).has
)

func GetFeature(featureID configv1alpha1.FeatureID, getHash getCurrentHash,
	deploy deployer.RequestHandler, getRefs getPolicyRefs) *feature {

	return &feature{
		id:          featureID,
		currentHash: getHash,
		deploy:      deploy,
		getRefs:     getRefs,
	}
}
