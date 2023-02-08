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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/pkg/scope"
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

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName))
	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger = logger.WithValues("admin", getClusterSummaryAdmin(clusterSummary))

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// Deploy drift detection manager first. Have manager up by the time resourcesummary is created
		err = deployDriftDetectionManagerInCluster(ctx, c, clusterNamespace, clusterName, applicant,
			clusterType, logger)
		if err != nil {
			return err
		}
	}

	remoteRestConfig, err := getKubernetesRestConfig(ctx, c, clusterNamespace, clusterName, getClusterSummaryAdmin(clusterSummary),
		clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	currentPolicies := make(map[string]configv1alpha1.Resource, 0)

	var resourceReports []configv1alpha1.ResourceReport
	resourceReports, err = deployReferencedObjects(ctx, c, remoteRestConfig, clusterSummary, featureHandler, logger)
	if err != nil {
		return err
	}

	clusterProfileOwnerRef, err := configv1alpha1.GetClusterProfileOwnerReference(clusterSummary)
	if err != nil {
		return err
	}

	deployed := make([]configv1alpha1.Resource, 0)
	for i := range resourceReports {
		// If we are here there are no conflicts (and error would have been returned by deployReferencedObjects)
		deployed = append(deployed, resourceReports[i].Resource)
	}
	err = updateClusterConfiguration(ctx, c, clusterSummary, clusterProfileOwnerRef, featureHandler.id, deployed, nil)
	if err != nil {
		return err
	}

	for i := range resourceReports {
		key := getPolicyInfo(&resourceReports[i].Resource)
		currentPolicies[key] = resourceReports[i].Resource
	}

	var undeployed []configv1alpha1.ResourceReport
	undeployed, err = undeployStaleResources(ctx, remoteRestConfig, c, remoteClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureResources), currentPolicies, logger)
	if err != nil {
		return err
	}
	resourceReports = append(resourceReports, undeployed...)

	err = updateClusterReportWithResourceReports(ctx, c, clusterSummary, resourceReports)
	if err != nil {
		return err
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {
		// deploy ResourceSummary
		err = deployResourceSummary(ctx, c, clusterNamespace, clusterName,
			applicant, clusterType, deployed, logger)
		if err != nil {
			return err
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return &configv1alpha1.DryRunReconciliationError{}
	}
	return nil
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

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName))
	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger = logger.WithValues("admin", getClusterSummaryAdmin(clusterSummary))

	remoteClient, err := getKubernetesClient(ctx, c, clusterNamespace, clusterName, getClusterSummaryAdmin(clusterSummary),
		clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	remoteRestConfig, err := getKubernetesRestConfig(ctx, c, clusterNamespace, clusterName, getClusterSummaryAdmin(clusterSummary),
		clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	var resourceReports []configv1alpha1.ResourceReport
	resourceReports, err = undeployStaleResources(ctx, remoteRestConfig, c, remoteClient, clusterSummary,
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

func getResourceRefs(clusterSummary *configv1alpha1.ClusterSummary) []libsveltosv1alpha1.PolicyRef {
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
