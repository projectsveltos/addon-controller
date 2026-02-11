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
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
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
	"sigs.k8s.io/yaml"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/patcher"
	"github.com/projectsveltos/libsveltos/lib/pullmode"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

var (
	storage    = repo.File{}
	helmLogger = textlogger.NewLogger(textlogger.NewConfig())
)

const (
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

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName))
	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger.V(logs.LogDebug).Info(fmt.Sprintf("deployHelmCharts (pullMode %t)", isPullMode))

	var kubeconfig string
	if !isPullMode {
		var closer func()
		kubeconfig, closer, err = getFileWithKubeconfig(ctx, c, clusterSummary, logger)
		if err != nil {
			return err
		}
		defer closer()
	} else {
		// If SveltosCluster is in pull mode, discard all previous staged resources. Those will be regenerated now.
		err = pullmode.DiscardStagedResourcesForDeployment(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, configv1beta1.ClusterSummaryKind, applicant, string(libsveltosv1beta1.FeatureHelm), logger)
		if err != nil {
			return err
		}
	}

	startInMgmtCluster := startDriftDetectionInMgmtCluster(o)
	err = manageDriftDetectionManagerDeploymentForHelm(ctx, c, clusterSummary, clusterNamespace, clusterName, clusterType,
		isPullMode, startInMgmtCluster, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to manage DriftDetectionManager for helm")
		return err
	}

	mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		return err
	}

	configurationHash, _ := o.HandlerOptions[configurationHash].([]byte)
	err = handleCharts(ctx, clusterSummary, c, remoteClient, kubeconfig, isPullMode, configurationHash, mgmtResources, logger)
	if err != nil {
		return err
	}

	if isPullMode {
		return nil
	}

	err = postProcessDeployedHelmCharts(ctx, clusterSummary, kubeconfig, mgmtResources, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to deploy helmCharts")
	}
	return err
}

func postProcessDeployedHelmCharts(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) error {

	c := getManagementClusterClient()

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	clusterNamespace := clusterSummary.Spec.ClusterNamespace
	clusterName := clusterSummary.Spec.ClusterName
	clusterType := clusterSummary.Spec.ClusterType

	var helmResources []libsveltosv1beta1.HelmResources
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection ||
		clusterSummary.Spec.ClusterProfileSpec.Reloader {

		var err error
		helmResources, err = collectResourcesFromManagedHelmChartsForDriftDetection(ctx, c, clusterSummary, kubeconfig,
			mgmtResources, logger)
		if err != nil {
			return err
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		clusterClient, err := getResourceSummaryClient(ctx, clusterNamespace, clusterName, clusterType, logger)
		if err != nil {
			return err
		}

		resourceSummaryNameInfo := getResourceSummaryNameInfo(clusterNamespace, clusterSummary.Name)

		// Deploy resourceSummary
		err = deployer.DeployResourceSummaryInCluster(ctx, clusterClient, resourceSummaryNameInfo, clusterNamespace, clusterName,
			clusterSummary.Name, clusterType, nil, nil, helmResources, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			logger)
		if err != nil {
			return err
		}
	}

	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}

	// Update Reloader instance. If ClusterProfile Reloader knob is set to true, sveltos will
	// start a rolling upgrade for all Deployment/StatefulSet/DaemonSet instances deployed by Sveltos
	// in the managed cluster when a mounted ConfigMap/Secret is updated. In order to do so, sveltos-agent
	// needs to be instructed which Deployment/StatefulSet/DaemonSet instances require this behavior.
	// Update corresponding Reloader instance (instance will be deleted if Reloader is set to false)
	resources := clusterops.ConvertHelmResourcesToObjectReference(helmResources)
	err = updateReloaderWithDeployedResources(ctx, clusterSummary, profileRef, libsveltosv1beta1.FeatureHelm,
		resources, !clusterSummary.Spec.ClusterProfileSpec.Reloader, logger)
	if err != nil {
		return err
	}

	cacheMgr := clustercache.GetManager()
	remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}
	return clusterops.ValidateHealthPolicies(ctx, remoteRestConfig, clusterSummary.Spec.ClusterProfileSpec.ValidateHealths,
		libsveltosv1beta1.FeatureHelm, false, logger)
}

func manageDriftDetectionManagerDeploymentForHelm(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, isPullMode, startInMgmtCluster bool, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err := deployDriftDetectionManagerInCluster(ctx, c, clusterNamespace, clusterName, clusterSummary.Name,
			string(libsveltosv1beta1.FeatureHelm), clusterType, isPullMode, startInMgmtCluster, logger)
		if err != nil {
			return err
		}

		// If SveltosCLuster is in pull mode, do nothing. A knob will notify agent about DriftDetection being set
		// and agent will manage the ResourceSummary life cycle in the managed cluster
		if isPullMode {
			return nil
		}

		clusterClient, err := getResourceSummaryClient(ctx, clusterNamespace, clusterName, clusterType, logger)
		if err != nil {
			return err
		}

		resourceSummaryNameInfo := getResourceSummaryNameInfo(clusterNamespace, clusterSummary.Name)

		// Since we are updating resources to watch for drift, remove helm section in ResourceSummary to eliminate
		// un-needed reconciliation (Sveltos is updating those resources so we don't want drift-detection to think
		// a configuration drift is happening)
		err = deployer.DeployResourceSummaryInCluster(ctx, clusterClient, resourceSummaryNameInfo, clusterNamespace, clusterName,
			clusterSummary.Name, clusterType, nil, nil, []libsveltosv1beta1.HelmResources{}, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to remove ResourceSummary.")
			return err
		}
	}

	return nil
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

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	if isPullMode {
		mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		}

		return undeployHelmChartsInPullMode(ctx, c, clusterSummary, mgmtResources, logger)
	}

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

	mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		return err
	}

	err = undeployHelmChartResources(ctx, c, clusterSummary, kubeconfig, mgmtResources, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to undeploy HelmCharts")
	}
	return err
}

func undeployHelmChartsInPullMode(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("undeployHelmCharts in pullmode")

	// Discard all previous staged resources. Those will be regenerated now.
	err := pullmode.DiscardStagedResourcesForDeployment(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name,
		string(libsveltosv1beta1.FeatureHelm), logger)
	if err != nil {
		return err
	}

	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}
	setters := prepareSetters(clusterSummary, libsveltosv1beta1.FeatureHelm, profileRef, nil, true)

	// If charts have pre/post delete hooks, those need to be deployed. A ConfigurationGroup to deploy those
	// is created. If this does not exist yet assume we still have to deploy those.
	status, err := pullmode.GetDeploymentStatus(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name,
		string(libsveltosv1beta1.FeatureHelm), logger)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to GetDeploymentStatus: %v", err))
			return err
		}
	} else {
		// if the status is deployed it means pre/post delete hooks have been deployed. So proceed undeploying
		if status != nil && status.DeploymentStatus != nil && *status.DeploymentStatus == libsveltosv1beta1.FeatureStatusProvisioned {
			if isLeavePolicies(clusterSummary, logger) {
				logger.V(logs.LogInfo).Info("ClusterProfile StopMatchingBehavior set to LeavePolicies")
			}

			err = clearRegisteredChartVersions(ctx, clusterSummary)
			if err != nil {
				return err
			}

			return pullmode.RemoveDeployedResources(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
				clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name,
				string(libsveltosv1beta1.FeatureHelm), logger, setters...)
		} else if status != nil && status.DeploymentStatus != nil && *status.DeploymentStatus == libsveltosv1beta1.FeatureStatusProvisioning {
			logger.V(logs.LogInfo).Info("Applier is handling delete hooks")
			return nil
		} else if status != nil && status.DeploymentStatus != nil && *status.DeploymentStatus == libsveltosv1beta1.FeatureStatusFailed {
			msg := "Applier failed."
			if status.FailureMessage != nil {
				msg += fmt.Sprintf("Failure message: %s", *status.FailureMessage)
			}
			logger.V(logs.LogInfo).Info(msg)
		}
	}

	// Walks all helm charts, if any pre/post delete hook is present, Sveltos asks applier to deploy those
	err = walkAndUndeployHelmChartsInPullMode(ctx, c, clusterSummary, mgmtResources, logger)
	if err != nil {
		return err
	}

	return nil
}

func clearRegisteredChartVersions(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary) error {
	chartManager, err := chartmanager.GetChartManagerInstance(ctx, getManagementClusterClient())
	if err != nil {
		return err
	}
	managedReleases := chartManager.GetManagedHelmReleases(clusterSummary)

	for i := range managedReleases {
		chartManager.UnregisterVersionForChart(clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, managedReleases[i].Namespace, managedReleases[i].Name)
	}

	return nil
}

func walkAndUndeployHelmChartsInPullMode(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) error {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return err
	}
	requeued := false

	// If this ClusterSummary is currently managing Helm charts that other ClusterProfiles are also attempting to
	// manage, it should not uninstall them. Instead, it should change its tier to allow the other ClusterProfiles to take
	// over management.
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]

		instantiatedChart, err := getInstantiatedChart(ctx, clusterSummary, currentChart, mgmtResources, logger)
		if err != nil {
			return err
		}

		l := logger.WithValues("chart", fmt.Sprintf("%s/%s", instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName))
		l.V(logs.LogDebug).Info(fmt.Sprintf("Undeploying chart: %v", instantiatedChart))

		canManage, err := determineChartOwnership(ctx, c, clusterSummary, instantiatedChart, l)
		if err != nil {
			return err
		}
		if canManage {
			// If another ClusterSummary is queued to manage this chart in this cluster, do not uninstall.
			// Let the other ClusterSummary take it over.
			otherRegisteredClusterSummaries := chartManager.GetRegisteredClusterSummariesForChart(
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
				clusterSummary.Spec.ClusterType, instantiatedChart)
			if len(otherRegisteredClusterSummaries) > 1 {
				// Set an artificial high tier. this allows other ClusterSummary to take over the
				// management of helm chart
				logger.V(logs.LogInfo).Info("can manage but other profiles wants to take it over")
				if clusterSummary.Spec.ClusterProfileSpec.Tier != math.MaxInt32 {
					l.V(logs.LogDebug).Info("adjust tier for chart")
					clusterSummary.Spec.ClusterProfileSpec.Tier = math.MaxInt32
					err = c.Update(ctx, clusterSummary)
					if err != nil {
						l.V(logs.LogInfo).Error(err, "Failed to update adjust tier")
						return err
					}
				}

				_, _, err = handleChart(ctx, clusterSummary, mgmtResources, instantiatedChart, "", true, l)
				if err != nil {
					l.V(logs.LogInfo).Error(err, "Failed to handle charts after tier adjustment")
					return err
				}

				l.V(logs.LogDebug).Info("unregister as chart manager")
				// Immediately unregister so next inline ClusterSummary can take this over
				chartManager.UnregisterClusterSummaryForChart(clusterSummary, instantiatedChart)

				// Requeue all other ClusterSummaries
				l.V(logs.LogDebug).Info("requeue conflicting clusterSummaries for this chart")
				err = requeueAllOtherClusterSummaries(ctx, c, clusterSummary.Spec.ClusterNamespace,
					otherRegisteredClusterSummaries, l)
				if err != nil {
					l.V(logs.LogInfo).Error(err, "Failed to requeue other ClusterSummaries during conflict resolution")
					return err
				}
				requeued = true
			} else {
				l.V(logs.LogDebug).Info("prepare delete hook resources")
				_, _, err = handleChart(ctx, clusterSummary, mgmtResources, instantiatedChart, "", true, l)
				if err != nil {
					l.V(logs.LogInfo).Error(err, "Failed to handle chart delete")
					return err
				}
			}
		}
	}

	if requeued {
		msg := "ClusterSummary tier adjusted to yield Helm chart management. Conflicting ClusterSummaries requeued."
		logger.V(logs.LogInfo).Info(msg, "newTier", clusterSummary.Spec.ClusterProfileSpec.Tier)
		// HandOverError is returned. This ClusterSummary will be reconciled back allowining the other clusterSummary
		// instance to take over first
		// Setting the next reconcile time
		return &configv1beta1.HandOverError{Message: msg}
	}

	err = commitStagedResourcesForDeployment(ctx, clusterSummary, nil, mgmtResources, logger)
	if err != nil {
		return err
	}

	return nil
}

func undeployHelmChartResources(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("undeployHelmChartResources")

	err := validatePreDeleteChecks(ctx, clusterSummary, libsveltosv1beta1.FeatureHelm, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "pre delete checks failed")
		return err
	}

	releaseReports, err := uninstallHelmCharts(ctx, c, clusterSummary, kubeconfig, logger)
	if err != nil {
		return err
	}

	err = validatePostDeleteChecks(ctx, clusterSummary, libsveltosv1beta1.FeatureHelm, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "post delete checks failed")
		return err
	}

	// First get the helm releases currently managed and uninstall all the ones
	// not referenced anymore. Only if this operation succeeds, removes all stale
	// helm release registration for this clusterSummary.
	var undeployedReports []configv1beta1.ReleaseReport
	undeployedReports, err = undeployStaleReleases(ctx, c, clusterSummary, kubeconfig, mgmtResources, logger)
	if err != nil {
		return err
	}
	releaseReports = append(releaseReports, undeployedReports...)

	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}

	err = updateReloaderWithDeployedResources(ctx, clusterSummary, profileRef, libsveltosv1beta1.FeatureKustomize,
		nil, true, logger)
	if err != nil {
		return err
	}

	isDrynRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
	clean := !clusterSummary.DeletionTimestamp.IsZero()
	err = clusterops.UpdateClusterConfiguration(ctx, c, isDrynRun, clean, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, profileRef, libsveltosv1beta1.FeatureHelm,
		nil, []configv1beta1.Chart{})
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

	csCopy, err := getClusterSummaryWithInstantiatedCharts(ctx, clusterSummary,
		mgmtResources, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get instantiated charts")
		return err
	}

	// In dry-run mode nothing gets deployed/undeployed. So if this instance used to manage
	// an helm release and it is now not referencing anymore, do not unsubscribe.
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
		chartManager.RemoveStaleRegistrations(csCopy)
		return nil
	}

	return &configv1beta1.DryRunReconciliationError{}
}

// canUninstallHelmChart verifies whether a ClusterSummary can remove an helm chart.
// A ClusterSummary can only uninstall a Helm chart if it's the designated manager for that chart and no
// other ClusterSummary is attempting to manage it. If another ClusterSummary is trying to take control
// of the same chart, the current ClusterSummary should not uninstall it, allowing the other to assume
// management.
func canUninstallHelmChart(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	instantiatedChart *configv1beta1.HelmChart, logger logr.Logger) (bool, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return false, err
	}

	canManage, err := determineChartOwnership(ctx, c, clusterSummary, instantiatedChart, logger)
	if err != nil {
		return false, err
	}
	if canManage {
		// If another ClusterSummary is queued to manage this chart in this cluster, do not uninstall.
		// Let the other ClusterSummary take it over.
		otherRegisteredClusterSummaries := chartManager.GetRegisteredClusterSummariesForChart(
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Spec.ClusterType, instantiatedChart)
		if len(otherRegisteredClusterSummaries) > 1 {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("Requeuing other ClusterSummary for chart %s from repo %s %s)",
				instantiatedChart.ChartName, instantiatedChart.RepositoryURL, instantiatedChart.RepositoryName))

			// Immediately unregister so next inline ClusterSummary can take this over
			chartManager.UnregisterClusterSummaryForChart(clusterSummary, instantiatedChart)
			err = requeueAllOtherClusterSummaries(ctx, c, clusterSummary.Spec.ClusterNamespace,
				otherRegisteredClusterSummaries, logger)
			if err != nil {
				// TODO: Handle errors to prevent bad state. ClusterSummary no longer manage the chart,
				// but no other ClusterSummary instance has been requeued.
				return false, err
			}
			return false, nil
		}
		return true, nil
	}

	return false, nil
}

func uninstallHelmCharts(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, logger logr.Logger) ([]configv1beta1.ReleaseReport, error) {

	mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}
	if len(chartManager.GetManagedHelmReleases(clusterSummary)) == 0 {
		chartManager.RemoveAllRegistrations(clusterSummary)
		return nil, nil
	}

	releaseReports := make([]configv1beta1.ReleaseReport, 0)
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]

		instantiatedChart, err := getInstantiatedChart(ctx, clusterSummary, currentChart, mgmtResources, logger)
		if err != nil {
			return nil, err
		}

		canUninstall, err := canUninstallHelmChart(ctx, c, clusterSummary, instantiatedChart, logger)
		if err != nil {
			return nil, err
		}
		if canUninstall {
			// If StopMatchingBehavior is LeavePolicies, do not uninstall helm charts
			if isLeavePolicies(clusterSummary, logger) {
				logger.V(logs.LogInfo).Info("ClusterProfile StopMatchingBehavior set to LeavePolicies")
			} else {
				credentialsPath, caPath, err := getCredentialsAndCAFiles(ctx, c, clusterSummary, instantiatedChart)
				if err != nil {
					logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to process credentials %v", err))
					return nil, err
				}

				registryOptions := &registryClientOptions{
					credentialsPath: credentialsPath, caPath: caPath,
					skipTLSVerify: getInsecureSkipTLSVerify(instantiatedChart),
					plainHTTP:     getPlainHTTP(instantiatedChart),
				}

				currentRelease, err := getReleaseInfo(instantiatedChart.ReleaseName, instantiatedChart.ReleaseNamespace,
					kubeconfig, registryOptions, getEnableClientCacheValue(instantiatedChart.Options))
				if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
					return nil, err
				}

				if currentRelease != nil && currentRelease.Status != string(release.StatusUninstalled) {
					err = doUninstallRelease(ctx, clusterSummary, instantiatedChart, kubeconfig, registryOptions, logger)
					if err != nil {
						if !errors.Is(err, driver.ErrReleaseNotFound) {
							return nil, err
						}
					}
				}
			}

			releaseReports = append(releaseReports, configv1beta1.ReleaseReport{
				ReleaseNamespace: instantiatedChart.ReleaseNamespace, ReleaseName: instantiatedChart.ReleaseName,
				Action: string(configv1beta1.UninstallHelmAction),
			})
		} else {
			releaseReports = append(releaseReports, configv1beta1.ReleaseReport{
				ReleaseNamespace: instantiatedChart.ReleaseNamespace, ReleaseName: instantiatedChart.ReleaseName,
				Action: string(configv1beta1.NoHelmAction), Message: "Currently managed by another ClusterProfile",
			})
		}
	}

	return releaseReports, nil
}

func helmHash(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) ([]byte, error) {

	config, err := getClusterProfileSpecHash(ctx, clusterSummary, logger)
	if err != nil {
		return nil, err
	}

	h := sha256.New()

	if clusterSummary.Spec.ClusterProfileSpec.HelmCharts == nil {
		return h.Sum(nil), nil
	}

	mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}

	sortedHelmCharts := getSortedHelmCharts(clusterSummary.Spec.ClusterProfileSpec.HelmCharts)

	for i := range sortedHelmCharts {
		currentChart := &sortedHelmCharts[i]
		helmChartHash, err := getHelmChartHash(ctx, c, clusterSummary, currentChart, mgmtResources, logger)
		if err != nil {
			return nil, err
		}

		config += helmChartHash
	}

	for i := range clusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		h := &clusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]
		if h.FeatureID == libsveltosv1beta1.FeatureHelm {
			config += render.AsCode(h)
		}
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getHelmChartHash(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) (string, error) {

	config := render.AsCode(*currentChart)

	clusterNamespace := clusterSummary.Spec.ClusterNamespace
	clusterName := clusterSummary.Spec.ClusterName
	clusterType := clusterSummary.Spec.ClusterType

	if isReferencingFluxSource(currentChart) {
		sourceRef, repoPath, err := getReferencedFluxSourceFromURL(currentChart)
		if err != nil {
			return "", err
		}

		namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c,
			clusterNamespace, clusterName, sourceRef.Namespace, clusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate namespace for %s: %v",
				sourceRef.Namespace, err))
			// Ignore template instantiation error
			return config, nil
		}

		name, err := libsveltostemplate.GetReferenceResourceName(ctx, c,
			clusterNamespace, clusterName, sourceRef.Name, clusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s: %v",
				sourceRef.Name, err))
			// Ignore template instantiation error
			return config, nil
		}

		source, err := getSource(ctx, c, namespace, name, sourceRef.Kind)
		if err != nil {
			return "", err
		}
		if source == nil {
			return config, nil
		}
		s := source.(sourcev1.Source)
		if s.GetArtifact() != nil {
			config += s.GetArtifact().Revision
			config += repoPath
		}
	}

	valuesHash, err := getHelmChartInstantiatedValues(ctx, clusterSummary,
		mgmtResources, currentChart, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get hash from referenced ConfigMap/Secret in ValuesFrom %v", err))
		return "", err
	}
	valuesAsString, err := valuesHash.YAML()
	if err != nil {
		return "", err
	}
	config += valuesAsString

	return config, nil
}

func getHelmRefs(clusterSummary *configv1beta1.ClusterSummary) []configv1beta1.PolicyRef {
	return nil
}

func handleCharts(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary, c, remoteClient client.Client,
	kubeconfig string, isPullMode bool, configurationHash []byte, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) error {

	var err error
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

	releaseReports, chartDeployed, deployError := walkChartsAndDeploy(ctx, c, clusterSummary, kubeconfig, mgmtResources,
		isPullMode, logger)
	// Even if there is a deployment error do not return just yet. Update various status and clean stale resources.

	// If there was an helm release previous managed by this ClusterSummary and currently not referenced
	// anymore, such helm release has been successfully remove at this point. So
	clusterSummary, err = updateStatusForNonReferencedHelmReleases(ctx, c, clusterSummary, mgmtResources, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("handleCharts failed %v", err))
		return err
	}

	if isPullMode {
		err = commitStagedResourcesForDeployment(ctx, clusterSummary, configurationHash, mgmtResources, logger)
		if err != nil {
			return err
		}

		err = updateClusterReportWithHelmReports(ctx, c, clusterSummary, releaseReports)
		if err != nil {
			return err
		}

		if deployError != nil {
			return deployError
		}
	} else {
		// First get the helm releases currently managed and uninstall all the ones
		// not referenced anymore. Only if this operation succeeds, removes all stale
		// helm release registration for this clusterSummary.
		var undeployedReports []configv1beta1.ReleaseReport
		undeployedReports, err = undeployStaleReleases(ctx, c, clusterSummary, kubeconfig, mgmtResources, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("undeployStaleReleases failed %v", err))
			return err
		}
		releaseReports = append(releaseReports, undeployedReports...)
		if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
			chartManager, mgrErr := chartmanager.GetChartManagerInstance(ctx, c)
			if mgrErr != nil {
				return mgrErr
			}

			csCopy, err := getClusterSummaryWithInstantiatedCharts(ctx, clusterSummary,
				mgmtResources, logger)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to get instantiated charts")
				return err
			}

			chartManager.RemoveStaleRegistrations(csCopy)
		}

		err = updateChartsInClusterConfiguration(ctx, c, clusterSummary, chartDeployed, logger)
		if err != nil {
			return err
		}

		err = updateClusterReportWithHelmReports(ctx, c, clusterSummary, releaseReports)
		if err != nil {
			return err
		}
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
	kubeconfig string, mgmtResources map[string]*unstructured.Unstructured, isPullMode bool, logger logr.Logger,
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

		l := logger.WithValues("chart", fmt.Sprintf("%s/%s", instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName))
		l.V(logs.LogDebug).Info(fmt.Sprintf("Deploying chart: %v", instantiatedChart))

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
				&configv1beta1.NonRetriableError{Message: conflictErrorMessage}
		}

		var report *configv1beta1.ReleaseReport
		var currentRelease *releaseInfo
		currentRelease, report, err = handleChart(ctx, clusterSummary, mgmtResources, instantiatedChart, kubeconfig,
			isPullMode, logger)
		if err != nil {
			if clusterSummary.Spec.ClusterProfileSpec.ContinueOnError {
				errorMsg += fmt.Sprintf("chart: %s, release: %s, %v\n",
					instantiatedChart.ChartName, instantiatedChart.ReleaseName, err)
				continue
			}
			return releaseReports, chartDeployed, err
		}

		valueHash, err := updateValueHashOnHelmChartSummary(ctx, instantiatedChart, clusterSummary, mgmtResources, logger)
		if err != nil {
			return releaseReports, chartDeployed, err
		}

		releaseReports = append(releaseReports, *report)

		if currentRelease != nil {
			if valueHash != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("release %s/%s (version %s) (value hash %x) status: %s",
					currentRelease.ReleaseNamespace, currentRelease.ReleaseName, currentRelease.ChartVersion,
					valueHash, currentRelease.Status))
			} else {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("release %s/%s (version %s) status: %s",
					currentRelease.ReleaseNamespace, currentRelease.ReleaseName, currentRelease.ChartVersion, currentRelease.Status))
			}
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
		return releaseReports, chartDeployed, &configv1beta1.NonRetriableError{Message: conflictErrorMessage}
	}

	return releaseReports, chartDeployed, nil
}

func generateConflictForHelmChart(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	instantiatedChart *configv1beta1.HelmChart) string {

	c := getManagementClusterClient()

	message := fmt.Sprintf("cannot manage chart %s/%s.",
		instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName)
	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return message
	}

	var managerName string
	managerName, err = chartManager.GetManagerForChart(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, instantiatedChart)
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
	instantiatedChart *configv1beta1.HelmChart, logger logr.Logger) (canManage bool, err error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return false, err
	}

	l := logger.WithValues("release", fmt.Sprintf("%s:%s",
		instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName))

	if !chartManager.CanManageChart(claimingHelmManager, instantiatedChart) {
		// Another ClusterSummay is already managing this chart. Get the:
		// 1. ClusterSummary managing the chart
		// 2. Use the ClusterSummary's tiers to decide who should managed it
		l.V(logs.LogDebug).Info("conflict detected")
		clusterSummaryManaging, err := chartManager.GetManagerForChart(claimingHelmManager.Spec.ClusterNamespace, claimingHelmManager.Spec.ClusterName,
			claimingHelmManager.Spec.ClusterType, instantiatedChart)
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

		if deployer.HasHigherOwnershipPriority(currentHelmManager.Spec.ClusterProfileSpec.Tier, claimingHelmManager.Spec.ClusterProfileSpec.Tier) {
			if claimingHelmManager.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
				// Since we are in DryRun mode do not reset the other ClusterSummary. It will still be managing
				// the helm chart
				return true, nil
			}
			// New ClusterSummary is taking over managing this chart. So reset helmReleaseSummaries for this chart
			// This needs to happen immediately. helmReleaseSummaries are used by Sveltos to rebuild list of which
			// clusterSummary is managing an helm chart if pod restarts
			err = resetHelmReleaseSummaries(ctx, c, currentHelmManager, instantiatedChart, logger)
			if err != nil {
				return false, err
			}

			// Reset Status of the ClusterSummary previously managing this resource
			err = requeueClusterSummary(ctx, libsveltosv1beta1.FeatureHelm, currentHelmManager, logger)
			if err != nil {
				return false, err
			}

			// Set current ClusterSummary as the new manager
			chartManager.SetManagerForChart(claimingHelmManager, instantiatedChart)
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
	registryOptions *registryClientOptions, isPullMode, templateOnly bool, logger logr.Logger,
) (*release.Release, *configv1beta1.ReleaseReport, error) {

	logger.V(logs.LogDebug).Info("install helm release")

	//nolint: gosec // maxHistory is guaranteed to be non-negative
	maxHistory := uint(getMaxHistoryValue(currentChart.Options))

	if !isPullMode {
		if fs := getFeatureSummaryForFeatureID(clusterSummary, libsveltosv1beta1.FeatureHelm); fs != nil {
			if fs.ConsecutiveFailures%maxHistory == 0 && fs.FailureMessage != nil {
				err := doUninstallRelease(ctx, clusterSummary, currentChart, kubeconfig, registryOptions, logger)
				if err != nil {
					// Ignore release not found error
					if errors.Is(err, driver.ErrReleaseNotFound) || strings.Contains(err.Error(), "release: not found") {
						logger.V(logs.LogInfo).Info("Release not found, ignoring error")
					} else {
						logger.V(logs.LogInfo).Info(fmt.Sprintf("handleInstall failed %v", err))
						return nil, nil, err
					}
				}
			}
		}
	}

	var report *configv1beta1.ReleaseReport

	helmRelease, err := doInstallRelease(ctx, clusterSummary, mgmtResources, currentChart, kubeconfig, registryOptions, templateOnly, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("doInstallRelease error %v", err))
		return nil, nil, err
	}
	report = &configv1beta1.ReleaseReport{
		ReleaseNamespace: currentChart.ReleaseNamespace, ReleaseName: currentChart.ReleaseName,
		ChartVersion: currentChart.ChartVersion, Action: string(configv1beta1.InstallHelmAction),
	}
	return helmRelease, report, nil
}

func handleUpgrade(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, currentChart *configv1beta1.HelmChart,
	currentRelease *releaseInfo, kubeconfig string, registryOptions *registryClientOptions,
	logger logr.Logger) (*release.Release, *configv1beta1.ReleaseReport, error) {

	var report *configv1beta1.ReleaseReport
	logger.V(logs.LogDebug).Info("upgrade helm release")
	helmRelease, err := doUpgradeRelease(ctx, clusterSummary, mgmtResources, currentChart, kubeconfig, registryOptions, logger)
	if err != nil {
		return nil, nil, err
	}

	var message string
	current, err := semver.NewVersion(currentRelease.ChartVersion)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("chartVersion: %s failed to get semantic version. Err: %v",
			currentRelease.ChartVersion, err))
		return helmRelease, nil, err
	}
	expected, err := semver.NewVersion(currentChart.ChartVersion)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("currentVersion: %s failed to get semantic version. Err: %v",
			currentChart.ChartVersion, err))
		return helmRelease, nil, err
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
	return helmRelease, report, nil
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

	parsedURL, err := url.Parse(currentChart.RepositoryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse registry URL '%s': %w", currentChart.RepositoryURL, err)
	}
	registryOptions.hostname = parsedURL.Host

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

		username, password, err := getUsernameAndPasswordFromSecret(parsedURL.Host, secret)
		if err != nil {
			return registryOptions, err
		}

		registryOptions.username = username
		registryOptions.password = password
	}

	return registryOptions, nil
}

func handleChart(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, instantiatedChart *configv1beta1.HelmChart,
	kubeconfig string, isPullMode bool, logger logr.Logger) (*releaseInfo, *configv1beta1.ReleaseReport, error) {

	registryOptions, err := createRegistryClientOptions(ctx, clusterSummary, instantiatedChart, logger)
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

	logger = logger.WithValues("releaseNamespace", instantiatedChart.ReleaseNamespace, "releaseName",
		instantiatedChart.ReleaseName, "version", instantiatedChart.ChartVersion)

	if instantiatedChart.RegistryCredentialsConfig != nil &&
		instantiatedChart.RegistryCredentialsConfig.CredentialsSecretRef != nil {

		err = doLogin(registryOptions, instantiatedChart.ReleaseNamespace, instantiatedChart.RepositoryURL)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to login %v", err))
			return nil, nil, err
		}
	}

	if isPullMode {
		releaseInfo, releaseReport, err := prepareChartForAgent(ctx, clusterSummary, mgmtResources, instantiatedChart,
			registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
		chartManager, err := chartmanager.GetChartManagerInstance(ctx, getManagementClusterClient())
		if err != nil {
			return nil, nil, err
		}
		chartManager.RegisterVersionForChart(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			instantiatedChart)

		return releaseInfo, releaseReport, nil
	}

	return deployHelmChart(ctx, clusterSummary, mgmtResources, instantiatedChart, kubeconfig, registryOptions, logger)
}

func deployHelmChart(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, instantiatedChart *configv1beta1.HelmChart,
	kubeconfig string, registryOptions *registryClientOptions, logger logr.Logger,
) (*releaseInfo, *configv1beta1.ReleaseReport, error) {

	currentRelease, err := getReleaseInfo(instantiatedChart.ReleaseName,
		instantiatedChart.ReleaseNamespace, kubeconfig, registryOptions,
		getEnableClientCacheValue(instantiatedChart.Options))
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil, err
	}
	var report *configv1beta1.ReleaseReport

	// In pull mode we cannot verify
	if shouldInstall(currentRelease, instantiatedChart) {
		_, report, err = handleInstall(ctx, clusterSummary, mgmtResources, instantiatedChart, kubeconfig,
			registryOptions, false, false, logger)
		if err != nil {
			return nil, nil, err
		}
	} else if shouldUpgrade(ctx, currentRelease, instantiatedChart, clusterSummary, mgmtResources, logger) {
		_, report, err = handleUpgrade(ctx, clusterSummary, mgmtResources, instantiatedChart, currentRelease, kubeconfig,
			registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
	} else if shouldUninstall(currentRelease, instantiatedChart) {
		report, err = handleUninstall(ctx, clusterSummary, instantiatedChart, kubeconfig, registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
		report.ChartVersion = currentRelease.ChartVersion
	} else if currentRelease == nil {
		logger.V(logs.LogDebug).Info("no action for helm release")
		report = &configv1beta1.ReleaseReport{
			ReleaseNamespace: instantiatedChart.ReleaseNamespace, ReleaseName: instantiatedChart.ReleaseName,
			ChartVersion: instantiatedChart.ChartVersion, Action: string(configv1beta1.NoHelmAction),
		}
		report.Message = notInstalledMessage
	} else {
		logger.V(logs.LogDebug).Info("no action for helm release")

		report, err = generateReportForSameVersion(ctx, currentRelease.Values, clusterSummary, mgmtResources,
			instantiatedChart, kubeconfig, registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
	}

	if currentRelease != nil {
		err = addExtraMetadata(ctx, instantiatedChart, clusterSummary, kubeconfig, registryOptions, logger)
		if err != nil {
			return nil, nil, err
		}
	}

	currentRelease, err = getReleaseInfo(instantiatedChart.ReleaseName, instantiatedChart.ReleaseNamespace,
		kubeconfig, registryOptions, getEnableClientCacheValue(instantiatedChart.Options))
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
			logger.V(logs.LogDebug).Info(
				fmt.Sprintf("Failed to download repository index: %v", err))
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

func getChartVersion(requestedChart *configv1beta1.HelmChart, chartRequested *chart.Chart) string {
	if requestedChart.ChartVersion == "" && isReferencingFluxSource(requestedChart) {
		return chartRequested.AppVersion()
	}

	return requestedChart.ChartVersion
}

// installRelease installs helm release in the Cluster.
// No action in DryRun mode.
func installRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary, settings *cli.EnvSettings,
	requestedChart *configv1beta1.HelmChart, kubeconfig string, registryOptions *registryClientOptions,
	values map[string]interface{}, mgmtResources map[string]*unstructured.Unstructured, templateOnly bool, logger logr.Logger,
) (*release.Release, error) {

	if !isReferencingFluxSource(requestedChart) && requestedChart.ChartName == "" {
		return nil, fmt.Errorf("chart name can not be empty")
	}

	logger = logger.WithValues("release", requestedChart.ReleaseName, "releaseNamespace",
		requestedChart.ReleaseNamespace, "chart", requestedChart.ChartName, "chartVersion", requestedChart.ChartVersion)

	tmpDir, chartName, repoURL, err := getHelmChartAndRepoName(ctx, clusterSummary, requestedChart, logger)
	if err != nil {
		return nil, err
	}

	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
		chartName = filepath.Join(tmpDir, chartName)
	}

	logger = logger.WithValues("repositoryURL", repoURL, "chart", chartName,
		"credentials", registryOptions.credentialsPath, "ca",
		registryOptions.caPath, "insecure", registryOptions.skipTLSVerify)
	logger.V(logs.LogDebug).Info("installing release")

	patches, err := initiatePatches(ctx, clusterSummary, requestedChart.ChartName, mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	installClient, err := getHelmInstallClient(ctx, requestedChart, kubeconfig, registryOptions, patches,
		templateOnly, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get helm install client: %v", err))
		return nil, err
	}

	cp, err := installClient.LocateChart(chartName, settings)
	if err != nil {
		handleLocateChartError(settings, requestedChart, registryOptions, err, logger)
		return nil, err
	}

	chartRequested, err := loader.Load(cp)
	if err != nil {
		logger.V(logs.LogDebug).Info("Load failed")
		return nil, err
	}

	requestedChart.ChartVersion = getChartVersion(requestedChart, chartRequested)

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
			_, err = upgradeRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig, registryOptions,
				values, mgmtResources, logger)
			return nil, err
		}
		return nil, err
	}

	logger.V(logs.LogDebug).Info("installing release done")

	return r, nil
}

func checkDependencies(chartRequested *chart.Chart, installClient *action.Install, cp string, settings *cli.EnvSettings) error {
	if req := chartRequested.Metadata.Dependencies; req != nil {
		err := action.CheckDependencies(chartRequested, req)
		if err != nil {
			if installClient.DependencyUpdate {
				man := &downloader.Manager{
					ChartPath:        cp,
					Keyring:          installClient.Keyring,
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

// uninstallRelease removes helm release from a managed Cluster.
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

	err = validatePreDeleteChecks(ctx, clusterSummary, libsveltosv1beta1.FeatureHelm, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "pre delete checks failed")
		return err
	}

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

	err = validatePostDeleteChecks(ctx, clusterSummary, libsveltosv1beta1.FeatureHelm, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "post delete checks failed")
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
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) (*release.Release, error) {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil, nil
	}

	if !isReferencingFluxSource(requestedChart) && requestedChart.ChartName == "" {
		return nil, fmt.Errorf("chart name can not be empty")
	}

	logger = logger.WithValues("release", requestedChart.ReleaseName, "releaseNamespace", requestedChart.ReleaseNamespace,
		"chartVersion", requestedChart.ChartVersion)

	tmpDir, chartName, repoURL, err := getHelmChartAndRepoName(ctx, clusterSummary, requestedChart, logger)
	if err != nil {
		return nil, err
	}
	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
		chartName = filepath.Join(tmpDir, chartName)
	}

	logger = logger.WithValues("repositoryURL", repoURL, "chart", chartName,
		"credentials", registryOptions.credentialsPath, "ca",
		registryOptions.caPath, "insecure", registryOptions.skipTLSVerify)
	logger.V(logs.LogDebug).Info("upgrading release")

	actionConfig, err := actionConfigInit(requestedChart.ReleaseNamespace, kubeconfig, registryOptions,
		getEnableClientCacheValue(requestedChart.Options))
	if err != nil {
		return nil, err
	}

	driftExclusionPatches := deployer.TransformDriftExclusionsToPatches(clusterSummary.Spec.ClusterProfileSpec.DriftExclusions)

	patches, err := initiatePatches(ctx, clusterSummary, requestedChart.ChartName, mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	patches = append(patches, driftExclusionPatches...)

	upgradeClient := getHelmUpgradeClient(requestedChart, actionConfig, registryOptions, patches)

	cp, err := upgradeClient.LocateChart(chartName, settings)
	if err != nil {
		handleLocateChartError(settings, requestedChart, registryOptions, err, logger)
		return nil, err
	}

	chartRequested, err := loader.Load(cp)
	if err != nil {
		return nil, err
	}

	requestedChart.ChartVersion = getChartVersion(requestedChart, chartRequested)

	if req := chartRequested.Metadata.Dependencies; req != nil {
		err = action.CheckDependencies(chartRequested, req)
		if err != nil {
			return nil, err
		}
	}

	err = upgradeCRDs(ctx, requestedChart, kubeconfig, chartRequested.CRDObjects(), logger)
	if err != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to upgrade crds: %v", err))
		return nil, err
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, getTimeoutValue(requestedChart.Options).Duration)
	defer cancel()

	helmRelease, err := upgradeClient.RunWithContext(ctxWithTimeout, requestedChart.ReleaseName, chartRequested, values)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to upgrade: %v", err))
		currentRelease, getErr := getCurrentRelease(requestedChart.ReleaseName, requestedChart.ReleaseNamespace,
			kubeconfig, registryOptions, getEnableClientCacheValue(requestedChart.Options))
		if getErr == nil && currentRelease.Info.Status.IsPending() {
			// This error: "another operation (install/upgrade/rollback) is in progress"
			// With Sveltos this error should never happen. A previous check ensures that only one
			// ClusterProfile/Profile can manage a Helm Chart with a given name in a specific namespace within
			// a managed cluster.
			// Ignore the recoverRelease result. Always return an error as we must install this release back
			_ = recoverRelease(ctx, clusterSummary, requestedChart, kubeconfig, registryOptions, logger)
			return nil, fmt.Errorf("tried recovering by uninstalling first")
		}
		return nil, err
	}

	logger.V(logs.LogDebug).Info("upgrading release done")

	return helmRelease, nil
}

func upgradeCRDsInFile(ctx context.Context, dr dynamic.ResourceInterface, chartFile *chart.File,
	logger logr.Logger) (int, error) {

	crds, err := deployer.GetUnstructured(chartFile.Data, logger)
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

	dr, err := k8s_utils.GetDynamicResourceInterface(destConfig,
		apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"), "")
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
func shouldUpgrade(ctx context.Context, currentRelease *releaseInfo, instantiatedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) bool {

	if instantiatedChart.HelmChartAction == configv1beta1.HelmChartActionUninstall {
		return false
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeContinuousWithDriftDetection {
		if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
			// In DryRun mode, if values are different, report will be generated
			oldValueHash := getValueHashFromHelmChartSummary(instantiatedChart, clusterSummary)

			// If Values configuration has changed, trigger an upgrade
			c := getManagementClusterClient()
			currentValueHash, err := getHelmChartValuesHash(ctx, c, instantiatedChart, clusterSummary, mgmtResources, logger)
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

			expected, err := semver.NewVersion(instantiatedChart.ChartVersion)
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

// doInstallRelease installs helm release in the Cluster.
// No action in DryRun mode.
func doInstallRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, requestedChart *configv1beta1.HelmChart,
	kubeconfig string, registryOptions *registryClientOptions, templateOnly bool, logger logr.Logger,
) (*release.Release, error) {

	// No-op in DryRun mode
	if !templateOnly && clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil, nil
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
			return nil, err
		}
	}

	values, err := getHelmChartInstantiatedValues(ctx, clusterSummary, mgmtResources, requestedChart, logger)
	if err != nil {
		return nil, err
	}

	helmRelease, err := installRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig,
		registryOptions, values, mgmtResources, templateOnly, logger)
	if err != nil {
		return nil, err
	}

	return helmRelease, nil
}

// doUninstallRelease uninstalls helm release from the Cluster.
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

// doUpgradeRelease upgrades helm release in the Cluster.
// No action in DryRun mode.
func doUpgradeRelease(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, requestedChart *configv1beta1.HelmChart,
	kubeconfig string, registryOptions *registryClientOptions, logger logr.Logger) (*release.Release, error) {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil, nil
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
			return nil, err
		}
	}

	values, err := getHelmChartInstantiatedValues(ctx, clusterSummary, mgmtResources, requestedChart, logger)
	if err != nil {
		return nil, err
	}

	return upgradeRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig, registryOptions,
		values, mgmtResources, logger)
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
	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}

	isDryRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
	clean := !clusterSummary.DeletionTimestamp.IsZero()
	return clusterops.UpdateClusterConfiguration(ctx, c, isDryRun, clean, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, profileRef, libsveltosv1beta1.FeatureHelm,
		nil, chartDeployed)
}

// undeployStaleReleases uninstalls all helm charts previously managed and not referenced anyomre
func undeployStaleReleases(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kubeconfig string, mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) ([]configv1beta1.ReleaseReport, error) {

	staleReleases, err := getStaleReleases(ctx, c, clusterSummary, mgmtResources, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get list of stale helm releases")
		return nil, err
	}

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	reports := make([]configv1beta1.ReleaseReport, 0)

	for i := range staleReleases {
		_, err := getReleaseInfo(staleReleases[i].Name,
			staleReleases[i].Namespace, kubeconfig, &registryClientOptions{}, false)
		if err != nil {
			if errors.Is(err, driver.ErrReleaseNotFound) {
				continue
			}
			return nil, err
		}

		if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
			// If another ClusterSummary is queued to manage this chart in this cluster, do not uninstall.
			// Let the other ClusterSummary take it over.

			currentChart := &configv1beta1.HelmChart{
				ReleaseNamespace: staleReleases[i].Namespace,
				ReleaseName:      staleReleases[i].Name,
			}
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

				continue
			}
		}

		if err := uninstallRelease(ctx, clusterSummary, staleReleases[i].Name,
			staleReleases[i].Namespace, kubeconfig, &registryClientOptions{}, nil, logger); err != nil {
			return nil, err
		}

		reports = append(reports, configv1beta1.ReleaseReport{
			ReleaseNamespace: staleReleases[i].Namespace, ReleaseName: staleReleases[i].Name,
			Action: string(configv1beta1.UninstallHelmAction),
		})
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
	clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) (*configv1beta1.ClusterSummary, error) {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return clusterSummary, nil
	}

	helmInfo := func(releaseNamespace, releaseName string) string {
		return fmt.Sprintf("%s/%s", releaseNamespace, releaseName)
	}

	currentlyReferenced := make(map[string]bool)

	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		instantiatedChart, err := getInstantiatedChart(ctx, clusterSummary, &currentChart, mgmtResources, logger)
		if err != nil {
			return clusterSummary, err
		}
		currentlyReferenced[helmInfo(instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName)] = true
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

	currentClusterSummary := &configv1beta1.ClusterSummary{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
	if err != nil {
		return clusterSummary, err
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

	clusterReportName := clusterops.GetClusterReportName(profileOwnerRef.Kind, profileOwnerRef.Name,
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

func getHelmChartInstantiatedValues(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, requestedChart *configv1beta1.HelmChart,
	logger logr.Logger) (chartutil.Values, error) {

	// Get management cluster resources once
	mgmtConfig := getManagementClusterConfig()
	mgmtClient := getManagementClusterClient()

	objects, err := fetchClusterObjects(ctx, mgmtConfig, mgmtClient, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, err
	}

	instantiatedValues, err := instantiateTemplateValues(ctx, mgmtConfig, mgmtClient,
		clusterSummary, requestedChart.ChartName, requestedChart.Values, objects, mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	// Create result map
	var result map[string]any
	if instantiatedValues != "" {
		err = yaml.Unmarshal([]byte(instantiatedValues), &result)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal main chart values (check if it's a valid YAML map): %w", err)
		}
	} else {
		result = make(map[string]any)
	}

	valuesFromToInstantiate, valuesFrom, err := getHelmChartValuesFrom(ctx, mgmtClient, clusterSummary,
		requestedChart, logger)
	if err != nil {
		return nil, err
	}

	for k := range valuesFrom {
		// Parse to map
		var instantiatedValuesMap map[string]any
		err = yaml.Unmarshal([]byte(valuesFrom[k]), &instantiatedValuesMap)
		if err != nil {
			return nil, err
		}

		err = mergo.Merge(&result, instantiatedValuesMap, mergo.WithOverride)
		if err != nil {
			return nil, err
		}
	}

	for k := range valuesFromToInstantiate {
		instantiatedValuesFrom, err := instantiateTemplateValues(ctx, mgmtConfig, mgmtClient,
			clusterSummary, requestedChart.ChartName, valuesFromToInstantiate[k], objects, mgmtResources, logger)
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

	logger.V(logs.LogDebug).Info(fmt.Sprintf("Deploying helm charts with values %#v", result))

	return chartutil.Values(result), nil
}

// getHelmChartValuesFrom return key-value pair from referenced ConfigMap/Secret.
// order is
func getHelmChartValuesFrom(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	helmChart *configv1beta1.HelmChart, logger logr.Logger) (valuesToInstantiate, valuesToUse []string, err error) {

	return getValuesFrom(ctx, c, clusterSummary, helmChart.ValuesFrom, logger)
}

// collectResourcesFromManagedHelmChartsForDriftDetection collects resources considering all
// helm charts contained in a ClusterSummary that are currently managed by the
// ClusterProfile instance.
// Resources with "projectsveltos.io/driftDetectionIgnore" annotation won't be included
func collectResourcesFromManagedHelmChartsForDriftDetection(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary, kubeconfig string, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) ([]libsveltosv1beta1.HelmResources, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	helmResources := make([]libsveltosv1beta1.HelmResources, 0, len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts))

	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]

		instantiatedChart, err := getInstantiatedChart(ctx, clusterSummary, currentChart, mgmtResources, logger)
		if err != nil {
			return nil, err
		}

		l := logger.WithValues("chart", instantiatedChart.ChartName, "releaseNamespace", instantiatedChart.ReleaseNamespace)
		l.V(logs.LogDebug).Info("collecting resources for helm chart")
		// Conflicts are already resolved by the time this is invoked. So it is safe to call CanManageChart
		if chartManager.CanManageChart(clusterSummary, instantiatedChart) {
			credentialsPath, caPath, err := getCredentialsAndCAFiles(ctx, c, clusterSummary, instantiatedChart)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to process credentials %v", err))
				return nil, err
			}

			registryOptions := &registryClientOptions{
				credentialsPath: credentialsPath, caPath: caPath,
				skipTLSVerify: getInsecureSkipTLSVerify(instantiatedChart),
				plainHTTP:     getPlainHTTP(instantiatedChart),
			}

			actionConfig, err := actionConfigInit(instantiatedChart.ReleaseNamespace, kubeconfig, registryOptions,
				getEnableClientCacheValue(instantiatedChart.Options))
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
			results, err := statusObject.Run(instantiatedChart.ReleaseName)
			if err != nil {
				return nil, err
			}

			resources, err := collectHelmContent(results, install, false, logger)
			if err != nil {
				return nil, err
			}

			l.V(logs.LogDebug).Info(fmt.Sprintf("found %d resources", len(resources)))

			helmInfo := libsveltosv1beta1.HelmResources{
				ChartName:        instantiatedChart.ChartName,
				ReleaseName:      instantiatedChart.ReleaseName,
				ReleaseNamespace: instantiatedChart.ReleaseNamespace,
				Resources:        unstructuredToSveltosResources(resources),
			}

			helmResources = append(helmResources, helmInfo)
		}
	}

	return helmResources, nil
}

func isHookRelevantForPreInstall(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPreInstall:
			return true
		case release.HookTest,
			release.HookPreUpgrade,
			release.HookPreDelete,
			release.HookPostDelete,
			release.HookPreRollback,
			release.HookPostRollback,
			release.HookPostInstall,
			release.HookPostUpgrade:
			return false
		}
	}
	return false
}

func isHookRelevantForPreUpgrade(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPreUpgrade:
			return true
		case release.HookTest,
			release.HookPreInstall,
			release.HookPreDelete,
			release.HookPostDelete,
			release.HookPreRollback,
			release.HookPostRollback,
			release.HookPostInstall,
			release.HookPostUpgrade:
			return false
		}
	}
	return false
}

func isHookRelevantForPostInstall(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPostInstall:
			return true
		case release.HookTest,
			release.HookPostUpgrade,
			release.HookPreDelete,
			release.HookPostDelete,
			release.HookPreRollback,
			release.HookPostRollback,
			release.HookPreInstall,
			release.HookPreUpgrade:
			return false
		}
	}
	return false
}

func isHookRelevantForPostUpgrade(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPostUpgrade:
			return true
		case release.HookTest,
			release.HookPostInstall,
			release.HookPreDelete,
			release.HookPostDelete,
			release.HookPreRollback,
			release.HookPostRollback,
			release.HookPreInstall,
			release.HookPreUpgrade:
			return false
		}
	}
	return false
}

func isHookRelevantForPreRollback(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPreRollback:
			return true
		case release.HookTest,
			release.HookPostInstall,
			release.HookPostUpgrade,
			release.HookPostDelete,
			release.HookPreDelete,
			release.HookPostRollback,
			release.HookPreInstall,
			release.HookPreUpgrade:
			return false
		}
	}
	return false
}

func isHookRelevantForPostRollback(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPostRollback:
			return true
		case release.HookTest,
			release.HookPreDelete,
			release.HookPostInstall,
			release.HookPostUpgrade,
			release.HookPreRollback,
			release.HookPostDelete,
			release.HookPreInstall,
			release.HookPreUpgrade:
			return false
		}
	}
	return false
}

func isHookRelevantForPreDelete(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPreDelete:
			return true
		case release.HookTest,
			release.HookPostInstall,
			release.HookPostUpgrade,
			release.HookPostDelete,
			release.HookPreRollback,
			release.HookPostRollback,
			release.HookPreInstall,
			release.HookPreUpgrade:
			return false
		}
	}
	return false
}

func isHookRelevantForPostDelete(hook *release.Hook) bool {
	for _, event := range hook.Events {
		switch event {
		case release.HookPostDelete:
			return true
		case release.HookTest,
			release.HookPreDelete,
			release.HookPostInstall,
			release.HookPostUpgrade,
			release.HookPreRollback,
			release.HookPostRollback,
			release.HookPreInstall,
			release.HookPreUpgrade:
			return false
		}
	}
	return false
}

// hasHookAnnotation checks if the resource has the helm.sh/hook annotation.
func hasHookAnnotation(obj *unstructured.Unstructured) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}

	_, exists := annotations["helm.sh/hook "]
	return exists
}

// hasHookDeleteAnnotation checks if the resource has the helm.sh/hook-delete-policy annotation.
func hasHookDeleteAnnotation(obj *unstructured.Unstructured) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}

	_, exists := annotations["helm.sh/hook-delete-policy"]
	return exists
}

func collectHelmContent(result *release.Release, helmActionVar helmAction, includeHookResources bool,
	logger logr.Logger) ([]*unstructured.Unstructured, error) {

	resources := make([]*unstructured.Unstructured, 0)
	if helmActionVar == uninstall {
		// Parse regular manifest
		mainResources, err := parseAndAppendResources(result.Manifest, logger)
		if err != nil {
			return nil, err
		}
		for i := range mainResources {
			if !hasHookAnnotation(mainResources[i]) && hasHookDeleteAnnotation(mainResources[i]) {
				logger.V(logs.LogDebug).Info(fmt.Sprintf("resource %s %s/%s has helm.sh/hook-delete-policy",
					mainResources[i].GetKind(), mainResources[i].GetNamespace(), mainResources[i].GetName()))
				resources = append(resources, mainResources[i])
			}
		}

		// if we are uninstalling, just send pre/post delete hook.
		// when pre/post delete hook resources are present, sveltos-applier
		// will: 1. apply pre delete resources, 2. wait for those to complete,
		// 3. apply post delete resources, 4. wait for those to compete,
		// 5. remove eveything else (as those non delete hook resources are not sent)
		deleteHookResources, err := collectHelmDeleteHooks(result, logger)
		if err != nil {
			return nil, err
		}

		resources = append(resources, deleteHookResources...)
		return resources, nil
	}

	if includeHookResources {
		preHookResources, err := collectPreHooks(result, helmActionVar, logger)
		if err != nil {
			return nil, err
		}
		resources = append(resources, preHookResources...)
	}

	// Parse regular manifest
	mainResources, err := parseAndAppendResources(result.Manifest, logger)
	if err != nil {
		return nil, err
	}
	for i := range mainResources {
		if !hasHookAnnotation(mainResources[i]) && hasHookDeleteAnnotation(mainResources[i]) {
			logger.V(logs.LogDebug).Info(fmt.Sprintf("resource %s %s/%s has helm.sh/hook-delete-policy",
				mainResources[i].GetKind(), mainResources[i].GetNamespace(), mainResources[i].GetName()))
			continue
		}
		resources = append(resources, mainResources[i])
	}

	if includeHookResources {
		postHookResources, err := collectPostHooks(result, helmActionVar, logger)
		if err != nil {
			return nil, err
		}
		resources = append(resources, postHookResources...)
	}

	return resources, nil
}

func collectPreHooks(result *release.Release, helmActionVar helmAction, logger logr.Logger,
) ([]*unstructured.Unstructured, error) {

	resources := make([]*unstructured.Unstructured, 0)
	// Parse pre-install/pre-upgrade/pre-rollback hook manifests
	for _, hook := range result.Hooks {
		var relevant bool
		switch helmActionVar {
		case install:
			relevant = isHookRelevantForPreInstall(hook)
		case upgrade:
			relevant = isHookRelevantForPreUpgrade(hook)
		case downgrade:
			relevant = isHookRelevantForPreRollback(hook)
		case uninstall:
			// nothing to do here. uninstall is processed above separately
		default:
			continue
		}

		if !relevant {
			continue
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("Pre %s Hook Kind: %s", helmActionVar, hook.Kind))

		hookResources, err := parseAndAppendResources(hook.Manifest, logger)
		if err != nil {
			logger.Error(err, "failed to parse hook manifest")
			return nil, err
		}
		resources = append(resources, hookResources...)
	}

	return resources, nil
}

func collectPostHooks(result *release.Release, helmActionVar helmAction, logger logr.Logger,
) ([]*unstructured.Unstructured, error) {

	resources := make([]*unstructured.Unstructured, 0)
	// Parse post-install/post-upgrade/post-rollback hook manifests
	for _, hook := range result.Hooks {
		var relevant bool
		switch helmActionVar {
		case install:
			relevant = isHookRelevantForPostInstall(hook)
		case upgrade:
			relevant = isHookRelevantForPostUpgrade(hook)
		case downgrade:
			relevant = isHookRelevantForPostRollback(hook)
		case uninstall:
			// nothing to do here. uninstall is processed above separately
		default:
			continue
		}

		if !relevant {
			continue
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("Pre %s Hook Kind: %s", helmActionVar, hook.Kind))

		hookResources, err := parseAndAppendResources(hook.Manifest, logger)
		if err != nil {
			logger.Error(err, "failed to parse hook manifest")
			return nil, err
		}
		resources = append(resources, hookResources...)
	}

	return resources, nil
}

func collectHelmDeleteHooks(result *release.Release, logger logr.Logger) ([]*unstructured.Unstructured, error) {
	resources := make([]*unstructured.Unstructured, 0)

	// Parse pre delete hook manifests
	for _, hook := range result.Hooks {
		// Skip hooks not relevant for install/upgrade
		if !isHookRelevantForPreDelete(hook) {
			continue
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("Pre delete Hook Kind: %s", hook.Kind))

		hookResources, err := parseAndAppendResources(hook.Manifest, logger)
		if err != nil {
			logger.Error(err, "failed to parse hook manifest")
			return nil, err
		}

		resources = append(resources, hookResources...)
	}

	// Parse post delete hook manifests
	for _, hook := range result.Hooks {
		// Skip hooks not relevant for install/upgrade
		if !isHookRelevantForPostDelete(hook) {
			continue
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("Post delete Hook Kind: %s ", hook.Kind))

		hookResources, err := parseAndAppendResources(hook.Manifest, logger)
		if err != nil {
			logger.Error(err, "failed to parse hook manifest")
			return nil, err
		}

		resources = append(resources, hookResources...)
	}

	return resources, nil
}

func parseAndAppendResources(yamlStr string, logger logr.Logger) ([]*unstructured.Unstructured, error) {
	objs, err := deployer.CustomSplit(yamlStr)
	if err != nil {
		return nil, err
	}

	var resources []*unstructured.Unstructured

	for _, obj := range objs {
		policy, err := k8s_utils.GetUnstructured([]byte(obj))
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", obj))
			return nil, err
		}

		if policy.GetKind() == "List" && policy.GetAPIVersion() == "v1" {
			list := &corev1.List{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(policy.Object, list); err != nil {
				return nil, err
			}

			for _, item := range list.Items {
				unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&item)
				if err != nil {
					logger.Error(err, fmt.Sprintf("failed to convert item from corev1.List %.100s", item))
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

func unstructuredToSveltosResources(policies []*unstructured.Unstructured) []libsveltosv1beta1.ResourceSummaryResource {
	resources := make([]libsveltosv1beta1.ResourceSummaryResource, len(policies))

	for i := range policies {
		r := libsveltosv1beta1.ResourceSummaryResource{
			Namespace:                   policies[i].GetNamespace(),
			Name:                        policies[i].GetName(),
			Kind:                        policies[i].GetKind(),
			Group:                       policies[i].GetObjectKind().GroupVersionKind().Group,
			Version:                     policies[i].GetObjectKind().GroupVersionKind().Version,
			IgnoreForConfigurationDrift: deployer.HasIgnoreConfigurationDriftAnnotation(policies[i]),
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

func getTakeOwnershipHelmValue(options *configv1beta1.HelmOptions, isUpgrade bool) bool {
	if options == nil {
		return false
	}

	if isUpgrade {
		return options.UpgradeOptions.TakeOwnership
	}

	return options.InstallOptions.TakeOwnership
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

func getPassCredentialsToAllValue(options *configv1beta1.HelmOptions) bool {
	if options != nil {
		return options.PassCredentialsAll
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
		maxHistory := options.UpgradeOptions.MaxHistory
		if maxHistory <= 0 {
			maxHistory = 1
		}
		return maxHistory
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

func getHelmInstallClient(ctx context.Context, requestedChart *configv1beta1.HelmChart, kubeconfig string,
	registryOptions *registryClientOptions, patches []libsveltosv1beta1.Patch, templateOnly bool, logger logr.Logger,
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
	installClient.PassCredentialsAll = getPassCredentialsToAllValue(requestedChart.Options)
	installClient.TakeOwnership = getTakeOwnershipHelmValue(requestedChart.Options, false)
	if actionConfig.RegistryClient != nil {
		installClient.SetRegistryClient(actionConfig.RegistryClient)
	}
	installClient.InsecureSkipTLSverify = registryOptions.skipTLSVerify
	installClient.PlainHTTP = registryOptions.plainHTTP
	installClient.CaFile = registryOptions.caPath

	if len(patches) > 0 {
		installClient.PostRenderer = &patcher.CustomPatchPostRenderer{Patches: patches}
	}

	installClient.DryRun = false
	if templateOnly {
		currentVersion, err := k8s_utils.GetKubernetesVersion(ctx, getManagementClusterConfig(), logger)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to get management cluster Kubernetes version", err)
		}
		// We simply want to get list of resources that would be deployed. This is to prepare
		// what agent for a SveltosCluster in pull mode needs to deploy
		installClient.DryRun = true
		installClient.ClientOnly = true
		installClient.IncludeCRDs = true
		installClient.KubeVersion, err = chartutil.ParseKubeVersion(currentVersion)
		if err != nil {
			return nil, err
		}
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
	upgradeClient.PassCredentialsAll = getPassCredentialsToAllValue(requestedChart.Options)
	upgradeClient.TakeOwnership = getTakeOwnershipHelmValue(requestedChart.Options, true)

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

	resources, err := collectHelmContent(results, install, true, logger)
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

		isDriftDetection := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection
		isDryRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		_, err = deployer.UpdateResource(ctx, dr, isDriftDetection, isDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			r, []string{}, logger)
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

func getHelmChartValuesHash(ctx context.Context, c client.Client, instantiatedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) ([]byte, error) {

	valuesHash, err := getHelmChartInstantiatedValues(ctx, clusterSummary, mgmtResources, instantiatedChart, logger)
	if err != nil {
		return nil, err
	}

	valuesAsString, err := valuesHash.YAML()
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	h.Write([]byte(valuesAsString))
	return h.Sum(nil), nil
}

func updateValueHashOnHelmChartSummary(ctx context.Context, requestedChart *configv1beta1.HelmChart,
	clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) ([]byte, error) {

	c := getManagementClusterClient()

	helmChartValuesHash, err := getHelmChartValuesHash(ctx, c, requestedChart, clusterSummary, mgmtResources, logger)
	if err != nil {
		return nil, err
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

	return helmChartValuesHash, err
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

// chartNameContainsRepositoryName checks if chartName starts with repositoryName
// followed by a '/'. This indicates that the repository name is already
// prefixed in the chartName.
func chartNameContainsRepositoryName(chartName, repositoryName string) bool {
	// To avoid issues with repositoryName potentially being a substring of another part
	// of the chartName (e.g., "myrepo/foo-repo/chart" and "repo"),
	// we specifically check for "repositoryName/" prefix.
	prefix := repositoryName + "/"
	return strings.HasPrefix(chartName, prefix)
}

// removeRepositoryNameFromChartName removes the repositoryName prefix
// from the chartName if it exists.
func removeRepositoryNameFromChartName(chartName, repositoryName string) string {
	prefix := repositoryName + "/"
	if strings.HasPrefix(chartName, prefix) {
		return strings.TrimPrefix(chartName, prefix)
	}
	return chartName // Return original chartName if prefix not found
}

// ensureRepositoryNamePrefix appends "repositoryName/" to chartName
// if it's not already present.
func ensureRepositoryNamePrefix(chartName, repositoryName string) string {
	// If repositoryName is empty, there's no prefix to add, so return chartName as is.
	if repositoryName == "" {
		return chartName
	}

	// If the chartName already contains the repositoryName prefix, return it as is.
	if chartNameContainsRepositoryName(chartName, repositoryName) {
		return chartName
	}

	// Otherwise, prepend the repositoryName and a slash.
	return repositoryName + "/" + chartName
}

// getHelmChartAndRepoName returns helm repo URL and chart name
func getHelmChartAndRepoName(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary, //nolint: gocritic // ignore
	requestedChart *configv1beta1.HelmChart, logger logr.Logger) (string, string, string, error) {

	tmpDir := ""
	chartName := requestedChart.ChartName
	repoURL := requestedChart.RepositoryURL
	if registry.IsOCI(repoURL) {
		u, err := url.Parse(repoURL)
		if err != nil {
			return "", "", "", err
		}

		if chartNameContainsRepositoryName(chartName, requestedChart.RepositoryName) {
			u.Path = path.Join(u.Path, removeRepositoryNameFromChartName(chartName, requestedChart.RepositoryName))
		} else {
			u.Path = path.Join(u.Path, chartName)
		}
		chartName = u.String()
		repoURL = ""
	} else if !chartNameContainsRepositoryName(chartName, requestedChart.RepositoryName) {
		chartName = ensureRepositoryNamePrefix(chartName, requestedChart.RepositoryName)
	}

	clusterNamespace := clusterSummary.Spec.ClusterNamespace
	clusterName := clusterSummary.Spec.ClusterName
	clusterType := clusterSummary.Spec.ClusterType

	if isReferencingFluxSource(requestedChart) {
		sourceRef, repoPath, err := getReferencedFluxSourceFromURL(requestedChart)
		if err != nil {
			return "", "", "", err
		}

		if sourceRef == nil {
			return "", "", "", fmt.Errorf("flux Source %v not found", sourceRef)
		}

		namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, sourceRef.Namespace, clusterType)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to instantiate namespace: %w", err)
		}

		name, err := libsveltostemplate.GetReferenceResourceName(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, sourceRef.Name, clusterType)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to instantiate name: %w", err)
		}

		source, err := getSource(ctx, getManagementClusterClient(), namespace, name, sourceRef.Kind)
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
func getUsernameAndPasswordFromSecret(hostname string, secret *corev1.Secret) (username, password string, err error) {
	if secret.Type == corev1.SecretTypeDockerConfigJson {
		dockerCfg, err := dockerconfig.LoadFromReader(bytes.NewReader(secret.Data[corev1.DockerConfigJsonKey]))
		if err != nil {
			return "", "", fmt.Errorf("unable to load Docker config from Secret '%s': %w", secret.Name, err)
		}
		authConfig, err := dockerCfg.GetAuthConfig(hostname)
		if err != nil {
			return "", "", fmt.Errorf("unable to get authentication data from Secret '%s': %w", secret.Name, err)
		}

		// Make sure that the obtained auth config is for the requested host.
		// When the docker config does not contain the credentials for a host,
		// the credential store returns an empty auth config.
		// Refer: https://github.com/docker/cli/blob/v20.10.16/cli/config/credentials/file_store.go#L44
		if credentials.ConvertToHostname(authConfig.ServerAddress) != hostname {
			return "", "", fmt.Errorf("no auth config for '%s' in the docker-registry Secret '%s'", hostname, secret.Name)
		}
		username = authConfig.Username
		password = authConfig.Password
	} else {
		username, password = string(secret.Data["username"]), string(secret.Data["password"])
	}
	switch {
	case username == "" && password == "":
		return "", "", nil
	case username == "" || password == "":
		return "", "", fmt.Errorf("invalid '%s' secret data: required fields 'username' and 'password'", secret.Name)
	}
	return username, password, nil
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

		err = requeueClusterSummary(ctx, libsveltosv1beta1.FeatureHelm, cs, logger)
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
	values, err = getHelmChartInstantiatedValues(ctx, clusterSummary, mgmtResources, requestedChart, logger)
	if err != nil {
		return "", err
	}

	r, err := installRelease(ctx, clusterSummary, settings, requestedChart, kubeconfig, registryOptions,
		values, mgmtResources, false, logger)
	if err != nil {
		return "", err
	}

	proposedValues := r.Config

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

	// Create a deep copy of the chart to avoid modifying the original.
	instantiatedChart := currentChart.DeepCopy()

	objects, err := fetchClusterObjects(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to fetch resources")
		return nil, err
	}

	// Call the new recursive helper function to instantiate all fields.
	if err := instantiateStructFields(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		instantiatedChart, clusterSummary, objects, mgmtResources, logger); err != nil {
		msg := fmt.Sprintf("failed to instantiated template: %v", err)
		logger.V(logs.LogInfo).Info(msg)
		return nil, &configv1beta1.TemplateInstantiationError{Message: msg}
	}

	return instantiatedChart, nil
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

// addExtraLabels adds ExtraLabels to policy.
// If policy already has a label with a key present in `ExtraLabels`, the value from `ExtraLabels` will
// override the existing value.
func addExtraLabels(policy *unstructured.Unstructured, extraLabels map[string]string) {
	if extraLabels == nil {
		return
	}

	if len(extraLabels) == 0 {
		return
	}

	for k := range extraLabels {
		deployer.AddLabel(policy, k, extraLabels[k])
	}
}

// addExtraAnnotations adds ExtraAnnotations to policy.
// If policy already has an annotation with a key present in `ExtraAnnotations`, the value from `ExtraAnnotations`
// will override the existing value.
func addExtraAnnotations(policy *unstructured.Unstructured, extraAnnotations map[string]string) {
	if extraAnnotations == nil {
		return
	}

	if len(extraAnnotations) == 0 {
		return
	}

	for k := range extraAnnotations {
		deployer.AddAnnotation(policy, k, extraAnnotations[k])
	}
}

// If in pull mode either remove resources or treat it as an upgrade
func prepareChartForAgent(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, instantiatedChart *configv1beta1.HelmChart,
	registryOptions *registryClientOptions, logger logr.Logger) (*releaseInfo, *configv1beta1.ReleaseReport, error) {

	logger = logger.WithValues("chart", fmt.Sprintf("%s/%s",
		instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName))

	// In pull mode always treat it as an install. This will allow us to get list of resources helm would install (equivalent
	// of helm template). Those resources will be made available for the agent inside ConfigurationBundles.
	helmRelease, _, err := handleInstall(ctx, clusterSummary, mgmtResources, instantiatedChart, "",
		registryOptions, true, true, logger)
	if err != nil {
		return nil, nil, err
	}

	rInfo := &releaseInfo{
		ReleaseName:      helmRelease.Name,
		ReleaseNamespace: helmRelease.Namespace,
		Revision:         strconv.Itoa(helmRelease.Version),
		Status:           helmRelease.Info.Status.String(),
		Chart:            helmRelease.Chart.Metadata.Name,
		ChartVersion:     helmRelease.Chart.Metadata.Version,
		AppVersion:       helmRelease.Chart.AppVersion(),
		ReleaseLabels:    helmRelease.Labels,
		Icon:             helmRelease.Chart.Metadata.Icon,
		Values:           helmRelease.Config,
	}

	helmActionVar, err := getHelmActionInPullMode(ctx, clusterSummary, instantiatedChart)
	if err != nil {
		return nil, nil, err
	}

	var releaseReport *configv1beta1.ReleaseReport
	if instantiatedChart.HelmChartAction == configv1beta1.HelmChartActionUninstall ||
		!clusterSummary.DeletionTimestamp.IsZero() {

		logger.V(logs.LogDebug).Info("uninstall chart in pull mode")
		helmActionVar = uninstall
		releaseReport = &configv1beta1.ReleaseReport{
			ReleaseNamespace: helmRelease.Namespace, ReleaseName: helmRelease.Name,
			Action: string(configv1beta1.UninstallHelmAction), ChartVersion: instantiatedChart.ChartVersion,
		}
	} else {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("action for helm chart in pull mode: %s", helmActionVar))
		releaseReport = &configv1beta1.ReleaseReport{
			ReleaseNamespace: helmRelease.Namespace, ReleaseName: helmRelease.Name,
			Action: string(configv1beta1.UpgradeHelmAction),
		}
	}

	var resources []*unstructured.Unstructured
	if helmActionVar == uninstall && clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		// if action is install=false and syncMode is dryRun, return empty resources.
	} else {
		resources, err = collectHelmContent(helmRelease, helmActionVar, true, logger)
		if err != nil {
			return nil, nil, err
		}
	}

	// Update Deployed GVK. In case ClusterSummary is deleted while applier is still deploying this content,
	// we know what needs to be deleted
	currentReports := prepareReports(resources)
	_, err = updateDeployedGroupVersionKind(ctx, clusterSummary, libsveltosv1beta1.FeatureHelm,
		nil, currentReports, logger)
	if err != nil {
		return nil, nil, err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("found %d resources", len(resources)))

	err = stageHelmResourcesForDeployment(ctx, clusterSummary, instantiatedChart, resources, helmActionVar,
		rInfo, logger)
	if err != nil {
		return nil, nil, err
	}

	// Do not call CommitStagedResourcesForDeployment now.
	// Walk all charts only then call CommitStagedResourcesForDeployment

	return rInfo, releaseReport, nil
}

// getHookWeight extracts the helm.sh/hook-weight annotation as an int. Defaults to 0 on error or missing.
func getHookWeight(obj *unstructured.Unstructured) int {
	annotations := obj.GetAnnotations()
	if weightStr, ok := annotations["helm.sh/hook-weight"]; ok {
		if weight, err := strconv.Atoi(weightStr); err == nil {
			return weight
		}
	}
	return 0
}

func appendResources(result [][]*unstructured.Unstructured,
	resources []*unstructured.Unstructured) [][]*unstructured.Unstructured {

	// Sort resources by hook-weight
	sort.Slice(resources, func(i, j int) bool {
		wi := getHookWeight(resources[i])
		wj := getHookWeight(resources[j])
		return wi < wj
	})

	const limit = 15
	for i := 0; i < len(resources); i += limit {
		end := i + limit
		if end > len(resources) {
			end = len(resources)
		}
		result = append(result, resources[i:end])
	}

	return result
}

func setNamespace(resource *unstructured.Unstructured, releaseNamespace string) *unstructured.Unstructured {
	if resource.GetNamespace() == "" {
		// Irrespective of whether resources are namespaced or not, set namespace to be the release namespace
		// sveltos-applier adjustNamespace will unset Namespace for cluster wide resources
		resource.SetNamespace(releaseNamespace)
	}
	return resource
}

// splitResources returns a slice of slice of resources according to those rules:
// 1. pre install/upgrade are stored in their own subgroups before anything else
// 2. then pre delete are stored in their own subgroups
// 3. then each CRD instance is in its own group with no other resources
// 4. then other resources (not hook ones) no more than 15 resources are put in the same group
// 5. then post install/upgrade are stored in their own subgroups
// 6. then post delete are stored in their own subgroups
// If operation is install there are no pre/post delete resources
// If operation is a delete only pre/post delete resources are present
func splitResources(resources []*unstructured.Unstructured, releaseNamespace string,
) [][]*unstructured.Unstructured {

	var (
		crdInstances          []*unstructured.Unstructured
		preInstallHooks       []*unstructured.Unstructured
		postInstallHooks      []*unstructured.Unstructured
		preRollbackHooks      []*unstructured.Unstructured
		postRollbackHooks     []*unstructured.Unstructured
		preDeleteHooks        []*unstructured.Unstructured
		postDeleteHooks       []*unstructured.Unstructured
		hookDeleteAnnotations []*unstructured.Unstructured
		otherResources        []*unstructured.Unstructured
	)

	// Separate CRD instances from other resources
	for _, resource := range resources {
		if resource.GetAPIVersion() == apiextensionsv1.SchemeGroupVersion.String() &&
			resource.GetKind() == "CustomResourceDefinition" {

			crdInstances = append(crdInstances, resource)
		} else if isHookResource(resource, "pre-install") {
			resource = setNamespace(resource, releaseNamespace)
			preInstallHooks = append(preInstallHooks, resource)
		} else if isHookResource(resource, "pre-upgrade") {
			resource = setNamespace(resource, releaseNamespace)
			preInstallHooks = append(preInstallHooks, resource)
		} else if isHookResource(resource, "post-install") {
			resource = setNamespace(resource, releaseNamespace)
			postInstallHooks = append(postInstallHooks, resource)
		} else if isHookResource(resource, "post-upgrade") {
			resource = setNamespace(resource, releaseNamespace)
			postInstallHooks = append(postInstallHooks, resource)
		} else if isHookResource(resource, "pre-rollback") {
			resource = setNamespace(resource, releaseNamespace)
			preRollbackHooks = append(preRollbackHooks, resource)
		} else if isHookResource(resource, "post-rollback") {
			resource = setNamespace(resource, releaseNamespace)
			postRollbackHooks = append(postRollbackHooks, resource)
		} else if isHookResource(resource, "pre-delete") {
			resource = setNamespace(resource, releaseNamespace)
			preDeleteHooks = append(preDeleteHooks, resource)
		} else if isHookResource(resource, "post-delete") {
			resource = setNamespace(resource, releaseNamespace)
			postDeleteHooks = append(postDeleteHooks, resource)
		} else if hasHookDeleteAnnotation(resource) {
			resource = setNamespace(resource, releaseNamespace)
			hookDeleteAnnotations = append(hookDeleteAnnotations, resource)
		} else {
			resource = setNamespace(resource, releaseNamespace)
			otherResources = append(otherResources, resource)
		}
	}

	var result [][]*unstructured.Unstructured

	// Put pre install instances in subgroups of no more than 15
	result = appendResources(result, preInstallHooks)

	// Put pre rollback instances in subgroups of no more than 15
	result = appendResources(result, preRollbackHooks)

	// Put hookDeleteAnnotations instances in subgroups of no more than 15
	result = appendResources(result, hookDeleteAnnotations)

	// Put pre delete instances in subgroups of no more than 15
	result = appendResources(result, preDeleteHooks)

	// Put each CRD instance in its own subgroup
	for _, crd := range crdInstances {
		result = append(result, []*unstructured.Unstructured{crd})
	}

	// Split other resources into subgroups of no more than 15
	result = appendResources(result, otherResources)

	// Put post install instances in subgroups of no more than 15
	result = appendResources(result, postInstallHooks)

	// Put post rollback instances in subgroups of no more than 15
	result = appendResources(result, postRollbackHooks)

	// Put post delete instances in subgroups of no more than 15
	result = appendResources(result, postDeleteHooks)

	return result
}

func stageHelmResourcesForDeployment(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	currentChart *configv1beta1.HelmChart, resources []*unstructured.Unstructured,
	helmActionVar helmAction, rInfo *releaseInfo, logger logr.Logger) error {

	baseKey := fmt.Sprintf("%s-%s-%s", currentChart.ReleaseNamespace, currentChart.ReleaseName,
		currentChart.RepositoryName)

	bundles := splitResources(resources, currentChart.ReleaseNamespace)

	for i := range bundles {
		key := fmt.Sprintf("%s-%d", baseKey, i)
		bundleResources := make(map[string][]unstructured.Unstructured)
		bundleResources[key] = convertPointerSliceToValueSlice(bundles[i])

		setters := prepareBundleSettersWithHelmInfo(currentChart, helmActionVar == uninstall, i == len(bundles)-1, rInfo)

		err := pullmode.StageResourcesForDeployment(ctx, getManagementClusterClient(),
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			configv1beta1.ClusterSummaryKind, clusterSummary.Name, string(libsveltosv1beta1.FeatureHelm),
			bundleResources, false, logger, setters...)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to stage resources for deployment: %v", err))
			return err
		}
	}

	return nil
}

func prepareBundleSettersWithHelmInfo(currentChart *configv1beta1.HelmChart, isUninstall, isLast bool,
	rInfo *releaseInfo) []pullmode.BundleOption {

	setters := make([]pullmode.BundleOption, 0)

	timeout := getTimeoutValue(currentChart.Options)
	setters = append(setters,
		pullmode.WithTimeout(&timeout),
		pullmode.WithReleaseInfo(currentChart.ReleaseNamespace, currentChart.ReleaseName,
			currentChart.RepositoryURL, rInfo.ChartVersion, rInfo.Icon, isUninstall, isLast))

	return setters
}

func commitStagedResourcesForDeployment(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	configurationHash []byte, mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) error {

	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}

	staleReleases, err := getStaleReleases(ctx, getManagementClusterClient(), clusterSummary, mgmtResources, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get list of stale helm releases")
		return err
	}

	// if a stale helm release is being deleted, run the pre/post delete checks
	setters := prepareSetters(clusterSummary, libsveltosv1beta1.FeatureHelm, profileRef, configurationHash, len(staleReleases) != 0)
	// Commit deployment
	return pullmode.CommitStagedResourcesForDeployment(ctx, getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind,
		clusterSummary.Name, string(libsveltosv1beta1.FeatureHelm), logger, setters...)
}

func isHookResource(u *unstructured.Unstructured, hookType string) bool {
	annotations := u.GetAnnotations()
	if annotations == nil {
		return false
	}

	hook, found := annotations["helm.sh/hook"]
	if !found {
		return false
	}

	// Helm allows multiple hooks separated by commas, e.g. "pre-delete,post-install"
	hooks := strings.Split(hook, ",")
	for _, h := range hooks {
		h = strings.TrimSpace(h)
		if h == hookType {
			return true
		}
	}

	return false
}

type helmAction string

const (
	install   helmAction = "install"
	upgrade   helmAction = "upgrade"
	downgrade helmAction = "downgrade"
	uninstall helmAction = "uninstall"
)

// getHelmActionInPullMode determines what Helm action is needed based on current and new version
func getHelmActionInPullMode(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	instantiatedChart *configv1beta1.HelmChart) (helmAction, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, getManagementClusterClient())
	if err != nil {
		return "", err
	}
	currentVersion := chartManager.GetVersionForChart(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, instantiatedChart)

	if currentVersion == "" {
		return install, nil
	}

	currVer, err := semver.NewVersion(currentVersion)
	if err != nil {
		return "", err
	}

	newVer, err := semver.NewVersion(instantiatedChart.ChartVersion)
	if err != nil {
		return "", err
	}

	switch {
	case newVer.GreaterThan(currVer):
		return upgrade, nil
	case newVer.LessThan(currVer):
		return downgrade, nil
	default:
		// In case same version, treat it as an upgrade
		return upgrade, nil
	}
}

func handleLocateChartError(settings *cli.EnvSettings, requestedChart *configv1beta1.HelmChart,
	registryOptions *registryClientOptions, err error, logger logr.Logger) {

	logger.V(logs.LogDebug).Info(fmt.Sprintf("LocateChart failed %v", err))

	if strings.Contains(err.Error(), "no chart version found for") {
		logger.V(logs.LogInfo).Info("Chart version not found, cleaning repository cache",
			"repo", requestedChart.RepositoryName)
		removeCachedData(settings, requestedChart.RepositoryName, requestedChart.RepositoryURL,
			registryOptions, logger)
	}
}

func removeCachedData(settings *cli.EnvSettings, name, repoURL string, registryOptions *registryClientOptions,
	logger logr.Logger) {

	logger = logger.WithValues("repoURL", repoURL, "repoName", name)

	entry := &repo.Entry{Name: name, URL: repoURL, Username: registryOptions.username, Password: registryOptions.password,
		InsecureSkipTLSverify: registryOptions.skipTLSVerify}
	chartRepo, err := repo.NewChartRepository(entry, getter.All(settings))
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get new chartRepository: %v", err))
		return
	}

	chartRepo.CachePath = settings.RepositoryCache

	if !storage.Has(entry.Name) {
		return
	}

	// Remove from in-memory storage first
	storage.Remove(entry.Name)

	logger.V(logs.LogDebug).Info("repository name already exists")

	if !registry.IsOCI(entry.URL) {
		// Create the cache path for this repository
		repoCache := filepath.Join(settings.RepositoryCache, helmpath.CacheIndexFile(entry.Name))

		// Remove the cache file
		if err := os.Remove(repoCache); err != nil {
			logger.V(logs.LogDebug).Error(err, "Failed to remove repository cache")
		}
	}

	_ = repoAddOrUpdate(settings, name, repoURL, registryOptions, logger)
}

// getStaleReleases returns releases which used to be managed by the ClusterSummary but are not referenced anymore
func getStaleReleases(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) ([]chartmanager.HelmReleaseInfo, error) {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, c)
	if err != nil {
		return nil, err
	}

	managedHelmReleases := chartManager.GetManagedHelmReleases(clusterSummary)

	// Build map of current referenced helm charts
	currentlyReferencedReleases := make(map[string]bool)
	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		currentChart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		instantiatedChart, err := getInstantiatedChart(ctx, clusterSummary, currentChart, mgmtResources, logger)
		if err != nil {
			return nil, err
		}
		currentlyReferencedReleases[chartManager.GetReleaseKey(instantiatedChart.ReleaseNamespace, instantiatedChart.ReleaseName)] = true
	}

	staleReleases := make([]chartmanager.HelmReleaseInfo, 0)

	for i := range managedHelmReleases {
		releaseKey := chartManager.GetReleaseKey(managedHelmReleases[i].Namespace, managedHelmReleases[i].Name)
		if _, ok := currentlyReferencedReleases[releaseKey]; !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("helm release %s (namespace %s) used to be managed but not referenced anymore",
				managedHelmReleases[i].Name, managedHelmReleases[i].Namespace))
			staleReleases = append(staleReleases, managedHelmReleases[i])
		}
	}

	return staleReleases, nil
}
