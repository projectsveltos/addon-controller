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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/gofrs/flock"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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
	settings          = cli.New()
	defaultUploadPath = "/tmp/charts"
	chartExtension    = "tgz"
	repoLock          sync.Mutex
	repoAdded         map[string]bool
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

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err = deployDriftDetectionManagerInCluster(ctx, c, clusterNamespace, clusterName, applicant,
			clusterType, logger)
		if err != nil {
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
			return nil
		}
	}

	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	// Update Reloader instance. If ClusterProfile Reloader knob is set to true, sveltos will
	// start a rolling upgrade for all Deployment/StatefulSet/DaemonSet instances deployed by Sveltos
	// in the managed cluster when a mounted ConfigMap/Secret is updated. In order to do so, sveltos-agent
	// needs to be instructed which Deployment/StatefulSet/DaemonSet instances require this behavior.
	// Update corresponding Reloader instance (instance will be deleted if Reloader is set to false)
	resources := convertHelmResourcesToObjectReference(helmResources)
	err = updateReloaderWithDeployedResources(ctx, c, clusterProfileOwnerRef, configv1alpha1.FeatureHelm,
		resources, clusterSummary, logger)
	if err != nil {
		return err
	}

	return nil
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

	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	err = updateReloaderWithDeployedResources(ctx, c, clusterProfileOwnerRef, configv1alpha1.FeatureKustomize,
		nil, clusterSummary, logger)
	if err != nil {
		return err
	}

	err = updateClusterConfiguration(ctx, c, clusterSummary, clusterProfileOwnerRef,
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
	releaseReports, chartDeployed, err = walkChartsAndDeploy(ctx, c, clusterSummary, remoteClient, kubeconfig, logger)
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
	remoteClient client.Client, kubeconfig string, logger logr.Logger,
) ([]configv1alpha1.ReleaseReport, []configv1alpha1.Chart, error) {

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
		currentRelease, report, err = handleChart(ctx, clusterSummary, mgtmResources, currentChart,
			remoteClient, kubeconfig, logger)
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
	remoteClient client.Client, kubeconfig string, logger logr.Logger) (*configv1alpha1.ReleaseReport, error) {

	var report *configv1alpha1.ReleaseReport
	logger.V(logs.LogDebug).Info("install helm release")
	err := doInstallRelease(ctx, clusterSummary, mgtmResources, remoteClient, currentChart,
		kubeconfig, logger)
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
	currentRelease *releaseInfo, remoteClient client.Client, kubeconfig string,
	logger logr.Logger) (*configv1alpha1.ReleaseReport, error) {

	var report *configv1alpha1.ReleaseReport
	logger.V(logs.LogDebug).Info("upgrade helm release")
	err := doUpgradeRelease(ctx, clusterSummary, mgtmResources, remoteClient, currentChart,
		kubeconfig, logger)
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
	remoteClient client.Client, kubeconfig string, logger logr.Logger,
) (*releaseInfo, *configv1alpha1.ReleaseReport, error) {

	currentRelease, err := getReleaseInfo(currentChart.ReleaseName,
		currentChart.ReleaseNamespace, kubeconfig, logger)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil, err
	}
	var report *configv1alpha1.ReleaseReport

	logger = logger.WithValues("releaseNamespace", currentChart.ReleaseNamespace, "releaseName", currentChart.ReleaseName)

	if shouldInstall(currentRelease, currentChart) {
		report, err = handleInstall(ctx, clusterSummary, mgtmResources, currentChart, remoteClient, kubeconfig, logger)
		if err != nil {
			return nil, nil, err
		}
	} else if shouldUpgrade(currentRelease, currentChart, clusterSummary) {
		report, err = handleUpgrade(ctx, clusterSummary, mgtmResources, currentChart, currentRelease, remoteClient, kubeconfig, logger)
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

	currentRelease, err = getReleaseInfo(currentChart.ReleaseName,
		currentChart.ReleaseNamespace, kubeconfig, logger)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil, err
	}

	return currentRelease, report, nil
}

// RepoAdd adds repo with given name and url
func repoAdd(settings *cli.EnvSettings, name, url string, logger logr.Logger) error {
	logger = logger.WithValues("repo", url)

	settings.Debug = true
	repoInfo := fmt.Sprintf("%s:%s", url, name)

	repoLock.Lock()
	defer repoLock.Unlock()

	if repoAdded == nil {
		repoAdded = make(map[string]bool)
	}

	if _, ok := repoAdded[repoInfo]; ok {
		logger.V(logs.LogDebug).Info("repo already added")
		return nil
	}

	logger.V(logs.LogDebug).Info("adding repo")

	// Ensure the file directory exists as it is required for file locking
	err := os.MkdirAll(filepath.Dir(settings.RepositoryConfig), os.ModePerm)
	if err != nil && !os.IsExist(err) {
		return err
	}

	// Acquire a file lock for process synchronization
	fileLock := flock.New(strings.Replace(settings.RepositoryConfig, filepath.Ext(settings.RepositoryConfig), ".lock", 1))
	lockCtx, cancel := context.WithTimeout(context.Background(), lockTimeout*time.Second)
	defer cancel()
	locked, err := fileLock.TryLockContext(lockCtx, time.Second)
	if err != nil {
		return err
	}
	if locked {
		err = safeCloser(fileLock, logger)
		if err != nil {
			return err
		}
	}

	b, err := os.ReadFile(settings.RepositoryConfig)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var f repo.File
	err = yaml.Unmarshal(b, &f)
	if err != nil {
		return err
	}

	cfg := &repo.Entry{Name: name, URL: url}
	r, err := repo.NewChartRepository(cfg, getter.All(settings))
	if err != nil {
		return err
	}

	_, err = r.DownloadIndexFile()
	if err != nil {
		return err
	}

	f.Update(cfg)

	err = f.WriteFile(settings.RepositoryConfig, writeFilePermission)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("adding repo done")
	repoAdded[repoInfo] = true

	return nil
}

// repoUpdate updates repo
func repoUpdate(settings *cli.EnvSettings, name, url string, logger logr.Logger) error {
	logger = logger.WithValues("repo", url)
	logger.V(logs.LogDebug).Info("updating repo")

	settings.Debug = true
	cfg := &repo.Entry{Name: name, URL: url}
	r, err := repo.NewChartRepository(cfg, getter.All(settings))
	if err != nil {
		return err
	}

	_, err = r.DownloadIndexFile()
	if err != nil {
		return err
	}

	return nil
}

// installRelease installs helm release in the CAPI cluster.
// No action in DryRun mode.
func installRelease(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	settings *cli.EnvSettings, releaseName, releaseNamespace, chartName, chartVersion, kubeconfig string,
	values map[string]interface{}, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger = logger.WithValues("release", releaseName, "releaseNamespace", releaseNamespace, "chart",
		chartName, "chartVersion", chartVersion)
	logger.V(logs.LogDebug).Info("installing release")

	settings.Debug = true
	if chartName == "" {
		return fmt.Errorf("chart name can not be empty")
	}

	// install with local uploaded charts, *.tgz
	splitChart := strings.Split(chartName, ".")
	if splitChart[len(splitChart)-1] == chartExtension && !strings.Contains(chartName, ":") {
		chartName = defaultUploadPath + "/" + chartName
	}

	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("actionConfigInit failed")
		return err
	}

	installObject := action.NewInstall(actionConfig)
	installObject.ReleaseName = releaseName
	installObject.Namespace = releaseNamespace
	installObject.Version = chartVersion

	cp, err := installObject.ChartPathOptions.LocateChart(chartName, settings)
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

	if req := chartRequested.Metadata.Dependencies; req != nil {
		// If CheckDependencies returns an error, we have unfulfilled dependencies.
		// As of Helm 2.4.0, this is treated as a stopping condition:
		// https://github.com/helm/helm/issues/2209
		err = action.CheckDependencies(chartRequested, req)
		if err != nil {
			if installObject.DependencyUpdate {
				man := &downloader.Manager{
					ChartPath:        cp,
					Keyring:          installObject.ChartPathOptions.Keyring,
					SkipUpdate:       false,
					Getters:          getter.All(settings),
					RepositoryConfig: settings.RepositoryConfig,
					RepositoryCache:  settings.RepositoryCache,
				}
				err = man.Update()
				if err != nil {
					return err
				}
				// Reload the chart with the updated Chart.lock file.
				if chartRequested, err = loader.Load(cp); err != nil {
					return fmt.Errorf("%w: failed reloading chart after repo update", err)
				}
			} else {
				return nil
			}
		}
	}

	err = validateInstallHelmResources(ctx, clusterSummary, installObject, chartRequested,
		values, logger)
	if err != nil {
		return err
	}

	installObject.DryRun = false
	_, err = installObject.Run(chartRequested, values)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("installing release done")
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

	settings.Debug = true
	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig, logger)
	if err != nil {
		return err
	}

	uninstallObject := action.NewUninstall(actionConfig)
	_, err = uninstallObject.Run(releaseName)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("uninstalling release done")
	return nil
}

// upgradeRelease upgrades helm release in CAPI cluster.
// No action in DryRun mode.
func upgradeRelease(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary, settings *cli.EnvSettings,
	releaseName, releaseNamespace, chartName, chartVersion, kubeconfig string,
	values map[string]interface{}, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger = logger.WithValues("release", releaseName, "releaseNamespace", releaseNamespace, "chart",
		chartName, "chartVersion", chartVersion)
	logger.V(logs.LogDebug).Info("upgrading release")

	settings.Debug = true
	if chartName == "" {
		return fmt.Errorf("chart name can not be empty")
	}

	// upgrade with local uploaded charts *.tgz
	splitChart := strings.Split(chartName, ".")
	if splitChart[len(splitChart)-1] == chartExtension {
		chartName = defaultUploadPath + "/" + chartName
	}

	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig, logger)
	if err != nil {
		return err
	}

	upgradeObject := action.NewUpgrade(actionConfig)
	upgradeObject.Install = true
	upgradeObject.Namespace = releaseNamespace
	upgradeObject.Version = chartVersion

	cp, err := upgradeObject.ChartPathOptions.LocateChart(chartName, settings)
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
	_, err = hisClient.Run(releaseName)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		err = installRelease(ctx, clusterSummary, settings, releaseName, releaseNamespace, chartName, chartVersion,
			kubeconfig, values, logger)
		if err != nil {
			return err
		}
		return nil
	} else if err != nil {
		return err
	}

	err = validateUpgradeHelmResources(ctx, clusterSummary, upgradeObject, releaseName, chartRequested,
		values, logger)
	if err != nil {
		return err
	}

	upgradeObject.DryRun = false
	_, err = upgradeObject.Run(releaseName, chartRequested, values)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("upgrading release done")
	return nil
}

func safeCloser(fileLock *flock.Flock, logger logr.Logger) error {
	if err := fileLock.Unlock(); err != nil {
		logger.V(logs.LogInfo).Info(err.Error())
		return err
	}
	return nil
}

func actionConfigInit(namespace, kubeconfig string, logger logr.Logger) (*action.Configuration, error) {
	actionConfig := new(action.Configuration)

	clientConfig := kube.GetConfig(kubeconfig, settings.KubeContext, namespace)

	err := actionConfig.Init(clientConfig, namespace, "secret", func(format string, v ...interface{}) {
		logger.V(logs.LogDebug).Info(fmt.Sprintf(format, v))
	})
	if err != nil {
		logger.V(logs.LogInfo).Info("%+v", err)
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

func getReleaseInfo(releaseName, releaseNamespace, kubeconfig string, logger logr.Logger) (*releaseInfo, error) {
	actionConfig, err := actionConfigInit(releaseNamespace, kubeconfig, logger)

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
		if currentRelease != nil &&
			currentRelease.ChartVersion == requestedChart.ChartVersion {

			return false
		}
	}

	return requestedChart.HelmChartAction != configv1alpha1.HelmChartActionUninstall
}

// shouldUninstall returns true if action is uninstall there is a release installed currently
func shouldUninstall(currentRelease *releaseInfo, requestedChart *configv1alpha1.HelmChart) bool {
	if requestedChart.HelmChartAction != configv1alpha1.HelmChartActionUninstall {
		return false
	}

	if currentRelease == nil {
		return false
	}

	return true
}

// doInstallRelease installs helm release in the CAPI Cluster.
// No action in DryRun mode.
func doInstallRelease(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, remoteClient client.Client,
	requestedChart *configv1alpha1.HelmChart, kubeconfig string, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Installing chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	// If release namespace does not exist, create it
	err := createNamespace(ctx, remoteClient, clusterSummary,
		requestedChart.ReleaseNamespace)
	if err != nil {
		return err
	}

	err = repoAdd(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	err = repoUpdate(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	var values chartutil.Values
	values, err = getInstantiatedValues(ctx, clusterSummary, mgtmResources, requestedChart, logger)
	if err != nil {
		return err
	}

	err = installRelease(ctx, clusterSummary, settings, requestedChart.ReleaseName,
		requestedChart.ReleaseNamespace, requestedChart.ChartName,
		requestedChart.ChartVersion, kubeconfig,
		values, logger)
	if err != nil {
		return err
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
	mgtmResources map[string]*unstructured.Unstructured, remoteClient client.Client,
	requestedChart *configv1alpha1.HelmChart, kubeconfig string, logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Upgrading chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	// If release namespace does not exist, create it
	err := createNamespace(ctx, remoteClient, clusterSummary,
		requestedChart.ReleaseNamespace)
	if err != nil {
		return err
	}

	err = repoAdd(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	err = repoUpdate(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	var values chartutil.Values
	values, err = getInstantiatedValues(ctx, clusterSummary, mgtmResources, requestedChart, logger)
	if err != nil {
		return err
	}

	err = upgradeRelease(ctx, clusterSummary, settings, requestedChart.ReleaseName,
		requestedChart.ReleaseNamespace, requestedChart.ChartName,
		requestedChart.ChartVersion, kubeconfig,
		values, logger)
	if err != nil {
		return err
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
	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	return updateClusterConfiguration(ctx, c, clusterSummary, clusterProfileOwnerRef, configv1alpha1.FeatureHelm, nil, chartDeployed)
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

	clusterProfileManager, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummaryManager)
	if err != nil || clusterProfileManager == nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterProfile. Err: %v", clusterSummaryManagerName))
		return defaultMessage
	}

	return fmt.Sprintf("Cannot manage it. Currently managed by %s %s", clusterProfileManager.Kind, clusterProfileManager.Name)
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

	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	clusterReportName := getClusterReportName(clusterProfileOwnerRef.Name,
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
			actionConfig, err := actionConfigInit(currentChart.ReleaseNamespace, kubeconfig, logger)

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

func validateInstallHelmResources(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	installObject *action.Install, chartRequested *chart.Chart, values map[string]interface{},
	logger logr.Logger) error {

	// If an Helm chart contains CRDs and also install instances of this CRD, dryRun mode won't work
	// as CRDs are not really installed by DryRun mode and fetching resources will fail.
	// "This chart or one of its subcharts contains CRDs. Rendering may fail or contain inaccuracies."
	// This limitation (do not have any validation if installing such an Helm chart) is listed in the
	// documentation.
	// Workaround here is to skip running Run for Helm in DryRun mode if there are no validation.
	openAPIValidations, luaValidations, err := getComplianceValidations(clusterSummary, logger)
	if err != nil {
		return err
	}

	if len(openAPIValidations) == 0 && len(luaValidations) == 0 {
		return nil
	}

	installObject.DryRun = true

	resources, err := installObject.Run(chartRequested, values)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to run %v", err))
		return err
	}

	var policies []*unstructured.Unstructured
	policies, err = collectHelmContent(resources.Manifest, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect helm resources %v", err))
		return err
	}

	err = validateHelmResourcesAgainstOpenAPIPolicies(ctx, policies, openAPIValidations, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to validate helm resources against openAPI policies %v", err))
		return err
	}

	err = validateHelmResourcesAgainstLuaPolicies(ctx, policies, luaValidations, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to validate helm resources against lua policies %v", err))
		return err
	}

	return nil
}

func validateUpgradeHelmResources(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	upgradeObject *action.Upgrade, releaseName string, chartRequested *chart.Chart,
	values map[string]interface{}, logger logr.Logger) error {

	// If an Helm chart contains CRDs and also install instances of this CRD, dryRun mode won't work
	// as CRDs are not really installed by DryRun mode and fetching resources will fail.
	// "This chart or one of its subcharts contains CRDs. Rendering may fail or contain inaccuracies."
	// This limitation (do not have any validation if installing such an Helm chart) is listed in the
	// documentation.
	// Workaround here is to skip running Run for Helm in DryRun mode if there are no validation.
	openAPIValidations, luaValidations, err := getComplianceValidations(clusterSummary, logger)
	if err != nil {
		return err
	}

	if len(openAPIValidations) == 0 && len(luaValidations) == 0 {
		return nil
	}

	upgradeObject.DryRun = true

	resources, err := upgradeObject.Run(releaseName, chartRequested, values)
	if err != nil {
		return err
	}

	var policies []*unstructured.Unstructured
	policies, err = collectHelmContent(resources.Manifest, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect helm resources %v", err))
		return err
	}

	err = validateHelmResourcesAgainstOpenAPIPolicies(ctx, policies, openAPIValidations, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to validate helm resources against openAPI policies %v", err))
		return err
	}

	err = validateHelmResourcesAgainstLuaPolicies(ctx, policies, luaValidations, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to validate helm resources against lua policies %v", err))
		return err
	}

	return nil
}

// validateHelmResourcesAgainstOpenAPIPolicies validates each individual resource against
// all openAPI policies currently enforced for the managed cluster where resource need to be
// applied
func validateHelmResourcesAgainstOpenAPIPolicies(ctx context.Context, policies []*unstructured.Unstructured,
	openAPIPolicies map[string][]byte, logger logr.Logger) error {

	for i := range policies {
		err := runOpenAPIValidations(ctx, openAPIPolicies, policies[i], logger)
		if err != nil {
			return err
		}
	}

	return nil
}

// getComplianceValidations returns OpenAPI and Lua compliance policies for cluster
func getComplianceValidations(clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger,
) (openAPIValidations, luaValidations map[string][]byte, err error) {

	openAPIValidations, err = getOpenAPIValidations(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, &clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return
	}

	luaValidations, err = getLuaValidations(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		&clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return
	}

	return
}

// validateHelmResourcesAgainstLuaPolicies validates all resources against all lua policies currently
// enforced for the managed cluster where resources need to be applied.
// Lua policies can be written to validate single resources (each deployment replica must be at least 3)
// or combined resources (each deployment must be exposed by a service).
func validateHelmResourcesAgainstLuaPolicies(ctx context.Context, policies []*unstructured.Unstructured,
	luaPolicies map[string][]byte, logger logr.Logger) error {

	err := runLuaValidations(ctx, luaPolicies, policies, logger)
	if err != nil {
		return err
	}

	return nil
}
