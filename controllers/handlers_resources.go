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
	"fmt"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	"github.com/projectsveltos/addon-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func deployResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1alpha1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	featureHandler := getHandlersForFeature(configv1alpha1.FeatureResources)

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndClusterClient(ctx, clusterNamespace, applicant, c, logger)
	if err != nil {
		return err
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName)).
		WithValues("clusterSummary", clusterSummary.Name).WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	err = handleDriftDetectionManagerDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
		clusterType, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	var localResourceReports []configv1alpha1.ResourceReport
	var remoteResourceReports []configv1alpha1.ResourceReport
	localResourceReports, remoteResourceReports, err = deployReferencedObjects(ctx, c, remoteRestConfig,
		clusterSummary, featureHandler, logger)
	if err != nil {
		return err
	}

	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	err = updateDeployedGroupVersionKind(ctx, clusterSummary, localResourceReports, remoteResourceReports, logger)
	if err != nil {
		return err
	}

	// If we are here there are no conflicts (and error would have been returned by deployReferencedObjects)
	remoteDeployed := make([]configv1alpha1.Resource, 0)
	for i := range remoteResourceReports {
		remoteDeployed = append(remoteDeployed, remoteResourceReports[i].Resource)
	}

	// TODO: track resource deployed in the management cluster
	err = updateClusterConfiguration(ctx, c, clusterSummary, clusterProfileOwnerRef, featureHandler.id, remoteDeployed, nil)
	if err != nil {
		return err
	}

	// Clean stale resources in the management cluster
	_, err = cleanResources(ctx, getManagementClusterConfig(), c, clusterSummary, localResourceReports, logger)
	if err != nil {
		return err
	}

	// Clean stale resources in the remote cluster
	var undeployed []configv1alpha1.ResourceReport
	undeployed, err = cleanResources(ctx, remoteRestConfig, remoteClient, clusterSummary, remoteResourceReports, logger)
	if err != nil {
		return err
	}
	remoteResourceReports = append(remoteResourceReports, undeployed...)

	err = handleWatchers(ctx, clusterSummary, localResourceReports)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, remoteResourceReports)
	if err != nil {
		return err
	}

	err = handleResourceSummaryDeployment(ctx, clusterSummary, clusterNamespace, clusterName,
		clusterType, remoteDeployed, logger)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return &configv1alpha1.DryRunReconciliationError{}
	}
	return nil
}

// handleDriftDetectionManagerDeployment deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// drift-detection-manager in the managed clyuster
func handleDriftDetectionManagerDeployment(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err := deployDriftDetectionManagerInCluster(ctx, getManagementClusterClient(), clusterNamespace,
			clusterName, clusterSummary.Name, clusterType, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

// handleResourceSummaryDeployment deploys, if sync mode is SyncModeContinuousWithDriftDetection,
// ResourceSummary in the managed cluster
func handleResourceSummaryDeployment(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	remoteDeployed []configv1alpha1.Resource, logger logr.Logger) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// deploy ResourceSummary
		err := deployResourceSummary(ctx, getManagementClusterClient(), clusterNamespace, clusterName,
			clusterSummary.Name, clusterType, remoteDeployed, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func handleWatchers(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	localResourceReports []configv1alpha1.ResourceReport) error {

	currentGVKs := make(map[schema.GroupVersionKind]bool)
	manager := getManager()
	for i := range localResourceReports {
		gvk := schema.GroupVersionKind{
			Group:   localResourceReports[i].Resource.Group,
			Kind:    localResourceReports[i].Resource.Kind,
			Version: localResourceReports[i].Resource.Version,
		}

		if err := manager.watchGVK(ctx, gvk, clusterSummary); err != nil {
			return err
		}

		currentGVKs[gvk] = true
	}

	manager.stopStaleWatch(currentGVKs, clusterSummary)
	return nil
}

func cleanResources(ctx context.Context, destRestConfig *rest.Config, destClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, resourceReports []configv1alpha1.ResourceReport,
	logger logr.Logger) ([]configv1alpha1.ResourceReport, error) {

	currentPolicies := make(map[string]configv1alpha1.Resource, 0)
	for i := range resourceReports {
		key := getPolicyInfo(&resourceReports[i].Resource)
		currentPolicies[key] = resourceReports[i].Resource
	}
	undeployed, err := undeployStaleResources(ctx, destRestConfig, destClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureResources), currentPolicies, logger)
	if err != nil {
		return nil, err
	}
	return undeployed, nil
}

func undeployResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	clusterType libsveltosv1alpha1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Get ClusterSummary that requested this
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

	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	var resourceReports []configv1alpha1.ResourceReport

	// Undeploy from management cluster
	_, err = undeployStaleResources(ctx, getManagementClusterConfig(), c, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureResources),
		map[string]configv1alpha1.Resource{}, logger)
	if err != nil {
		return err
	}

	// Undeploy from managed cluster
	resourceReports, err = undeployStaleResources(ctx, remoteRestConfig, remoteClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureResources),
		map[string]configv1alpha1.Resource{}, logger)
	if err != nil {
		return err
	}

	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	err = updateClusterConfiguration(ctx, c, clusterSummary, clusterProfileOwnerRef,
		configv1alpha1.FeatureResources, []configv1alpha1.Resource{}, nil)
	if err != nil {
		return err
	}

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, resourceReports)
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

// resourcesHash returns the hash of all the ClusterSummary referenced ResourceRefs.
func resourcesHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	for i := range clusterSummary.Spec.ClusterProfileSpec.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i]
		namespace := getReferenceResourceNamespace(clusterSummaryScope.Namespace(), reference.Namespace)
		var err error
		if reference.Kind == string(libsveltosv1alpha1.ConfigMapReferencedResourceKind) {
			configmap := &corev1.ConfigMap{}
			err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: reference.Name}, configmap)
			if err == nil {
				config += render.AsCode(configmap.Data)
			}
		} else {
			secret := &corev1.Secret{}
			err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: reference.Name}, secret)
			if err == nil {
				config += render.AsCode(secret.Data)
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

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getResourceRefs(clusterSummary *configv1alpha1.ClusterSummary) []configv1alpha1.PolicyRef {
	return clusterSummary.Spec.ClusterProfileSpec.PolicyRefs
}

// updateClusterReportWithResourceReports updates ClusterReport Status with ResourceReports.
// This is no-op unless mode is DryRun.
func updateClusterReportWithResourceReports(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	resourceReports []configv1alpha1.ResourceReport) error {

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

		clusterReport.Status.ResourceReports = resourceReports
		return c.Status().Update(ctx, clusterReport)
	})
	return err
}

func deployResourceSummary(ctx context.Context, c client.Client,
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
		clusterType, resources, nil, logger)
}

func updateDeployedGroupVersionKind(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	localResourceReports, remoteResourceReports []configv1alpha1.ResourceReport, logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("update status with deployed GroupVersionKinds")
	reports := localResourceReports
	reports = append(reports, remoteResourceReports...)

	gvks := make([]schema.GroupVersionKind, 0)
	gvkMap := make(map[schema.GroupVersionKind]bool)
	for i := range reports {
		gvk := schema.GroupVersionKind{
			Group:   reports[i].Resource.Group,
			Version: reports[i].Resource.Version,
			Kind:    reports[i].Resource.Kind,
		}
		if _, ok := gvkMap[gvk]; !ok {
			gvks = append(gvks, gvk)
			gvkMap[gvk] = true
		}
	}

	// update status with list of GroupVersionKinds deployed in a Managed and Management Cluster
	setDeployedGroupVersionKind(clusterSummary, gvks)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return getManagementClusterClient().Status().Update(ctx, clusterSummary)
	})
	return err
}

// setDeployedGroupVersionKind sets the list of deployed GroupVersionKinds
func setDeployedGroupVersionKind(clusterSummary *configv1alpha1.ClusterSummary, gvks []schema.GroupVersionKind) {
	for i := range clusterSummary.Status.FeatureSummaries {
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == configv1alpha1.FeatureResources {
			setDeployedGroupVersionKindField(&clusterSummary.Status.FeatureSummaries[i], gvks)
			return
		}
	}

	if clusterSummary.Status.FeatureSummaries == nil {
		clusterSummary.Status.FeatureSummaries = make([]configv1alpha1.FeatureSummary, 0)
	}

	clusterSummary.Status.FeatureSummaries = append(
		clusterSummary.Status.FeatureSummaries,
		configv1alpha1.FeatureSummary{
			FeatureID: configv1alpha1.FeatureResources,
		},
	)

	for i := range clusterSummary.Status.FeatureSummaries {
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == configv1alpha1.FeatureResources {
			setDeployedGroupVersionKindField(&clusterSummary.Status.FeatureSummaries[i], gvks)
			return
		}
	}
}

func setDeployedGroupVersionKindField(fs *configv1alpha1.FeatureSummary, gvks []schema.GroupVersionKind) {
	tmp := make([]string, 0)

	// Preserve the order
	current := make(map[string]bool)
	for _, k := range fs.DeployedGroupVersionKind {
		current[k] = true
		tmp = append(tmp, k)
	}

	for i := range gvks {
		key := fmt.Sprintf("%s.%s.%s", gvks[i].Kind, gvks[i].Version, gvks[i].Group)
		if _, ok := current[key]; !ok {
			current[key] = true
			tmp = append(tmp, key)
		}
	}

	fs.DeployedGroupVersionKind = tmp
}
