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
	"errors"
	"fmt"
	"sort"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	pullmode "github.com/projectsveltos/libsveltos/lib/pullmode"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

func deployResources(ctx context.Context, c client.Client,
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

	remoteRestConfig, logger, err := getRestConfig(ctx, c, clusterSummary, logger)
	if err != nil {
		return err
	}

	if isPullMode {
		// If SveltosCluster is in pull mode, discard all previous staged resources. Those will be regenerated now.
		err = pullmode.DiscardStagedResourcesForDeployment(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, configv1beta1.ClusterSummaryKind, applicant, string(libsveltosv1beta1.FeatureResources), logger)
		if err != nil {
			return err
		}
	}

	err = handleDriftDetectionManagerDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
		clusterType, isPullMode, startDriftDetectionInMgmtCluster(o), logger)
	if err != nil {
		return err
	}

	featureHandler := getHandlersForFeature(libsveltosv1beta1.FeatureResources)
	localResourceReports, remoteResourceReports, deployError := deployPolicyRefs(ctx, c, remoteRestConfig, remoteClient,
		clusterSummary, featureHandler, logger)

	configurationHash, _ := o.HandlerOptions[configurationHash].([]byte)
	return postProcessDeployedResources(ctx, remoteRestConfig, clusterSummary, localResourceReports,
		remoteResourceReports, deployError, isPullMode, configurationHash, logger)
}

func postProcessDeployedResources(ctx context.Context, remoteRestConfig *rest.Config,
	clusterSummary *configv1beta1.ClusterSummary, localResourceReports, remoteResourceReports []libsveltosv1beta1.ResourceReport,
	deployError error, isPullMode bool, configurationHash []byte, logger logr.Logger) error {

	c := getManagementClusterClient()
	featureHandler := getHandlersForFeature(libsveltosv1beta1.FeatureResources)

	// Irrespective of error, update deployed gvks. Otherwise cleanup won't happen in case
	var gvkErr error
	clusterSummary, gvkErr = updateDeployedGroupVersionKind(ctx, clusterSummary, libsveltosv1beta1.FeatureResources,
		localResourceReports, remoteResourceReports, logger)
	if gvkErr != nil {
		return gvkErr
	}

	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}

	remoteDeployed := make([]libsveltosv1beta1.Resource, len(remoteResourceReports))

	if !isPullMode {
		remoteResources := clusterops.ConvertResourceReportsToObjectReference(remoteResourceReports)
		err = updateReloaderWithDeployedResources(ctx, clusterSummary, profileRef, libsveltosv1beta1.FeatureResources,
			remoteResources, !clusterSummary.Spec.ClusterProfileSpec.Reloader, logger)
		if err != nil {
			return err
		}

		for i := range remoteResourceReports {
			remoteDeployed[i] = remoteResourceReports[i].Resource
		}

		isDrynRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		clean := !clusterSummary.DeletionTimestamp.IsZero()
		// If SveltosCluster is in pull mode, once the agent has deployed the resources, as soon as the addon controller
		// sees the status as provisioned, will update the ClusterConfiguration.
		err = clusterops.UpdateClusterConfiguration(ctx, c, isDrynRun, clean, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, profileRef, featureHandler.id, remoteDeployed,
			nil)
		if err != nil {
			return err
		}

		// TODO: track resource deployed in the management cluster
	}

	// If a deployment error happened, do not try to clean stale resources. Because of the error potentially
	// all resources might be considered stale at this time.
	if deployError != nil {
		return deployError
	}

	// Agent in the managed cluster will take care of this for SveltosClusters in pull mode. We still need to
	// clean stale resources deployed in the management cluster
	var undeployed []libsveltosv1beta1.ResourceReport
	_, undeployed, err = cleanStaleResources(ctx, clusterSummary, localResourceReports, remoteResourceReports, logger)
	if err != nil {
		return err
	}
	remoteResourceReports = append(remoteResourceReports, undeployed...)

	err = handleWatchers(ctx, clusterSummary, localResourceReports, featureHandler)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, isPullMode, remoteResourceReports,
		featureHandler.id)
	if err != nil {
		return err
	}

	if isPullMode {
		setters := prepareSetters(clusterSummary, libsveltosv1beta1.FeatureResources, profileRef, configurationHash)

		err = pullmode.CommitStagedResourcesForDeployment(ctx, c,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources),
			logger, setters...)
		if err != nil {
			return err
		}

		// For Sveltos in pull mode, health checks are run by agent. Also the agent sees there is an empty ResouceSummary
		// so it knows it has to track resources it deployed
		return deployError
	}

	// This remaining section is only for SveltosCluster in NON pull mode
	err = handleResourceSummaryDeployment(ctx, clusterSummary, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Spec.ClusterType, isPullMode, remoteDeployed, logger)
	if err != nil {
		return err
	}

	return clusterops.ValidateHealthPolicies(ctx, remoteRestConfig, clusterSummary.Spec.ClusterProfileSpec.ValidateHealths,
		libsveltosv1beta1.FeatureResources, logger)
}

func cleanStaleResources(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
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
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType, isPullMode, startInMgmtCluster bool,
	logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err := deployDriftDetectionManagerInCluster(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), clusterType,
			isPullMode, startInMgmtCluster, logger)
		if err != nil {
			return err
		}

		// If SveltosCLuster is in pull mode, do nothing. A knob will notify agent about DriftDetection being set
		// and agent will manage the ResourceSummary life cycle in the managed cluster

		// Since we are updating resources to watch for drift, remove resource section in ResourceSummary to eliminate
		// un-needed reconciliation (Sveltos is updating those resources so we don't want drift-detection to think
		// a configuration drift is happening)
		err = handleResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
			clusterType, isPullMode, []libsveltosv1beta1.Resource{}, logger)
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
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType, pullMode bool,
	remoteDeployed []libsveltosv1beta1.Resource, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// If SveltosCLuster is in pull mode, do nothing. A knob will notify agent about DriftDetection being set
		// and agent will manage the ResourceSummary life cycle in the managed cluster
		if pullMode {
			return nil
		}

		// deploy ResourceSummary
		err := deployResourceSummary(ctx, clusterNamespace, clusterName, clusterSummary, clusterType,
			remoteDeployed, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func handleWatchers(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	localResourceReports []libsveltosv1beta1.ResourceReport, featureHandler feature) error {

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
	resourceReports []libsveltosv1beta1.ResourceReport, logger logr.Logger,
) ([]libsveltosv1beta1.ResourceReport, error) {

	// This is a SveltosCluster in pull mode
	if destRestConfig == nil {
		return nil, nil
	}

	currentPolicies := make(map[string]libsveltosv1beta1.Resource, 0)
	for i := range resourceReports {
		key := deployer.GetPolicyInfo(&resourceReports[i].Resource)
		currentPolicies[key] = resourceReports[i].Resource
	}

	undeployed, err := undeployStaleResources(ctx, isMgmtCluster, destRestConfig, destClient, libsveltosv1beta1.FeatureResources,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, libsveltosv1beta1.FeatureResources),
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

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	// Undeploy from management cluster
	_, localUndeployErr := undeployStaleResources(ctx, true, getManagementClusterConfig(), c, libsveltosv1beta1.FeatureResources,
		clusterSummary, getDeployedGroupVersionKinds(clusterSummary, libsveltosv1beta1.FeatureResources),
		map[string]libsveltosv1beta1.Resource{}, logger)

	if isPullMode {
		err = pullModeUndeployResources(ctx, c, clusterSummary, libsveltosv1beta1.FeatureResources, logger)
		combinedErr := errors.Join(localUndeployErr, err)
		if combinedErr != nil {
			return combinedErr
		}
	} else {
		// Only resources previously deployed by ClusterSummary are removed here. Even if profile is created by serviceAccount
		// use cluster-admin account to do the removal
		remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
			"", "", clusterSummary.Spec.ClusterType, logger)
		if err != nil {
			return err
		}

		cacheMgr := clustercache.GetManager()
		remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
			"", "", clusterSummary.Spec.ClusterType, logger)
		if err != nil {
			return err
		}

		var resourceReports []libsveltosv1beta1.ResourceReport

		// Undeploy from managed cluster
		resourceReports, err = undeployStaleResources(ctx, false, remoteRestConfig, remoteClient,
			libsveltosv1beta1.FeatureResources, clusterSummary,
			getDeployedGroupVersionKinds(clusterSummary, libsveltosv1beta1.FeatureResources),
			map[string]libsveltosv1beta1.Resource{}, logger)
		combinedErr := errors.Join(localUndeployErr, err)
		if combinedErr != nil {
			return combinedErr
		}

		profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
		if err != nil {
			return err
		}

		err = updateReloaderWithDeployedResources(ctx, clusterSummary, profileRef, libsveltosv1beta1.FeatureResources,
			nil, true, logger)
		if err != nil {
			return err
		}

		isDrynRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		err = clusterops.UpdateClusterConfiguration(ctx, c, isDrynRun, true, clusterNamespace, clusterName,
			clusterType, profileRef, libsveltosv1beta1.FeatureResources, []libsveltosv1beta1.Resource{}, nil)
		if err != nil {
			return err
		}

		err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, isPullMode,
			resourceReports, libsveltosv1beta1.FeatureResources)
		if err != nil {
			return err
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return &configv1beta1.DryRunReconciliationError{}
	}

	manager := getManager()
	manager.stopStaleWatchForMgmtResource(nil, clusterSummary, libsveltosv1beta1.FeatureResources)

	return nil
}

func pullModeUndeployResources(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	fID libsveltosv1beta1.FeatureID, logger logr.Logger) error {

	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return err
	}

	setters := prepareSetters(clusterSummary, fID, profileRef, nil)

	// discard all previous staged resources. This will instruct agent to undeploy
	err = pullmode.RemoveDeployedResources(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name,
		string(fID), logger, setters...)
	if err != nil {
		return err
	}

	// Get result. If agent has undeployed eveything status is set to FeatureStatusRemoved
	status, err := pullmode.GetRemoveStatus(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name,
		string(fID), logger)
	if err != nil {
		return err
	}

	if status.DeploymentStatus == nil || *status.DeploymentStatus != libsveltosv1beta1.FeatureStatusRemoved {
		msg := "resources are still being removed."
		if status.FailureMessage != nil {
			msg += *status.FailureMessage
		}
		return errors.New(msg)
	}

	return nil
}

// resourcesHash returns the hash of all the ClusterSummary referenced ResourceRefs.
func resourcesHash(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) ([]byte, error) {

	clusterProfileSpecHash, err := getClusterProfileSpecHash(ctx, clusterSummary)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	var config string
	config += string(clusterProfileSpecHash)

	referencedObjects := make([]corev1.ObjectReference, len(clusterSummary.Spec.ClusterProfileSpec.PolicyRefs))
	for i := range clusterSummary.Spec.ClusterProfileSpec.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i]
		namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, reference.Namespace,
			clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate namespace for %s %s/%s: %v",
				reference.Kind, reference.Namespace, reference.Name, err))
			return nil, err
		}

		name, err := libsveltostemplate.GetReferenceResourceName(ctx, c, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, reference.Name, clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s %s/%s: %v",
				reference.Kind, reference.Namespace, reference.Name, err))
			return nil, err
		}

		referencedObjects[i] = corev1.ObjectReference{
			Kind:      clusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Kind,
			Namespace: namespace,
			Name:      name,
		}
	}

	sort.Sort(SortedCorev1ObjectReference(referencedObjects))

	for i := range referencedObjects {
		reference := &referencedObjects[i]
		if reference.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			configmap := &corev1.ConfigMap{}
			err = c.Get(ctx, types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configmap)
			if err == nil {
				config += getConfigMapHash(configmap)
			}
		} else if reference.Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
			secret := &corev1.Secret{}
			err = c.Get(ctx, types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, secret)
			if err == nil {
				config += getSecretHash(secret)
			}
		} else {
			var source client.Object
			source, err = getSource(ctx, c, reference.Namespace, reference.Name, reference.Kind)
			if err == nil && source != nil {
				s := source.(sourcev1.Source)
				if s.GetArtifact() != nil {
					config += s.GetArtifact().Revision
				}
				if source.GetAnnotations() != nil {
					config += getDataSectionHash(source.GetAnnotations())
				}
			}
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s %s/%s does not exist yet",
					reference.Kind, reference.Namespace, reference.Name))
				continue
			}
			logger.Error(err, fmt.Sprintf("failed to get %s %s/%s",
				reference.Kind, reference.Namespace, reference.Name))
			return nil, err
		}
	}

	for i := range clusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		h := &clusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]
		if h.FeatureID == libsveltosv1beta1.FeatureResources {
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
	clusterSummary *configv1beta1.ClusterSummary, pullMode bool, resourceReports []libsveltosv1beta1.ResourceReport,
	featureID libsveltosv1beta1.FeatureID) error {

	// In  pullmmode, agent will update the ClusterReports
	if pullMode {
		return nil
	}

	profileOwnerRef, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	profileRef := &corev1.ObjectReference{
		APIVersion: profileOwnerRef.APIVersion,
		Kind:       profileOwnerRef.Kind,
		Name:       profileOwnerRef.Name,
	}

	isDryRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
	return clusterops.UpdateClusterReportWithResourceReports(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, isDryRun, profileRef,
		resourceReports, featureID)
}

func deployResourceSummary(ctx context.Context, clusterNamespace, clusterName string,
	clusterSummary *configv1beta1.ClusterSummary, clusterType libsveltosv1beta1.ClusterType,
	deployed []libsveltosv1beta1.Resource, logger logr.Logger) error {

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

	clusterClient, err := getResourceSummaryClient(ctx, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	resourceSummaryNameInfo := getResourceSummaryNameInfo(clusterNamespace, clusterSummary.Name)

	return deployer.DeployResourceSummaryInCluster(ctx, clusterClient, resourceSummaryNameInfo, clusterNamespace,
		clusterName, clusterSummary.Name, clusterType, resources, nil, nil,
		clusterSummary.Spec.ClusterProfileSpec.DriftExclusions, logger)
}

// deployPolicyRefs deploys in a managed Cluster the policies contained in the Data section of each
// referenced ConfigMap/Secret
func deployPolicyRefs(ctx context.Context, c client.Client, remoteConfig *rest.Config, remoteClient client.Client,
	clusterSummary *configv1beta1.ClusterSummary, featureHandler feature, logger logr.Logger,
) (localReports, remoteReports []libsveltosv1beta1.ResourceReport, err error) {

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

	return deployReferencedObjects(ctx, c, remoteConfig, remoteClient, clusterSummary,
		objectsToDeployLocally, objectsToDeployRemotely, logger)
}
