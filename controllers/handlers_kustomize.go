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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/funcmap"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
	"github.com/projectsveltos/libsveltos/lib/utils"
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

	featureHandler := getHandlersForFeature(configv1beta1.FeatureKustomize)

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndClusterClient(ctx, clusterNamespace, applicant, c, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, logger, err := getRestConfig(ctx, c, clusterSummary, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("deploying kustomize resources")

	err = handleDriftDetectionManagerDeploymentForKustomize(ctx, clusterSummary, clusterNamespace,
		clusterName, clusterType, startDriftDetectionInMgmtCluster(o), logger)
	if err != nil {
		return err
	}

	localResourceReports, remoteResourceReports, deployError := deployEachKustomizeRefs(ctx, c, remoteRestConfig,
		clusterSummary, logger)

	// Irrespective of error, update deployed gvks. Otherwise cleanup won't happen in case
	var gvkErr error
	clusterSummary, gvkErr = updateDeployedGroupVersionKind(ctx, clusterSummary, configv1beta1.FeatureKustomize,
		localResourceReports, remoteResourceReports, logger)
	if gvkErr != nil {
		return gvkErr
	}

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}
	remoteResources := convertResourceReportsToObjectReference(remoteResourceReports)
	err = updateReloaderWithDeployedResources(ctx, c, profileOwnerRef, configv1beta1.FeatureKustomize,
		remoteResources, clusterSummary, logger)
	if err != nil {
		return err
	}

	// If we are here there are no conflicts (and error would have been returned by deployKustomizeRef)
	remoteDeployed := make([]configv1beta1.Resource, 0)
	for i := range remoteResourceReports {
		remoteDeployed = append(remoteDeployed, remoteResourceReports[i].Resource)
	}

	// TODO: track resource deployed in the management cluster
	err = updateClusterConfiguration(ctx, c, clusterSummary, profileOwnerRef, featureHandler.id, remoteDeployed, nil)
	if err != nil {
		return err
	}

	var undeployed []configv1beta1.ResourceReport
	_, undeployed, err = cleanStaleKustomizeResources(ctx, remoteRestConfig, remoteClient, clusterSummary,
		localResourceReports, remoteResourceReports, logger)
	if err != nil {
		return err
	}
	remoteResourceReports = append(remoteResourceReports, undeployed...)

	err = handleWatchers(ctx, clusterSummary, localResourceReports, featureHandler)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, remoteResourceReports, configv1beta1.FeatureKustomize)
	if err != nil {
		return err
	}

	err = handleKustomizeResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
		clusterType, remoteDeployed, logger)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return &configv1beta1.DryRunReconciliationError{}
	}

	if deployError != nil {
		return deployError
	}

	return validateHealthPolicies(ctx, remoteRestConfig, clusterSummary, configv1beta1.FeatureKustomize, logger)
}

func cleanStaleKustomizeResources(ctx context.Context, remoteRestConfig *rest.Config, remoteClient client.Client,
	clusterSummary *configv1beta1.ClusterSummary, localResourceReports, remoteResourceReports []configv1beta1.ResourceReport,
	logger logr.Logger) (localUndeployed, remoteUndeployed []configv1beta1.ResourceReport, err error) {
	// Clean stale resources in the management cluster
	localUndeployed, err = cleanKustomizeResources(ctx, true, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary, localResourceReports, logger)
	if err != nil {
		return
	}

	// Clean stale resources in the remote cluster
	remoteUndeployed, err = cleanKustomizeResources(ctx, false, remoteRestConfig, remoteClient,
		clusterSummary, remoteResourceReports, logger)
	if err != nil {
		return
	}

	return
}

func undeployKustomizeRefs(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

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

	var resourceReports []configv1beta1.ResourceReport

	// Undeploy from management cluster
	_, err = undeployStaleResources(ctx, true, getManagementClusterConfig(), c, configv1beta1.FeatureKustomize,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, configv1beta1.FeatureKustomize),
		map[string]configv1beta1.Resource{}, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	// Undeploy from managed cluster
	resourceReports, err = undeployStaleResources(ctx, false, remoteRestConfig, remoteClient, configv1beta1.FeatureKustomize,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, configv1beta1.FeatureKustomize),
		map[string]configv1beta1.Resource{}, logger)
	if err != nil {
		return err
	}

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
		configv1beta1.FeatureKustomize, []configv1beta1.Resource{}, nil)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, resourceReports, configv1beta1.FeatureKustomize)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return &configv1beta1.DryRunReconciliationError{}
	}

	manager := getManager()
	manager.stopStaleWatchForMgmtResource(nil, clusterSummary, configv1beta1.FeatureKustomize)

	return nil
}

// resourcesHash returns the hash of all the ClusterSummary referenced KustomizationRefs.
func kustomizationHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	clusterProfileSpecHash, err := getClusterProfileSpecHash(ctx, clusterSummaryScope.ClusterSummary)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	var config string
	config += string(clusterProfileSpecHash)

	clusterSummary := clusterSummaryScope.ClusterSummary
	config += render.AsCode(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs)
	for i := range clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs {
		kustomizationRef := &clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i]

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
		if h.FeatureID == configv1beta1.FeatureKustomize {
			config += render.AsCode(h)
		}
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getHashFromKustomizationRef(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	kustomizationRef *configv1beta1.KustomizationRef, logger logr.Logger) ([]byte, error) {

	var result string
	namespace := libsveltostemplate.GetReferenceResourceNamespace(
		clusterSummary.Namespace, kustomizationRef.Namespace)

	name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), kustomizationRef.Name)
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

	instantiatedValue, err :=
		instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
			clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			requestorName, stringifiedValues, mgmtResources, logger)
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
	logger logr.Logger) (localReports, remoteReports []configv1beta1.ResourceReport, err error) {

	var tmpDir string
	tmpDir, err = prepareFileSystem(ctx, c, kustomizationRef, clusterSummary, logger)
	if err != nil {
		return nil, nil, err
	}

	if tmpDir == "" {
		return nil, nil, nil
	}

	defer os.RemoveAll(tmpDir)

	// Path can be expressed as a template and instantiate using Cluster fields.
	instantiatedPath, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.GetName(), kustomizationRef.Path, nil, logger)
	if err != nil {
		return nil, nil, err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("using path %s", instantiatedPath))

	// check build path exists
	dirPath := filepath.Join(tmpDir, instantiatedPath)
	_, err = os.Stat(dirPath)
	if err != nil {
		err = fmt.Errorf("kustomization path not found: %w", err)
		return nil, nil, err
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

	namespace := libsveltostemplate.GetReferenceResourceNamespace(
		clusterSummary.Namespace, kustomizationRef.Namespace)

	name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), kustomizationRef.Name)
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

	namespace := libsveltostemplate.GetReferenceResourceNamespace(
		clusterSummary.Namespace, kustomizationRef.Namespace)

	name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), kustomizationRef.Name)
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

	namespace := libsveltostemplate.GetReferenceResourceNamespace(
		clusterSummary.Namespace, kustomizationRef.Namespace)

	name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), kustomizationRef.Name)
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
	for i := range resources {
		resource := resources[i]
		yaml, err := resource.AsYAML()
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get resource YAML %v", err))
			return nil, nil, nil, err
		}

		// Assume it is a template only if there are values to substitute
		if len(instantiatedSubstituteValues) > 0 {
			// All objects coming from Kustomize output can be expressed as template. Those will be instantiated using
			// substitute values first, and the resource in the management cluster later.
			templateName := fmt.Sprintf("%s-substitutevalues", clusterSummary.Name)
			yaml, err = instantiateResourceWithSubstituteValues(templateName, yaml, instantiatedSubstituteValues, logger)
			if err != nil {
				msg := fmt.Sprintf("failed to instantiate resource with substitute values: %v", err)
				logger.V(logs.LogInfo).Info(msg)
				return nil, nil, nil, errors.Wrap(err, msg)
			}
		}

		var u *unstructured.Unstructured
		u, err = utils.GetUnstructured(yaml)
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
) (localReports, remoteReports []configv1beta1.ResourceReport, err error) {

	// Assume that if objects are deployed in the management clusters, those are needed before any resource is deployed
	// in the managed cluster. So try to deploy those first if any.

	localConfig := rest.CopyConfig(getManagementClusterConfig())
	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	if adminName != "" {
		localConfig.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", adminNamespace, adminName),
		}
	}

	objectsToDeployLocally, objectsToDeployRemotely, mgmtResources, err :=
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
		ref, configv1beta1.FeatureKustomize, clusterSummary, mgmtResources, []string{}, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deploy to management cluster %v", err))
		return localReports, nil, err
	}

	remoteClient, err := client.New(remoteRestConfig, client.Options{})
	if err != nil {
		return nil, nil, err
	}

	remoteReports, err = deployUnstructured(ctx, false, remoteRestConfig, remoteClient, objectsToDeployRemotely,
		ref, configv1beta1.FeatureKustomize, clusterSummary, mgmtResources, []string{}, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deploy to remote cluster %v", err))
		return localReports, remoteReports, err
	}

	return localReports, remoteReports, err
}

func cleanKustomizeResources(ctx context.Context, isMgmtCluster bool, destRestConfig *rest.Config,
	destClient client.Client, clusterSummary *configv1beta1.ClusterSummary,
	resourceReports []configv1beta1.ResourceReport, logger logr.Logger) ([]configv1beta1.ResourceReport, error) {

	currentPolicies := make(map[string]configv1beta1.Resource, 0)
	for i := range resourceReports {
		key := getPolicyInfo(&resourceReports[i].Resource)
		currentPolicies[key] = resourceReports[i].Resource
	}
	undeployed, err := undeployStaleResources(ctx, isMgmtCluster, destRestConfig, destClient,
		configv1beta1.FeatureKustomize, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1beta1.FeatureKustomize), currentPolicies, logger)
	if err != nil {
		return nil, err
	}
	return undeployed, nil
}

// handleDriftDetectionManagerDeploymentForKustomize deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// drift-detection-manager in the managed clyuster
func handleDriftDetectionManagerDeploymentForKustomize(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType, startInMgmtCluster bool,
	logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err := deployDriftDetectionManagerInCluster(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, clusterSummary.Name, clusterType, startInMgmtCluster, logger)
		if err != nil {
			return err
		}

		// Since we are updating resources to watch for drift, remove kustomize section in ResourceSummary to eliminate
		// un-needed reconciliation (Sveltos is updating those resources so we don't want drift-detection to think
		// a configuration drift is happening)
		err = handleKustomizeResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
			clusterType, []configv1beta1.Resource{}, logger)
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
	remoteDeployed []configv1beta1.Resource, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// deploy ResourceSummary
		err := deployResourceSummaryWithKustomizeResources(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, clusterSummary, clusterType, remoteDeployed, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func deployResourceSummaryWithKustomizeResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterSummary *configv1beta1.ClusterSummary,
	clusterType libsveltosv1beta1.ClusterType,
	deployed []configv1beta1.Resource, logger logr.Logger) error {

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

	return deployResourceSummaryInCluster(ctx, c, clusterNamespace, clusterName, clusterSummary.Name,
		clusterType, nil, resources, nil, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions, logger)
}

// deployEachKustomizeRefs walks KustomizationRefs and deploys resources
func deployEachKustomizeRefs(ctx context.Context, c client.Client, remoteRestConfig *rest.Config,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger,
) (localResourceReports, remoteResourceReports []configv1beta1.ResourceReport, err error) {

	for i := range clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs {
		kustomizationRef := &clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i]
		var tmpLocal []configv1beta1.ResourceReport
		var tmpRemote []configv1beta1.ResourceReport
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
	substituteValues map[string]string, logger logr.Logger) ([]byte, error) {

	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(funcmap.SveltosFuncMap()).Parse(string(resource))
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer

	if err := tmpl.Execute(&buffer, substituteValues); err != nil {
		return nil, errors.Wrapf(err, "error executing template %q", resource)
	}
	instantiatedValues := buffer.String()

	logger.V(logs.LogDebug).Info(fmt.Sprintf("Values %q", instantiatedValues))
	return []byte(instantiatedValues), nil
}
