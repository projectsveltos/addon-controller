/*
Copyright 2023-24. projectsveltos.io. All rights reserved.

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
	archivetar "archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	kustomizetypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/funcmap"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/pullmode"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

const (
	permission0600 = 0600
	permission0755 = 0755
	maxSize        = int64(20 * 1024 * 1024)
)

var (
	kustomizeRenderMutex sync.Mutex
)

// buildKustomization wraps krusty.MakeKustomizer with the following settings:
// - load files from outside the kustomization.yaml root
// - disable plugins except for the builtin ones
func buildKustomization(fs filesys.FileSystem, dirPath string) (resmap.ResMap, error) {
	// Temporary workaround for concurrent map read and map write bug
	// https://github.com/kubernetes-sigs/kustomize/issues/3659
	kustomizeRenderMutex.Lock()
	defer kustomizeRenderMutex.Unlock()

	buildOptions := &krusty.Options{
		LoadRestrictions: kustomizetypes.LoadRestrictionsNone,
		PluginConfig:     kustomizetypes.DisabledPluginConfig(),
	}

	k := krusty.MakeKustomizer(buildOptions)
	return k.Run(fs, dirPath)
}

func deployKustomizeRefs(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, _, err := getClusterSummaryAndClusterClient(ctx, clusterNamespace, applicant, c, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, logger, err := getRestConfig(ctx, c, clusterSummary, logger)
	if err != nil {
		return err
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("deploying kustomize resources")

	if isPullMode {
		// If SveltosCluster is in pull mode, discard all previous staged resources. Those will be regenerated now.
		err = pullmode.DiscardStagedResourcesForDeployment(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, configv1beta1.ClusterSummaryKind, applicant, string(libsveltosv1beta1.FeatureKustomize), logger)
		if err != nil {
			return err
		}
	}

	err = handleDriftDetectionManagerDeploymentForKustomize(ctx, clusterSummary, clusterNamespace,
		clusterName, clusterType, isPullMode, startDriftDetectionInMgmtCluster(o), logger)
	if err != nil {
		return err
	}

	localResourceReports, remoteResourceReports, deployError := deployEachKustomizeRefs(ctx, c, remoteRestConfig,
		clusterSummary, logger)

	configurationHash, _ := o.HandlerOptions[configurationHash].([]byte)
	return processKustomizeDeployment(ctx, remoteRestConfig, clusterSummary, localResourceReports,
		remoteResourceReports, deployError, isPullMode, configurationHash, logger)
}

func processKustomizeDeployment(ctx context.Context, remoteRestConfig *rest.Config,
	clusterSummary *configv1beta1.ClusterSummary, localResourceReports, remoteResourceReports []libsveltosv1beta1.ResourceReport,
	deployError error, isPullMode bool, configurationHash []byte, logger logr.Logger) error {

	featureHandler := getHandlersForFeature(libsveltosv1beta1.FeatureKustomize)
	c := getManagementClusterClient()

	// Irrespective of error, update deployed gvks. Otherwise cleanup won't happen in case
	var gvkErr error
	clusterSummary, gvkErr = updateDeployedGroupVersionKind(ctx, clusterSummary, libsveltosv1beta1.FeatureKustomize,
		localResourceReports, remoteResourceReports, logger)
	if gvkErr != nil {
		return gvkErr
	}

	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}

	remoteDeployed := make([]libsveltosv1beta1.Resource, len(remoteResourceReports))
	resourceDeployed := make([]configv1beta1.DeployedResource, len(remoteResourceReports)+len(localResourceReports))

	if !isPullMode {
		remoteResources := clusterops.ConvertResourceReportsToObjectReference(remoteResourceReports)
		err = updateReloaderWithDeployedResources(ctx, clusterSummary, profileRef, libsveltosv1beta1.FeatureKustomize,
			remoteResources, !clusterSummary.Spec.ClusterProfileSpec.Reloader, logger)
		if err != nil {
			return err
		}

		// If we are here there are no conflicts (and error would have been returned by deployKustomizeRef)
		for i := range remoteResourceReports {
			remoteDeployed[i] = remoteResourceReports[i].Resource
			resourceDeployed[i] = *resourceToDeployedResource(&remoteResourceReports[i].Resource,
				configv1beta1.DeploymentTypeRemote)
		}
		for i := range localResourceReports {
			resourceDeployed[len(remoteResourceReports)+i] = *resourceToDeployedResource(&localResourceReports[i].Resource,
				configv1beta1.DeploymentTypeLocal)
		}

		// TODO: track resource deployed in the management cluster
		isDrynRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		clean := !clusterSummary.DeletionTimestamp.IsZero()
		err = clusterops.UpdateClusterConfiguration(ctx, c, isDrynRun, clean, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, profileRef, featureHandler.id, resourceDeployed, nil)
		if err != nil {
			return err
		}
	}

	// If a deployment error happened, do not try to clean stale resources. Because of the error potentially
	// all resources might be considered stale at this time.
	if deployError != nil {
		return deployError
	}

	// Agent in the managed cluster will take care of this for SveltosClusters in pull mode. We still need to
	// clean stale resources deployed in the management cluster
	var undeployed []libsveltosv1beta1.ResourceReport
	_, undeployed, err = cleanStaleKustomizeResources(ctx, clusterSummary, localResourceReports,
		remoteResourceReports, logger)
	if err != nil {
		return err
	}
	remoteResourceReports = append(remoteResourceReports, undeployed...)

	err = handleWatchers(ctx, clusterSummary, localResourceReports, featureHandler)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, remoteRestConfig == nil,
		remoteResourceReports, libsveltosv1beta1.FeatureKustomize)
	if err != nil {
		return err
	}

	if isPullMode {
		setters := prepareSetters(clusterSummary, libsveltosv1beta1.FeatureKustomize, profileRef, configurationHash)

		err = pullmode.CommitStagedResourcesForDeployment(ctx, c,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureKustomize),
			logger, setters...)
		if err != nil {
			return err
		}

		// For Sveltos in pull mode, health checks are run by agent. Also the agent sees there is an empty ResouceSummary
		// so it knows it has to track resources it deployed
		return deployError
	}

	err = handleKustomizeResourceSummaryDeployment(ctx, clusterSummary, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, remoteDeployed, logger)
	if err != nil {
		return err
	}

	return clusterops.ValidateHealthPolicies(ctx, remoteRestConfig, clusterSummary.Spec.ClusterProfileSpec.ValidateHealths,
		libsveltosv1beta1.FeatureKustomize, logger)
}

func cleanStaleKustomizeResources(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	localResourceReports, remoteResourceReports []libsveltosv1beta1.ResourceReport, logger logr.Logger,
) (localUndeployed, remoteUndeployed []libsveltosv1beta1.ResourceReport, err error) {

	// Only resources previously deployed by ClusterSummary are removed here. Even if profile is created by serviceAccount
	// use cluster-admin account to do the removal
	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, "", "", clusterSummary.Spec.ClusterType,
		logger)
	if err != nil {
		return nil, nil, err
	}

	cacheMgr := clustercache.GetManager()
	remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, "", "", clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, nil, err
	}

	// Clean stale resources in the management cluster
	localUndeployed, err = cleanKustomizeResources(ctx, true, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary, localResourceReports, logger)
	if err != nil {
		return nil, nil, err
	}

	// Clean stale resources in the remote cluster
	remoteUndeployed, err = cleanKustomizeResources(ctx, false, remoteRestConfig, remoteClient,
		clusterSummary, remoteResourceReports, logger)
	if err != nil {
		return nil, nil, err
	}

	return localUndeployed, remoteUndeployed, nil
}

func undeployKustomizeRefs(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	clusterSummary, err := configv1beta1.GetClusterSummary(ctx, c, clusterNamespace, applicant)
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

	logger.V(logs.LogDebug).Info("undeployKustomizeRefs")

	var resourceReports []libsveltosv1beta1.ResourceReport

	// Undeploy from management cluster
	_, err = undeployStaleResources(ctx, true, getManagementClusterConfig(), c, libsveltosv1beta1.FeatureKustomize,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, libsveltosv1beta1.FeatureKustomize),
		map[string]libsveltosv1beta1.Resource{}, logger)
	if err != nil {
		return err
	}

	if isPullMode {
		err = pullModeUndeployResources(ctx, c, clusterSummary, libsveltosv1beta1.FeatureKustomize, logger)
		if err != nil {
			return err
		}
	} else {
		// Only resources previously deployed by ClusterSummary are removed here. Even if profile is created by serviceAccount
		// use cluster-admin account to do the removal
		cacheMgr := clustercache.GetManager()
		remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
			"", "", clusterSummary.Spec.ClusterType, logger)
		if err != nil {
			return err
		}

		remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
			"", "", clusterSummary.Spec.ClusterType, logger)
		if err != nil {
			return err
		}

		// Undeploy from managed cluster
		resourceReports, err = undeployStaleResources(ctx, false, remoteRestConfig, remoteClient, libsveltosv1beta1.FeatureKustomize,
			clusterSummary, getDeployedGroupVersionKinds(clusterSummary, libsveltosv1beta1.FeatureKustomize),
			map[string]libsveltosv1beta1.Resource{}, logger)
		if err != nil {
			return err
		}

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
		err = clusterops.UpdateClusterConfiguration(ctx, c, isDrynRun, true, clusterNamespace, clusterName, clusterType,
			profileRef, libsveltosv1beta1.FeatureKustomize, []configv1beta1.DeployedResource{}, nil)
		if err != nil {
			return err
		}

		err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, remoteRestConfig == nil,
			resourceReports, libsveltosv1beta1.FeatureKustomize)
		if err != nil {
			return err
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return &configv1beta1.DryRunReconciliationError{}
	}

	manager := getManager()
	manager.stopStaleWatchForMgmtResource(nil, clusterSummary, libsveltosv1beta1.FeatureKustomize)

	return nil
}

// resourcesHash returns the hash of all the ClusterSummary referenced KustomizationRefs.
func kustomizationHash(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) ([]byte, error) {

	clusterProfileSpecHash, err := getClusterProfileSpecHash(ctx, clusterSummary)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	var config string
	config += string(clusterProfileSpecHash)

	sortedKustomizationRefs := getSortedKustomizationRefs(clusterSummary)

	config += render.AsCode(sortedKustomizationRefs)
	for i := range sortedKustomizationRefs {
		kustomizationRef := &sortedKustomizationRefs[i]

		result, err := getHashFromKustomizationRef(ctx, c, clusterSummary,
			kustomizationRef, logger)
		if err != nil {
			return nil, err
		}
		config += string(result)

		valueFromHash, err := getKustomizeReferenceResourceHash(ctx, c, clusterSummary,
			kustomizationRef, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(
				fmt.Sprintf("failed to get hash from referenced ConfigMap/Secret in ValuesFrom %v", err))
			return nil, err
		}

		config += valueFromHash
	}

	for i := range clusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		h := &clusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]
		if h.FeatureID == libsveltosv1beta1.FeatureKustomize {
			config += render.AsCode(h)
		}
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getHashFromKustomizationRef(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kustomizationRef *configv1beta1.KustomizationRef, logger logr.Logger) ([]byte, error) {

	var result string
	namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Namespace, clusterSummary.Spec.ClusterType)
	if err != nil {
		return nil, err
	}

	name, err := libsveltostemplate.GetReferenceResourceName(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Name, clusterSummary.Spec.ClusterType)
	if err != nil {
		return nil, err
	}

	if kustomizationRef.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
		configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ConfigMap %v", err))
			return nil, err
		}
		if configMap == nil {
			return nil, nil
		}
		result += getConfigMapHash(configMap)
	} else if kustomizationRef.Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
		secret, err := getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get Secret %v", err))
			return nil, err
		}
		if secret == nil {
			return nil, nil
		}
		result += getSecretHash(secret)
	} else {
		source, err := getSource(ctx, c, namespace, name, kustomizationRef.Kind)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get source %v", err))
			return nil, err
		}
		if source == nil {
			return nil, nil
		}
		s := source.(sourcev1.Source)
		if s.GetArtifact() != nil {
			result += s.GetArtifact().Revision
		}
		if source.GetAnnotations() != nil {
			result += getDataSectionHash(source.GetAnnotations())
		}
	}

	return []byte(result), nil
}

// instantiateKustomizeSubstituteValues gets all substitute values for a KustomizationRef and
// instantiate those using resources in the management cluster.
func instantiateKustomizeSubstituteValues(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, values map[string]string, logger logr.Logger,
) (map[string]string, error) {

	stringifiedValues, err := stringifyMap(values)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to stringify values map: %v", err))
		return nil, err
	}

	requestorName := clusterSummary.Namespace + clusterSummary.Name + "kustomize"

	objects, err := fecthClusterObjects(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, err
	}

	instantiatedValue, err :=
		instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
			clusterSummary, requestorName, stringifiedValues, objects, mgmtResources, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate values %v", err))
		return nil, err
	}

	result, err := parseMapFromString(instantiatedValue)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse string back to map %v", err))
		return nil, err
	}

	return result, nil
}

// getKustomizeSubstituteValues returns all key-value pair looking at both Values and ValuesFrom
func getKustomizeSubstituteValues(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kustomizationRef *configv1beta1.KustomizationRef, logger logr.Logger) (templatedValues, nonTemplatedValues map[string]string, err error) {

	values := make(map[string]string)
	for k := range kustomizationRef.Values {
		values[k] = kustomizationRef.Values[k]
	}

	// Get key-value pairs from ValuesFrom
	templatedValues, nonTemplatedValues, err = getKustomizeSubstituteValuesFrom(ctx, c, clusterSummary, kustomizationRef, logger)
	if err != nil {
		return nil, nil, err
	}

	// Values are always treated as templates. So copy the templated values here
	for k := range templatedValues {
		values[k] = templatedValues[k]
	}

	return values, nonTemplatedValues, nil
}

// getKustomizeSubstituteValuesFrom return key-value pair from referenced ConfigMap/Secret
func getKustomizeSubstituteValuesFrom(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kustomizationRef *configv1beta1.KustomizationRef, logger logr.Logger) (templatedValues, nonTemplatedValues map[string]string, err error) {

	return getValuesFrom(ctx, c, clusterSummary, kustomizationRef.ValuesFrom, true, logger)
}

func getKustomizeReferenceResourceHash(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kustomizationRef *configv1beta1.KustomizationRef, logger logr.Logger) (string, error) {

	return getValuesFromResourceHash(ctx, c, clusterSummary, kustomizationRef.ValuesFrom, logger)
}

func getKustomizationRefs(clusterSummary *configv1beta1.ClusterSummary) []configv1beta1.PolicyRef {
	return nil
}

func deployKustomizeRef(ctx context.Context, c client.Client, remoteRestConfig *rest.Config,
	kustomizationRef *configv1beta1.KustomizationRef, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) (localReports, remoteReports []libsveltosv1beta1.ResourceReport, err error) {

	var tmpDir string
	tmpDir, err = prepareFileSystem(ctx, c, kustomizationRef, clusterSummary, logger)
	if err != nil {
		return nil, nil, err
	}

	if tmpDir == "" {
		return nil, nil, nil
	}

	defer os.RemoveAll(tmpDir)

	objects, err := fecthClusterObjects(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, nil, err
	}

	// Path can be expressed as a template and instantiate using Cluster fields.
	instantiatedPath, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary, clusterSummary.GetName(), kustomizationRef.Path, objects, nil, logger)
	if err != nil {
		return nil, nil, err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("using path %s", instantiatedPath))

	// New: Instantiate components paths
	var instantiatedComponents []string
	for _, comp := range kustomizationRef.Components {
		instantiatedComp, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
			clusterSummary, clusterSummary.GetName(), comp, objects, nil, logger)
		if err != nil {
			return nil, nil, err
		}
		instantiatedComponents = append(instantiatedComponents, instantiatedComp)
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("using path %s with components %v", instantiatedPath, instantiatedComponents))

	// check build path exists
	dirPath := filepath.Join(tmpDir, instantiatedPath)
	_, err = os.Stat(dirPath)
	if err != nil {
		err = fmt.Errorf("kustomization path not found: %w", err)
		return nil, nil, err
	}

	// Read the existing kustomization.yaml file
	kustFilePath := filepath.Join(dirPath, "kustomization.yaml")
	kustData, err := os.ReadFile(kustFilePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read kustomization.yaml: %w", err)
	}

	// Unmarshal the YAML into a map
	var existingKust map[string]interface{}
	if err := yaml.Unmarshal(kustData, &existingKust); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal kustomization.yaml: %w", err)
	}

	// Add the new components to the map
	if len(instantiatedComponents) > 0 {
		// The components field is a list, so we need to append to it
		if components, ok := existingKust["components"].([]interface{}); ok {
			for _, comp := range instantiatedComponents {
				components = append(components, comp)
			}
			existingKust["components"] = components
		} else {
			// If the field doesn't exist or is not a list, create a new list
			var newComponents []interface{}
			for _, comp := range instantiatedComponents {
				newComponents = append(newComponents, comp)
			}
			existingKust["components"] = newComponents
		}
	}

	// Marshal the modified map back to YAML
	modifiedKustData, err := yaml.Marshal(&existingKust)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal modified kustomization: %w", err)
	}

	// Write the modified content back to the file
	err = os.WriteFile(kustFilePath, modifiedKustData, permission0600)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to write modified kustomization.yaml: %w", err)
	}

	fs := filesys.MakeFsOnDisk()

	var resMap resmap.ResMap
	resMap, err = buildKustomization(fs, dirPath)
	if err != nil {
		return nil, nil, err
	}

	return deployKustomizeResources(ctx, c, remoteRestConfig, kustomizationRef, resMap, clusterSummary, logger)
}

func prepareFileSystem(ctx context.Context, c client.Client,
	kustomizationRef *configv1beta1.KustomizationRef, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) (string, error) {

	if kustomizationRef.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
		return prepareFileSystemWithConfigMap(ctx, c, kustomizationRef, clusterSummary, logger)
	} else if kustomizationRef.Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
		return prepareFileSystemWithSecret(ctx, c, kustomizationRef, clusterSummary, logger)
	}

	namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Namespace, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	name, err := libsveltostemplate.GetReferenceResourceName(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Name, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	source, err := getSource(ctx, c, namespace, name, kustomizationRef.Kind)
	if err != nil {
		return "", err
	}

	if source == nil {
		return "", fmt.Errorf("source %s %s/%s not found", kustomizationRef.Kind, namespace, name)
	}

	s := source.(sourcev1.Source)
	return prepareFileSystemWithFluxSource(s, logger)
}

func prepareFileSystemWithConfigMap(ctx context.Context, c client.Client,
	kustomizationRef *configv1beta1.KustomizationRef, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) (string, error) {

	namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Namespace, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	name, err := libsveltostemplate.GetReferenceResourceName(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Name, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
	if err != nil {
		return "", err
	}

	return prepareFileSystemWithData(configMap.BinaryData, kustomizationRef, logger)
}

func prepareFileSystemWithSecret(ctx context.Context, c client.Client,
	kustomizationRef *configv1beta1.KustomizationRef, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) (string, error) {

	namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Namespace, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	name, err := libsveltostemplate.GetReferenceResourceName(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, kustomizationRef.Name, clusterSummary.Spec.ClusterType)
	if err != nil {
		return "", err
	}

	secret, err := getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
	if err != nil {
		return "", err
	}

	return prepareFileSystemWithData(secret.Data, kustomizationRef, logger)
}

func prepareFileSystemWithData(binaryData map[string][]byte,
	kustomizationRef *configv1beta1.KustomizationRef, logger logr.Logger) (string, error) {

	key := "kustomize.tar.gz"
	binaryTarGz, ok := binaryData[key]
	if !ok {
		return "", fmt.Errorf("%s missing", key)
	}

	// Create tmp dir.
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("kustomization-%s-%s",
		kustomizationRef.Namespace, kustomizationRef.Name))
	if err != nil {
		err = fmt.Errorf("tmp dir error: %w", err)
		return "", err
	}

	filePath := path.Join(tmpDir, key)

	err = os.WriteFile(filePath, binaryTarGz, permission0600)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to write file %s: %v", filePath, err))
		return "", err
	}

	err = extractTarGz(filePath, tmpDir)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to extract tar.gz: %v", err))
		return "", err
	}

	logger.V(logs.LogDebug).Info("extracted .tar.gz")
	return tmpDir, nil
}

func getKustomizedResources(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	deploymentType configv1beta1.DeploymentType, resMap resmap.ResMap,
	kustomizationRef *configv1beta1.KustomizationRef, logger logr.Logger,
) (objectsToDeployLocally, objectsToDeployRemotely []*unstructured.Unstructured, mgmtResources map[string]*unstructured.Unstructured, err error) {

	mgmtResources, err = collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		return nil, nil, nil, err
	}

	// Get key-value pairs from ValuesFrom
	templatedValues, nonTemplatedValues, err := getKustomizeSubstituteValues(ctx, c, clusterSummary, kustomizationRef, logger)
	if err != nil {
		return nil, nil, nil, err
	}

	// Get substitute values. Those are collected from Data sections of referenced ConfigMap/Secret instances.
	// Those values can be expressed as template. This method instantiates those using resources in
	// the managemenet cluster
	instantiatedSubstituteValues, err := instantiateKustomizeSubstituteValues(ctx, clusterSummary, mgmtResources, templatedValues, logger)
	if err != nil {
		return nil, nil, nil, err
	}
	for k := range nonTemplatedValues {
		instantiatedSubstituteValues[k] = nonTemplatedValues[k]
	}

	resources := resMap.Resources()
	objectsToDeployLocally = make([]*unstructured.Unstructured, 0, len(resources))
	objectsToDeployRemotely = make([]*unstructured.Unstructured, 0, len(resources))
	for i := range resources {
		resource := resources[i]
		resourceYAML, err := resource.AsYAML()
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get resource YAML %v", err))
			return nil, nil, nil, err
		}

		// Assume it is a template only if there are values to substitute
		if len(instantiatedSubstituteValues) > 0 {
			// All objects coming from Kustomize output can be expressed as template. Those will be instantiated using
			// substitute values first, and the resource in the management cluster later.
			templateName := fmt.Sprintf("%s-substitutevalues", clusterSummary.Name)
			resourceYAML, err = instantiateResourceWithSubstituteValues(templateName, resourceYAML,
				instantiatedSubstituteValues, funcmap.HasTextTemplateAnnotation(clusterSummary.Annotations),
				logger)
			if err != nil {
				msg := fmt.Sprintf("failed to instantiate resource with substitute values: %v", err)
				logger.V(logs.LogInfo).Info(msg)
				return nil, nil, nil, errors.Wrap(err, msg)
			}
		}

		var u *unstructured.Unstructured
		u, err = k8s_utils.GetUnstructured(resourceYAML)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get unstructured %v", err))
			return nil, nil, nil, err
		}

		if kustomizationRef.TargetNamespace != "" {
			u.SetNamespace(kustomizationRef.TargetNamespace)
		}

		if deploymentType == configv1beta1.DeploymentTypeLocal {
			objectsToDeployLocally = append(objectsToDeployLocally, u)
		} else {
			objectsToDeployRemotely = append(objectsToDeployRemotely, u)
		}
	}

	return objectsToDeployLocally, objectsToDeployRemotely, mgmtResources, nil
}

func deployKustomizeResources(ctx context.Context, c client.Client, remoteRestConfig *rest.Config,
	kustomizationRef *configv1beta1.KustomizationRef, resMap resmap.ResMap,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger,
) (localReports, remoteReports []libsveltosv1beta1.ResourceReport, err error) {

	// Assume that if objects are deployed in the management clusters, those are needed before any resource is deployed
	// in the managed cluster. So try to deploy those first if any.

	localConfig := rest.CopyConfig(getManagementClusterConfig())
	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	if adminName != "" {
		localConfig.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", adminNamespace, adminName),
		}
	}

	objectsToDeployLocally, objectsToDeployRemotely, _, err :=
		getKustomizedResources(ctx, c, clusterSummary, kustomizationRef.DeploymentType, resMap,
			kustomizationRef, logger)
	if err != nil {
		return nil, nil, err
	}

	ref := &corev1.ObjectReference{
		Kind:      kustomizationRef.Kind,
		Namespace: kustomizationRef.Namespace,
		Name:      kustomizationRef.Name,
	}
	localReports, err = deployUnstructured(ctx, true, localConfig, c, objectsToDeployLocally,
		ref, libsveltosv1beta1.FeatureKustomize, clusterSummary, []string{}, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deploy to management cluster %v", err))
		return localReports, nil, err
	}

	// Only for SveltosCluster in pull mode and when the content must be deployed in the managed cluster
	if remoteRestConfig == nil {
		bundleResources := make(map[string][]unstructured.Unstructured)
		key := fmt.Sprintf("%s-%s-%s", ref.Kind, ref.Namespace, ref.Name)
		bundleResources[key] = convertPointerSliceToValueSlice(objectsToDeployRemotely)

		return localReports, nil, pullmode.StageResourcesForDeployment(ctx, getManagementClusterClient(),
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureKustomize), bundleResources, false, logger)
	}

	remoteClient, err := client.New(remoteRestConfig, client.Options{})
	if err != nil {
		return nil, nil, err
	}

	remoteReports, err = deployUnstructured(ctx, false, remoteRestConfig, remoteClient, objectsToDeployRemotely,
		ref, libsveltosv1beta1.FeatureKustomize, clusterSummary, []string{}, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deploy to remote cluster %v", err))
		return localReports, remoteReports, err
	}

	return localReports, remoteReports, err
}

func cleanKustomizeResources(ctx context.Context, isMgmtCluster bool, destRestConfig *rest.Config,
	destClient client.Client, clusterSummary *configv1beta1.ClusterSummary,
	resourceReports []libsveltosv1beta1.ResourceReport, logger logr.Logger) ([]libsveltosv1beta1.ResourceReport, error) {

	// This is a SveltosCluster in pull mode
	if destRestConfig == nil {
		return nil, nil
	}

	currentPolicies := make(map[string]libsveltosv1beta1.Resource, 0)
	for i := range resourceReports {
		key := deployer.GetPolicyInfo(&resourceReports[i].Resource)
		currentPolicies[key] = resourceReports[i].Resource
	}
	undeployed, err := undeployStaleResources(ctx, isMgmtCluster, destRestConfig, destClient,
		libsveltosv1beta1.FeatureKustomize, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, libsveltosv1beta1.FeatureKustomize), currentPolicies, logger)
	if err != nil {
		return nil, err
	}
	return undeployed, nil
}

// handleDriftDetectionManagerDeploymentForKustomize deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// drift-detection-manager in the managed clyuster
func handleDriftDetectionManagerDeploymentForKustomize(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType, isPullMode, startInMgmtCluster bool,
	logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err := deployDriftDetectionManagerInCluster(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, clusterSummary.Name, string(libsveltosv1beta1.FeatureKustomize), clusterType,
			startInMgmtCluster, isPullMode, logger)
		if err != nil {
			return err
		}

		// Since we are updating resources to watch for drift, remove kustomize section in ResourceSummary to eliminate
		// un-needed reconciliation (Sveltos is updating those resources so we don't want drift-detection to think
		// a configuration drift is happening)
		err = handleKustomizeResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
			clusterType, []libsveltosv1beta1.Resource{}, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to remove ResourceSummary.")
			return err
		}
	}

	return nil
}

// handleKustomizeResourceSummaryDeployment deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// ResourceSummary in the managed cluster
func handleKustomizeResourceSummaryDeployment(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	remoteDeployed []libsveltosv1beta1.Resource, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// deploy ResourceSummary
		err := deployResourceSummaryWithKustomizeResources(ctx, clusterNamespace, clusterName, clusterSummary,
			clusterType, remoteDeployed, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func deployResourceSummaryWithKustomizeResources(ctx context.Context, clusterNamespace, clusterName string,
	clusterSummary *configv1beta1.ClusterSummary, clusterType libsveltosv1beta1.ClusterType,
	deployed []libsveltosv1beta1.Resource, logger logr.Logger) error {

	resources := make([]libsveltosv1beta1.Resource, len(deployed))

	for i := range deployed {
		resources[i] = libsveltosv1beta1.Resource{
			Namespace: deployed[i].Namespace,
			Name:      deployed[i].Name,
			Group:     deployed[i].Group,
			Kind:      deployed[i].Kind,
			Version:   deployed[i].Version,
		}
	}

	clusterClient, err := getResourceSummaryClient(ctx, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	resourceSummaryNameInfo := getResourceSummaryNameInfo(clusterNamespace, clusterSummary.Name)

	return deployer.DeployResourceSummaryInCluster(ctx, clusterClient, resourceSummaryNameInfo, clusterNamespace,
		clusterName, clusterSummary.Name, clusterType, nil, resources, nil,
		clusterSummary.Spec.ClusterProfileSpec.DriftExclusions, logger)
}

// deployEachKustomizeRefs walks KustomizationRefs and deploys resources
func deployEachKustomizeRefs(ctx context.Context, c client.Client, remoteRestConfig *rest.Config,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger,
) (localResourceReports, remoteResourceReports []libsveltosv1beta1.ResourceReport, err error) {

	capacity := len(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs)
	localResourceReports = make([]libsveltosv1beta1.ResourceReport, 0, capacity)
	remoteResourceReports = make([]libsveltosv1beta1.ResourceReport, 0, capacity)
	for i := range clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs {
		kustomizationRef := &clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i]
		var tmpLocal []libsveltosv1beta1.ResourceReport
		var tmpRemote []libsveltosv1beta1.ResourceReport
		tmpLocal, tmpRemote, err = deployKustomizeRef(ctx, c, remoteRestConfig, kustomizationRef, clusterSummary, logger)
		if err != nil {
			return nil, nil, err
		}
		localResourceReports = append(localResourceReports, tmpLocal...)
		remoteResourceReports = append(remoteResourceReports, tmpRemote...)
	}

	return localResourceReports, remoteResourceReports, err
}

func extractTarGz(src, dest string) error {
	// Open the tarball for reading
	tarball, err := os.Open(src)
	if err != nil {
		return err
	}
	defer tarball.Close()

	// Create a gzip reader to decompress the tarball
	gzipReader, err := gzip.NewReader(io.LimitReader(tarball, maxSize))
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	// Create a tar reader to read the uncompressed tarball
	tarReader := archivetar.NewReader(gzipReader)

	// Iterate over each file in the tarball and extract it to the destination
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, filepath.Clean(header.Name))
		if !strings.HasPrefix(target, dest) {
			return fmt.Errorf("tar archive entry %q is outside of destination directory", header.Name)
		}

		switch header.Typeflag {
		case archivetar.TypeDir:
			if err := os.MkdirAll(target, permission0755); err != nil {
				return err
			}
		case archivetar.TypeReg:
			//nolint: gosec // OK
			file, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, io.LimitReader(tarReader, maxSize)); err != nil {
				return err
			}
			file.Close()
		}
	}

	return nil
}

func instantiateResourceWithSubstituteValues(templateName string, resource []byte,
	substituteValues map[string]string, useTxtFuncMap bool, logger logr.Logger) ([]byte, error) {

	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(funcmap.SveltosFuncMap(useTxtFuncMap)).
		Parse(string(resource))
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer

	if err := tmpl.Execute(&buffer, substituteValues); err != nil {
		return nil, fmt.Errorf("error executing template %q: %w", resource, err)
	}
	instantiatedValues := buffer.String()

	logger.V(logs.LogDebug).Info(fmt.Sprintf("Values %q", instantiatedValues))
	return []byte(instantiatedValues), nil
}
