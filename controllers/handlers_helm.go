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

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

var (
	defaultUploadPath = "/tmp/charts"
	chartExtension    = "tgz"
	storage           = repo.File{}
	helmLogger        = textlogger.NewLogger(textlogger.NewConfig())
)

const (
	writeFilePermission = 0644
	lockTimeout         = 30
	notInstalledMessage = "Not installed yet and action is uninstall"
)

type releaseInfo struct {
	ReleaseName      string      `json:"releaseName"`
	ReleaseNamespace string      `json:"releaseNamespace"`
	Revision         string      `json:"revision"`
	Updated          metav1.Time `json:"updated"`
	Status           string      `json:"status"`
	Chart            string      `json:"chart"`
	ChartVersion     string      `json:"chart_version"`
	AppVersion       string      `json:"app_version"`
}

func deployHelmCharts(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1alpha1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndClusterClient(ctx, clusterNamespace, applicant, c, logger)
	if err != nil {
		return err
	}

	startInMgmtCluster := startDriftDetectionInMgmtCluster(o)
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err = deployDriftDetectionManagerInCluster(ctx, c, clusterNamespace, clusterName, applicant,
			clusterType, startInMgmtCluster, logger)
		if err != nil {
			return err
		}

		// Since we are updating resources to watch for drift, remove helm section in ResourceSummary to eliminate
		// un-needed reconciliation (Sveltos is updating those resources so we don't want drift-detection to think
		// a configuration drift is happening)
		err = deployResourceSummaryInCluster(ctx, c, clusterNamespace, clusterName, clusterSummary.Name,
			clusterType, nil, nil, []libsveltosv1alpha1.HelmResources{}, logger)
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

	var kubeconfig string
	kubeconfig, err = clusterproxy.CreateKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return err
	}
	defer os.Remove(kubeconfig)

	err = handleCharts(ctx, clusterSummary, c, remoteClient, kubeconfig, logger)
	if err != nil {
		return err
	}

	var helmResources []libsveltosv1alpha1.HelmResources
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection ||
		clusterSummary.Spec.ClusterProfileSpec.Reloader {

		helmResources, err = collectResourcesFromManagedHelmCharts(ctx, c, clusterSummary, kubeconfig, logger)
		if err != nil {
			return err
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// Deploy resourceSummary
		err = deployResourceSummaryInCluster(ctx, c, clusterNamespace, clusterName, clusterSummary.Name,
			clusterType, nil, nil, helmResources, logger)
		if err != nil {
			return err
		}
	}

	profileOwnerRef, err := configv1alpha1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	// Update Reloader instance. If ClusterProfile Reloader knob is set to true, sveltos will
	// start a rolling upgrade for all Deployment/StatefulSet/DaemonSet instances deployed by Sveltos
	// in the managed cluster when a mounted ConfigMap/Secret is updated. In order to do so, sveltos-agent
	// needs to be instructed which Deployment/StatefulSet/DaemonSet instances require this behavior.
	// Update corresponding Reloader instance (instance will be deleted if Reloader is set to false)
	resources := convertHelmResourcesToObjectReference(helmResources)
	err = updateReloaderWithDeployedResources(ctx, c, profileOwnerRef, configv1alpha1.FeatureHelm,
		resources, clusterSummary, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}
	return validateHealthPolicies(ctx, remoteRestConfig, clusterSummary, configv1alpha1.FeatureHelm, logger)
}

func undeployHelmCharts(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1alpha1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
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

	var kubeconfig string
	kubeconfig, err = clusterproxy.CreateKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return err
	}
	defer os.Remove(kubeconfig)

	var releaseReports []configv1alpha1.ReleaseReport
	releaseReports, err = uninstallHelmCharts(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}

	// First get the helm releases currently managed and uninstall all the ones
	// not referenced anymore. Only if this operation succeeds, removes all stale
	// helm release registration for this clusterSummary.
	var undeployedReports []configv1alpha1.ReleaseReport
	undeployedReports, err = undeployStaleReleases(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}
	releaseReports = append(releaseReports, undeployedReports...)

	profileOwnerRef, err := configv1alpha1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	err = updateReloaderWithDeployedResources(ctx, c, profileOwnerRef, configv1alpha1.FeatureKustomize,
		nil, clusterSummary, logger)
	if err != nil {
		return err
	}

	err = updateClusterConfiguration(ctx, c, clusterSummary, profileOwnerRef,
		configv1alpha1.FeatureHelm, nil, []configv1alpha1.Chart{})
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
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1alpha1.SyncModeDryRun {
		chartManager.RemoveStaleRegistrations(clusterSummary)
		return nil
	}

	return &configv1alpha1.DryRunReconciliationError{}
}

func uninstallHelmCharts(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	kubeconfig string, logger logr.Logger) ([]configv1alpha1.ReleaseReport, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	releaseReports := make([]configv1alpha1.ReleaseReport, 0)
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		if chartManager.CanManageChart(clusterSummary, currentChart) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("Uninstalling chart %s from repo %s %s)",
				currentChart.ChartName,
				currentChart.RepositoryURL,
				currentChart.RepositoryName))

			// If another ClusterSummary is queued to manage this chart in this cluster, do not uninstall.
			// Let the other ClusterSummary take it over.
			if chartManager.GetNumberOfRegisteredClusterSummaries(clusterSummary.Spec.ClusterNamespace,
				clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, currentChart) > 1 {
				// Immediately unregister so next inline ClusterSummary can take this over
				chartManager.UnregisterClusterSummaryForChart(clusterSummary, currentChart)
			} else {
				// If StopMatchingBehavior is LeavePolicies, do not uninstall helm charts
				if !clusterSummary.DeletionTimestamp.IsZero() &&
					clusterSummary.Spec.ClusterProfileSpec.StopMatchingBehavior == configv1alpha1.LeavePolicies {

					logger.V(logs.LogInfo).Info("ClusterProfile StopMatchingBehavior set to LeavePolicies")
				} else {
					err = doUninstallRelease(clusterSummary, currentChart, kubeconfig, logger)
					if err != nil {
						if !errors.Is(err, driver.ErrReleaseNotFound) {
							return nil, err
						}
					}
				}
			}

			releaseReports = append(releaseReports, configv1alpha1.ReleaseReport{
				ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
				Action: string(configv1alpha1.UninstallHelmAction),
			})
		} else {
			releaseReports = append(releaseReports, configv1alpha1.ReleaseReport{
				ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
				Action: string(configv1alpha1.NoHelmAction), Message: "Currently managed by another ClusterProfile",
			})
		}
	}

	return releaseReports, nil
}

func helmHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	// If SyncMode changes (from not ContinuousWithDriftDetection to ContinuousWithDriftDetection
	// or viceversa) reconcile.
	config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode)

	// If Reloader changes, Reloader needs to be deployed or undeployed
	// So consider it in the hash
	config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Reloader)

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterProfileSpec.HelmCharts == nil {
		return h.Sum(nil), nil
	}
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]

		config += render.AsCode(*currentChart)
	}

	for i := range clusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		h := &clusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]
		if h.FeatureID == configv1alpha1.FeatureHelm {
			config += render.AsCode(h)
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// Use the version. This will cause drift-detection, Sveltos CRDs
		// to be redeployed on upgrade
		config += getVersion()
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getHelmRefs(clusterSummary *configv1alpha1.ClusterSummary) []configv1alpha1.PolicyRef {
	return nil
}

func handleCharts(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	c, remoteClient client.Client, kubeconfig string, logger logr.Logger) error {

	// Before any helm release, managed by this ClusterSummary, is deployed, update ClusterSummary
	// Status. Order is important. If pod is restarted, it needs to rebuild internal state keeping
	// track of which ClusterSummary was managing which helm release.
	// Here only currently referenced helm releases are considered. If ClusterSummary was managing
	// an helm release and it is not referencing it anymore, such entry will be removed from ClusterSummary.Status
	// only after helm release is successfully undeployed.
	conflict, err := updateStatusForeferencedHelmReleases(ctx, c, clusterSummary)
	if err != nil {
		return err
	}

	var releaseReports []configv1alpha1.ReleaseReport
	var chartDeployed []configv1alpha1.Chart
	releaseReports, chartDeployed, err = walkChartsAndDeploy(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}

	// If there was an helm release previous managed by this ClusterSummary and currently not referenced
	// anymore, such helm release has been successfully remove at this point. So
	err = updateStatusForNonReferencedHelmReleases(ctx, c, clusterSummary)
	if err != nil {
		return err
	}

	// First get the helm releases currently managed and uninstall all the ones
	// not referenced anymore. Only if this operation succeeds, removes all stale
	// helm release registration for this clusterSummary.
	var undeployedReports []configv1alpha1.ReleaseReport
	undeployedReports, err = undeployStaleReleases(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}
	releaseReports = append(releaseReports, undeployedReports...)

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1alpha1.SyncModeDryRun {
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

	if conflict {
		return fmt.Errorf("conflict managing one or more helm charts")
	}

	err = updateClusterReportWithHelmReports(ctx, c, clusterSummary, releaseReports)
	if err != nil {
		return err
	}

	// In DryRun mode always return an error.
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return &configv1alpha1.DryRunReconciliationError{}
	}
	return nil
}

// walkChartsAndDeploy walks all referenced helm charts. Deploys (install or upgrade) any chart
// this clusterSummary is registered to manage.
func walkChartsAndDeploy(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	kubeconfig string, logger logr.Logger) ([]configv1alpha1.ReleaseReport, []configv1alpha1.Chart, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, nil, err
	}

	var mgtmResources map[string]*unstructured.Unstructured
	mgtmResources, err = collectMgmtResources(ctx, clusterSummary)
	if err != nil {
		return nil, nil, err
	}

	releaseReports := make([]configv1alpha1.ReleaseReport, 0)
	chartDeployed := make([]configv1alpha1.Chart, 0)
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		if !chartManager.CanManageChart(clusterSummary, currentChart) {
			var report *configv1alpha1.ReleaseReport
			report, err = createReportForUnmanagedHelmRelease(ctx, c, clusterSummary, currentChart, logger)
			if err != nil {
				return nil, nil, err
			} else {
				releaseReports = append(releaseReports, *report)
			}
			// error is reported above, in updateHelmChartStatus.
			continue
		}

		var report *configv1alpha1.ReleaseReport
		var currentRelease *releaseInfo
		currentRelease, report, err = handleChart(ctx, clusterSummary, mgtmResources, currentChart, kubeconfig, logger)
		if err != nil {
			return nil, nil, err
		}
		releaseReports = append(releaseReports, *report)

		if currentRelease != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("release %s/%s (version %s) status: %s",
				currentRelease.ReleaseNamespace, currentRelease.ReleaseName, currentRelease.ChartVersion, currentRelease.Status))
			if currentRelease.Status == release.StatusDeployed.String() {
				// Deployed chart is used for updating ClusterConfiguration. There is no ClusterConfiguration for mgmt cluster
				chartDeployed = append(chartDeployed, configv1alpha1.Chart{
					RepoURL:         currentChart.RepositoryURL,
					Namespace:       currentRelease.ReleaseNamespace,
					ReleaseName:     currentRelease.ReleaseName,
					ChartVersion:    currentRelease.ChartVersion,
					AppVersion:      currentRelease.AppVersion,
					LastAppliedTime: &currentRelease.Updated,
				})
			}
		}
	}

	return releaseReports, chartDeployed, nil
}

func handleInstall(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, currentChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) (*configv1alpha1.ReleaseReport, error) {

	var report *configv1alpha1.ReleaseReport
	logger.V(logs.LogDebug).Info("install helm release")
	err := doInstallRelease(ctx, clusterSummary, mgtmResources, currentChart, kubeconfig, logger)
	if err != nil {
		return nil, err
	}
	report = &configv1alpha1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: string(configv1alpha1.InstallHelmAction),
	}
	return report, nil
}

func handleUpgrade(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, currentChart *configv1alpha1.HelmChart,
	currentRelease *releaseInfo, kubeconfig string,
	logger logr.Logger) (*configv1alpha1.ReleaseReport, error) {

	var report *configv1alpha1.ReleaseReport
	logger.V(logs.LogDebug).Info("upgrade helm release")
	err := doUpgradeRelease(ctx, clusterSummary, mgtmResources, currentChart, kubeconfig, logger)
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
	report = &configv1alpha1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: string(configv1alpha1.UpgradeHelmAction),
		Message: message,
	}
	return report, nil
}

func handleUninstall(clusterSummary *configv1alpha1.ClusterSummary, currentChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) (*configv1alpha1.ReleaseReport, error) {

	var report *configv1alpha1.ReleaseReport
	logger.V(logs.LogDebug).Info("uniinstall helm release")
	err := doUninstallRelease(clusterSummary, currentChart, kubeconfig, logger)
	if err != nil {
		return nil, err
	}
	report = &configv1alpha1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		Action: string(configv1alpha1.UninstallHelmAction),
	}
	return report, nil
}

func handleChart(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, currentChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) (*releaseInfo, *configv1alpha1.ReleaseReport, error) {

	currentRelease, err := getReleaseInfo(currentChart.ReleaseName,
		currentChart.ReleaseNamespace, kubeconfig)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil, err
	}
	var report *configv1alpha1.ReleaseReport

	logger = logger.WithValues("releaseNamespace", currentChart.ReleaseNamespace, "releaseName", currentChart.ReleaseName,
		"version", currentChart.ChartVersion)

	if currentRelease != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("current installed version %s", currentChart.ChartVersion))
	}

	if shouldInstall(currentRelease, currentChart) {
		report, err = handleInstall(ctx, clusterSummary, mgtmResources, currentChart, kubeconfig, logger)
		if err != nil {
			return nil, nil, err
		}
	} else if shouldUpgrade(currentRelease, currentChart, clusterSummary) {
		report, err = handleUpgrade(ctx, clusterSummary, mgtmResources, currentChart, currentRelease, kubeconfig, logger)
		if err != nil {
			return nil, nil, err
		}
	} else if shouldUninstall(currentRelease, currentChart) {
		report, err = handleUninstall(clusterSummary, currentChart, kubeconfig, logger)
		if err != nil {
			return nil, nil, err
		}
		report.ChartVersion = currentRelease.ChartVersion
	} else if currentRelease == nil {
		logger.V(logs.LogDebug).Info("no action for helm release")
		report = &configv1alpha1.ReleaseReport{
			ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
			ChartVersion: currentChart.ChartVersion, Action: string(configv1alpha1.NoHelmAction),
		}
		report.Message = notInstalledMessage
	} else {
		logger.V(logs.LogDebug).Info("no action for helm release")
		report = &configv1alpha1.ReleaseReport{
			ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
			ChartVersion: currentChart.ChartVersion, Action: string(configv1alpha1.NoHelmAction),
		}
		report.Message = "Already managing this helm release and specified version already installed"
	}

	currentRelease, err = getReleaseInfo(currentChart.ReleaseName, currentChart.ReleaseNamespace, kubeconfig)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil, err
	}

	return currentRelease, report, nil
}

// repoAddOrUpdate adds/updates repo with given name and url
func repoAddOrUpdate(settings *cli.EnvSettings, name, url string, logger logr.Logger) error {
	logger = logger.WithValues("repoURL", url, "repoName", name)

	entry := &repo.Entry{Name: name, URL: url}
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
		_, err = chartRepo.DownloadIndexFile()
		if err != nil {
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
func installRelease(clusterSummary *configv1alpha1.ClusterSummary, settings *cli.EnvSettings, requestedChart *configv1alpha1.HelmChart,
	kubeconfig string, values map[string]interface{}, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger = logger.WithValues("release", requestedChart.ReleaseName, "releaseNamespace",
		requestedChart.ReleaseNamespace, "chart", requestedChart.ChartName, "chartVersion", requestedChart.ChartVersion)
	logger.V(logs.LogDebug).Info("installing release")

	if requestedChart.ChartName == "" {
		return fmt.Errorf("chart name can not be empty")
	}

	chartName := requestedChart.ChartName
	// install with local uploaded charts, *.tgz
	splitChart := strings.Split(chartName, ".")
	if splitChart[len(splitChart)-1] == chartExtension && !strings.Contains(chartName, ":") {
		chartName = defaultUploadPath + "/" + chartName
	}

	installClient, err := getHelmInstallClient(requestedChart, kubeconfig)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get helm install client: %v", err))
		return err
	}

	cp, err := installClient.ChartPathOptions.LocateChart(chartName, settings)
	if err != nil {
		logger.V(logs.LogDebug).Info("LocateChart failed")
		return err
	}

	chartRequested, err := loader.Load(cp)
	if err != nil {
		logger.V(logs.LogDebug).Info("Load failed")
		return err
	}

	validInstallableChart := isChartInstallable(chartRequested)
	if !validInstallableChart {
		return fmt.Errorf("chart is not installable")
	}

	if getDependenciesUpdateValue(requestedChart.Options) {
		err = checkDependencies(chartRequested, installClient, cp, settings)
		if err != nil {
			return err
		}
	}

	// Reload the chart with the updated Chart.lock file.
	if chartRequested, err = loader.Load(cp); err != nil {
		return fmt.Errorf("%w: failed reloading chart after repo update", err)
	}

	installClient.DryRun = false
	_, err = installClient.Run(chartRequested, values)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("installing release done")
	return nil
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
func uninstallRelease(clusterSummary *configv1alpha1.ClusterSummary,
	releaseName, releaseNamespace, kubeconfig string, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger = logger.WithValues("release", releaseName, "releaseNamespace", releaseNamespace)
	logger.V(logs.LogDebug).Info("uninstalling release")

	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig)
	if err != nil {
		return err
	}

	uninstallClient := action.NewUninstall(actionConfig)
	uninstallClient.DryRun = false
	uninstallClient.Wait = false
	uninstallClient.DisableHooks = false

	_, err = uninstallClient.Run(releaseName)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("uninstalling release done")
	return nil
}

// upgradeRelease upgrades helm release in CAPI cluster.
// No action in DryRun mode.
func upgradeRelease(clusterSummary *configv1alpha1.ClusterSummary, settings *cli.EnvSettings, requestedChart *configv1alpha1.HelmChart,
	kubeconfig string, values map[string]interface{}, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger = logger.WithValues("release", requestedChart.ReleaseName, "releaseNamespace", requestedChart.ReleaseNamespace, "chart",
		requestedChart.ChartName, "chartVersion", requestedChart.ChartVersion)
	logger.V(logs.LogDebug).Info("upgrading release")

	if requestedChart.ChartName == "" {
		return fmt.Errorf("chart name can not be empty")
	}

	chartName := requestedChart.ChartName
	// upgrade with local uploaded charts *.tgz
	splitChart := strings.Split(chartName, ".")
	if splitChart[len(splitChart)-1] == chartExtension {
		chartName = defaultUploadPath + "/" + chartName
	}

	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig)
	if err != nil {
		return err
	}

	upgradeClient, err := getHelmUpgradeClient(requestedChart, actionConfig)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get helm upgrade client: %v", err))
		return err
	}

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

	hisClient := action.NewHistory(actionConfig)
	hisClient.Max = 1
	_, err = hisClient.Run(requestedChart.ReleaseName)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		err = installRelease(clusterSummary, settings, requestedChart, kubeconfig, values, logger)
		if err != nil {
			return err
		}
		return nil
	} else if err != nil {
		return err
	}

	upgradeClient.DryRun = false
	_, err = upgradeClient.Run(requestedChart.ReleaseName, chartRequested, values)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("upgrading release done")
	return nil
}

func debugf(format string, v ...interface{}) {
	helmLogger.V(logs.LogDebug).Info(fmt.Sprintf(format, v...))
}

func actionConfigInit(namespace, kubeconfig string) (*action.Configuration, error) {
	settings := getSettings(namespace)

	actionConfig := new(action.Configuration)

	configFlags := genericclioptions.NewConfigFlags(true)
	configFlags.KubeConfig = &kubeconfig
	configFlags.Namespace = &namespace
	insecure := true
	configFlags.Insecure = &insecure

	registryClient, err := registry.NewClient(
		registry.ClientOptDebug(settings.Debug),
		registry.ClientOptEnableCache(true),
	)
	if err != nil {
		return nil, err
	}
	actionConfig.RegistryClient = registryClient

	err = actionConfig.Init(
		configFlags,
		settings.Namespace(),
		"secret",
		debugf,
	)
	if err != nil {
		return nil, err
	}

	return actionConfig, nil
}

func isChartInstallable(ch *chart.Chart) bool {
	switch ch.Metadata.Type {
	case "", "application":
		return true
	}

	return false
}

func getReleaseInfo(releaseName, releaseNamespace, kubeconfig string) (*releaseInfo, error) {
	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig)

	if err != nil {
		return nil, err
	}

	statusObject := action.NewStatus(actionConfig)
	results, err := statusObject.Run(releaseName)
	if err != nil {
		return nil, err
	}

	element := &releaseInfo{
		ReleaseName:      results.Name,
		ReleaseNamespace: results.Namespace,
		Revision:         strconv.Itoa(results.Version),
		Status:           results.Info.Status.String(),
		Chart:            results.Chart.Metadata.Name,
		ChartVersion:     results.Chart.Metadata.Version,
		AppVersion:       results.Chart.AppVersion(),
	}

	var t metav1.Time
	if lastDeployed := results.Info.LastDeployed; !lastDeployed.IsZero() {
		t = metav1.Time{Time: lastDeployed.Time}
	}
	element.Updated = t

	return element, nil
}

// shouldInstall returns true if action is not uninstall and either there
// is no installed or version, or version is same requested by customer but status is
// not yet deployed
func shouldInstall(currentRelease *releaseInfo, requestedChart *configv1alpha1.HelmChart) bool {
	if requestedChart.HelmChartAction == configv1alpha1.HelmChartActionUninstall {
		return false
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
func shouldUpgrade(currentRelease *releaseInfo, requestedChart *configv1alpha1.HelmChart,
	clusterSummary *configv1alpha1.ClusterSummary) bool {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1alpha1.SyncModeContinuousWithDriftDetection {
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

	return requestedChart.HelmChartAction != configv1alpha1.HelmChartActionUninstall
}

// shouldUninstall returns true if action is uninstall there is a release installed currently
func shouldUninstall(currentRelease *releaseInfo, requestedChart *configv1alpha1.HelmChart) bool {
	if currentRelease == nil {
		return false
	}

	if requestedChart.HelmChartAction != configv1alpha1.HelmChartActionUninstall {
		return false
	}

	return true
}

// doInstallRelease installs helm release in the CAPI Cluster.
// No action in DryRun mode.
func doInstallRelease(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, requestedChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Installing chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	settings := getSettings(requestedChart.ReleaseNamespace)

	err := repoAddOrUpdate(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	var values chartutil.Values
	values, err = getInstantiatedValues(ctx, clusterSummary, mgtmResources, requestedChart, logger)
	if err != nil {
		return err
	}

	err = installRelease(clusterSummary, settings, requestedChart, kubeconfig, values, logger)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.ExtraLabels != nil ||
		clusterSummary.Spec.ClusterProfileSpec.ExtraAnnotations != nil {

		return addExtraMetadata(ctx, requestedChart, clusterSummary, kubeconfig, logger)
	}
	return nil
}

// doUninstallRelease uninstalls helm release from the CAPI Cluster.
// No action in DryRun mode.
func doUninstallRelease(clusterSummary *configv1alpha1.ClusterSummary, requestedChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Uninstalling chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	return uninstallRelease(clusterSummary, requestedChart.ReleaseName, requestedChart.ReleaseNamespace,
		kubeconfig, logger)
}

// doUpgradeRelease upgrades helm release in the CAPI Cluster.
// No action in DryRun mode.
func doUpgradeRelease(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, requestedChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Upgrading chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	settings := getSettings(requestedChart.ReleaseNamespace)

	err := repoAddOrUpdate(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	var values chartutil.Values
	values, err = getInstantiatedValues(ctx, clusterSummary, mgtmResources, requestedChart, logger)
	if err != nil {
		return err
	}

	err = upgradeRelease(clusterSummary, settings, requestedChart, kubeconfig, values, logger)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.ExtraLabels != nil ||
		clusterSummary.Spec.ClusterProfileSpec.ExtraAnnotations != nil {

		return addExtraMetadata(ctx, requestedChart, clusterSummary, kubeconfig, logger)
	}

	return nil
}

// updateChartsInClusterConfiguration updates deployed chart info on ClusterConfiguration
// No action in DryRun mode.
func updateChartsInClusterConfiguration(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	chartDeployed []configv1alpha1.Chart, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("update deployed chart info. Number of deployed helm chart: %d",
		len(chartDeployed)))
	profileOwnerRef, err := configv1alpha1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	return updateClusterConfiguration(ctx, c, clusterSummary, profileOwnerRef, configv1alpha1.FeatureHelm, nil, chartDeployed)
}

// undeployStaleReleases uninstalls all helm charts previously managed and not referenced anyomre
func undeployStaleReleases(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	kubeconfig string, logger logr.Logger) ([]configv1alpha1.ReleaseReport, error) {

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

	reports := make([]configv1alpha1.ReleaseReport, 0)

	for i := range managedHelmReleases {
		releaseKey := chartManager.GetReleaseKey(managedHelmReleases[i].Namespace, managedHelmReleases[i].Name)
		if _, ok := currentlyReferencedReleases[releaseKey]; !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("helm release %s (namespace %s) used to be managed but not referenced anymore",
				managedHelmReleases[i].Name, managedHelmReleases[i].Namespace))

			_, err := getReleaseInfo(managedHelmReleases[i].Name,
				managedHelmReleases[i].Namespace, kubeconfig)
			if err != nil {
				if errors.Is(err, driver.ErrReleaseNotFound) {
					continue
				}
				return nil, err
			}

			if err := uninstallRelease(clusterSummary, managedHelmReleases[i].Name, managedHelmReleases[i].Namespace,
				kubeconfig, logger); err != nil {
				return nil, err
			}

			reports = append(reports, configv1alpha1.ReleaseReport{
				ReleaseNamespace: managedHelmReleases[i].Namespace, ReleaseName: managedHelmReleases[i].Name,
				Action: string(configv1alpha1.UninstallHelmAction),
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
func updateStatusForeferencedHelmReleases(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary) (bool, error) {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return false, nil
	}

	if len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts) == 0 &&
		len(clusterSummary.Status.HelmReleaseSummaries) == 0 {
		// Nothing to do
		return false, nil
	}

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return false, nil
	}

	helmInfo := func(releaseNamespace, releaseName string) string {
		return fmt.Sprintf("%s/%s", releaseNamespace, releaseName)
	}

	currentlyReferenced := make(map[string]bool)

	conflict := false

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err = c.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
		if err != nil {
			return err
		}

		helmReleaseSummaries := make([]configv1alpha1.HelmChartSummary, len(currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts))
		for i := range currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			currentChart := &currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			if chartManager.CanManageChart(currentClusterSummary, currentChart) {
				helmReleaseSummaries[i] = configv1alpha1.HelmChartSummary{
					ReleaseName:      currentChart.ReleaseName,
					ReleaseNamespace: currentChart.ReleaseNamespace,
					Status:           configv1alpha1.HelChartStatusManaging,
				}
				currentlyReferenced[helmInfo(currentChart.ReleaseNamespace, currentChart.ReleaseName)] = true
			} else {
				var managerName string
				managerName, err = chartManager.GetManagerForChart(currentClusterSummary.Spec.ClusterNamespace,
					currentClusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, currentChart)
				if err != nil {
					return err
				}
				helmReleaseSummaries[i] = configv1alpha1.HelmChartSummary{
					ReleaseName:      currentChart.ReleaseName,
					ReleaseNamespace: currentChart.ReleaseNamespace,
					Status:           configv1alpha1.HelChartStatusConflict,
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
			if summary.Status == configv1alpha1.HelChartStatusManaging {
				if _, ok := currentlyReferenced[helmInfo(summary.ReleaseNamespace, summary.ReleaseName)]; !ok {
					helmReleaseSummaries = append(helmReleaseSummaries, *summary)
				}
			}
		}

		currentClusterSummary.Status.HelmReleaseSummaries = helmReleaseSummaries

		return c.Status().Update(ctx, currentClusterSummary)
	})
	return conflict, err
}

// updateStatusForNonReferencedHelmReleases walks ClusterSummary.Status entries.
// Removes any entry pointing to a helm release currently not referenced by ClusterSummary.
// No action in DryRun mode.
func updateStatusForNonReferencedHelmReleases(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	currentClusterSummary := &configv1alpha1.ClusterSummary{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
	if err != nil {
		return err
	}

	helmInfo := func(releaseNamespace, releaseName string) string {
		return fmt.Sprintf("%s/%s", releaseNamespace, releaseName)
	}

	currentlyReferenced := make(map[string]bool)

	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		currentlyReferenced[helmInfo(currentChart.ReleaseNamespace, currentChart.ReleaseName)] = true
	}

	helmReleaseSummaries := make([]configv1alpha1.HelmChartSummary, 0)
	for i := range clusterSummary.Status.HelmReleaseSummaries {
		summary := &clusterSummary.Status.HelmReleaseSummaries[i]
		if _, ok := currentlyReferenced[helmInfo(summary.ReleaseNamespace, summary.ReleaseName)]; ok {
			helmReleaseSummaries = append(helmReleaseSummaries, *summary)
		}
	}

	if len(helmReleaseSummaries) == len(clusterSummary.Status.HelmReleaseSummaries) {
		// Nothing has changed
		return nil
	}

	currentClusterSummary.Status.HelmReleaseSummaries = helmReleaseSummaries

	return c.Status().Update(ctx, currentClusterSummary)
}

// getHelmChartConflictManager returns a message listing ClusterProfile managing an helm chart.
// - clusterSummaryManagerName is the name of the ClusterSummary currently managing the helm chart
// (essentially what chartManager.GetManagerForChart returns, given registrations are done using ClusterSummaries)
// - clusterNamespace is the namespace of the cluster
func getHelmChartConflictManager(ctx context.Context, c client.Client,
	clusterNamespace, clusterSummaryManagerName string, logger logr.Logger) string {

	defaultMessage := "Cannot manage it. Currently managed by different ClusterProfile"
	clusterSummaryManager, err := configv1alpha1.GetClusterSummary(ctx, c, clusterNamespace, clusterSummaryManagerName)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterSummary. Err: %v", clusterSummaryManagerName))
		return defaultMessage
	}

	profileOwnerRef, err := configv1alpha1.GetProfileOwnerReference(clusterSummaryManager)
	if err != nil || profileOwnerRef == nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterProfile. Err: %v", clusterSummaryManagerName))
		return defaultMessage
	}

	return fmt.Sprintf("Cannot manage it. Currently managed by %s %s", profileOwnerRef.Kind, profileOwnerRef.Name)
}

// createReportForUnmanagedHelmRelease creates ReleaseReport for an un-managed (by this instance) helm release
func createReportForUnmanagedHelmRelease(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	currentChart *configv1alpha1.HelmChart, logger logr.Logger) (*configv1alpha1.ReleaseReport, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	report := &configv1alpha1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: string(configv1alpha1.NoHelmAction),
	}
	if clusterSummaryManagerName, err := chartManager.GetManagerForChart(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, currentChart); err == nil {
		report.Message = getHelmChartConflictManager(ctx, c, clusterSummary.Spec.ClusterNamespace,
			clusterSummaryManagerName, logger)
		report.Action = string(configv1alpha1.ConflictHelmAction)
	} else if currentChart.HelmChartAction == configv1alpha1.HelmChartActionInstall {
		report.Action = string(configv1alpha1.InstallHelmAction)
	} else {
		report.Message = notInstalledMessage
	}

	return report, nil
}

// updateClusterReportWithHelmReports updates ClusterReport Status with HelmReports.
// This is no-op unless mode is DryRun.
func updateClusterReportWithHelmReports(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	releaseReports []configv1alpha1.ReleaseReport) error {

	// This is no-op unless in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1alpha1.SyncModeDryRun {
		return nil
	}

	profileOwnerRef, err := configv1alpha1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	clusterReportName := getClusterReportName(profileOwnerRef.Kind, profileOwnerRef.Name,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		clusterReport := &configv1alpha1.ClusterReport{}
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

func getInstantiatedValues(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, requestedChart *configv1alpha1.HelmChart,
	logger logr.Logger) (chartutil.Values, error) {

	instantiatedValues, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		requestedChart.ChartName, requestedChart.Values, mgtmResources, logger)
	if err != nil {
		return nil, err
	}

	return chartutil.ReadValues([]byte(instantiatedValues))
}

// collectResourcesFromManagedHelmCharts collects resources considering all
// helm charts contained in a ClusterSummary that are currently managed by the
// ClusterProfile instance
func collectResourcesFromManagedHelmCharts(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, kubeconfig string, logger logr.Logger,
) ([]libsveltosv1alpha1.HelmResources, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	helmResources := make([]libsveltosv1alpha1.HelmResources, 0)

	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		l := logger.WithValues("chart", currentChart.ChartName, "releaseNamespace", currentChart.ReleaseNamespace)
		l.V(logs.LogDebug).Info("collecting resources for helm chart")
		if chartManager.CanManageChart(clusterSummary, currentChart) {
			actionConfig, err := actionConfigInit(currentChart.ReleaseNamespace, kubeconfig)

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

			helmInfo := libsveltosv1alpha1.HelmResources{
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
	resources := make([]*unstructured.Unstructured, 0)

	elements := strings.Split(manifest, separator)
	for i := range elements {
		section := removeCommentsAndEmptyLines(elements[i])
		if section == "" {
			continue
		}

		policy, err := utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
			return nil, err
		}

		resources = append(resources, policy)
	}

	return resources, nil
}

func unstructuredToSveltosResources(policies []*unstructured.Unstructured) []libsveltosv1alpha1.Resource {
	resources := make([]libsveltosv1alpha1.Resource, 0)

	for i := range policies {
		r := libsveltosv1alpha1.Resource{
			Namespace: policies[i].GetNamespace(),
			Name:      policies[i].GetName(),
			Kind:      policies[i].GetKind(),
			Group:     policies[i].GetObjectKind().GroupVersionKind().Group,
			Version:   policies[i].GetObjectKind().GroupVersionKind().Version,
		}

		resources = append(resources, r)
	}

	return resources
}

func getSettings(namespace string) *cli.EnvSettings {
	settings := cli.New()
	settings.RepositoryCache = "/tmp/.helmcache"
	settings.RepositoryConfig = "/tmp/.helmrepo"
	settings.SetNamespace(namespace)
	settings.Debug = true

	return settings
}

func getWaitHelmValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.Wait
	}

	return false
}

func getWaitForJobsHelmValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.WaitForJobs
	}

	return false
}

func getCreateNamespaceHelmValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.CreateNamespace
	}

	return true // for backward compatibility
}

func getSkipCRDsHelmValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.SkipCRDs
	}

	return false
}

func getAtomicHelmValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.Atomic
	}

	return false
}

func getDisableHooksHelmValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.DisableHooks
	}

	return false
}

func getDisableOpenAPIValidationValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.DisableOpenAPIValidation
	}

	return false
}

func getTimeoutValue(options *configv1alpha1.HelmOptions) *metav1.Duration {
	if options != nil {
		return options.Timeout
	}

	return nil
}

func getDependenciesUpdateValue(options *configv1alpha1.HelmOptions) bool {
	if options != nil {
		return options.DependencyUpdate
	}

	return false
}

func getLabelsValue(options *configv1alpha1.HelmOptions) map[string]string {
	if options != nil {
		return options.Labels
	}

	return map[string]string{}
}

func getHelmInstallClient(requestedChart *configv1alpha1.HelmChart, kubeconfig string) (*action.Install, error) {
	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig)
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
	installClient.DisableHooks = getDisableHooksHelmValue(requestedChart.Options)
	installClient.DisableOpenAPIValidation = getDisableOpenAPIValidationValue(requestedChart.Options)
	if timeout := getTimeoutValue(requestedChart.Options); timeout != nil {
		installClient.Timeout, err = time.ParseDuration(timeout.String())
		if err != nil {
			return nil, err
		}
	}
	installClient.Labels = getLabelsValue(requestedChart.Options)

	return installClient, nil
}

func getHelmUpgradeClient(requestedChart *configv1alpha1.HelmChart, actionConfig *action.Configuration) (*action.Upgrade, error) {
	upgradeClient := action.NewUpgrade(actionConfig)
	upgradeClient.Install = true
	upgradeClient.Namespace = requestedChart.ReleaseNamespace
	upgradeClient.Version = requestedChart.ChartVersion
	upgradeClient.Wait = getWaitHelmValue(requestedChart.Options)
	upgradeClient.WaitForJobs = getWaitForJobsHelmValue(requestedChart.Options)
	upgradeClient.SkipCRDs = getSkipCRDsHelmValue(requestedChart.Options)
	upgradeClient.Atomic = getAtomicHelmValue(requestedChart.Options)
	upgradeClient.DisableHooks = getDisableHooksHelmValue(requestedChart.Options)
	upgradeClient.DisableOpenAPIValidation = getDisableOpenAPIValidationValue(requestedChart.Options)
	if timeout := getTimeoutValue(requestedChart.Options); timeout != nil {
		var err error
		upgradeClient.Timeout, err = time.ParseDuration(timeout.String())
		if err != nil {
			return nil, err
		}
	}
	upgradeClient.Labels = getLabelsValue(requestedChart.Options)

	return upgradeClient, nil
}

func addExtraMetadata(ctx context.Context, requestedChart *configv1alpha1.HelmChart,
	clusterSummary *configv1alpha1.ClusterSummary, kubeconfig string, logger logr.Logger) error {

	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig)
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

	logger.V(logs.LogDebug).Info("adding extra labels/annotations to %d resources", len(resources))
	for i := range resources {
		r := resources[i]

		var dr dynamic.ResourceInterface
		dr, err = utils.GetDynamicResourceInterface(config, r.GroupVersionKind(), r.GetNamespace())
		if err != nil {
			return err
		}

		addExtraLabels(r, clusterSummary.Spec.ClusterProfileSpec.ExtraLabels)
		addExtraAnnotations(r, clusterSummary.Spec.ClusterProfileSpec.ExtraAnnotations)

		err = updateResource(ctx, dr, clusterSummary, r, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update resource %s %s/%s: %v",
				r.GetKind(), r.GetNamespace(), r.GetName(), err))
			return err
		}
	}

	return nil
}
