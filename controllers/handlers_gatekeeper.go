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

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/gatekeeper"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

func deployGatekeeper(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, clusterClient, err := getClusterSummaryAndCAPIClusterClient(ctx, applicant, c, logger)
	if err != nil {
		return err
	}

	// First verify if gatekeeper is installed, if not install it
	present, ready, err := isGatekeeperReady(ctx, clusterClient, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "Failed to verify presence of gatekeeper deployments")
		return err
	}

	if !present {
		err = deployGatekeeperInWorklaodCluster(ctx, clusterClient, logger)
		if err != nil {
			return err
		}

		err = applyAuditOptions(ctx, clusterClient, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("gatekeeper deployments are not ready yet")
	}

	clusterRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	currentPolicies := make(map[string]bool, 0)
	if clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration != nil {
		var configMaps []corev1.ConfigMap

		// do not use getGatekeeperRefs.
		// When deploying, Constraints need to be deployed first. Then policies can. Otherwise
		// needed CRD won't be found.
		// getGatekeeperRefs returns policies first, constraints later. Cause when removing stale
		// objects we need to first remove policies, then constraints in order to avoid CRD not found.
		var refs []corev1.ObjectReference
		if clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration != nil {
			refs = clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs
		}

		configMaps, err = collectConfigMaps(ctx, c, refs, logger)
		if err != nil {
			return err
		}

		configMaps, err = sortConfigMapByConstraintsFirst(configMaps, logger)
		if err != nil {
			return err
		}

		err = updateGatekeeperSortedPolicies(ctx, c, clusterSummary, configMaps)
		if err != nil {
			return err
		}

		var deployed []string
		deployed, err = deployConfigMaps(ctx, configMaps, clusterSummary, clusterClient, clusterRestConfig, logger)
		if err != nil {
			return err
		}

		for _, k := range deployed {
			currentPolicies[k] = true
		}
	}

	err = undeployStaleResources(ctx, clusterRestConfig, clusterClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureGatekeeper), currentPolicies)
	if err != nil {
		return err
	}

	return nil
}

func unDeployGatekeeper(ctx context.Context, c client.Client,
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
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureGatekeeper), map[string]bool{})
	if err != nil {
		return err
	}

	return nil
}

// gatekeeperHash returns the hash of all the Gatekeeper referenced configmaps.
func gatekeeperHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration == nil {
		return h.Sum(nil), nil
	}

	for i := range clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs[i]
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

func getGatekeeperRefs(clusterSummary *configv1alpha1.ClusterSummary) []corev1.ObjectReference {
	if clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration != nil {
		if clusterSummary.Status.GatekeeperSortedPolicies != nil {
			return clusterSummary.Status.GatekeeperSortedPolicies
		} else {
			return clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs
		}
	}
	return nil
}

// isGatekeeperReady checks whether gatekeeper deployments are present and ready
// Return present=false, ready=false if at least one deployment is not present.
// Return present=true, ready=false if at least one deployment is not ready.
// Return present=true, ready=true if all deployments are present and ready
func isGatekeeperReady(ctx context.Context, c client.Client, logger logr.Logger) (present, ready bool, err error) {
	present = true
	ready = true

	for i := range gatekeeper.Deployments {
		var tmpPresent, tmpReady bool
		tmpPresent, tmpReady, err = isDeploymentReady(ctx, c, gatekeeper.Namespace, gatekeeper.Deployments[i], logger)
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

func deployGatekeeperInWorklaodCluster(ctx context.Context, c client.Client,
	logger logr.Logger) error {

	if err := createNamespace(ctx, c, gatekeeper.Namespace); err != nil {
		return err
	}

	return deployDoc(ctx, c, gatekeeper.GatekeeperYAML, logger)
}

func applyAuditOptions(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	auditDeployment := &appsv1.Deployment{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: gatekeeper.Namespace, Name: gatekeeper.AuditDeployment},
		auditDeployment)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get gatekeeper audit deployment")
		return err
	}

	containers := auditDeployment.Spec.Template.Spec.Containers
	containers[0].Args = append(containers[0].Args,
		fmt.Sprintf("--audit-chunk-size=%d", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditChunkSize),
		fmt.Sprintf("--audit-interval=%d", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditInterval),
		fmt.Sprintf("--audit-from-cache=%t", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditFromCache))

	auditDeployment.Spec.Template.Spec.Containers = containers

	return c.Update(ctx, auditDeployment)
}

// sortConfigMapByConstraintsFirst sort configMap slice by putting ConfigMaps with ConstraintTemplate first
func sortConfigMapByConstraintsFirst(configMaps []corev1.ConfigMap, logger logr.Logger) ([]corev1.ConfigMap, error) {
	results := make([]corev1.ConfigMap, len(configMaps))

	startIndex := 0
	endIndex := len(configMaps) - 1

	for i := range configMaps {
		ok, err := hasContraintTemplates(&configMaps[i], logger)
		if err != nil {
			return nil, err
		}

		if ok {
			results[startIndex] = configMaps[i]
			startIndex++
		} else {
			results[endIndex] = configMaps[i]
			endIndex--
		}
	}

	return results, nil
}

func hasContraintTemplates(cm *corev1.ConfigMap, logger logr.Logger) (bool, error) {
	policies, err := collectContentOfConfigMap(cm, logger)
	if err != nil {
		return false, err
	}

	for i := range policies {
		if policies[i].GetKind() == "ConstraintTemplate" {
			return true, nil
		}
	}

	return false, nil
}

func updateGatekeeperSortedPolicies(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, configMaps []corev1.ConfigMap) error {

	length := len(configMaps)
	clusterSummary.Status.GatekeeperSortedPolicies = make([]corev1.ObjectReference, length)
	for i := range configMaps {
		clusterSummary.Status.GatekeeperSortedPolicies[i] = corev1.ObjectReference{
			Kind:       configMaps[length-i-1].GetObjectKind().GroupVersionKind().Kind,
			Namespace:  configMaps[length-i-1].GetNamespace(),
			Name:       configMaps[length-i-1].GetName(),
			UID:        configMaps[length-i-1].GetUID(),
			APIVersion: configMaps[length-i-1].GetResourceVersion(),
		}
	}

	return c.Status().Update(ctx, clusterSummary)
}
