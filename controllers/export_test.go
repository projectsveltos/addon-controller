/*
Copyright 2022-23. projectsveltos.io. All rights reserved.

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

var (
	GetMatchingClusters          = (*ClusterProfileReconciler).getMatchingClusters
	UpdateClusterSummaries       = (*ClusterProfileReconciler).updateClusterSummaries
	CreateClusterSummary         = (*ClusterProfileReconciler).createClusterSummary
	UpdateClusterSummary         = (*ClusterProfileReconciler).updateClusterSummary
	DeleteClusterSummary         = (*ClusterProfileReconciler).deleteClusterSummary
	UpdateClusterConfiguration   = (*ClusterProfileReconciler).updateClusterConfiguration
	CleanClusterConfiguration    = (*ClusterProfileReconciler).cleanClusterConfiguration
	CleanClusterReports          = (*ClusterProfileReconciler).cleanClusterReports
	UpdateClusterReports         = (*ClusterProfileReconciler).updateClusterReports
	UpdateClusterSummarySyncMode = (*ClusterProfileReconciler).updateClusterSummarySyncMode

	RequeueClusterProfileForCluster = (*ClusterProfileReconciler).requeueClusterProfileForCluster
	RequeueClusterProfileForMachine = (*ClusterProfileReconciler).requeueClusterProfileForMachine
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
	ShouldReconcile                = (*ClusterSummaryReconciler).shouldReconcile
	UpdateChartMap                 = (*ClusterSummaryReconciler).updateChartMap
	ShouldRedeploy                 = (*ClusterSummaryReconciler).shouldRedeploy
	CanRemoveFinalizer             = (*ClusterSummaryReconciler).canRemoveFinalizer

	ConvertResultStatus               = (*ClusterSummaryReconciler).convertResultStatus
	RequeueClusterSummaryForReference = (*ClusterSummaryReconciler).requeueClusterSummaryForReference
	RequeueClusterSummaryForCluster   = (*ClusterSummaryReconciler).requeueClusterSummaryForCluster
)

var (
	CreatFeatureHandlerMaps = creatFeatureHandlerMaps
	GetHandlersForFeature   = getHandlersForFeature
	GenericDeploy           = genericDeploy
	GenericUndeploy         = genericUndeploy

	GetClusterSummary             = getClusterSummary
	AddLabel                      = addLabel
	CreateNamespace               = createNamespace
	GetEntryKey                   = getEntryKey
	DeployContentOfConfigMap      = deployContentOfConfigMap
	DeployContentOfSecret         = deployContentOfSecret
	DeployContent                 = deployContent
	GetClusterSummaryAdmin        = getClusterSummaryAdmin
	AddAnnotation                 = addAnnotation
	ComputePolicyHash             = computePolicyHash
	GetPolicyInfo                 = getPolicyInfo
	UndeployStaleResources        = undeployStaleResources
	GetDeployedGroupVersionKinds  = getDeployedGroupVersionKinds
	CanDelete                     = canDelete
	HandleResourceDelete          = handleResourceDelete
	GetSecret                     = getSecret
	GetReferenceResourceNamespace = getReferenceResourceNamespace

	ResourcesHash   = resourcesHash
	GetResourceRefs = getResourceRefs

	HelmHash                                 = helmHash
	ShouldInstall                            = shouldInstall
	ShouldUninstall                          = shouldUninstall
	ShouldUpgrade                            = shouldUpgrade
	UpdateChartsInClusterConfiguration       = updateChartsInClusterConfiguration
	UpdateStatusForReferencedHelmReleases    = updateStatusForReferencedHelmReleases
	UpdateStatusForNonReferencedHelmReleases = updateStatusForNonReferencedHelmReleases
	CreateReportForUnmanagedHelmRelease      = createReportForUnmanagedHelmRelease
	UpdateClusterReportWithHelmReports       = updateClusterReportWithHelmReports
	HandleCharts                             = handleCharts

	InstantiateTemplateValues = instantiateTemplateValues
)

type (
	ReleaseInfo = releaseInfo
)

var (
	GetClusterReportName        = getClusterReportName
	GetClusterConfigurationName = getClusterConfigurationName
)

var (
	DeployDebuggingConfigurationCRD = deployDebuggingConfigurationCRD
	DeployResourceSummaryCRD        = deployResourceSummaryCRD
	DeployResourceSummaryInCluster  = deployResourceSummaryInCluster
	DeployResourceSummaryInstance   = deployResourceSummaryInstance

	GetResourceSummaryNamespace = getResourceSummaryNamespace
	GetResourceSummaryName      = getResourceSummaryName
)

var (
	CollectResourceSummariesFromCluster = collectResourceSummariesFromCluster
)
