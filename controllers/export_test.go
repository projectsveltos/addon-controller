/*
Copyright 2022-24. projectsveltos.io. All rights reserved.

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
	UpdateClusterSummaries                = updateClusterSummaries
	CreateClusterSummary                  = createClusterSummary
	UpdateClusterSummary                  = updateClusterSummary
	UpdateClusterConfigurationWithProfile = updateClusterConfigurationWithProfile
	CleanClusterConfiguration             = cleanClusterConfiguration
	CleanClusterReports                   = cleanClusterReports
	CleanClusterSummaries                 = cleanClusterSummaries
	UpdateClusterSummarySyncMode          = updateClusterSummarySyncMode
	UpdateClusterReports                  = updateClusterReports
	GetMatchingClusters                   = getMatchingClusters
	GetMaxUpdate                          = getMaxUpdate
	ReviseUpdatedAndUpdatingClusters      = reviseUpdatedAndUpdatingClusters
	GetUpdatedAndUpdatingClusters         = getUpdatedAndUpdatingClusters
)

var (
	RequeueClusterProfileForCluster = (*ClusterProfileReconciler).requeueClusterProfileForCluster
	RequeueClusterProfileForMachine = (*ClusterProfileReconciler).requeueClusterProfileForMachine
)

var (
	RequeueProfileForCluster   = (*ProfileReconciler).requeueProfileForCluster
	RequeueProfileForMachine   = (*ProfileReconciler).requeueProfileForMachine
	LimitReferencesToNamespace = (*ProfileReconciler).limitReferencesToNamespace
)

var (
	IsFeatureDeployed                    = (*ClusterSummaryReconciler).isFeatureDeployed
	IsFeatureFailedWithNonRetriableError = (*ClusterSummaryReconciler).isFeatureFailedWithNonRetriableError
	GetHash                              = (*ClusterSummaryReconciler).getHash
	UpdateFeatureStatus                  = (*ClusterSummaryReconciler).updateFeatureStatus
	DeployFeature                        = (*ClusterSummaryReconciler).deployFeature
	UndeployFeature                      = (*ClusterSummaryReconciler).undeployFeature
	GetCurrentReferences                 = (*ClusterSummaryReconciler).getCurrentReferences
	IsPaused                             = (*ClusterSummaryReconciler).isPaused
	ShouldReconcile                      = (*ClusterSummaryReconciler).shouldReconcile
	UpdateChartMap                       = (*ClusterSummaryReconciler).updateChartMap
	ShouldRedeploy                       = (*ClusterSummaryReconciler).shouldRedeploy
	CanRemoveFinalizer                   = (*ClusterSummaryReconciler).canRemoveFinalizer
	ReconcileDelete                      = (*ClusterSummaryReconciler).reconcileDelete
	AreDependenciesDeployed              = (*ClusterSummaryReconciler).areDependenciesDeployed

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
	CollectContent                = collectContent
	UndeployStaleResources        = undeployStaleResources
	GetDeployedGroupVersionKinds  = getDeployedGroupVersionKinds
	CanDelete                     = canDelete
	HandleResourceDelete          = handleResourceDelete
	GetSecret                     = getSecret
	GetReferenceResourceNamespace = getReferenceResourceNamespace

	ResourcesHash   = resourcesHash
	GetResourceRefs = getResourceRefs

	UndeployKustomizeRefs = undeployKustomizeRefs
	KustomizationHash     = kustomizationHash
	ExtractTarGz          = extractTarGz

	HelmHash                                 = helmHash
	ShouldInstall                            = shouldInstall
	ShouldUninstall                          = shouldUninstall
	ShouldUpgrade                            = shouldUpgrade
	UpdateChartsInClusterConfiguration       = updateChartsInClusterConfiguration
	UpdateStatusForeferencedHelmReleases     = updateStatusForeferencedHelmReleases
	UpdateStatusForNonReferencedHelmReleases = updateStatusForNonReferencedHelmReleases
	CreateReportForUnmanagedHelmRelease      = createReportForUnmanagedHelmRelease
	UpdateClusterReportWithHelmReports       = updateClusterReportWithHelmReports
	HandleCharts                             = handleCharts

	InstantiateTemplateValues = instantiateTemplateValues

	IsCluterSummaryProvisioned = isCluterSummaryProvisioned
)

type (
	ReleaseInfo = releaseInfo
)

var (
	GetClusterReportName        = getClusterReportName
	GetClusterConfigurationName = getClusterConfigurationName
)

var (
	DeployDebuggingConfigurationCRD                  = deployDebuggingConfigurationCRD
	DeployResourceSummaryCRD                         = deployResourceSummaryCRD
	DeployResourceSummaryInCluster                   = deployResourceSummaryInCluster
	DeployResourceSummaryInstance                    = deployResourceSummaryInstance
	UpdateDeployedGroupVersionKind                   = updateDeployedGroupVersionKind
	DeployDriftDetectionManagerInManagementCluster   = deployDriftDetectionManagerInManagementCluster
	GetDriftDetectionManagerLabels                   = getDriftDetectionManagerLabels
	RemoveDriftDetectionManagerFromManagementCluster = removeDriftDetectionManagerFromManagementCluster
	GetDriftDetectionNamespaceInMgmtCluster          = getDriftDetectionNamespaceInMgmtCluster

	GetResourceSummaryNamespace = getResourceSummaryNamespace
	GetResourceSummaryName      = getResourceSummaryName
)

var (
	CollectResourceSummariesFromCluster = collectResourceSummariesFromCluster
)

var (
	InitializeManager = initializeManager
)

const (
	ReasonLabel = reasonLabel
)

var (
	IsHealthy      = isHealthy
	FetchResources = fetchResources
)

// reloader utils
var (
	WatchForRollingUpgrade                  = watchForRollingUpgrade
	CreateReloaderInstance                  = createReloaderInstance
	DeployReloaderInstance                  = deployReloaderInstance
	RemoveReloaderInstance                  = removeReloaderInstance
	UpdateReloaderWithDeployedResources     = updateReloaderWithDeployedResources
	ConvertResourceReportsToObjectReference = convertResourceReportsToObjectReference
	ConvertHelmResourcesToObjectReference   = convertHelmResourcesToObjectReference
)

var (
	GetMgmtResourceName = getMgmtResourceName
)
