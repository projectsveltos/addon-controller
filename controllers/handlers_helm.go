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
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/gofrs/flock"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

var (
	settings          = cli.New()
	defaultUploadPath = "/tmp/charts"
	chartExtension    = "tgz"
)

const (
	writeFilePermission = 0644
	lockTimeout         = 30
)

type releaseInfo struct {
	Name         string      `json:"name"`
	Namespace    string      `json:"namespace"`
	Revision     string      `json:"revision"`
	Updated      metav1.Time `json:"updated"`
	Status       string      `json:"status"`
	Chart        string      `json:"chart"`
	ChartVersion string      `json:"chart_version"`
	AppVersion   string      `json:"app_version"`
}

func deployHelmCharts(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndCAPIClusterClient(ctx, applicant, c, logger)
	if err != nil {
		return err
	}

	var kubeconfigContent []byte
	kubeconfigContent, err = getSecretData(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	var kubeconfig string
	kubeconfig, err = createKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return err
	}
	defer os.Remove(kubeconfig)

	chartDeployed := make([]configv1alpha1.Chart, 0)
	for i := range clusterSummary.Spec.ClusterFeatureSpec.HelmCharts {
		var currentRelease *releaseInfo

		currentRelease, err = getReleaseInfo(clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].ReleaseName,
			clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].ReleaseNamespace,
			kubeconfig, logger)
		// If error is ErrReleaseNotFound move forward
		if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
			return err
		}

		if shouldInstall(currentRelease, &clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i]) {
			err = doInstallRelease(ctx, remoteClient, &clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i],
				kubeconfig, logger)
			if err != nil {
				return err
			}
		} else if shouldUpgrade(currentRelease, &clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i]) {
			err = doUpgradeRelease(ctx, remoteClient, &clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i],
				kubeconfig, logger)
			if err != nil {
				return err
			}
		} else if shouldUninstall(currentRelease, &clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i]) {
			err = doUninstallRelease(&clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i], kubeconfig, logger)
			if err != nil {
				return err
			}
		}

		currentRelease, err = getReleaseInfo(clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].ReleaseName,
			clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].ReleaseNamespace,
			kubeconfig, logger)
		// If error is ErrReleaseNotFound move forward
		if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
			return err
		}

		if currentRelease != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("chart %s (version %s) status: %s",
				currentRelease.Name, currentRelease.ChartVersion, currentRelease.Status))
			if currentRelease.Status == release.StatusDeployed.String() {
				chartDeployed = append(chartDeployed, configv1alpha1.Chart{
					RepoURL:         clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].RepositoryURL,
					Namespace:       currentRelease.Namespace,
					ChartName:       currentRelease.Name,
					ChartVersion:    currentRelease.ChartVersion,
					AppVersion:      currentRelease.AppVersion,
					LastAppliedTime: &currentRelease.Updated,
				})
			}
		}
	}

	return updateChartsInClusterConfiguration(ctx, c, clusterSummary, chartDeployed, logger)
}

func undeployHelmCharts(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	err := c.Get(ctx, types.NamespacedName{Name: applicant}, clusterSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	var kubeconfigContent []byte
	kubeconfigContent, err = getSecretData(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	var kubeconfig string
	kubeconfig, err = createKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return err
	}
	defer os.Remove(kubeconfig)

	for i := range clusterSummary.Spec.ClusterFeatureSpec.HelmCharts {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Uninstalling chart %s from repo %s %s)",
			clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].ChartName,
			clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].RepositoryURL,
			clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].RepositoryName))

		err = uninstallRelease(clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].ReleaseName,
			clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i].ReleaseNamespace, kubeconfig, logger)
		if err != nil {
			if !errors.Is(err, driver.ErrReleaseNotFound) {
				return err
			}
		}
	}
	return nil
}

func helmHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterFeatureSpec.HelmCharts == nil {
		return h.Sum(nil), nil
	}
	for i := range clusterSummary.Spec.ClusterFeatureSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterFeatureSpec.HelmCharts[i]

		config += render.AsCode(*currentChart)
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getHelmRefs(clusterSummary *configv1alpha1.ClusterSummary) []corev1.ObjectReference {
	return nil
}

// RepoAdd adds repo with given name and url
func repoAdd(settings *cli.EnvSettings, name, url string, logger logr.Logger) error {
	logger = logger.WithValues("repo", url)
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
	return nil
}

func installRelease(settings *cli.EnvSettings, releaseName, releaseNamespace, chartName, chartVersion, kubeconfig string,
	logger logr.Logger) error {

	logger = logger.WithValues("release", releaseName, "releaseNamespace", releaseNamespace, "chart",
		chartName, "chartVersion", chartVersion)
	logger.V(logs.LogDebug).Info("installing release")

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
		return err
	}

	installObject := action.NewInstall(actionConfig)
	installObject.ReleaseName = releaseName
	installObject.Namespace = releaseNamespace
	installObject.Version = chartVersion

	cp, err := installObject.ChartPathOptions.LocateChart(chartName, settings)
	if err != nil {
		return err
	}

	chartRequested, err := loader.Load(cp)
	if err != nil {
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
			} else {
				return nil
			}
		}
	}

	vals := map[string]interface{}{}
	_, err = installObject.Run(chartRequested, vals)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("installing release done")
	return nil
}

func uninstallRelease(releaseName, releaseNamespace, kubeconfig string, logger logr.Logger) error {
	logger = logger.WithValues("release", releaseName, "releaseNamespace", releaseNamespace)
	logger.V(logs.LogDebug).Info("uninstalling release")

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

func upgradeRelease(settings *cli.EnvSettings,
	releaseName, releaseNamespace, chartName, chartVersion, kubeconfig string,
	logger logr.Logger) error {

	logger = logger.WithValues("release", releaseName, "releaseNamespace", releaseNamespace, "chart",
		chartName, "chartVersion", chartVersion)
	logger.V(logs.LogDebug).Info("upgrading release")

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
		err = installRelease(settings, releaseName, releaseNamespace, chartName, chartVersion, kubeconfig, logger)
		if err != nil {
			return err
		}
		return nil
	} else if err != nil {
		return err
	}

	_, err = upgradeObject.Run(releaseName, chartRequested, nil)
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
		Name:         results.Name,
		Namespace:    results.Namespace,
		Revision:     strconv.Itoa(results.Version),
		Status:       results.Info.Status.String(),
		Chart:        results.Chart.Metadata.Name,
		ChartVersion: results.Chart.Metadata.Version,
		AppVersion:   results.Chart.AppVersion(),
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
func shouldUpgrade(currentRelease *releaseInfo, requestedChart *configv1alpha1.HelmChart) bool {
	if requestedChart.HelmChartAction == configv1alpha1.HelmChartActionUninstall {
		return false
	}

	if currentRelease != nil &&
		currentRelease.ChartVersion == requestedChart.ChartVersion {

		return false
	}

	return true
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

func doInstallRelease(ctx context.Context, remoteClient client.Client, requestedChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) error {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Installing chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	// If release namespace does not exist, create it
	err := createNamespace(ctx, remoteClient,
		requestedChart.ReleaseNamespace)
	if err != nil {
		return err
	}

	err = repoAdd(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	// TODO: do we need repo update here???

	err = installRelease(settings, requestedChart.ReleaseName,
		requestedChart.ReleaseNamespace,
		requestedChart.ChartName,
		requestedChart.ChartVersion,
		kubeconfig, logger)
	if err != nil {
		return err
	}

	return nil
}

func doUninstallRelease(requestedChart *configv1alpha1.HelmChart, kubeconfig string, logger logr.Logger) error {
	logger.V(logs.LogInfo).Info(fmt.Sprintf("Uninstalling chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	return uninstallRelease(requestedChart.ReleaseName, requestedChart.ReleaseNamespace, kubeconfig, logger)
}

func doUpgradeRelease(ctx context.Context, remoteClient client.Client, requestedChart *configv1alpha1.HelmChart,
	kubeconfig string, logger logr.Logger) error {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Upgrading chart %s from repo %s %s)",
		requestedChart.ChartName,
		requestedChart.RepositoryURL,
		requestedChart.RepositoryName))

	// If release namespace does not exist, create it
	err := createNamespace(ctx, remoteClient,
		requestedChart.ReleaseNamespace)
	if err != nil {
		return err
	}

	err = repoAdd(settings, requestedChart.RepositoryName,
		requestedChart.RepositoryURL, logger)
	if err != nil {
		return err
	}

	err = upgradeRelease(settings, requestedChart.ReleaseName,
		requestedChart.ReleaseNamespace,
		requestedChart.ChartName,
		requestedChart.ChartVersion,
		kubeconfig, logger)
	if err != nil {
		return err
	}

	return nil
}

// updateChartsInClusterConfiguration updates deployed chart info on ClusterConfiguration
func updateChartsInClusterConfiguration(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	chartDeployed []configv1alpha1.Chart, logger logr.Logger) error {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("update deployed chart info. Number of deployed helm chart: %d", len(chartDeployed)))
	clusterFeatureOwnerRef, err := configv1alpha1.GetOwnerClusterFeatureName(clusterSummary)
	if err != nil {
		return err
	}

	return updateClusterConfiguration(ctx, c, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterFeatureOwnerRef, configv1alpha1.FeatureHelm, nil, chartDeployed)
}
