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
	"context"
	"crypto/sha256"
	"fmt"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

func deployResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	featureHandler := getHandlersForFeature(configv1beta1.FeatureResources)

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndClusterClient(ctx, clusterNamespace, applicant, c, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, logger, err := getRestConfig(ctx, c, clusterSummary, logger)
	if err != nil {
		return err
	}

	err = handleDriftDetectionManagerDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
		clusterType, startDriftDetectionInMgmtCluster(o), logger)
	if err != nil {
		return err
	}

	localResourceReports, remoteResourceReports, deployError := deployPolicyRefs(ctx, c, remoteRestConfig,
		clusterSummary, featureHandler, logger)

	// Irrespective of error, update deployed gvks. Otherwise cleanup won't happen in case
	var gvkErr error
	clusterSummary, gvkErr = updateDeployedGroupVersionKind(ctx, clusterSummary, configv1beta1.FeatureResources,
		localResourceReports, remoteResourceReports, logger)
	if gvkErr != nil {
		return gvkErr
	}

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	remoteResources := convertResourceReportsToObjectReference(remoteResourceReports)
	err = updateReloaderWithDeployedResources(ctx, c, profileOwnerRef, configv1beta1.FeatureResources,
		remoteResources, clusterSummary, logger)
	if err != nil {
		return err
	}

	remoteDeployed := make([]configv1beta1.Resource, len(remoteResourceReports))
	for i := range remoteResourceReports {
		remoteDeployed[i] = remoteResourceReports[i].Resource
	}

	// TODO: track resource deployed in the management cluster
	err = updateClusterConfiguration(ctx, c, clusterSummary, profileOwnerRef, featureHandler.id, remoteDeployed, nil)
	if err != nil {
		return err
	}

	var undeployed []configv1beta1.ResourceReport
	_, undeployed, err = cleanStaleResources(ctx, remoteRestConfig, remoteClient, clusterSummary,
		localResourceReports, remoteResourceReports, logger)
	if err != nil {
		return err
	}
	remoteResourceReports = append(remoteResourceReports, undeployed...)

	err = handleWatchers(ctx, clusterSummary, localResourceReports, featureHandler)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, remoteResourceReports, featureHandler.id)
	if err != nil {
		return err
	}

	err = handleResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
		clusterType, remoteDeployed, logger)
	if err != nil {
		return err
	}

	if deployError != nil {
		return deployError
	}

	return validateHealthPolicies(ctx, remoteRestConfig, clusterSummary, configv1beta1.FeatureResources, logger)
}

func cleanStaleResources(ctx context.Context, remoteRestConfig *rest.Config, remoteClient client.Client,
	clusterSummary *configv1beta1.ClusterSummary, localResourceReports, remoteResourceReports []configv1beta1.ResourceReport,
	logger logr.Logger) (localUndeployed, remoteUndeployed []configv1beta1.ResourceReport, err error) {

	// Clean stale resources in the management cluster
	localUndeployed, err = cleanPolicyRefResources(ctx, true, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary, localResourceReports, logger)
	if err != nil {
		return localUndeployed, nil, err
	}

	// Clean stale resources in the remote cluster
	remoteUndeployed, err = cleanPolicyRefResources(ctx, false, remoteRestConfig, remoteClient, clusterSummary,
		remoteResourceReports, logger)
	if err != nil {
		return localUndeployed, remoteUndeployed, err
	}

	return localUndeployed, remoteUndeployed, nil
}

// handleDriftDetectionManagerDeployment deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// drift-detection-manager in the managed clyuster
func handleDriftDetectionManagerDeployment(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType, startInMgmtCluster bool,
	logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err := deployDriftDetectionManagerInCluster(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, clusterSummary.Name, clusterType, startInMgmtCluster, logger)
		if err != nil {
			return err
		}

		// Since we are updating resources to watch for drift, remove resource section in ResourceSummary to eliminate
		// un-needed reconciliation (Sveltos is updating those resources so we don't want drift-detection to think
		// a configuration drift is happening)
		err = handleResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
			clusterType, []configv1beta1.Resource{}, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to remove ResourceSummary.")
			return err
		}
	}

	return nil
}

// handleResourceSummaryDeployment deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// ResourceSummary in the managed cluster
func handleResourceSummaryDeployment(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	remoteDeployed []configv1beta1.Resource, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// deploy ResourceSummary
		err := deployResourceSummary(ctx, getManagementClusterClient(), clusterNamespace, clusterName,
			clusterSummary, clusterType, remoteDeployed, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func handleWatchers(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	localResourceReports []configv1beta1.ResourceReport, featureHandler feature) error {

	manager := getManager()
	currentResources := make(map[corev1.ObjectReference]bool)

	// Only if mode is SyncModeContinuousWithDriftDetection starts those watcher.
	// A watcher for TemplateResourceRefs is started as part of ClusterSummary reconciler
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		for i := range localResourceReports {
			gvk := schema.GroupVersionKind{
				Group:   localResourceReports[i].Resource.Group,
				Kind:    localResourceReports[i].Resource.Kind,
				Version: localResourceReports[i].Resource.Version,
			}

			apiVersion, kind := gvk.ToAPIVersionAndKind()

			ref := &corev1.ObjectReference{
				Kind:       kind,
				APIVersion: apiVersion,
				Namespace:  localResourceReports[i].Resource.Namespace,
				Name:       localResourceReports[i].Resource.Name,
			}

			if err := manager.startWatcherForMgmtResource(ctx, gvk, ref, clusterSummary, featureHandler.id); err != nil {
				return err
			}

			currentResources[*ref] = true
		}
	}

	manager.stopStaleWatchForMgmtResource(currentResources, clusterSummary, featureHandler.id)
	return nil
}

func cleanPolicyRefResources(ctx context.Context, isMgmtCluster bool, destRestConfig *rest.Config,
	destClient client.Client, clusterSummary *configv1beta1.ClusterSummary,
	resourceReports []configv1beta1.ResourceReport, logger logr.Logger,
) ([]configv1beta1.ResourceReport, error) {

	currentPolicies := make(map[string]configv1beta1.Resource, 0)
	for i := range resourceReports {
		key := getPolicyInfo(&resourceReports[i].Resource)
		currentPolicies[key] = resourceReports[i].Resource
	}

	undeployed, err := undeployStaleResources(ctx, isMgmtCluster, destRestConfig, destClient, configv1beta1.FeatureResources,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, configv1beta1.FeatureResources),
		currentPolicies, logger)
	if err != nil {
		return nil, err
	}
	return undeployed, nil
}

func undeployResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, err := configv1beta1.GetClusterSummary(ctx, c, clusterNamespace, applicant)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName)).
		WithValues("clusterSummary", clusterSummary.Name).WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	logger.V(logs.LogDebug).Info("undeployResources")

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	cacheMgr := clustercache.GetManager()
	remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	var resourceReports []configv1beta1.ResourceReport

	// Undeploy from management cluster
	if _, err = undeployStaleResources(ctx, true, getManagementClusterConfig(), c, configv1beta1.FeatureResources,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, configv1beta1.FeatureResources),
		map[string]configv1beta1.Resource{}, logger); err != nil {
		return err
	}

	// Undeploy from managed cluster
	resourceReports, err = undeployStaleResources(ctx, false, remoteRestConfig, remoteClient,
		configv1beta1.FeatureResources, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1beta1.FeatureResources),
		map[string]configv1beta1.Resource{}, logger)
	if err != nil {
		return err
	}

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	err = updateReloaderWithDeployedResources(ctx, c, profileOwnerRef, configv1beta1.FeatureResources,
		nil, clusterSummary, logger)
	if err != nil {
		return err
	}

	err = updateClusterConfiguration(ctx, c, clusterSummary, profileOwnerRef,
		configv1beta1.FeatureResources, []configv1beta1.Resource{}, nil)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, resourceReports, configv1beta1.FeatureResources)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return &configv1beta1.DryRunReconciliationError{}
	}

	manager := getManager()
	manager.stopStaleWatchForMgmtResource(nil, clusterSummary, configv1beta1.FeatureResources)

	return nil
}

// resourcesHash returns the hash of all the ClusterSummary referenced ResourceRefs.
func resourcesHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	clusterProfileSpecHash, err := getClusterProfileSpecHash(ctx, clusterSummaryScope.ClusterSummary)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	var config string
	config += string(clusterProfileSpecHash)

	clusterSummary := clusterSummaryScope.ClusterSummary
	for i := range clusterSummary.Spec.ClusterProfileSpec.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i]
		namespace := libsveltostemplate.GetReferenceResourceNamespace(
			clusterSummaryScope.Namespace(), reference.Namespace)

		name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), reference.Name)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s %s/%s: %v",
				reference.Kind, reference.Namespace, reference.Name, err))
			return nil, err
		}

		if reference.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			configmap := &corev1.ConfigMap{}
			err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, configmap)
			if err == nil {
				config += getConfigMapHash(configmap)
			}
		} else if reference.Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
			secret := &corev1.Secret{}
			err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret)
			if err == nil {
				config += getSecretHash(secret)
			}
		} else {
			var source client.Object
			source, err = getSource(ctx, c, namespace, name, reference.Kind)
			if err == nil && source == nil {
				s := source.(sourcev1.Source)
				if s.GetArtifact() != nil {
					config += s.GetArtifact().Revision
				}
			}
			if source.GetAnnotations() != nil {
				config += getDataSectionHash(source.GetAnnotations())
			}
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s %s/%s does not exist yet",
					reference.Kind, reference.Namespace, name))
				continue
			}
			logger.Error(err, fmt.Sprintf("failed to get %s %s/%s",
				reference.Kind, reference.Namespace, name))
			return nil, err
		}
	}

	for i := range clusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		h := &clusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]
		if h.FeatureID == configv1beta1.FeatureResources {
			config += render.AsCode(h)
		}
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getResourceRefs(clusterSummary *configv1beta1.ClusterSummary) []configv1beta1.PolicyRef {
	return clusterSummary.Spec.ClusterProfileSpec.PolicyRefs
}

// updateClusterReportWithResourceReports updates ClusterReport Status with ResourceReports.
// This is no-op unless mode is DryRun.
func updateClusterReportWithResourceReports(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary, resourceReports []configv1beta1.ResourceReport,
	featureID configv1beta1.FeatureID) error {

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

		if featureID == configv1beta1.FeatureResources {
			clusterReport.Status.ResourceReports = resourceReports
		} else if featureID == configv1beta1.FeatureKustomize {
			clusterReport.Status.KustomizeResourceReports = resourceReports
		}
		return c.Status().Update(ctx, clusterReport)
	})
	return err
}

func deployResourceSummary(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterSummary *configv1beta1.ClusterSummary,
	clusterType libsveltosv1beta1.ClusterType,
	deployed []configv1beta1.Resource, logger logr.Logger) error {

	resources := make([]libsveltosv1beta1.Resource, len(deployed))

	for i := range deployed {
		resources[i] = libsveltosv1beta1.Resource{
			Namespace:                   deployed[i].Namespace,
			Name:                        deployed[i].Name,
			Group:                       deployed[i].Group,
			Kind:                        deployed[i].Kind,
			Version:                     deployed[i].Version,
			IgnoreForConfigurationDrift: deployed[i].IgnoreForConfigurationDrift,
		}
	}

	return deployResourceSummaryInCluster(ctx, c, clusterNamespace, clusterName, clusterSummary.Name,
		clusterType, resources, nil, nil, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions, logger)
}

// deployPolicyRefs deploys in a managed Cluster the policies contained in the Data section of each
// referenced ConfigMap/Secret
func deployPolicyRefs(ctx context.Context, c client.Client, remoteConfig *rest.Config,
	clusterSummary *configv1beta1.ClusterSummary, featureHandler feature,
	logger logr.Logger) (localReports, remoteReports []configv1beta1.ResourceReport, err error) {

	refs := featureHandler.getRefs(clusterSummary)

	var objectsToDeployLocally []client.Object
	var objectsToDeployRemotely []client.Object
	// collect all referenced resources whose content need to be deployed
	// in the management cluster (local) or manaded cluster (remote)
	objectsToDeployLocally, objectsToDeployRemotely, err =
		collectReferencedObjects(ctx, c, clusterSummary, refs, logger)
	if err != nil {
		return nil, nil, err
	}

	return deployReferencedObjects(ctx, c, remoteConfig, clusterSummary,
		objectsToDeployLocally, objectsToDeployRemotely, logger)
}
