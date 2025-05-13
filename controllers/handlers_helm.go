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

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"dario.cat/mergo"
	"github.com/Masterminds/semver/v3"
	dockerconfig "github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/credentials"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/helmpath"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/patcher"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

var (
	storage    = repo.File{}
	helmLogger = textlogger.NewLogger(textlogger.NewConfig())
)

const (
	writeFilePermission        = 0644
	lockTimeout                = 30
	notInstalledMessage        = "Not installed yet and action is uninstall"
	defaultMaxHistory          = 2
	defaultDeletionPropagation = "background"
)

type registryClientOptions struct {
	credentialsPath string
	caPath          string
	skipTLSVerify   bool
	plainHTTP       bool
	username        string
	password        string
	hostname        string
}

type releaseInfo struct {
	ReleaseName      string                 `json:"releaseName"`
	ReleaseNamespace string                 `json:"releaseNamespace"`
	Revision         string                 `json:"revision"`
	Updated          metav1.Time            `json:"updated"`
	Status           string                 `json:"status"`
	Chart            string                 `json:"chart"`
	ChartVersion     string                 `json:"chart_version"`
	AppVersion       string                 `json:"app_version"`
	ReleaseLabels    map[string]string      `json:"release_labels"`
	Icon             string                 `json:"icon"`
	Values           map[string]interface{} `json:"values"`
}

func deployHelmCharts(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndClusterClient(ctx, clusterNamespace, applicant, c, logger)
	if err != nil {
		return err
	}

	startInMgmtCluster := startDriftDetectionInMgmtCluster(o)
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err = deployDriftDetectionManagerInCluster(ctx, c, clusterNamespace, clusterName, applicant,
			clusterType, startInMgmtCluster, logger)
		if err != nil {
			return err
		}

		// Since we are updating resources to watch for drift, remove helm section in ResourceSummary to eliminate
		// un-needed reconciliation (Sveltos is updating those resources so we don't want drift-detection to think
		// a configuration drift is happening)
		err = deployResourceSummaryInCluster(ctx, c, clusterNamespace, clusterName, clusterSummary.Name, clusterType, nil, nil,
			[]libsveltosv1beta1.HelmResources{}, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to remove ResourceSummary.")
			return err
		}
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName))
	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger = logger.WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	kubeconfigContent, err := clusterproxy.GetSecretData(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	kubeconfig, closer, err := clusterproxy.CreateKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return err
	}
	defer closer()

	err = handleCharts(ctx, clusterSummary, c, remoteClient, kubeconfig, logger)
	if err != nil {
		return err
	}

	var helmResources []libsveltosv1beta1.HelmResources
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection ||
		clusterSummary.Spec.ClusterProfileSpec.Reloader {

		helmResources, err = collectResourcesFromManagedHelmChartsForDriftDetection(ctx, c, clusterSummary, kubeconfig, logger)
		if err != nil {
			return err
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Deploy resourceSummary
		err = deployResourceSummaryInCluster(ctx, c, clusterNamespace, clusterName, clusterSummary.Name,
			clusterType, nil, nil, helmResources, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions, logger)
		if err != nil {
			return err
		}
	}

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	// Update Reloader instance. If ClusterProfile Reloader knob is set to true, sveltos will
	// start a rolling upgrade for all Deployment/StatefulSet/DaemonSet instances deployed by Sveltos
	// in the managed cluster when a mounted ConfigMap/Secret is updated. In order to do so, sveltos-agent
	// needs to be instructed which Deployment/StatefulSet/DaemonSet instances require this behavior.
	// Update corresponding Reloader instance (instance will be deleted if Reloader is set to false)
	resources := convertHelmResourcesToObjectReference(helmResources)
	err = updateReloaderWithDeployedResources(ctx, c, profileOwnerRef, configv1beta1.FeatureHelm,
		resources, clusterSummary, logger)
	if err != nil {
		return err
	}

	cacheMgr := clustercache.GetManager()
	remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}
	return validateHealthPolicies(ctx, remoteRestConfig, clusterSummary, configv1beta1.FeatureHelm, logger)
}

func undeployHelmCharts(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1beta1.ClusterSummary{}
	err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: applicant}, clusterSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName))
	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger = logger.WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	logger.V(logs.LogDebug).Info("undeployHelmCharts")

	kubeconfigContent, err := clusterproxy.GetSecretData(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	kubeconfig, closer, err := clusterproxy.CreateKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return err
	}
	defer closer()

	return undeployHelmChartResources(ctx, c, clusterSummary, kubeconfig, logger)
}

func undeployHelmChartResources(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, logger logr.Logger) error {

	releaseReports, err := uninstallHelmCharts(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}

	// First get the helm releases currently managed and uninstall all the ones
	// not referenced anymore. Only if this operation succeeds, removes all stale
	// helm release registration for this clusterSummary.
	var undeployedReports []configv1beta1.ReleaseReport
	undeployedReports, err = undeployStaleReleases(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}
	releaseReports = append(releaseReports, undeployedReports...)

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	err = updateReloaderWithDeployedResources(ctx, c, profileOwnerRef, configv1beta1.FeatureKustomize,
		nil, clusterSummary, logger)
	if err != nil {
		return err
	}

	err = updateClusterConfiguration(ctx, c, clusterSummary, profileOwnerRef,
		configv1beta1.FeatureHelm, nil, []configv1beta1.Chart{})
	if err != nil {
		return err
	}

	err = updateClusterReportWithHelmReports(ctx, c, clusterSummary, releaseReports)
	if err != nil {
		return err
	}

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return err
	}

	// In dry-run mode nothing gets deployed/undeployed. So if this instance used to manage
	// an helm release and it is now not referencing anymore, do not unsubscribe.
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
		chartManager.RemoveStaleRegistrations(clusterSummary)
		return nil
	}

	return &configv1beta1.DryRunReconciliationError{}
}

func uninstallHelmCharts(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, logger logr.Logger) ([]configv1beta1.ReleaseReport, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	releaseReports := make([]configv1beta1.ReleaseReport, 0)
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		canManage, err := determineChartOwnership(ctx, c, clusterSummary, currentChart, logger)
		if err != nil {
			return nil, err
		}
		if canManage {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("Uninstalling chart %s from repo %s %s)",
				currentChart.ChartName,
				currentChart.RepositoryURL,
				currentChart.RepositoryName))

			// If another ClusterSummary is queued to manage this chart in this cluster, do not uninstall.
			// Let the other ClusterSummary take it over.
			otherRegisteredClusterSummaries := chartManager.GetRegisteredClusterSummariesForChart(
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
				clusterSummary.Spec.ClusterType, currentChart)
			if len(otherRegisteredClusterSummaries) > 1 {
				// Immediately unregister so next inline ClusterSummary can take this over
				chartManager.UnregisterClusterSummaryForChart(clusterSummary, currentChart)
				err = requeueAllOtherClusterSummaries(ctx, c, clusterSummary.Spec.ClusterNamespace,
					otherRegisteredClusterSummaries, logger)
				if err != nil {
					// TODO: Handle errors to prevent bad state. ClusterSummary no longer manage the chart,
					// but no other ClusterSummary instance has been requeued.
					return nil, err
				}
			} else {
				// If StopMatchingBehavior is LeavePolicies, do not uninstall helm charts
				if !clusterSummary.DeletionTimestamp.IsZero() &&
					clusterSummary.Spec.ClusterProfileSpec.StopMatchingBehavior == configv1beta1.LeavePolicies {

					logger.V(logs.LogInfo).Info("ClusterProfile StopMatchingBehavior set to LeavePolicies")
				} else {
					credentialsPath, caPath, err := getCredentialsAndCAFiles(ctx, c, clusterSummary, currentChart)
					if err != nil {
						logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to process credentials %v", err))
						return nil, err
					}

					registryOptions := &registryClientOptions{
						credentialsPath: credentialsPath, caPath: caPath,
						skipTLSVerify: getInsecureSkipTLSVerify(currentChart),
						plainHTTP:     getPlainHTTP(currentChart),
					}

					currentRelease, err := getReleaseInfo(currentChart.ReleaseName, currentChart.ReleaseNamespace,
						kubeconfig, registryOptions, getEnableClientCacheValue(currentChart.Options))
					if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
						return nil, err
					}

					if currentRelease != nil && currentRelease.Status != string(release.StatusUninstalled) {
						err = doUninstallRelease(ctx, clusterSummary, currentChart, kubeconfig, registryOptions, logger)
						if err != nil {
							if !errors.Is(err, driver.ErrReleaseNotFound) {
								return nil, err
							}
						}
					}
				}
			}

			releaseReports = append(releaseReports, configv1beta1.ReleaseReport{
				ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
				Action: string(configv1beta1.UninstallHelmAction),
			})
		} else {
			releaseReports = append(releaseReports, configv1beta1.ReleaseReport{
				ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
				Action: string(configv1beta1.NoHelmAction), Message: "Currently managed by another ClusterProfile",
			})
		}
	}

	return releaseReports, nil
}

func helmHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	clusterProfileSpecHash, err := getClusterProfileSpecHash(ctx, clusterSummaryScope.ClusterSummary)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	var config string
	config += string(clusterProfileSpecHash)

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterProfileSpec.HelmCharts == nil {
		return h.Sum(nil), nil
	}

	sortedHelmCharts := getSortedHelmCharts(clusterSummary)

	for i := range sortedHelmCharts {
		currentChart := &sortedHelmCharts[i]
		config += render.AsCode(*currentChart)

		if isReferencingFluxSource(currentChart) {
			sourceRef, repoPath, err := getReferencedFluxSourceFromURL(currentChart)
			if err != nil {
				return nil, err
			}
			source, err := getSource(ctx, c, sourceRef.Namespace, sourceRef.Name, sourceRef.Kind)
			if err != nil {
				return nil, err
			}
			if source == nil {
				continue
			}
			s := source.(sourcev1.Source)
			if s.GetArtifact() != nil {
				config += s.GetArtifact().Revision
				config += repoPath
			}
		}

		valueFromHash, err := getHelmReferenceResourceHash(ctx, c, clusterSummaryScope.ClusterSummary,
			currentChart, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(
				fmt.Sprintf("failed to get hash from referenced ConfigMap/Secret in ValuesFrom %v", err))
			return nil, err
		}

		config += valueFromHash
	}

	for i := range clusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		h := &clusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]
		if h.FeatureID == configv1beta1.FeatureHelm {
			config += render.AsCode(h)
		}
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getHelmReferenceResourceHash(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	helmChart *configv1beta1.HelmChart, logger logr.Logger) (string, error) {

	return getValuesFromResourceHash(ctx, c, clusterSummary, helmChart.ValuesFrom, logger)
}

func getHelmRefs(clusterSummary *configv1beta1.ClusterSummary) []configv1beta1.PolicyRef {
	return nil
}

func handleCharts(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	c, remoteClient client.Client, kubeconfig string, logger logr.Logger) error {

	mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		return err
	}

	// Before any helm release, managed by this ClusterSummary, is deployed, update ClusterSummary
	// Status. Order is important. If pod is restarted, it needs to rebuild internal state keeping
	// track of which ClusterSummary was managing which helm release.
	// Here only currently referenced helm releases are considered. If ClusterSummary was managing
	// an helm release and it is not referencing it anymore, such entry will be removed from ClusterSummary.Status
	// only after helm release is successfully undeployed.
	clusterSummary, _, err = updateStatusForReferencedHelmReleases(ctx, c, clusterSummary, mgmtResources, logger)
	if err != nil {
		return err
	}

	releaseReports, chartDeployed, deployError := walkChartsAndDeploy(ctx, c, clusterSummary, kubeconfig, mgmtResources, logger)
	// Even if there is a deployment error do not return just yet. Update various status and clean stale resources.

	// If there was an helm release previous managed by this ClusterSummary and currently not referenced
	// anymore, such helm release has been successfully remove at this point. So
	clusterSummary, err = updateStatusForNonReferencedHelmReleases(ctx, c, clusterSummary)
	if err != nil {
		return err
	}
	// First get the helm releases currently managed and uninstall all the ones
	// not referenced anymore. Only if this operation succeeds, removes all stale
	// helm release registration for this clusterSummary.
	var undeployedReports []configv1beta1.ReleaseReport
	undeployedReports, err = undeployStaleReleases(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}
	releaseReports = append(releaseReports, undeployedReports...)
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
		chartManager, mgrErr := chartmanager.GetChartManagerInstance(ctx, c)
		if mgrErr != nil {
			return mgrErr
		}

		chartManager.RemoveStaleRegistrations(clusterSummary)
	}

	err = updateChartsInClusterConfiguration(ctx, c, clusterSummary, chartDeployed, logger)
	if err != nil {
		return err
	}

	err = updateClusterReportWithHelmReports(ctx, c, clusterSummary, releaseReports)
	if err != nil {
		return err
	}
	// In DryRun mode always return an error.
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return &configv1beta1.DryRunReconciliationError{}
	}

	if deployError != nil {
		return deployError
	}

	return nil
}

// walkChartsAndDeploy walks all referenced helm charts. Deploys (install or upgrade) any chart
// this clusterSummary is registered to manage.
func walkChartsAndDeploy(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) ([]configv1beta1.ReleaseReport, []configv1beta1.Chart, error) {

	errorMsg := ""
	conflictErrorMessage := ""
	releaseReports := make([]configv1beta1.ReleaseReport, 0, len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts))
	chartDeployed := make([]configv1beta1.Chart, 0, len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts))
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]

		instantiatedChart, err := getInstantiatedChart(ctx, clusterSummary, currentChart, mgmtResources, logger)
		if err != nil {
			return releaseReports, chartDeployed, err
		}

		// Eventual conflicts are already resolved before this method is called (in updateStatusForeferencedHelmReleases)
		// So it is safe to call CanManageChart here
		canManage, err := canManageChart(ctx, c, clusterSummary, instantiatedChart, logger)
		if err != nil {
			return releaseReports, chartDeployed, err
		}

		if !canManage {
			var report *configv1beta1.ReleaseReport
			report, err = createReportForUnmanagedHelmRelease(ctx, c, clusterSummary, instantiatedChart, logger)
			if err != nil {
				return releaseReports, chartDeployed, err
			}

			releaseReports = append(releaseReports, *report)
			conflictErrorMessage += generateConflictForHelmChart(ctx, clusterSummary, instantiatedChart)
			// error is reported above, in updateStatusForReferencedHelmReleases.
			if clusterSummary.Spec.ClusterProfileSpec.ContinueOnConflict ||
				clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {

				continue
			}

			// for helm chart a conflict is a non retriable error.
			// when profile currently managing the helm chart is removed, all
			// conflicting profiles will be automatically reconciled.
			return releaseReports, chartDeployed,
				&NonRetriableError{Message: conflictErrorMessage}
		}

		var report *configv1beta1.ReleaseReport
		var currentRelease *releaseInfo
		currentRelease, report, err = handleChart(ctx, clusterSummary, mgmtResources, instantiatedChart, kubeconfig, logger)
		if err != nil {
			if clusterSummary.Spec.ClusterProfileSpec.ContinueOnError {
				errorMsg += fmt.Sprintf("chart: %s, release: %s, %v\n",
					instantiatedChart.ChartName, instantiatedChart.ReleaseName, err)
				continue
			}
			return releaseReports, chartDeployed, err
		}

		err = updateValueHashOnHelmChartSummary(ctx, instantiatedChart, clusterSummary, logger)
		if err != nil {
			return releaseReports, chartDeployed, err
		}

		releaseReports = append(releaseReports, *report)

		if currentRelease != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("release %s/%s (version %s) status: %s",
				currentRelease.ReleaseNamespace, currentRelease.ReleaseName, currentRelease.ChartVersion, currentRelease.Status))
			if currentRelease.Status == release.StatusDeployed.String() {
				// Deployed chart is used for updating ClusterConfiguration. There is no ClusterConfiguration for mgmt cluster
				chartDeployed = append(chartDeployed, configv1beta1.Chart{
					RepoURL:         instantiatedChart.RepositoryURL,
					Namespace:       currentRelease.ReleaseNamespace,
					ReleaseName:     currentRelease.ReleaseName,
					ChartVersion:    currentRelease.ChartVersion,
					AppVersion:      currentRelease.AppVersion,
					LastAppliedTime: &currentRelease.Updated,
					Icon:            currentRelease.Icon,
				})
			}
		}
	}

	// This has to come before conflictErrorMessage as conflictErrorMessage is not retriable
	// while any other generic error is
	if errorMsg != "" {
		return releaseReports, chartDeployed, fmt.Errorf("%s", errorMsg)
	}

	if conflictErrorMessage != "" {
		// for helm chart a conflict is a non retriable error.
		// when profile currently managing the helm chart is removed, all
		// conflicting profiles will be automatically reconciled.
		return releaseReports, chartDeployed, &NonRetriableError{Message: conflictErrorMessage}
	}

	return releaseReports, chartDeployed, nil
}

func generateConflictForHelmChart(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary, currentChart *configv1beta1.HelmChart) string {
	c := getManagementClusterClient()

	message := fmt.Sprintf("cannot manage chart %s/%s.", currentChart.ReleaseNamespace, currentChart.ReleaseName)
	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return message
	}

	var managerName string
	managerName, err = chartManager.GetManagerForChart(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, currentChart)
	if err != nil {
		return message
	}
	message += fmt.Sprintf(" ClusterSummary %s managing it.\n", managerName)
	return message
}

// determineChartOwnership determines whether the provided cluster summary (`claimingHelmManager`) has the authority to manage
// the given Helm chart (`currentChart`).
//
// It achieves this by first checking if any other cluster summary is currently responsible for managing the chart.
// If no other manager exists, then this cluster summary is eligible and the function returns true.
//
// However, if another cluster summary is already managing the chart, a tier-based comparison is performed to determine
// which cluster summary has higher ownership priority.
// The cluster summary with the higher tier level is authorized to manage the chart.
//
// If this cluster summary wins the ownership competition, the previously managing cluster summary is re-queued for
// reconciliation to ensure it updates its state.
func determineChartOwnership(ctx context.Context, c client.Client, claimingHelmManager *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, logger logr.Logger) (canManage bool, err error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return false, err
	}

	l := logger.WithValues("release", fmt.Sprintf("%s:%s", currentChart.ReleaseNamespace, currentChart.ReleaseName))

	if !chartManager.CanManageChart(claimingHelmManager, currentChart) {
		// Another ClusterSummay is already managing this chart. Get the:
		// 1. ClusterSummary managing the chart
		// 2. Use the ClusterSummary's tiers to decide who should managed it
		l.V(logs.LogDebug).Info("conflict detected")
		clusterSummaryManaging, err := chartManager.GetManagerForChart(claimingHelmManager.Spec.ClusterNamespace, claimingHelmManager.Spec.ClusterName,
			claimingHelmManager.Spec.ClusterType, currentChart)
		if err != nil {
			return false, err
		}

		currentHelmManager := &configv1beta1.ClusterSummary{}
		err = c.Get(ctx, types.NamespacedName{Namespace: claimingHelmManager.Spec.ClusterNamespace, Name: clusterSummaryManaging}, currentHelmManager)
		if err != nil {
			l.V(logs.LogDebug).Info(fmt.Sprintf("failed to fetch ClusterSummary %s/%s currently managing the chart",
				claimingHelmManager.Spec.ClusterNamespace, clusterSummaryManaging))
			return false, err
		}

		if hasHigherOwnershipPriority(currentHelmManager.Spec.ClusterProfileSpec.Tier, claimingHelmManager.Spec.ClusterProfileSpec.Tier) {
			if claimingHelmManager.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
				// Since we are in DryRun mode do not reset the other ClusterSummary. It will still be managing
				// the helm chart
				return true, nil
			}
			// New ClusterSummary is taking over managing this chart. So reset helmReleaseSummaries for this chart
			// This needs to happen immediately. helmReleaseSummaries are used by Sveltos to rebuild list of which
			// clusterSummary is managing an helm chart if pod restarts
			err = resetHelmReleaseSummaries(ctx, c, currentHelmManager, currentChart, logger)
			if err != nil {
				return false, err
			}

			// Reset Status of the ClusterSummary previously managing this resource
			err = requeueClusterSummary(ctx, configv1beta1.FeatureHelm, currentHelmManager, logger)
			if err != nil {
				return false, err
			}

			// Set current ClusterSummary as the new manager
			chartManager.SetManagerForChart(claimingHelmManager, currentChart)
			return true, nil
		}
		return false, nil
	}

	// Nobody else is managing the chart
	return true, nil
}

func resetHelmReleaseSummaries(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, logger logr.Logger) error {

	for i := range clusterSummary.Status.HelmReleaseSummaries {
		hr := clusterSummary.Status.HelmReleaseSummaries[i]
		if hr.ReleaseNamespace == currentChart.ReleaseNamespace &&
			hr.ReleaseName == currentChart.ReleaseName {

			logger.V(logs.LogDebug).Info("removing entry for chart %s/%s in ClusterSummary..Status.HelmReleaseSummaries %s/%s",
				currentChart.ReleaseNamespace, currentChart.ReleaseName, clusterSummary.Namespace, clusterSummary.Name)
			length := len(clusterSummary.Status.HelmReleaseSummaries)
			if i < length-1 {
				clusterSummary.Status.HelmReleaseSummaries[i] = clusterSummary.Status.HelmReleaseSummaries[length-1]
			}
			clusterSummary.Status.HelmReleaseSummaries = clusterSummary.Status.HelmReleaseSummaries[:length-1]
			break
		}
	}

	return c.Status().Update(ctx, clusterSummary)
}

func handleInstall(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, currentChart *configv1beta1.HelmChart, kubeconfig string,
	registryOptions *registryClientOptions, logger logr.Logger) (*configv1beta1.ReleaseReport, error) {

	var report *configv1beta1.ReleaseReport
	logger.V(logs.LogDebug).Info("install helm release")
	err := doInstallRelease(ctx, clusterSummary, mgmtResources, currentChart, kubeconfig, registryOptions, logger)
	if err != nil {
		return nil, err
	}
	report = &configv1beta1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: string(configv1beta1.InstallHelmAction),
	}
	return report, nil
}

func handleUpgrade(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, currentChart *configv1beta1.HelmChart,
	currentRelease *releaseInfo, kubeconfig string, registryOptions *registryClientOptions,
	logger logr.Logger) (*configv1beta1.ReleaseReport, error) {

	var report *configv1beta1.ReleaseReport
	logger.V(logs.LogDebug).Info("upgrade helm release")
	err := doUpgradeRelease(ctx, clusterSummary, mgmtResources, currentChart, kubeconfig, registryOptions, logger)
	if err != nil {
		return nil, err
	}
	var message string
	current, err := semver.NewVersion(currentRelease.ChartVersion)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get semantic version. Err: %v", err))
		return nil, err
	}
	expected, err := semver.NewVersion(currentChart.ChartVersion)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get semantic version. Err: %v", err))
		return nil, err
	}
	if current.Compare(expected) != 0 {
		message = fmt.Sprintf("Current version: %q. Would move to version: %q",
			currentRelease.ChartVersion, currentChart.ChartVersion)
	} else {
		message = fmt.Sprintf("No op, already at version: %s", currentRelease.ChartVersion)
	}
	report = &configv1beta1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: string(configv1beta1.UpgradeHelmAction),
		Message: message,
	}
	return report, nil
}

func handleUninstall(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, kubeconfig string, registryOptions *registryClientOptions,
	logger logr.Logger) (*configv1beta1.ReleaseReport, error) {

	var report *configv1beta1.ReleaseReport
	logger.V(logs.LogDebug).Info("uninstall helm release")
	err := doUninstallRelease(ctx, clusterSummary, currentChart, kubeconfig, registryOptions, logger)
	if err != nil {
		return nil, err
	}
	report = &configv1beta1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		Action: string(configv1beta1.UninstallHelmAction),
	}
	return report, nil
}

func createRegistryClientOptions(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, logger logr.Logger) (*registryClientOptions, error) {

	registryOptions := &registryClientOptions{}
	if currentChart.RegistryCredentialsConfig == nil {
		return registryOptions, nil
	}

	credentialsPath, caPath, err := getCredentialsAndCAFiles(ctx, getManagementClusterClient(), clusterSummary,
		currentChart)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to process credentials %v", err))
		return registryOptions, err
	}

	registryOptions.credentialsPath = credentialsPath
	registryOptions.caPath = caPath
	registryOptions.plainHTTP = getPlainHTTP(currentChart)
	registryOptions.skipTLSVerify = getInsecureSkipTLSVerify(currentChart)

	if currentChart.RegistryCredentialsConfig.CredentialsSecretRef != nil {
		credentialSecretNamespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, getManagementClusterClient(),
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			currentChart.RegistryCredentialsConfig.CredentialsSecretRef.Namespace, clusterSummary.Spec.ClusterType)
		if err != nil {
			return nil, err
		}

		secret := &corev1.Secret{}
		err = getManagementClusterClient().Get(ctx,
			types.NamespacedName{
				Namespace: credentialSecretNamespace,
				Name:      currentChart.RegistryCredentialsConfig.CredentialsSecretRef.Name,
			},
			secret)
		if err != nil {
			return registryOptions, err
		}

		username, password, hostname, err := getUsernameAndPasswordFromSecret(currentChart.RepositoryURL, secret)
		if err != nil {
			return registryOptions, err
		}

		registryOptions.username = username
		registryOptions.password = password
		registryOptions.hostname = hostname
	}

	return registryOptions, nil
}

func handleChart(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, currentChart *configv1beta1.HelmChart,
	kubeconfig string, logger logr.Logger) (*releaseInfo, *configv1beta1.ReleaseReport, error) {

	registryOptions, err := createRegistryClientOptions(ctx, clusterSummary, currentChart, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse credentials info: %v", err))
		return nil, nil, err
	}

	if registryOptions.credentialsPath != "" {
		defer os.Remove(registryOptions.credentialsPath)
	}
	if registryOptions.caPath != "" {
		defer os.Remove(registryOptions.caPath)
	}

	currentRelease, err := getReleaseInfo(currentChart.ReleaseName,
		currentChart.ReleaseNamespace, kubeconfig, registryOptions, getEnableClientCacheValue(currentChart.Options))
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil, err
	}
	var report *configv1beta1.ReleaseReport

	logger = logger.WithValues("releaseNamespace", currentChart.ReleaseNamespace, "releaseName",
		currentChart.ReleaseName, "version", currentChart.ChartVersion)

	if currentChart.RegistryCredentialsConfig != nil {
		err = doLogin(registryOptions, currentChart.ReleaseNamespace, currentChart.RepositoryURL)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to login %v", err))
			return nil, nil, err
		}
	}

	if shouldInstall(currentRelease, currentChart) {
		report, err = handleInstall(ctx, clusterSummary, mgmtResources, currentChart, kubeconfig,
			registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
	} else if shouldUpgrade(ctx, currentRelease, currentChart, clusterSummary, logger) {
		report, err = handleUpgrade(ctx, clusterSummary, mgmtResources, currentChart, currentRelease, kubeconfig,
			registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
	} else if shouldUninstall(currentRelease, currentChart) {
		report, err = handleUninstall(ctx, clusterSummary, currentChart, kubeconfig, registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
		report.ChartVersion = currentRelease.ChartVersion
	} else if currentRelease == nil {
		logger.V(logs.LogDebug).Info("no action for helm release")
		report = &configv1beta1.ReleaseReport{
			ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
			ChartVersion: currentChart.ChartVersion, Action: string(configv1beta1.NoHelmAction),
		}
		report.Message = notInstalledMessage
	} else {
		logger.V(logs.LogDebug).Info("no action for helm release")

		report, err = generateReportForSameVersion(ctx, currentRelease.Values, clusterSummary, mgmtResources, currentChart,
			kubeconfig, registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
	}

	if currentRelease != nil {
		err = addExtraMetadata(ctx, currentChart, clusterSummary, kubeconfig, registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
	}

	currentRelease, err = getReleaseInfo(currentChart.ReleaseName, currentChart.ReleaseNamespace, kubeconfig,
		registryOptions, getEnableClientCacheValue(currentChart.Options))
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil, err
	}

	return currentRelease, report, nil
}

// repoAddOrUpdate adds/updates repo with given name and url
func repoAddOrUpdate(settings *cli.EnvSettings, name, repoURL string, registryOptions *registryClientOptions,
	logger logr.Logger) error {

	logger = logger.WithValues("repoURL", repoURL, "repoName", name)

	entry := &repo.Entry{Name: name, URL: repoURL, Username: registryOptions.username, Password: registryOptions.password,
		InsecureSkipTLSverify: registryOptions.skipTLSVerify}
	chartRepo, err := repo.NewChartRepository(entry, getter.All(settings))
	if err != nil {
		return err
	}

	chartRepo.CachePath = settings.RepositoryCache

	if storage.Has(entry.Name) {
		logger.V(logs.LogDebug).Info("repository name already exists")
		return nil
	}

	if !registry.IsOCI(entry.URL) {
		logger.V(logs.LogInfo).Info("non OCI. Download index file.")
		_, err = chartRepo.DownloadIndexFile()
		if err != nil {
			// Check if error is "no chart version found for"
			if strings.Contains(err.Error(), "no chart version found for") {
				logger.V(logs.LogInfo).Info("Chart version not found, cleaning repository cache", "repo", entry.Name)

				// Create the cache path for this repository
				repoCache := filepath.Join(settings.RepositoryCache, helmpath.CacheIndexFile(entry.Name))

				// Remove the cache file
				if err := os.Remove(repoCache); err != nil {
					logger.V(logs.LogDebug).Error(err, "Failed to remove repository cache")
				}

				// Try downloading again after clearing cache
				_, err = chartRepo.DownloadIndexFile()
				if err != nil {
					return err
				}
				return nil
			}

			return err
		}
	}

	storage.Update(entry)
	const permissions = 0o644

	err = storage.WriteFile(settings.RepositoryConfig, permissions)
	if err != nil {
		return err
	}

	return nil
}

// installRelease installs helm release in the CAPI cluster.
// No action in DryRun mode.
func installRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary, settings *cli.EnvSettings,
	requestedChart *configv1beta1.HelmChart, kubeconfig string, registryOptions *registryClientOptions,
	values map[string]interface{}, mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (map[string]interface{}, error) {

	if !isReferencingFluxSource(requestedChart) && requestedChart.ChartName == "" {
		return nil, fmt.Errorf("chart name can not be empty")
	}

	logger = logger.WithValues("release", requestedChart.ReleaseName, "releaseNamespace",
		requestedChart.ReleaseNamespace, "chart", requestedChart.ChartName, "chartVersion", requestedChart.ChartVersion)

	tmpDir, chartName, repoURL, err := getHelmChartAndRepoName(ctx, requestedChart, logger)
	if err != nil {
		return nil, err
	}

	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
		chartName = filepath.Join(tmpDir, chartName)
	}

	logger = logger.WithValues("repositoryURL", repoURL, "chart", chartName)
	logger = logger.WithValues("credentials", registryOptions.credentialsPath, "ca",
		registryOptions.caPath, "insecure", registryOptions.skipTLSVerify)
	logger.V(logs.LogDebug).Info("installing release")

	patches, err := initiatePatches(ctx, clusterSummary, requestedChart.ChartName, mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	installClient, err := getHelmInstallClient(requestedChart, kubeconfig, registryOptions, patches)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get helm install client: %v", err))
		return nil, err
	}

	cp, err := installClient.ChartPathOptions.LocateChart(chartName, settings)
	if err != nil {
		logger.V(logs.LogDebug).Info("LocateChart failed")
		return nil, err
	}

	chartRequested, err := loader.Load(cp)
	if err != nil {
		logger.V(logs.LogDebug).Info("Load failed")
		return nil, err
	}

	validInstallableChart := isChartInstallable(chartRequested)
	if !validInstallableChart {
		return nil, fmt.Errorf("chart is not installable")
	}

	if getDependenciesUpdateValue(requestedChart.Options) {
		err = checkDependencies(chartRequested, installClient, cp, settings)
		if err != nil {
			return nil, err
		}
	}

	// Reload the chart with the updated Chart.lock file.
	if chartRequested, err = loader.Load(cp); err != nil {
		return nil, fmt.Errorf("%w: failed reloading chart after repo update", err)
	}

	installClient.DryRun = false
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		installClient.DryRun = true
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, getTimeoutValue(requestedChart.Options).Duration)
	defer cancel()

	r, err := installClient.RunWithContext(ctxWithTimeout, chartRequested, values)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to install: %v", err))
		// This condition should never occur.  A previous check ensures that only one
		// ClusterProfile/Profile can manage a Helm Chart with a given name in a
		// specific namespace within a managed cluster.  If this code is reached,
		// that check has already passed.  Therefore, the "cannot re-use a name that
		// is still in use" error should be impossible.
		// There is no constant defined in the helm library but this is an error seen more than once.
		if err.Error() == "cannot re-use a name that is still in use" {
			return nil, upgradeRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig, registryOptions,
				values, mgmtResources, logger)
		}
		return nil, err
	}

	logger.V(logs.LogDebug).Info("installing release done")

	return r.Config, nil
}

func checkDependencies(chartRequested *chart.Chart, installClient *action.Install, cp string, settings *cli.EnvSettings) error {
	if req := chartRequested.Metadata.Dependencies; req != nil {
		err := action.CheckDependencies(chartRequested, req)
		if err != nil {
			if installClient.DependencyUpdate {
				man := &downloader.Manager{
					ChartPath:        cp,
					Keyring:          installClient.ChartPathOptions.Keyring,
					SkipUpdate:       false,
					Getters:          getter.All(settings),
					RepositoryConfig: settings.RepositoryConfig,
					RepositoryCache:  settings.RepositoryCache,
				}
				err = man.Update()
				if err != nil {
					return err
				}
			} else {
				return nil
			}
		}
	}

	return nil
}

// uninstallRelease removes helm release from a CAPI Cluster.
// No action in DryRun mode.
func uninstallRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	releaseName, releaseNamespace, kubeconfig string, registryOptions *registryClientOptions,
	helmChart *configv1beta1.HelmChart, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	cluster, err := clusterproxy.GetCluster(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if !cluster.GetDeletionTimestamp().IsZero() {
		// if cluster is marked for deletion, no need to worry about removing helm charts deployed
		// there.
		return nil
	}

	logger = logger.WithValues("release", releaseName, "releaseNamespace", releaseNamespace)
	logger.V(logs.LogDebug).Info("uninstalling release")

	enableClientCache := false
	if helmChart != nil {
		enableClientCache = getEnableClientCacheValue(helmChart.Options)
	}

	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig, registryOptions, enableClientCache)
	if err != nil {
		return err
	}

	uninstallClient := getHelmUninstallClient(helmChart, actionConfig)

	_, err = uninstallClient.Run(releaseName)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("uninstalling release done")

	return nil
}

// upgradeRelease upgrades helm release in managed cluster.
// No action in DryRun mode.
func upgradeRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	settings *cli.EnvSettings, requestedChart *configv1beta1.HelmChart,
	kubeconfig string, registryOptions *registryClientOptions, values map[string]interface{},
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	if !isReferencingFluxSource(requestedChart) && requestedChart.ChartName == "" {
		return fmt.Errorf("chart name can not be empty")
	}

	logger = logger.WithValues("release", requestedChart.ReleaseName, "releaseNamespace", requestedChart.ReleaseNamespace,
		"chartVersion", requestedChart.ChartVersion)

	tmpDir, chartName, repoURL, err := getHelmChartAndRepoName(ctx, requestedChart, logger)
	if err != nil {
		return err
	}
	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
		chartName = filepath.Join(tmpDir, chartName)
	}

	logger = logger.WithValues("repositoryURL", repoURL, "chart", chartName)
	logger = logger.WithValues("credentials", registryOptions.credentialsPath, "ca",
		registryOptions.caPath, "insecure", registryOptions.skipTLSVerify)
	logger.V(logs.LogDebug).Info("upgrading release")

	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig, registryOptions,
		getEnableClientCacheValue(requestedChart.Options))
	if err != nil {
		return err
	}

	driftExclusionPatches := transformDriftExclusionsToPatches(clusterSummary.Spec.ClusterProfileSpec.DriftExclusions)

	patches, err := initiatePatches(ctx, clusterSummary, requestedChart.ChartName, mgmtResources, logger)
	if err != nil {
		return err
	}

	patches = append(patches, driftExclusionPatches...)

	upgradeClient := getHelmUpgradeClient(requestedChart, actionConfig, registryOptions, patches)

	cp, err := upgradeClient.ChartPathOptions.LocateChart(chartName, settings)
	if err != nil {
		return err
	}

	chartRequested, err := loader.Load(cp)
	if err != nil {
		return err
	}
	if req := chartRequested.Metadata.Dependencies; req != nil {
		err = action.CheckDependencies(chartRequested, req)
		if err != nil {
			return err
		}
	}

	upgradeClient.DryRun = false

	err = upgradeCRDs(ctx, requestedChart, kubeconfig, chartRequested.CRDObjects(), logger)
	if err != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to upgrade crds: %v", err))
		return err
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, getTimeoutValue(requestedChart.Options).Duration)
	defer cancel()

	_, err = upgradeClient.RunWithContext(ctxWithTimeout, requestedChart.ReleaseName, chartRequested, values)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to upgrade: %v", err))
		currentRelease, err := getCurrentRelease(requestedChart.ReleaseName, requestedChart.ReleaseNamespace,
			kubeconfig, registryOptions, getEnableClientCacheValue(requestedChart.Options))
		if err == nil && currentRelease.Info.Status.IsPending() {
			// This error: "another operation (install/upgrade/rollback) is in progress"
			// With Sveltos this error should never happen. A previous check ensures that only one
			// ClusterProfile/Profile can manage a Helm Chart with a given name in a specific namespace within
			// a managed cluster.
			// Ignore the recoverRelease result. Always return an error as we must install this release back
			_ = recoverRelease(ctx, clusterSummary, requestedChart, kubeconfig, registryOptions, logger)
			return fmt.Errorf("tried recovering by uininstalling first")
		}
		return err
	}

	logger.V(logs.LogDebug).Info("upgrading release done")

	return nil
}

func upgradeCRDsInFile(ctx context.Context, dr dynamic.ResourceInterface, chartFile *chart.File,
	logger logr.Logger) (int, error) {

	crds, err := getUnstructured(chartFile.Data, logger)
	if err != nil {
		return 0, err
	}

	forceConflict := true
	options := metav1.PatchOptions{
		FieldManager: "application/apply-patch",
		Force:        &forceConflict,
	}

	upgradedCRDs := 0

	for i := range crds {
		crdData, err := runtime.Encode(unstructured.UnstructuredJSONScheme, crds[i])
		if err != nil {
			return upgradedCRDs, err
		}

		_, err = dr.Patch(ctx, crds[i].GetName(), types.ApplyPatchType, crdData, options)
		if err != nil {
			return upgradedCRDs, err
		}

		upgradedCRDs += 1
	}

	return upgradedCRDs, nil
}

// upgradeCRDs upgrades CRDs
func upgradeCRDs(ctx context.Context, requestedChart *configv1beta1.HelmChart, kubeconfig string,
	crds []chart.CRD, logger logr.Logger) error {

	if !getUpgradeCRDs(requestedChart.Options) {
		return nil
	}

	logger.V(logs.LogDebug).Info("upgrade crds")

	destConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}

	dr, err := k8s_utils.GetDynamicResourceInterface(destConfig, apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"), "")
	if err != nil {
		return err
	}

	upgradedCRDs := 0

	// We do these one file at a time in the order they were read.
	for _, obj := range crds {
		tmpUpgradedCRDs, err := upgradeCRDsInFile(ctx, dr, obj.File, logger)
		if err != nil {
			return err
		}

		upgradedCRDs += tmpUpgradedCRDs
	}

	// Give time for the CRD to be recognized.
	if upgradedCRDs > 0 {
		const waitForCRDs = 30
		time.Sleep(waitForCRDs * time.Second)
	}
	logger.V(logs.LogDebug).Info(fmt.Sprintf("CRDs upgraded: %d", upgradedCRDs))
	return nil
}

func debugf(format string, v ...interface{}) {
	helmLogger.V(logs.LogDebug).Info(fmt.Sprintf(format, v...))
}

func getRegistryClient(namespace string, registryOptions *registryClientOptions, enableClientCache bool,
) (*registry.Client, error) {

	settings := getSettings(namespace, registryOptions)

	if registryOptions.caPath != "" || registryOptions.skipTLSVerify {
		registryClient, err :=
			newRegistryClientWithTLS("", "", registryOptions.caPath, registryOptions.skipTLSVerify, settings)
		if err != nil {
			return nil, err
		}
		return registryClient, nil
	}

	registryClient, err := newDefaultRegistryClient(registryOptions.plainHTTP, settings, enableClientCache)
	if err != nil {
		return nil, err
	}
	return registryClient, nil
}

func newDefaultRegistryClient(plainHTTP bool, settings *cli.EnvSettings,
	enableClientCache bool) (*registry.Client, error) {

	opts := []registry.ClientOption{
		registry.ClientOptDebug(settings.Debug),
		registry.ClientOptEnableCache(enableClientCache),
		registry.ClientOptWriter(os.Stderr),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
	}
	if plainHTTP {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}

	// Create a new registry client
	registryClient, err := registry.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return registryClient, nil
}

func newRegistryClientWithTLS(certFile, keyFile, caFile string, insecureSkipTLSverify bool,
	settings *cli.EnvSettings) (*registry.Client, error) {

	// Create a new registry client
	registryClient, err := registry.NewRegistryClientWithTLS(os.Stderr, certFile, keyFile, caFile, insecureSkipTLSverify,
		settings.RegistryConfig, settings.Debug)
	if err != nil {
		return nil, err
	}
	return registryClient, nil
}

func actionConfigInit(namespace, kubeconfig string, registryOptions *registryClientOptions,
	enableClientCache bool) (*action.Configuration, error) {

	actionConfig := new(action.Configuration)

	configFlags := genericclioptions.NewConfigFlags(false)
	configFlags.KubeConfig = &kubeconfig
	configFlags.Namespace = &namespace
	insecure := registryOptions.skipTLSVerify
	configFlags.Insecure = &insecure
	// Use a 5m timeout
	timeout := "5m"
	configFlags.Timeout = &timeout

	err := actionConfig.Init(configFlags, namespace, "secret", debugf)
	if err != nil {
		return nil, err
	}

	registryClient, err := getRegistryClient(namespace, registryOptions, enableClientCache)
	if err != nil {
		return nil, err
	}

	actionConfig.RegistryClient = registryClient

	return actionConfig, nil
}

func isChartInstallable(ch *chart.Chart) bool {
	switch ch.Metadata.Type {
	case "", "application":
		return true
	}

	return false
}

func getCurrentRelease(releaseName, releaseNamespace, kubeconfig string, registryOptions *registryClientOptions,
	enableClientCache bool) (*release.Release, error) {

	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig, registryOptions, enableClientCache)

	if err != nil {
		return nil, err
	}

	statusObject := action.NewStatus(actionConfig)
	return statusObject.Run(releaseName)
}

func getReleaseInfo(releaseName, releaseNamespace, kubeconfig string, registryOptions *registryClientOptions,
	enableClientCache bool) (*releaseInfo, error) {

	currentRelease, err := getCurrentRelease(releaseName, releaseNamespace, kubeconfig, registryOptions, enableClientCache)
	if err != nil {
		return nil, err
	}

	element := &releaseInfo{
		ReleaseName:      currentRelease.Name,
		ReleaseNamespace: currentRelease.Namespace,
		Revision:         strconv.Itoa(currentRelease.Version),
		Status:           currentRelease.Info.Status.String(),
		Chart:            currentRelease.Chart.Metadata.Name,
		ChartVersion:     currentRelease.Chart.Metadata.Version,
		AppVersion:       currentRelease.Chart.AppVersion(),
		ReleaseLabels:    currentRelease.Labels,
		Icon:             currentRelease.Chart.Metadata.Icon,
		Values:           currentRelease.Config,
	}

	var t metav1.Time
	if lastDeployed := currentRelease.Info.LastDeployed; !lastDeployed.IsZero() {
		t = metav1.Time{Time: lastDeployed.Time}
	}
	element.Updated = t

	return element, nil
}

func recoverRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	requestedChart *configv1beta1.HelmChart, kubeconfig string, registryOptions *registryClientOptions,
	logger logr.Logger) error {

	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig, registryOptions, true)
	if err != nil {
		return err
	}

	statusObject := action.NewStatus(actionConfig)
	lastRelease, err := statusObject.Run(requestedChart.ReleaseName)
	if err != nil {
		return err
	}

	if !lastRelease.Info.Status.IsPending() {
		return fmt.Errorf("not in the expected status to unlock")
	}

	return uninstallRelease(ctx, clusterSummary, requestedChart.ReleaseName,
		requestedChart.ReleaseNamespace, kubeconfig, registryOptions, requestedChart, logger)
}

// shouldInstall returns true if action is not uninstall and either there
// is no installed or version, or version is same requested by customer but status is
// not yet deployed
func shouldInstall(currentRelease *releaseInfo, requestedChart *configv1beta1.HelmChart) bool {
	if requestedChart.HelmChartAction == configv1beta1.HelmChartActionUninstall {
		return false
	}

	// If the release was uninstalled with KeepHistory flag, this is the
	// status seen at this point. Release should be installed in this state.
	// Upgrade would fail
	if currentRelease != nil &&
		currentRelease.Status == release.StatusUninstalled.String() {

		return true
	}

	if currentRelease != nil &&
		currentRelease.ChartVersion != requestedChart.ChartVersion {

		return false
	}

	if currentRelease != nil &&
		currentRelease.Status == release.StatusDeployed.String() {

		return false
	}

	return true
}

// shouldUpgrade returns true if action is not uninstall and current installed chart is different
// than what currently requested by customer
func shouldUpgrade(ctx context.Context, currentRelease *releaseInfo, requestedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) bool {

	if requestedChart.HelmChartAction == configv1beta1.HelmChartActionUninstall {
		return false
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeContinuousWithDriftDetection {
		if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
			// In DryRun mode, if values are different, report will be generated
			oldValueHash := getValueHashFromHelmChartSummary(requestedChart, clusterSummary)

			// If Values configuration has changed, trigger an upgrade
			c := getManagementClusterClient()
			currentValueHash, err := getHelmChartValuesHash(ctx, c, requestedChart, clusterSummary, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get current values hash: %v", err))
				currentValueHash = []byte("") // force upgrade
			}
			if !reflect.DeepEqual(oldValueHash, currentValueHash) {
				return true
			}
		}

		// With drift detection mode, there is reconciliation due to configuration drift even
		// when version is same. So skip this check in SyncModeContinuousWithDriftDetection
		if currentRelease != nil {
			if currentRelease.Status != release.StatusDeployed.String() {
				return true
			}

			current, err := semver.NewVersion(currentRelease.ChartVersion)
			if err != nil {
				return true
			}

			expected, err := semver.NewVersion(requestedChart.ChartVersion)
			if err != nil {
				return true
			}

			return !current.Equal(expected)
		}
	}

	return true
}

// shouldUninstall returns true if action is uninstall there is a release installed currently
func shouldUninstall(currentRelease *releaseInfo, requestedChart *configv1beta1.HelmChart) bool {
	if currentRelease == nil {
		return false
	}

	if currentRelease.Status == string(release.StatusUninstalled) {
		return false
	}

	if requestedChart.HelmChartAction != configv1beta1.HelmChartActionUninstall {
		return false
	}

	return true
}

// doInstallRelease installs helm release in the CAPI Cluster.
// No action in DryRun mode.
func doInstallRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, requestedChart *configv1beta1.HelmChart,
	kubeconfig string, registryOptions *registryClientOptions, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Installing chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	settings := getSettings(requestedChart.ReleaseNamespace, registryOptions)

	if !isReferencingFluxSource(requestedChart) {
		err := repoAddOrUpdate(settings, requestedChart.RepositoryName,
			requestedChart.RepositoryURL, registryOptions, logger)
		if err != nil {
			return err
		}
	}

	var values chartutil.Values
	values, err := getInstantiatedValues(ctx, clusterSummary, mgmtResources, requestedChart, logger)
	if err != nil {
		return err
	}

	_, err = installRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig, registryOptions,
		values, mgmtResources, logger)
	if err != nil {
		return err
	}

	return nil
}

// doUninstallRelease uninstalls helm release from the CAPI Cluster.
// No action in DryRun mode.
func doUninstallRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	requestedChart *configv1beta1.HelmChart, kubeconfig string, registryOptions *registryClientOptions,
	logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Uninstalling chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	return uninstallRelease(ctx, clusterSummary, requestedChart.ReleaseName, requestedChart.ReleaseNamespace,
		kubeconfig, registryOptions, requestedChart, logger)
}

// doUpgradeRelease upgrades helm release in the CAPI Cluster.
// No action in DryRun mode.
func doUpgradeRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, requestedChart *configv1beta1.HelmChart,
	kubeconfig string, registryOptions *registryClientOptions, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Upgrading chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	settings := getSettings(requestedChart.ReleaseNamespace, registryOptions)

	if !isReferencingFluxSource(requestedChart) {
		err := repoAddOrUpdate(settings, requestedChart.RepositoryName,
			requestedChart.RepositoryURL, registryOptions, logger)
		if err != nil {
			return err
		}
	}

	var values chartutil.Values
	values, err := getInstantiatedValues(ctx, clusterSummary, mgmtResources, requestedChart, logger)
	if err != nil {
		return err
	}

	err = upgradeRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig, registryOptions,
		values, mgmtResources, logger)
	if err != nil {
		return err
	}

	return nil
}

// updateChartsInClusterConfiguration updates deployed chart info on ClusterConfiguration
// No action in DryRun mode.
func updateChartsInClusterConfiguration(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	chartDeployed []configv1beta1.Chart, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("update deployed chart info. Number of deployed helm chart: %d",
		len(chartDeployed)))
	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	return updateClusterConfiguration(ctx, c, clusterSummary, profileOwnerRef, configv1beta1.FeatureHelm, nil, chartDeployed)
}

// undeployStaleReleases uninstalls all helm charts previously managed and not referenced anyomre
func undeployStaleReleases(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, logger logr.Logger) ([]configv1beta1.ReleaseReport, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	managedHelmReleases := chartManager.GetManagedHelmReleases(clusterSummary)

	// Build map of current referenced helm charts
	currentlyReferencedReleases := make(map[string]bool)
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		currentlyReferencedReleases[chartManager.GetReleaseKey(currentChart.ReleaseNamespace, currentChart.ReleaseName)] = true
	}

	reports := make([]configv1beta1.ReleaseReport, 0)

	for i := range managedHelmReleases {
		releaseKey := chartManager.GetReleaseKey(managedHelmReleases[i].Namespace, managedHelmReleases[i].Name)
		if _, ok := currentlyReferencedReleases[releaseKey]; !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("helm release %s (namespace %s) used to be managed but not referenced anymore",
				managedHelmReleases[i].Name, managedHelmReleases[i].Namespace))

			_, err := getReleaseInfo(managedHelmReleases[i].Name,
				managedHelmReleases[i].Namespace, kubeconfig, &registryClientOptions{}, false)
			if err != nil {
				if errors.Is(err, driver.ErrReleaseNotFound) {
					continue
				}
				return nil, err
			}

			if err := uninstallRelease(ctx, clusterSummary, managedHelmReleases[i].Name,
				managedHelmReleases[i].Namespace, kubeconfig, &registryClientOptions{}, nil,
				logger); err != nil {
				return nil, err
			}

			reports = append(reports, configv1beta1.ReleaseReport{
				ReleaseNamespace: managedHelmReleases[i].Namespace, ReleaseName: managedHelmReleases[i].Name,
				Action: string(configv1beta1.UninstallHelmAction),
			})
		}
	}

	return reports, nil
}

// updateStatusForeferencedHelmReleases considers helm releases ClusterSummary currently
// references. For each of those helm releases, adds an entry in ClusterSummary.Status reporting
// whether such helm release is managed by this ClusterSummary or not.
// This method also returns:
// - an error if any occurs
// - whether there is at least one helm release ClusterSummary is referencing, but currently not
// allowed to manage.
// No action in DryRun mode.
func updateStatusForReferencedHelmReleases(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) (*configv1beta1.ClusterSummary, bool, error) {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return clusterSummary, false, nil
	}

	if len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts) == 0 &&
		len(clusterSummary.Status.HelmReleaseSummaries) == 0 {
		// Nothing to do
		return clusterSummary, false, nil
	}

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return clusterSummary, false, nil
	}

	helmInfo := func(releaseNamespace, releaseName string) string {
		return fmt.Sprintf("%s/%s", releaseNamespace, releaseName)
	}

	currentlyReferenced := make(map[string]bool)

	conflict := false

	currentClusterSummary := &configv1beta1.ClusterSummary{}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err = c.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
		if err != nil {
			return err
		}

		helmReleaseSummaries := make([]configv1beta1.HelmChartSummary, len(currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts))
		for i := range currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			currentChart := &currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]

			instantiatedChart, err := getInstantiatedChart(ctx, clusterSummary, currentChart, mgmtResources, logger)
			if err != nil {
				return err
			}

			var canManage bool
			canManage, err = determineChartOwnership(ctx, c, clusterSummary, instantiatedChart, logger)
			if err != nil {
				return err
			}
			if canManage {
				helmReleaseSummaries[i] = configv1beta1.HelmChartSummary{
					ReleaseName:      instantiatedChart.ReleaseName,
					ReleaseNamespace: instantiatedChart.ReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusManaging,
					ValuesHash:       getValueHashFromHelmChartSummary(instantiatedChart, clusterSummary), // if a value is currently stored, keep it.
					// after chart is deployed such value will be updated
				}
				currentlyReferenced[helmInfo(instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName)] = true
			} else {
				var managerName string
				managerName, err = chartManager.GetManagerForChart(currentClusterSummary.Spec.ClusterNamespace,
					currentClusterSummary.Spec.ClusterName, currentClusterSummary.Spec.ClusterType, instantiatedChart)
				if err != nil {
					return err
				}
				helmReleaseSummaries[i] = configv1beta1.HelmChartSummary{
					ReleaseName:      instantiatedChart.ReleaseName,
					ReleaseNamespace: instantiatedChart.ReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusConflict,
					ConflictMessage:  fmt.Sprintf("ClusterSummary %s managing it", managerName),
				}
				conflict = true
			}
		}

		// If there is any helm release which:
		// - was managed by this ClusterSummary
		// - is not referenced anymore by this ClusterSummary
		// still leave an entry in ClusterSummary.Status
		for i := range currentClusterSummary.Status.HelmReleaseSummaries {
			summary := &currentClusterSummary.Status.HelmReleaseSummaries[i]
			if summary.Status == configv1beta1.HelmChartStatusManaging {
				if _, ok := currentlyReferenced[helmInfo(summary.ReleaseNamespace, summary.ReleaseName)]; !ok {
					helmReleaseSummaries = append(helmReleaseSummaries, *summary)
				}
			}
		}

		currentClusterSummary.Status.HelmReleaseSummaries = helmReleaseSummaries

		return c.Status().Update(ctx, currentClusterSummary)
	})
	return currentClusterSummary, conflict, err
}

// updateStatusForNonReferencedHelmReleases walks ClusterSummary.Status entries.
// Removes any entry pointing to a helm release currently not referenced by ClusterSummary.
// No action in DryRun mode.
func updateStatusForNonReferencedHelmReleases(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary) (*configv1beta1.ClusterSummary, error) {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return clusterSummary, nil
	}

	currentClusterSummary := &configv1beta1.ClusterSummary{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
	if err != nil {
		return clusterSummary, err
	}

	helmInfo := func(releaseNamespace, releaseName string) string {
		return fmt.Sprintf("%s/%s", releaseNamespace, releaseName)
	}

	currentlyReferenced := make(map[string]bool)

	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		currentlyReferenced[helmInfo(currentChart.ReleaseNamespace, currentChart.ReleaseName)] = true
	}

	helmReleaseSummaries := make([]configv1beta1.HelmChartSummary, 0, len(clusterSummary.Status.HelmReleaseSummaries))
	for i := range clusterSummary.Status.HelmReleaseSummaries {
		summary := &clusterSummary.Status.HelmReleaseSummaries[i]
		if _, ok := currentlyReferenced[helmInfo(summary.ReleaseNamespace, summary.ReleaseName)]; ok {
			helmReleaseSummaries = append(helmReleaseSummaries, *summary)
		}
	}

	if len(helmReleaseSummaries) == len(clusterSummary.Status.HelmReleaseSummaries) {
		// Nothing has changed
		return clusterSummary, nil
	}

	currentClusterSummary.Status.HelmReleaseSummaries = helmReleaseSummaries

	err = c.Status().Update(ctx, currentClusterSummary)
	if err != nil {
		return clusterSummary, err
	}

	return currentClusterSummary, nil
}

// getHelmChartConflictManager returns a message listing ClusterProfile managing an helm chart.
// - clusterSummaryManagerName is the name of the ClusterSummary currently managing the helm chart
// (essentially what chartManager.GetManagerForChart returns, given registrations are done using ClusterSummaries)
// - clusterNamespace is the namespace of the cluster
func getHelmChartConflictManager(ctx context.Context, c client.Client,
	clusterNamespace, clusterSummaryManagerName string, logger logr.Logger) string {

	defaultMessage := "Cannot manage it. Currently managed by different ClusterProfile"
	clusterSummaryManager, err := configv1beta1.GetClusterSummary(ctx, c, clusterNamespace, clusterSummaryManagerName)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterSummary. Err: %v", clusterSummaryManagerName))
		return defaultMessage
	}

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummaryManager)
	if err != nil || profileOwnerRef == nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterProfile. Err: %v", clusterSummaryManagerName))
		return defaultMessage
	}

	return fmt.Sprintf("Cannot manage it. Currently managed by %s %s", profileOwnerRef.Kind, profileOwnerRef.Name)
}

// createReportForUnmanagedHelmRelease creates ReleaseReport for an un-managed (by this instance) helm release
func createReportForUnmanagedHelmRelease(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, logger logr.Logger) (*configv1beta1.ReleaseReport, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	report := &configv1beta1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: string(configv1beta1.NoHelmAction),
	}
	if clusterSummaryManagerName, err := chartManager.GetManagerForChart(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, currentChart); err == nil {
		report.Message = getHelmChartConflictManager(ctx, c, clusterSummary.Spec.ClusterNamespace,
			clusterSummaryManagerName, logger)
		report.Action = string(configv1beta1.ConflictHelmAction)
	} else if currentChart.HelmChartAction == configv1beta1.HelmChartActionInstall {
		report.Action = string(configv1beta1.InstallHelmAction)
	} else {
		report.Message = notInstalledMessage
	}

	return report, nil
}

// updateClusterReportWithHelmReports updates ClusterReport Status with HelmReports.
// This is no-op unless mode is DryRun.
func updateClusterReportWithHelmReports(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary,
	releaseReports []configv1beta1.ReleaseReport) error {

	// This is no-op unless in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
		return nil
	}

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	clusterReportName := getClusterReportName(profileOwnerRef.Kind, profileOwnerRef.Name,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		clusterReport := &configv1beta1.ClusterReport{}
		err = c.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Spec.ClusterNamespace, Name: clusterReportName}, clusterReport)
		if err != nil {
			return err
		}

		clusterReport.Status.ReleaseReports = releaseReports
		return c.Status().Update(ctx, clusterReport)
	})

	return err
}

func getInstantiatedValues(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, requestedChart *configv1beta1.HelmChart,
	logger logr.Logger) (chartutil.Values, error) {

	// Get management cluster resources once
	mgmtConfig := getManagementClusterConfig()
	mgmtClient := getManagementClusterClient()

	instantiatedValues, err := instantiateTemplateValues(ctx, mgmtConfig, mgmtClient,
		clusterSummary, requestedChart.ChartName, requestedChart.Values, mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	// Create result map
	var result map[string]any
	err = yaml.Unmarshal([]byte(instantiatedValues), &result)
	if err != nil {
		return nil, err
	}

	valuesFrom, err := getHelmChartValuesFrom(ctx, mgmtClient, clusterSummary, requestedChart, logger)
	if err != nil {
		return nil, err
	}

	for k := range valuesFrom {
		instantiatedValuesFrom, err := instantiateTemplateValues(ctx, mgmtConfig, mgmtClient,
			clusterSummary, requestedChart.ChartName, valuesFrom[k], mgmtResources, logger)
		if err != nil {
			return nil, err
		}

		// Parse to map
		var instantiatedValuesMap map[string]any
		err = yaml.Unmarshal([]byte(instantiatedValuesFrom), &instantiatedValuesMap)
		if err != nil {
			return nil, err
		}

		err = mergo.Merge(&result, instantiatedValuesMap, mergo.WithOverride)
		if err != nil {
			return nil, err
		}
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("Deploying helm charts with values %#v", valuesFrom))

	return chartutil.Values(result), nil
}

// getHelmChartValuesFrom return key-value pair from referenced ConfigMap/Secret.
// order is
func getHelmChartValuesFrom(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	helmChart *configv1beta1.HelmChart, logger logr.Logger) (values []string, err error) {

	values = []string{}
	for i := range helmChart.ValuesFrom {
		vf := &helmChart.ValuesFrom[i]
		namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, vf.Namespace,
			clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate namespace for %s %s/%s: %v",
				vf.Kind, vf.Namespace, vf.Name, err))
			return nil, err
		}

		name, err := libsveltostemplate.GetReferenceResourceName(ctx, c, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, vf.Name, clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s %s/%s: %v",
				vf.Kind, vf.Namespace, vf.Name, err))
			return nil, err
		}

		if vf.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
			if err != nil {
				err = handleReferenceError(err, vf.Kind, namespace, name, vf.Optional, logger)
				if err == nil {
					continue
				}
				return nil, err
			}

			for _, value := range configMap.Data {
				values = append(values, value)
			}
		} else if vf.Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
			secret, err := getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
			if err != nil {
				err = handleReferenceError(err, vf.Kind, namespace, name, vf.Optional, logger)
				if err == nil {
					continue
				}
				return nil, err
			}
			for _, value := range secret.Data {
				values = append(values, string(value))
			}
		}
	}
	return values, nil
}

// collectResourcesFromManagedHelmChartsForDriftDetection collects resources considering all
// helm charts contained in a ClusterSummary that are currently managed by the
// ClusterProfile instance.
// Resources with "projectsveltos.io/driftDetectionIgnore" annotation won't be included
func collectResourcesFromManagedHelmChartsForDriftDetection(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary, kubeconfig string, logger logr.Logger,
) ([]libsveltosv1beta1.HelmResources, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	helmResources := make([]libsveltosv1beta1.HelmResources, 0, len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts))

	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		l := logger.WithValues("chart", currentChart.ChartName, "releaseNamespace", currentChart.ReleaseNamespace)
		l.V(logs.LogDebug).Info("collecting resources for helm chart")
		// Conflicts are already resolved by the time this is invoked. So it is safe to call CanManageChart
		if chartManager.CanManageChart(clusterSummary, currentChart) {
			credentialsPath, caPath, err := getCredentialsAndCAFiles(ctx, c, clusterSummary, currentChart)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to process credentials %v", err))
				return nil, err
			}

			registryOptions := &registryClientOptions{
				credentialsPath: credentialsPath, caPath: caPath,
				skipTLSVerify: getInsecureSkipTLSVerify(currentChart),
				plainHTTP:     getPlainHTTP(currentChart),
			}

			actionConfig, err := actionConfigInit(currentChart.ReleaseNamespace, kubeconfig, registryOptions,
				getEnableClientCacheValue(currentChart.Options))
			if credentialsPath != "" {
				os.Remove(credentialsPath)
			}
			if caPath != "" {
				os.Remove(caPath)
			}
			if err != nil {
				return nil, err
			}

			statusObject := action.NewStatus(actionConfig)
			results, err := statusObject.Run(currentChart.ReleaseName)
			if err != nil {
				return nil, err
			}

			resources, err := collectHelmContent(results.Manifest, logger)
			if err != nil {
				return nil, err
			}

			l.V(logs.LogDebug).Info(fmt.Sprintf("found %d resources", len(resources)))

			helmInfo := libsveltosv1beta1.HelmResources{
				ChartName:        currentChart.ChartName,
				ReleaseName:      currentChart.ReleaseName,
				ReleaseNamespace: currentChart.ReleaseNamespace,
				Resources:        unstructuredToSveltosResources(resources),
			}

			helmResources = append(helmResources, helmInfo)
		}
	}

	return helmResources, nil
}

func collectHelmContent(manifest string, logger logr.Logger) ([]*unstructured.Unstructured, error) {
	elements, err := customSplit(manifest)
	if err != nil {
		return nil, err
	}

	resources := make([]*unstructured.Unstructured, 0, len(elements))

	for i := range elements {
		policy, err := k8s_utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
			return nil, err
		}

		// If object is corev1.List, expand it here
		if policy.GetKind() == "List" && policy.GetAPIVersion() == "v1" {
			list := &corev1.List{}
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(policy.Object, list)
			if err != nil {
				return nil, err
			}

			for _, item := range list.Items {
				// Convert raw extension to unstructured
				unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&item)
				if err != nil {
					logger.Error(err, fmt.Sprintf("failed to get unstructure from corev1.List %.100s", item))
					return nil, err
				}
				u := &unstructured.Unstructured{}
				u.SetUnstructuredContent(unstructuredObj)
				resources = append(resources, u)
			}
		} else {
			resources = append(resources, policy)
		}
	}

	return resources, nil
}

func unstructuredToSveltosResources(policies []*unstructured.Unstructured) []libsveltosv1beta1.Resource {
	resources := make([]libsveltosv1beta1.Resource, len(policies))

	for i := range policies {
		r := libsveltosv1beta1.Resource{
			Namespace:                   policies[i].GetNamespace(),
			Name:                        policies[i].GetName(),
			Kind:                        policies[i].GetKind(),
			Group:                       policies[i].GetObjectKind().GroupVersionKind().Group,
			Version:                     policies[i].GetObjectKind().GroupVersionKind().Version,
			IgnoreForConfigurationDrift: hasIgnoreConfigurationDriftAnnotation(policies[i]),
		}

		resources[i] = r
	}

	return resources
}

func getSettings(namespace string, registryOptions *registryClientOptions) *cli.EnvSettings {
	settings := cli.New()
	settings.SetNamespace(namespace)
	settings.Debug = true
	settings.RegistryConfig = registryOptions.credentialsPath

	return settings
}

func getEnableClientCacheValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.EnableClientCache
	}

	return false
}

func getWaitHelmValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.Wait
	}

	return false
}

func getWaitForJobsHelmValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.WaitForJobs
	}

	return false
}

func getCreateNamespaceHelmValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.InstallOptions.CreateNamespace
	}

	return true // for backward compatibility
}

func getSkipCRDsHelmValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.SkipCRDs
	}

	return false
}

func getAtomicHelmValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.Atomic
	}

	return false
}

func getDisableHooksHelmInstallValue(options *configv1beta1.HelmOptions) bool {
	if options == nil {
		return false
	}

	if options.InstallOptions.DisableHooks {
		return true
	}

	return options.DisableHooks
}

func getDisableHooksHelmUninstallValue(options *configv1beta1.HelmOptions) bool {
	if options == nil {
		return false
	}

	if options.UninstallOptions.DisableHooks {
		return true
	}

	return options.DisableHooks
}

func getDisableHooksHelmUpgradeValue(options *configv1beta1.HelmOptions) bool {
	if options == nil {
		return false
	}

	if options.UpgradeOptions.DisableHooks {
		return true
	}

	return options.DisableHooks
}

func getDisableOpenAPIValidationValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.DisableOpenAPIValidation
	}

	return false
}

func getSkipSchemaValidation(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.SkipSchemaValidation
	}

	return false
}

func getTimeoutValue(options *configv1beta1.HelmOptions) metav1.Duration {
	if options != nil && options.Timeout != nil {
		return *options.Timeout
	}

	const helmTimeout = 5
	return metav1.Duration{Duration: helmTimeout * time.Minute}
}

func getDependenciesUpdateValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.DependencyUpdate
	}

	return false
}

func getLabelsValue(options *configv1beta1.HelmOptions) map[string]string {
	if options != nil {
		return options.Labels
	}
	return map[string]string{}
}

func getReplaceValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.InstallOptions.Replace
	}
	return true
}

func getForceValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.Force
	}
	return false
}

func getReuseValues(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.ReuseValues
	}
	return false
}

func getResetValues(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.ResetValues
	}
	return false
}

func getResetThenReuseValues(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.ResetThenReuseValues
	}
	return false
}

func getUpgradeCRDs(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.UpgradeCRDs
	}
	return false
}

func getDescriptionValue(options *configv1beta1.HelmOptions) string {
	if options != nil {
		return options.Description
	}

	return ""
}

func getKeepHistoryValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UninstallOptions.KeepHistory
	}

	return false
}

func getDeletionPropagation(options *configv1beta1.HelmOptions) string {
	if options != nil {
		return options.UninstallOptions.DeletionPropagation
	}

	return defaultDeletionPropagation
}

func getMaxHistoryValue(options *configv1beta1.HelmOptions) int {
	if options != nil {
		return options.UpgradeOptions.MaxHistory
	}

	return defaultMaxHistory
}

func getCleanupOnFailValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.CleanupOnFail
	}

	return false
}

func getSubNotesValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.SubNotes
	}

	return false
}

func getRecreateValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.UpgradeOptions.Recreate
	}

	return false
}

func getHelmInstallClient(requestedChart *configv1beta1.HelmChart, kubeconfig string,
	registryOptions *registryClientOptions, patches []libsveltosv1beta1.Patch,
) (*action.Install, error) {

	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig, registryOptions,
		getEnableClientCacheValue(requestedChart.Options))
	if err != nil {
		return nil, err
	}

	installClient := action.NewInstall(actionConfig)
	installClient.ReleaseName = requestedChart.ReleaseName
	installClient.Namespace = requestedChart.ReleaseNamespace
	installClient.Version = requestedChart.ChartVersion
	installClient.Wait = getWaitHelmValue(requestedChart.Options)
	installClient.WaitForJobs = getWaitForJobsHelmValue(requestedChart.Options)
	installClient.CreateNamespace = getCreateNamespaceHelmValue(requestedChart.Options)
	installClient.SkipCRDs = getSkipCRDsHelmValue(requestedChart.Options)
	installClient.Atomic = getAtomicHelmValue(requestedChart.Options)
	installClient.DisableHooks = getDisableHooksHelmInstallValue(requestedChart.Options)
	installClient.DisableOpenAPIValidation = getDisableOpenAPIValidationValue(requestedChart.Options)
	installClient.Timeout = getTimeoutValue(requestedChart.Options).Duration
	installClient.SkipSchemaValidation = getSkipSchemaValidation(requestedChart.Options)
	installClient.Replace = getReplaceValue(requestedChart.Options)
	installClient.Labels = getLabelsValue(requestedChart.Options)
	installClient.Description = getDescriptionValue(requestedChart.Options)
	if actionConfig.RegistryClient != nil {
		installClient.SetRegistryClient(actionConfig.RegistryClient)
	}
	installClient.InsecureSkipTLSverify = registryOptions.skipTLSVerify
	installClient.PlainHTTP = registryOptions.plainHTTP
	installClient.CaFile = registryOptions.caPath

	if len(patches) > 0 {
		installClient.PostRenderer = &patcher.CustomPatchPostRenderer{Patches: patches}
	}

	return installClient, nil
}

func getHelmUpgradeClient(requestedChart *configv1beta1.HelmChart, actionConfig *action.Configuration,
	registryOptions *registryClientOptions, patches []libsveltosv1beta1.Patch) *action.Upgrade {

	upgradeClient := action.NewUpgrade(actionConfig)
	upgradeClient.Install = true
	upgradeClient.Namespace = requestedChart.ReleaseNamespace
	upgradeClient.Version = requestedChart.ChartVersion
	upgradeClient.Wait = getWaitHelmValue(requestedChart.Options)
	upgradeClient.WaitForJobs = getWaitForJobsHelmValue(requestedChart.Options)
	upgradeClient.SkipCRDs = getSkipCRDsHelmValue(requestedChart.Options)
	upgradeClient.Atomic = getAtomicHelmValue(requestedChart.Options)
	upgradeClient.DisableHooks = getDisableHooksHelmUpgradeValue(requestedChart.Options)
	upgradeClient.DisableOpenAPIValidation = getDisableOpenAPIValidationValue(requestedChart.Options)
	upgradeClient.Timeout = getTimeoutValue(requestedChart.Options).Duration
	upgradeClient.SkipSchemaValidation = getSkipSchemaValidation(requestedChart.Options)
	upgradeClient.ResetValues = getResetValues(requestedChart.Options)
	upgradeClient.ReuseValues = getReuseValues(requestedChart.Options)
	upgradeClient.ResetThenReuseValues = getResetThenReuseValues(requestedChart.Options)
	upgradeClient.Force = getForceValue(requestedChart.Options)
	upgradeClient.Labels = getLabelsValue(requestedChart.Options)
	upgradeClient.Description = getDescriptionValue(requestedChart.Options)
	upgradeClient.MaxHistory = getMaxHistoryValue(requestedChart.Options)
	upgradeClient.CleanupOnFail = getCleanupOnFailValue(requestedChart.Options)
	upgradeClient.SubNotes = getSubNotesValue(requestedChart.Options)
	upgradeClient.Recreate = getRecreateValue(requestedChart.Options)
	upgradeClient.InsecureSkipTLSverify = registryOptions.skipTLSVerify
	upgradeClient.PlainHTTP = registryOptions.plainHTTP
	upgradeClient.CaFile = registryOptions.caPath

	if actionConfig.RegistryClient != nil {
		upgradeClient.SetRegistryClient(actionConfig.RegistryClient)
	}

	if len(patches) > 0 {
		upgradeClient.PostRenderer = &patcher.CustomPatchPostRenderer{Patches: patches}
	}

	return upgradeClient
}

func getHelmUninstallClient(requestedChart *configv1beta1.HelmChart, actionConfig *action.Configuration,
) *action.Uninstall {

	uninstallClient := action.NewUninstall(actionConfig)
	uninstallClient.DryRun = false
	if requestedChart != nil {
		uninstallClient.Timeout = getTimeoutValue(requestedChart.Options).Duration
		uninstallClient.Description = getDescriptionValue(requestedChart.Options)
		uninstallClient.Wait = getWaitHelmValue(requestedChart.Options)
		uninstallClient.DisableHooks = getDisableHooksHelmUninstallValue(requestedChart.Options)
		uninstallClient.KeepHistory = getKeepHistoryValue(requestedChart.Options)
		uninstallClient.DeletionPropagation = getDeletionPropagation(requestedChart.Options)
	}
	return uninstallClient
}

func addExtraMetadata(ctx context.Context, requestedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary, kubeconfig string,
	registryOptions *registryClientOptions, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	if clusterSummary.Spec.ClusterProfileSpec.ExtraLabels == nil &&
		clusterSummary.Spec.ClusterProfileSpec.ExtraAnnotations == nil {

		return nil
	}

	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig, registryOptions,
		getEnableClientCacheValue(requestedChart.Options))
	if err != nil {
		return err
	}

	statusObject := action.NewStatus(actionConfig)
	results, err := statusObject.Run(requestedChart.ReleaseName)
	if err != nil {
		return err
	}

	resources, err := collectHelmContent(results.Manifest, logger)
	if err != nil {
		return err
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		logger.Error(err, "BuildConfigFromFlags")
		return err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("adding extra labels/annotations to %d resources", len(resources)))
	for i := range resources {
		r := resources[i]

		// For some helm charts, collectHelmContent returns an empty namespace for namespaced resources
		// If resource is a namespaced one and namespace is empty, set namespace to release namespace.
		namespace, err := getResourceNamespace(r, requestedChart.ReleaseNamespace, config)
		if err != nil {
			return err
		}

		var dr dynamic.ResourceInterface
		dr, err = k8s_utils.GetDynamicResourceInterface(config, r.GroupVersionKind(), namespace)
		if err != nil {
			return err
		}

		addExtraLabels(r, clusterSummary.Spec.ClusterProfileSpec.ExtraLabels)
		addExtraAnnotations(r, clusterSummary.Spec.ClusterProfileSpec.ExtraAnnotations)

		_, err = updateResource(ctx, dr, clusterSummary, r, []string{}, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update resource %s %s/%s: %v",
				r.GetKind(), r.GetNamespace(), r.GetName(), err))
			return err
		}
	}

	return nil
}

func getResourceNamespace(r *unstructured.Unstructured, releaseNamespace string, config *rest.Config) (string, error) {
	namespace := r.GetNamespace()
	if namespace == "" {
		namespacedResource, err := isNamespaced(r, config)
		if err != nil {
			return "", err
		}
		if namespacedResource {
			namespace = releaseNamespace
		}
	}

	return namespace, nil
}

func getHelmChartValuesHash(ctx context.Context, c client.Client, requestedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) ([]byte, error) {

	valuesFromHash, err := getHelmReferenceResourceHash(ctx, c, clusterSummary, requestedChart, logger)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	config := render.AsCode(requestedChart.Values)
	config += valuesFromHash
	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func updateValueHashOnHelmChartSummary(ctx context.Context, requestedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	c := getManagementClusterClient()

	helmChartValuesHash, err := getHelmChartValuesHash(ctx, c, requestedChart, clusterSummary, logger)
	if err != nil {
		return err
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err = c.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
		if err != nil {
			return err
		}

		for i := range currentClusterSummary.Status.HelmReleaseSummaries {
			rs := &currentClusterSummary.Status.HelmReleaseSummaries[i]
			if rs.ReleaseName == requestedChart.ReleaseName &&
				rs.ReleaseNamespace == requestedChart.ReleaseNamespace {

				rs.ValuesHash = helmChartValuesHash
			}
		}

		return c.Status().Update(ctx, currentClusterSummary)
	})

	return err
}

// getValueHashFromHelmChartSummary returns the valueHash stored for this chart
// in the ClusterSummary
func getValueHashFromHelmChartSummary(requestedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary) []byte {

	for i := range clusterSummary.Status.HelmReleaseSummaries {
		rs := &clusterSummary.Status.HelmReleaseSummaries[i]
		if rs.ReleaseName == requestedChart.ReleaseName &&
			rs.ReleaseNamespace == requestedChart.ReleaseNamespace {

			return rs.ValuesHash
		}
	}

	return nil
}

func getCredentialsAndCAFiles(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	requestedChart *configv1beta1.HelmChart) (credentialsPath, caPath string, err error) {

	credentialsPath, err = createFileWithCredentials(ctx, c, clusterSummary, requestedChart)
	if err != nil {
		return "", "", err
	}

	caPath, err = createFileWithCA(ctx, c, clusterSummary, requestedChart)
	if err != nil {
		return "", "", err
	}

	return credentialsPath, caPath, nil
}

// createFileWithCredentials fetches the credentials from a Secret and writes it to a temporary file.
// Returns the path to the temporary file.
func createFileWithCredentials(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	requestedChart *configv1beta1.HelmChart) (string, error) {

	if requestedChart.RegistryCredentialsConfig == nil ||
		requestedChart.RegistryCredentialsConfig.CredentialsSecretRef == nil {

		return "", nil
	}
	credSecretRef := requestedChart.RegistryCredentialsConfig.CredentialsSecretRef
	namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c,
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		requestedChart.RegistryCredentialsConfig.CredentialsSecretRef.Namespace, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	secret := &corev1.Secret{}
	err = c.Get(ctx,
		types.NamespacedName{
			Namespace: namespace,
			Name:      credSecretRef.Name,
		},
		secret)
	if err != nil {
		return "", err
	}

	if secret.Type != corev1.SecretTypeDockerConfigJson {
		return "", nil
	}

	if secret.Data == nil {
		return "", errors.New(fmt.Sprintf("secret %s/%s referenced in HelmChart section contains no data",
			namespace, credSecretRef.Name))
	}

	if requestedChart.RegistryCredentialsConfig.Key != "" {
		if _, ok := secret.Data[requestedChart.RegistryCredentialsConfig.Key]; !ok {
			return "", errors.New(fmt.Sprintf("secret %s/%s referenced in HelmChart has no key %s",
				namespace, credSecretRef.Name, requestedChart.RegistryCredentialsConfig.Key))
		}
		return createTemporaryFile(requestedChart.RegistryCredentialsConfig.Key,
			secret.Data[requestedChart.RegistryCredentialsConfig.Key])
	}

	for k := range secret.Data {
		return createTemporaryFile("config-*.json", secret.Data[k])
	}

	return "", nil
}

// createFileWithCA fetches the CA certificate from a Secret and writes it to a temporary file.
// Returns the path to the temporary file.
func createFileWithCA(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	requestedChart *configv1beta1.HelmChart) (string, error) {

	if requestedChart.RegistryCredentialsConfig == nil {
		return "", nil
	}

	if requestedChart.RegistryCredentialsConfig.CASecretRef == nil {
		return "", nil
	}

	namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c,
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		requestedChart.RegistryCredentialsConfig.CASecretRef.Namespace, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	secret := &corev1.Secret{}
	err = c.Get(ctx,
		types.NamespacedName{
			Namespace: namespace,
			Name:      requestedChart.RegistryCredentialsConfig.CASecretRef.Name,
		},
		secret)
	if err != nil {
		return "", err
	}

	if secret.Data == nil {
		return "", errors.New(fmt.Sprintf("secret %s/%s referenced in HelmChart section contains no data",
			requestedChart.RegistryCredentialsConfig.CASecretRef.Namespace,
			requestedChart.RegistryCredentialsConfig.CASecretRef.Name))
	}

	const key = "ca.crt"
	_, ok := secret.Data[key]
	if !ok {
		return "", errors.New(fmt.Sprintf("secret %s/%s referenced in HelmChart section contains no key %s",
			requestedChart.RegistryCredentialsConfig.CASecretRef.Namespace,
			requestedChart.RegistryCredentialsConfig.CASecretRef.Name, key))
	}

	return createTemporaryFile("ca-*.crt", secret.Data[key])
}

func createTemporaryFile(pattern string, data []byte) (string, error) {
	tmpfile, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer tmpfile.Close()

	if _, err := tmpfile.Write(data); err != nil {
		return "", err
	}

	return tmpfile.Name(), nil
}

func getInsecureSkipTLSVerify(currentChart *configv1beta1.HelmChart) bool {
	if currentChart.RegistryCredentialsConfig == nil {
		return configv1beta1.RegistryCredentialsConfig{}.InsecureSkipTLSVerify
	}

	return currentChart.RegistryCredentialsConfig.InsecureSkipTLSVerify
}

func getPlainHTTP(currentChart *configv1beta1.HelmChart) bool {
	if currentChart.RegistryCredentialsConfig == nil {
		return configv1beta1.RegistryCredentialsConfig{}.PlainHTTP
	}

	return currentChart.RegistryCredentialsConfig.PlainHTTP
}

// getHelmChartAndRepoName returns helm repo URL and chart name
func getHelmChartAndRepoName(ctx context.Context, requestedChart *configv1beta1.HelmChart, //nolint: gocritic // ignore
	logger logr.Logger) (string, string, string, error) {

	tmpDir := ""
	chartName := requestedChart.ChartName
	repoURL := requestedChart.RepositoryURL
	if registry.IsOCI(repoURL) {
		u, err := url.Parse(repoURL)
		if err != nil {
			return "", "", "", err
		}

		u.Path = path.Join(u.Path, chartName)
		chartName = u.String()
		repoURL = ""
	}

	if isReferencingFluxSource(requestedChart) {
		sourceRef, repoPath, err := getReferencedFluxSourceFromURL(requestedChart)
		if err != nil {
			return "", "", "", err
		}

		if sourceRef == nil {
			return "", "", "", fmt.Errorf("Flux Source %v not found", sourceRef)
		}

		source, err := getSource(ctx, getManagementClusterClient(), sourceRef.Namespace, sourceRef.Name, sourceRef.Kind)
		if err != nil {
			return "", "", "", err
		}

		s := source.(sourcev1.Source)
		tmpDir, err = prepareFileSystemWithFluxSource(s, logger)
		if err != nil {
			return "", "", "", err
		}

		chartName = repoPath
		repoURL = ""
	}

	return tmpDir, chartName, repoURL, nil
}

func doLogin(registryOptions *registryClientOptions, releaseNamespace, registryURL string) error {
	if registry.IsOCI(registryURL) {
		registryClient, err := getRegistryClient(releaseNamespace, registryOptions, true)
		if err != nil {
			return err
		}

		cfg := &action.Configuration{
			RegistryClient: registryClient,
		}

		return action.NewRegistryLogin(cfg).Run(os.Stderr,
			registryOptions.hostname,
			registryOptions.username,
			registryOptions.password,
			action.WithCertFile(""),
			action.WithKeyFile(""),
			action.WithCAFile(registryOptions.caPath),
			action.WithInsecure(registryOptions.skipTLSVerify))
	}

	return nil
}

// usernameAndPasswordFromSecret derives authentication data from a Secret to login to an OCI registry. This Secret
// may either hold "username" and "password" fields or be of the corev1.SecretTypeDockerConfigJson type and hold
// a corev1.DockerConfigJsonKey field with a complete Docker configuration. If both, "username" and "password" are
// empty a nil error will be returned.
// Credit to https://github.com/kubepack/lib-helm
func getUsernameAndPasswordFromSecret(registryURL string, secret *corev1.Secret) (username, password, host string, err error) {
	parsedURL, err := url.Parse(registryURL)
	if err != nil {
		return "", "", "", fmt.Errorf("unable to parse registry URL '%s' while reconciling Secret '%s': %w",
			registryURL, secret.Name, err)
	}
	if secret.Type == corev1.SecretTypeDockerConfigJson {
		dockerCfg, err := dockerconfig.LoadFromReader(bytes.NewReader(secret.Data[corev1.DockerConfigJsonKey]))
		if err != nil {
			return "", "", "", fmt.Errorf("unable to load Docker config from Secret '%s': %w", secret.Name, err)
		}
		authConfig, err := dockerCfg.GetAuthConfig(parsedURL.Host)
		if err != nil {
			return "", "", "", fmt.Errorf("unable to get authentication data from Secret '%s': %w", secret.Name, err)
		}

		// Make sure that the obtained auth config is for the requested host.
		// When the docker config does not contain the credentials for a host,
		// the credential store returns an empty auth config.
		// Refer: https://github.com/docker/cli/blob/v20.10.16/cli/config/credentials/file_store.go#L44
		if credentials.ConvertToHostname(authConfig.ServerAddress) != parsedURL.Host {
			return "", "", "", fmt.Errorf("no auth config for '%s' in the docker-registry Secret '%s'", parsedURL.Host, secret.Name)
		}
		username = authConfig.Username
		password = authConfig.Password
	} else {
		username, password = string(secret.Data["username"]), string(secret.Data["password"])
	}
	switch {
	case username == "" && password == "":
		return "", "", "", nil
	case username == "" || password == "":
		return "", "", "", fmt.Errorf("invalid '%s' secret data: required fields 'username' and 'password'", secret.Name)
	}
	return username, password, parsedURL.Host, nil
}

func requeueAllOtherClusterSummaries(ctx context.Context, c client.Client,
	namespace string, clusterSummaryNames []string, logger logr.Logger) error {

	for i := range clusterSummaryNames {
		name := &clusterSummaryNames[i]

		cs := &configv1beta1.ClusterSummary{}
		err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: *name}, cs)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}

		err = requeueClusterSummary(ctx, configv1beta1.FeatureHelm, cs, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

// evaluateValuesDiff evaluates and returns diff between Helm values
func evaluateValuesDiff(ctx context.Context, currentValues map[string]interface{},
	clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	requestedChart *configv1beta1.HelmChart, kubeconfig string, registryOptions *registryClientOptions,
	logger logr.Logger) (string, error) {

	settings := getSettings(requestedChart.ReleaseNamespace, registryOptions)

	err := repoAddOrUpdate(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, registryOptions, logger)
	if err != nil {
		return "", err
	}

	var values chartutil.Values
	values, err = getInstantiatedValues(ctx, clusterSummary, mgmtResources, requestedChart, logger)
	if err != nil {
		return "", err
	}

	proposedValues, err := installRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig, registryOptions,
		values, mgmtResources, logger)
	if err != nil {
		return "", err
	}

	return valuesDiff(currentValues, proposedValues)
}

// valuesDiff evaluates and returns values diff
func valuesDiff(from, to map[string]interface{}) (string, error) {
	if from == nil {
		from = map[string]interface{}{}
	}
	if to == nil {
		to = map[string]interface{}{}
	}

	fromTempFile, err := os.CreateTemp("", "from-temp-file-")
	if err != nil {
		return "", err
	}
	defer os.Remove(fromTempFile.Name()) // Clean up the file after use
	fromWriter := io.Writer(fromTempFile)
	err = printMap(from, fromWriter)
	if err != nil {
		return "", err
	}

	toTempFile, err := os.CreateTemp("", "to-temp-file-")
	if err != nil {
		return "", err
	}
	defer os.Remove(toTempFile.Name()) // Clean up the file after use
	toWriter := io.Writer(toTempFile)
	err = printMap(to, toWriter)
	if err != nil {
		return "", err
	}

	fromContent, err := os.ReadFile(fromTempFile.Name())
	if err != nil {
		return "", err
	}

	toContent, err := os.ReadFile(toTempFile.Name())
	if err != nil {
		return "", err
	}

	edits := myers.ComputeEdits(span.URIFromPath("helm values"), string(fromContent), string(toContent))

	diff := fmt.Sprint(gotextdiff.ToUnified("deployed values", "proposed values", string(fromContent), edits))

	return diff, nil
}

// Print the object inside the writer w.
func printMap(obj map[string]interface{}, w io.Writer) error {
	if obj == nil {
		return nil
	}
	data, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// generateReportForSameVersion considers values used when helm chart was deployed and current
// proposed values, generates a report considering this diff and return.
func generateReportForSameVersion(ctx context.Context, currentValues map[string]interface{},
	clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	currentChart *configv1beta1.HelmChart, kubeconfig string, registryOptions *registryClientOptions,
	logger logr.Logger) (*configv1beta1.ReleaseReport, error) {

	defaultMessage := "Already managing this helm release and specified version already installed"
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
		// Only evaluate helm value diffs for DryRun mode
		report := &configv1beta1.ReleaseReport{
			ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
			ChartVersion: currentChart.ChartVersion, Action: string(configv1beta1.NoHelmAction),
		}

		report.Message = defaultMessage
		return report, nil
	}

	diff, err := evaluateValuesDiff(ctx, currentValues, clusterSummary, mgmtResources, currentChart,
		kubeconfig, registryOptions, logger)
	if err != nil {
		return nil, err
	}
	helmAction := string(configv1beta1.NoHelmAction)
	message := defaultMessage
	if diff != "" {
		helmAction = string(configv1beta1.UpdateHelmValuesAction)
		message = diff
	}
	report := &configv1beta1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: helmAction, Message: message,
	}

	return report, nil
}

func getInstantiatedChart(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) (*configv1beta1.HelmChart, error) {

	// Marshal the struct to YAML
	jsonData, err := yaml.Marshal(*currentChart)
	if err != nil {
		return nil, err
	}

	instantiatedChartString, err := instantiateTemplateValues(ctx, getManagementClusterConfig(),
		getManagementClusterClient(), clusterSummary, currentChart.ChartName, string(jsonData), mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	var instantiatedChart configv1beta1.HelmChart
	err = yaml.Unmarshal([]byte(instantiatedChartString), &instantiatedChart)
	if err != nil {
		return nil, err
	}

	return &instantiatedChart, nil
}

func canManageChart(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	instantiatedChart *configv1beta1.HelmChart, logger logr.Logger) (bool, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return false, err
	}

	if chartManager.CanManageChart(clusterSummary, instantiatedChart) {
		return true, nil
	}

	return determineChartOwnership(ctx, c, clusterSummary, instantiatedChart, logger)
}
