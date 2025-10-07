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
	IsProfilePaused                       = isProfilePaused
	GetMaxUpdate                          = getMaxUpdate
	ReviseUpdatedAndUpdatingClusters      = reviseUpdatedAndUpdatingClusters
	GetUpdatedAndUpdatingClusters         = getUpdatedAndUpdatingClusters
)

var (
	RequeueClusterProfileForCluster = (*ClusterProfileReconciler).requeueClusterProfileForCluster
	RequeueClusterProfileForMachine = (*ClusterProfileReconciler).requeueClusterProfileForMachine
	GetClustersFromClusterSets      = (*ClusterProfileReconciler).getClustersFromClusterSets
)

var (
	RequeueProfileForCluster   = (*ProfileReconciler).requeueProfileForCluster
	RequeueProfileForMachine   = (*ProfileReconciler).requeueProfileForMachine
	LimitReferencesToNamespace = (*ProfileReconciler).limitReferencesToNamespace
	GetClustersFromSets        = (*ProfileReconciler).getClustersFromSets
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
	IsReady                              = (*ClusterSummaryReconciler).isReady
	ShouldReconcile                      = (*ClusterSummaryReconciler).shouldReconcile
	UpdateChartMap                       = (*ClusterSummaryReconciler).updateChartMap
	ShouldRedeploy                       = (*ClusterSummaryReconciler).shouldRedeploy
	CanRemoveFinalizer                   = (*ClusterSummaryReconciler).canRemoveFinalizer
	ReconcileDelete                      = (*ClusterSummaryReconciler).reconcileDelete
	AreDependenciesDeployed              = (*ClusterSummaryReconciler).areDependenciesDeployed
	SetFailureMessage                    = (*ClusterSummaryReconciler).setFailureMessage
	ResetFeatureStatus                   = (*ClusterSummaryReconciler).resetFeatureStatus

	ConvertResultStatus               = (*ClusterSummaryReconciler).convertResultStatus
	RequeueClusterSummaryForReference = (*ClusterSummaryReconciler).requeueClusterSummaryForReference
	RequeueClusterSummaryForCluster   = (*ClusterSummaryReconciler).requeueClusterSummaryForCluster
)

var (
	GetProfileSpecHash          = (*ClusterPromotionReconciler).getProfileSpecHash
	GetStagesHash               = (*ClusterPromotionReconciler).getStagesHash
	ProfileSpecChanged          = (*ClusterPromotionReconciler).profileSpecChanged
	StagesChanged               = (*ClusterPromotionReconciler).stagesChanged
	ReconcileStageProfile       = (*ClusterPromotionReconciler).reconcileStageProfile
	CheckCurrentStageDeployment = (*ClusterPromotionReconciler).checkCurrentStageDeployment
	GetNextStage                = (*ClusterPromotionReconciler).getNextStage
	CanAutoAdvance              = (*ClusterPromotionReconciler).canAutoAdvance
	CanManualAdvance            = (*ClusterPromotionReconciler).canManualAdvance
	CleanClusterProfiles        = (*ClusterPromotionReconciler).cleanClusterProfiles
	IsPromotionWindowOpen       = (*ClusterPromotionReconciler).isPromotionWindowOpen

	GetClusterProfileName     = getClusterProfileName
	ResetStageStatuses        = resetStageStatuses
	AddStageStatus            = addStageStatus
	UpdateStageStatus         = updateStageStatus
	GetClusterPromotionLabels = getClusterPromotionLabels
)

var (
	CreatFeatureHandlerMaps = creatFeatureHandlerMaps
	GetHandlersForFeature   = getHandlersForFeature
	GenericDeploy           = genericDeploy
	GenericUndeploy         = genericUndeploy

	GetEntryKey                  = getEntryKey
	DeployContentOfConfigMap     = deployContentOfConfigMap
	DeployContentOfSecret        = deployContentOfSecret
	DeployContent                = deployContent
	GetClusterSummaryAdmin       = getClusterSummaryAdmin
	CollectContent               = collectContent
	UndeployStaleResources       = undeployStaleResources
	GetDeployedGroupVersionKinds = getDeployedGroupVersionKinds
	GetSecret                    = getSecret
	ReadFiles                    = readFiles

	AddExtraLabels      = addExtraLabels
	AddExtraAnnotations = addExtraAnnotations
	AdjustNamespace     = adjustNamespace

	ResourcesHash   = resourcesHash
	GetResourceRefs = getResourceRefs

	UndeployKustomizeRefs             = undeployKustomizeRefs
	KustomizationHash                 = kustomizationHash
	GetKustomizeReferenceResourceHash = getKustomizeReferenceResourceHash
	ExtractTarGz                      = extractTarGz
	//nolint: gocritic // getDataSectionHash is generic and needs instantiation
	GetStringDataSectionHash = func(aMap map[string]string) string { return getDataSectionHash(aMap) }
	//nolint: gocritic // getDataSectionHash is generic and needs instantiation
	GetByteDataSectionHash                  = func(aMap map[string][]byte) string { return getDataSectionHash(aMap) }
	InstantiateKustomizeSubstituteValues    = instantiateKustomizeSubstituteValues
	GetKustomizeSubstituteValuesFrom        = getKustomizeSubstituteValuesFrom
	GetKustomizeSubstituteValues            = getKustomizeSubstituteValues
	InstantiateResourceWithSubstituteValues = instantiateResourceWithSubstituteValues

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
	GetHelmReferenceResourceHash             = getHelmReferenceResourceHash
	GetHelmChartValuesHash                   = getHelmChartValuesHash
	GetCredentialsAndCAFiles                 = getCredentialsAndCAFiles
	GetInstantiatedChart                     = getInstantiatedChart
	GetHelmChartValuesFrom                   = getHelmChartValuesFrom

	InstantiateTemplateValues = instantiateTemplateValues
	FecthClusterObjects       = fecthClusterObjects

	IsCluterSummaryProvisioned = isCluterSummaryProvisioned
	IsNamespaced               = isNamespaced
	StringifyMap               = stringifyMap
	ParseMapFromString         = parseMapFromString

	GetTemplateResourceRefHash = getTemplateResourceRefHash
)

type (
	ReleaseInfo = releaseInfo
)

var (
	GetClusterConfigurationName = getClusterConfigurationName
)

var (
	DeployDebuggingConfigurationCRD                  = deployDebuggingConfigurationCRD
	DeployResourceSummaryCRD                         = deployResourceSummaryCRD
	DeployDriftDetectionManagerInCluster             = deployDriftDetectionManagerInCluster
	UpdateDeployedGroupVersionKind                   = updateDeployedGroupVersionKind
	DeployDriftDetectionManagerInManagementCluster   = deployDriftDetectionManagerInManagementCluster
	GetDriftDetectionManagerLabels                   = getDriftDetectionManagerLabels
	RemoveDriftDetectionManagerFromManagementCluster = removeDriftDetectionManagerFromManagementCluster
	GetDriftDetectionNamespaceInMgmtCluster          = getDriftDetectionNamespaceInMgmtCluster
)

var (
	CollectResourceSummariesFromCluster = collectResourceSummariesFromCluster
)

var (
	InitializeManager = initializeManager
)

var (
	GetTemplateResourceName      = getTemplateResourceName
	GetTemplateResourceNamespace = getTemplateResourceNamespace
)

var (
	SelectClusters              = selectClusters
	SelectMoreClusters          = selectMoreClusters
	PruneConnectionDownClusters = pruneConnectionDownClusters
)

var (
	DeplAssociatedClusterExist = deplAssociatedClusterExist
	RemoveStaleResourceSummary = removeStaleResourceSummary
	RemoveDuplicates           = removeDuplicates
)

var (
	GetListOfClustersWithDriftDetection = getListOfClustersWithDriftDetection
)
