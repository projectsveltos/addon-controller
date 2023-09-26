/*
Copyright 2023. projectsveltos.io. All rights reserved.

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
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/fluxcd/pkg/http/fetch"
	"github.com/fluxcd/pkg/tar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

const (
	permission0600 = 0600
	permission0755 = 0755
	maxSize        = int64(20 * 1024 * 1024)
)

func deployKustomizeRefs(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1alpha1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	featureHandler := getHandlersForFeature(configv1alpha1.FeatureKustomize)

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndClusterClient(ctx, clusterNamespace, applicant, c, logger)
	if err != nil {
		return err
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName))
	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger = logger.WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("deploying kustomize resources")

	localResourceReports, remoteResourceReports, err := deployEachKustomizeRefs(ctx, c, remoteRestConfig,
		clusterSummary, logger)

	// Irrespective of error, update deployed gvks. Otherwise cleanup won't happen in case
	gvkErr := updateDeployedGroupVersionKind(ctx, clusterSummary, configv1alpha1.FeatureKustomize,
		localResourceReports, remoteResourceReports, logger)
	if err != nil {
		return err
	}
	if gvkErr != nil {
		return gvkErr
	}

	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}
	remoteResources := convertResourceReportsToObjectReference(remoteResourceReports)
	err = updateReloaderWithDeployedResources(ctx, c, clusterProfileOwnerRef, configv1alpha1.FeatureKustomize,
		remoteResources, clusterSummary, logger)
	if err != nil {
		return err
	}

	// If we are here there are no conflicts (and error would have been returned by deployKustomizeRef)
	remoteDeployed := make([]configv1alpha1.Resource, 0)
	for i := range remoteResourceReports {
		remoteDeployed = append(remoteDeployed, remoteResourceReports[i].Resource)
	}

	// TODO: track resource deployed in the management cluster
	err = updateClusterConfiguration(ctx, c, clusterSummary, clusterProfileOwnerRef, featureHandler.id, remoteDeployed, nil)
	if err != nil {
		return err
	}

	var undeployed []configv1alpha1.ResourceReport
	_, undeployed, err = cleanStaleKustomizeResources(ctx, remoteRestConfig, remoteClient, clusterSummary,
		localResourceReports, remoteResourceReports, logger)
	if err != nil {
		return err
	}
	remoteResourceReports = append(remoteResourceReports, undeployed...)

	err = handleWatchers(ctx, clusterSummary, localResourceReports)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, remoteResourceReports, configv1alpha1.FeatureKustomize)
	if err != nil {
		return err
	}

	err = handleKustomizeResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
		clusterType, remoteDeployed, logger)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return &configv1alpha1.DryRunReconciliationError{}
	}
	return validateHealthPolicies(ctx, remoteRestConfig, clusterSummary, configv1alpha1.FeatureKustomize, logger)
}

func cleanStaleKustomizeResources(ctx context.Context, remoteRestConfig *rest.Config, remoteClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, localResourceReports, remoteResourceReports []configv1alpha1.ResourceReport,
	logger logr.Logger) (localUndeployed, remoteUndeployed []configv1alpha1.ResourceReport, err error) {
	// Clean stale resources in the management cluster
	localUndeployed, err = cleanKustomizeResources(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary, localResourceReports, logger)
	if err != nil {
		return
	}

	// Clean stale resources in the remote cluster
	remoteUndeployed, err = cleanKustomizeResources(ctx, remoteRestConfig, remoteClient,
		clusterSummary, remoteResourceReports, logger)
	if err != nil {
		return
	}

	return
}

func undeployKustomizeRefs(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1alpha1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	clusterSummary, err := configv1alpha1.GetClusterSummary(ctx, c, clusterNamespace, applicant)
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

	var resourceReports []configv1alpha1.ResourceReport

	// Undeploy from management cluster
	_, err = undeployStaleResources(ctx, getManagementClusterConfig(), c, configv1alpha1.FeatureKustomize,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureKustomize),
		map[string]configv1alpha1.Resource{}, logger)
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
	resourceReports, err = undeployStaleResources(ctx, remoteRestConfig, remoteClient, configv1alpha1.FeatureKustomize,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureKustomize),
		map[string]configv1alpha1.Resource{}, logger)
	if err != nil {
		return err
	}

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
		configv1alpha1.FeatureKustomize, []configv1alpha1.Resource{}, nil)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, resourceReports, configv1alpha1.FeatureKustomize)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return &configv1alpha1.DryRunReconciliationError{}
	}

	manager := getManager()
	manager.stopStaleWatch(nil, clusterSummary)

	return nil
}

// resourcesHash returns the hash of all the ClusterSummary referenced KustomizationRefs.
func kustomizationHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	// If SyncMode changes (from not ContinuousWithDriftDetection to ContinuousWithDriftDetection
	// or viceversa) reconcile.
	config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode)

	// If Reloader changes, Reloader needs to be deployed or undeployed
	// So consider it in the hash
	config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Reloader)

	config += render.AsCode(clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs)

	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs {
		kustomizationRef := &clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i]

		if kustomizationRef.Kind == string(libsveltosv1alpha1.ConfigMapReferencedResourceKind) {
			namespace := getReferenceResourceNamespace(clusterSummaryScope.ClusterSummary.Namespace,
				kustomizationRef.Namespace)
			configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: kustomizationRef.Name})
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ConfigMap %v", err))
				return nil, err
			}
			if configMap == nil {
				continue
			}
			config += render.AsCode(configMap.BinaryData)
		} else if kustomizationRef.Kind == string(libsveltosv1alpha1.SecretReferencedResourceKind) {
			namespace := getReferenceResourceNamespace(clusterSummaryScope.ClusterSummary.Namespace,
				kustomizationRef.Namespace)
			secret, err := getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: kustomizationRef.Name})
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get Secret %v", err))
				return nil, err
			}
			if secret == nil {
				continue
			}
			config += render.AsCode(secret.Data)
		} else {
			source, err := getSource(ctx, c, kustomizationRef, clusterSummaryScope.ClusterSummary)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get source %v", err))
				return nil, err
			}
			if source == nil {
				continue
			}
			if source.GetArtifact() != nil {
				config += source.GetArtifact().Revision
			}
		}
	}

	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		h := &clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]
		if h.FeatureID == configv1alpha1.FeatureKustomize {
			config += render.AsCode(h)
		}
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getKustomizationRefs(clusterSummary *configv1alpha1.ClusterSummary) []configv1alpha1.PolicyRef {
	return nil
}

func deployKustomizeRef(ctx context.Context, c client.Client, remoteRestConfig *rest.Config,
	kustomizationRef *configv1alpha1.KustomizationRef, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (localReports, remoteReports []configv1alpha1.ResourceReport, err error) {

	var tmpDir string
	tmpDir, err = prepareFileSystem(ctx, c, kustomizationRef, clusterSummary, logger)
	if err != nil {
		return nil, nil, err
	}

	if tmpDir == "" {
		return nil, nil, nil
	}

	defer os.RemoveAll(tmpDir)

	// check build path exists
	dirPath := filepath.Join(tmpDir, kustomizationRef.Path)
	_, err = os.Stat(dirPath)
	if err != nil {
		err = fmt.Errorf("kustomization path not found: %w", err)
		return nil, nil, err
	}

	fs := filesys.MakeFsOnDisk()

	kustomizer := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	var resMap resmap.ResMap
	resMap, err = kustomizer.Run(fs, dirPath)
	if err != nil {
		return nil, nil, err
	}

	return deployKustomizeResources(ctx, c, remoteRestConfig, kustomizationRef, resMap, clusterSummary, logger)
}

func prepareFileSystem(ctx context.Context, c client.Client,
	kustomizationRef *configv1alpha1.KustomizationRef, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (string, error) {

	if kustomizationRef.Kind == string(libsveltosv1alpha1.ConfigMapReferencedResourceKind) {
		return prepareFileSystemWithConfigMap(ctx, c, kustomizationRef, clusterSummary, logger)
	} else if kustomizationRef.Kind == string(libsveltosv1alpha1.SecretReferencedResourceKind) {
		return prepareFileSystemWithSecret(ctx, c, kustomizationRef, clusterSummary, logger)
	}

	return prepareFileSystemWithFluxSource(ctx, c, kustomizationRef, clusterSummary, logger)
}

func prepareFileSystemWithConfigMap(ctx context.Context, c client.Client,
	kustomizationRef *configv1alpha1.KustomizationRef, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (string, error) {

	namespace := getReferenceResourceNamespace(clusterSummary.Namespace, kustomizationRef.Namespace)
	configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: kustomizationRef.Name})
	if err != nil {
		return "", err
	}

	return prepareFileSystemWithData(configMap.BinaryData, kustomizationRef, logger)
}

func prepareFileSystemWithSecret(ctx context.Context, c client.Client,
	kustomizationRef *configv1alpha1.KustomizationRef, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (string, error) {

	namespace := getReferenceResourceNamespace(clusterSummary.Namespace, kustomizationRef.Namespace)
	secret, err := getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: kustomizationRef.Name})
	if err != nil {
		return "", err
	}

	return prepareFileSystemWithData(secret.Data, kustomizationRef, logger)
}

func prepareFileSystemWithData(binaryData map[string][]byte,
	kustomizationRef *configv1alpha1.KustomizationRef, logger logr.Logger) (string, error) {

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

func prepareFileSystemWithFluxSource(ctx context.Context, c client.Client,
	kustomizationRef *configv1alpha1.KustomizationRef, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (string, error) {

	source, err := getSource(ctx, c, kustomizationRef, clusterSummary)
	if err != nil {
		return "", err
	}

	if source == nil {
		return "", fmt.Errorf("source %s %s/%s not found",
			kustomizationRef.Kind, kustomizationRef.Namespace, kustomizationRef.Name)
	}

	if source.GetArtifact() == nil {
		msg := "Source is not ready, artifact not found"
		logger.V(logs.LogInfo).Info(msg)
		return "", err
	}

	// Update status with the reconciliation progress.
	// revision := source.GetArtifact().Revision

	// Create tmp dir.
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("kustomization-%s-%s", kustomizationRef.Namespace, kustomizationRef.Name))
	if err != nil {
		err = fmt.Errorf("tmp dir error: %w", err)
		return "", err
	}

	artifactFetcher := fetch.NewArchiveFetcher(
		1,
		tar.UnlimitedUntarSize,
		tar.UnlimitedUntarSize,
		os.Getenv("SOURCE_CONTROLLER_LOCALHOST"),
	)

	// Download artifact and extract files to the tmp dir.
	err = artifactFetcher.Fetch(source.GetArtifact().URL, source.GetArtifact().Digest, tmpDir)
	if err != nil {
		return "", err
	}

	return tmpDir, nil
}

func getKustomizedResources(deploymentType configv1alpha1.DeploymentType, resMap resmap.ResMap,
	kustomizationRef *configv1alpha1.KustomizationRef, logger logr.Logger,
) (objectsToDeployLocally, objectsToDeployRemotely []*unstructured.Unstructured, err error) {

	resources := resMap.Resources()
	for i := range resources {
		resource := resources[i]
		yaml, err := resource.AsYAML()
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get resource YAML %v", err))
			return nil, nil, err
		}
		var u *unstructured.Unstructured
		u, err = utils.GetUnstructured(yaml)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get unstructured %v", err))
			return nil, nil, err
		}

		if kustomizationRef.TargetNamespace != "" {
			u.SetNamespace(kustomizationRef.TargetNamespace)
		}

		if deploymentType == configv1alpha1.DeploymentTypeLocal {
			objectsToDeployLocally = append(objectsToDeployLocally, u)
		} else {
			objectsToDeployRemotely = append(objectsToDeployRemotely, u)
		}
	}

	return objectsToDeployLocally, objectsToDeployRemotely, nil
}

func deployKustomizeResources(ctx context.Context, c client.Client, remoteRestConfig *rest.Config,
	kustomizationRef *configv1alpha1.KustomizationRef, resMap resmap.ResMap,
	clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger,
) (localReports, remoteReports []configv1alpha1.ResourceReport, err error) {

	// Assume that if objects are deployed in the management clusters, those are needed before any resource is deployed
	// in the managed cluster. So try to deploy those first if any.

	localConfig := rest.CopyConfig(getManagementClusterConfig())
	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	if adminName != "" {
		localConfig.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", adminNamespace, adminName),
		}
	}

	objectsToDeployLocally, objectsToDeployRemotely, err :=
		getKustomizedResources(kustomizationRef.DeploymentType, resMap, kustomizationRef, logger)
	if err != nil {
		return nil, nil, err
	}

	ref := &corev1.ObjectReference{
		Kind:      kustomizationRef.Kind,
		Namespace: kustomizationRef.Namespace,
		Name:      kustomizationRef.Name,
	}

	localReports, err = deployUnstructured(ctx, localConfig, c, objectsToDeployLocally,
		ref, configv1alpha1.FeatureKustomize, clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deploy to management cluster %v", err))
		return localReports, nil, err
	}

	remoteClient, err := client.New(remoteRestConfig, client.Options{})
	if err != nil {
		return nil, nil, err
	}

	err = validateUnstructred(ctx, true, objectsToDeployRemotely, clusterSummary, logger)
	if err != nil {
		return nil, nil, err
	}

	remoteReports, err = deployUnstructured(ctx, remoteRestConfig, remoteClient, objectsToDeployRemotely,
		ref, configv1alpha1.FeatureKustomize, clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deploy to remote cluster %v", err))
		return localReports, remoteReports, err
	}

	return localReports, remoteReports, err
}

func getSource(ctx context.Context, c client.Client, kustomizationRef *configv1alpha1.KustomizationRef,
	clusterSummary *configv1alpha1.ClusterSummary) (sourcev1.Source, error) {

	var src sourcev1.Source
	namespace := getReferenceResourceNamespace(clusterSummary.Namespace, kustomizationRef.Namespace)
	namespacedName := types.NamespacedName{
		Namespace: namespace,
		Name:      kustomizationRef.Name,
	}

	switch kustomizationRef.Kind {
	case sourcev1.GitRepositoryKind:
		var repository sourcev1.GitRepository
		err := c.Get(ctx, namespacedName, &repository)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		src = &repository
	case sourcev1b2.OCIRepositoryKind:
		var repository sourcev1b2.OCIRepository
		err := c.Get(ctx, namespacedName, &repository)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return src, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		src = &repository
	case sourcev1b2.BucketKind:
		var bucket sourcev1b2.Bucket
		err := c.Get(ctx, namespacedName, &bucket)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return src, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		src = &bucket
	default:
		return src, fmt.Errorf("source `%s` kind '%s' not supported",
			kustomizationRef.Name, kustomizationRef.Kind)
	}
	return src, nil
}

func cleanKustomizeResources(ctx context.Context, destRestConfig *rest.Config, destClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, resourceReports []configv1alpha1.ResourceReport,
	logger logr.Logger) ([]configv1alpha1.ResourceReport, error) {

	currentPolicies := make(map[string]configv1alpha1.Resource, 0)
	for i := range resourceReports {
		key := getPolicyInfo(&resourceReports[i].Resource)
		currentPolicies[key] = resourceReports[i].Resource
	}
	undeployed, err := undeployStaleResources(ctx, destRestConfig, destClient, configv1alpha1.FeatureKustomize,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureKustomize),
		currentPolicies, logger)
	if err != nil {
		return nil, err
	}
	return undeployed, nil
}

// handleKustomizeResourceSummaryDeployment deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// ResourceSummary in the managed cluster
func handleKustomizeResourceSummaryDeployment(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	remoteDeployed []configv1alpha1.Resource, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// deploy ResourceSummary
		err := deployResourceSummaryWithKustomizeResources(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, clusterSummary.Name, clusterType, remoteDeployed, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func deployResourceSummaryWithKustomizeResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant string,
	clusterType libsveltosv1alpha1.ClusterType,
	deployed []configv1alpha1.Resource, logger logr.Logger) error {

	resources := make([]libsveltosv1alpha1.Resource, len(deployed))

	for i := range deployed {
		resources[i] = libsveltosv1alpha1.Resource{
			Namespace: deployed[i].Namespace,
			Name:      deployed[i].Name,
			Group:     deployed[i].Group,
			Kind:      deployed[i].Kind,
			Version:   deployed[i].Version,
		}
	}

	return deployResourceSummaryInCluster(ctx, c, clusterNamespace, clusterName, applicant,
		clusterType, nil, resources, nil, logger)
}

// deployEachKustomizeRefs walks KustomizationRefs and deploys resources
func deployEachKustomizeRefs(ctx context.Context, c client.Client, remoteRestConfig *rest.Config,
	clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger,
) (localResourceReports, remoteResourceReports []configv1alpha1.ResourceReport, err error) {

	for i := range clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs {
		kustomizationRef := &clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i]
		var tmpLocal []configv1alpha1.ResourceReport
		var tmpRemote []configv1alpha1.ResourceReport
		tmpLocal, tmpRemote, err = deployKustomizeRef(ctx, c, remoteRestConfig, kustomizationRef, clusterSummary, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to deploy kustomize resources: %v", err))
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
