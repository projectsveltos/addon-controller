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
	"fmt"
	"strings"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/contour"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

func deployContour(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndCAPIClusterClient(ctx, applicant, c, logger)
	if err != nil {
		return err
	}

	if shouldInstallContourGateway(clusterSummary) {
		err = deployContourGateway(ctx, remoteClient, logger)
		if err != nil {
			return err
		}
	}

	if shouldInstallContour(clusterSummary) {
		err = deployRegularContour(ctx, remoteClient, logger)
		if err != nil {
			return err
		}
	}

	remoteRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	currentPolicies := make(map[string]configv1alpha1.Resource, 0)
	refs := getContourRefs(clusterSummary)

	var configMaps []corev1.ConfigMap
	configMaps, err = collectConfigMaps(ctx, c, refs, logger)
	if err != nil {
		return err
	}

	var deployed []configv1alpha1.Resource
	deployed, err = deployConfigMaps(ctx, c, remoteRestConfig, configv1alpha1.FeatureContour,
		configMaps, clusterSummary, logger)
	if err != nil {
		return err
	}

	for i := range deployed {
		key := getPolicyInfo(&deployed[i])
		currentPolicies[key] = deployed[i]
	}

	err = undeployStaleResources(ctx, remoteRestConfig, remoteClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureContour), currentPolicies)
	if err != nil {
		return err
	}

	return nil
}

func unDeployContour(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := c.Get(ctx, types.NamespacedName{Name: applicant}, clusterSummary); err != nil {
		return err
	}

	// Get CAPI Cluster
	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info(fmt.Sprintf("Cluster %s/%s not found. Nothing to cleanup", clusterNamespace, clusterName))
			return nil
		}
		return err
	}

	clusterClient, err := getKubernetesClient(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	clusterRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	err = undeployStaleResources(ctx, clusterRestConfig, clusterClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureContour), map[string]configv1alpha1.Resource{})
	if err != nil {
		return err
	}

	return nil
}

// contourHash returns the hash of all the Contour referenced configmaps.
func contourHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration == nil {
		return h.Sum(nil), nil
	}
	for i := range clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.PolicyRefs[i]
		configmap := &corev1.ConfigMap{}
		err := c.Get(ctx, types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configmap)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("configMap %s/%s does not exist yet",
					reference.Namespace, reference.Name))
				continue
			}
			logger.Error(err, fmt.Sprintf("failed to get configMap %s/%s",
				reference.Namespace, reference.Name))
			return nil, err
		}

		config += render.AsCode(configmap.Data)
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getContourRefs(clusterSummary *configv1alpha1.ClusterSummary) []corev1.ObjectReference {
	if clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration != nil {
		return clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.PolicyRefs
	}
	return nil
}

// shouldInstallContourGateway returns true if Contour Gateway provisioner needs to be installed
func shouldInstallContourGateway(clusterSummary *configv1alpha1.ClusterSummary) bool {
	// Unless kube-prometheus stack is deployed, prometheus operator needs to be installed
	return clusterSummary != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.InstallationMode ==
			configv1alpha1.ContourInstallationModeGateway
}

// shouldInstallContour returns true if regular contour needs to be installed
func shouldInstallContour(clusterSummary *configv1alpha1.ClusterSummary) bool {
	return clusterSummary != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.InstallationMode ==
			configv1alpha1.ContourInstallationModeContour
}

// isContourGatewayReady checks whether contour, in gateway mode, deployments are present and ready
// Return present=false, ready=false if at least one deployment is not present.
// Return present=true, ready=false if at least one deployment is not ready.
// Return present=true, ready=true if all deployments are present and ready
func isContourGatewayReady(ctx context.Context, c client.Client, logger logr.Logger) (present, ready bool, err error) {
	const expectedInfoLength = 2
	present = true
	ready = true

	for i := range contour.ContourGatewayDeployments {
		deploymentInfo := strings.Split(contour.ContourGatewayDeployments[i], "/")
		if len(deploymentInfo) != expectedInfoLength {
			err = fmt.Errorf("deployment %s has incorrect info", contour.ContourGatewayDeployments[i])
			return
		}
		var tmpPresent, tmpReady bool
		tmpPresent, tmpReady, err = isDeploymentReady(ctx, c, deploymentInfo[0], deploymentInfo[1], logger)
		if err != nil {
			return
		}
		if !tmpPresent {
			present = false
		}
		if !tmpReady {
			ready = false
		}
	}

	return
}

// isContourReady checks whether contour, in regular mode, deployments are present and ready
// Return present=false, ready=false if at least one deployment is not present.
// Return present=true, ready=false if at least one deployment is not ready.
// Return present=true, ready=true if all deployments are present and ready
func isContourReady(ctx context.Context, c client.Client, logger logr.Logger) (present, ready bool, err error) {
	const expectedInfoLength = 2
	present = true
	ready = true

	for i := range contour.ContourDeployments {
		deploymentInfo := strings.Split(contour.ContourDeployments[i], "/")
		if len(deploymentInfo) != expectedInfoLength {
			err = fmt.Errorf("deployment %s has incorrect info", contour.ContourDeployments[i])
			return
		}
		var tmpPresent, tmpReady bool
		tmpPresent, tmpReady, err = isDeploymentReady(ctx, c, deploymentInfo[0], deploymentInfo[1], logger)
		if err != nil {
			return
		}
		if !tmpPresent {
			present = false
		}
		if !tmpReady {
			ready = false
		}
	}

	return
}

func deployContourGateway(ctx context.Context, c client.Client, logger logr.Logger) error {
	present, ready, err := isContourGatewayReady(ctx, c, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "Failed to verify presence of contour gateway provisioner deployment")
		return err
	}

	if !present {
		err = deployContourGatewayInWorklaodCluster(ctx, c, logger)
		if err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("contour gateway provisioner deployment is not ready yet")
	}

	return nil
}

func deployRegularContour(ctx context.Context, c client.Client, logger logr.Logger) error {
	present, ready, err := isContourReady(ctx, c, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "Failed to verify presence of contour deployment")
		return err
	}

	if !present {
		err = deployContourInWorklaodCluster(ctx, c, logger)
		if err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("contour deployment is not ready yet")
	}

	return nil
}

func deployContourGatewayInWorklaodCluster(ctx context.Context, c client.Client, logger logr.Logger) error {
	return deployDoc(ctx, c, contour.ContourGatewayYAML, logger)
}

func deployContourInWorklaodCluster(ctx context.Context, c client.Client, logger logr.Logger) error {
	return deployDoc(ctx, c, contour.ContourYAML, logger)
}
