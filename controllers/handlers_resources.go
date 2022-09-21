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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

func deployResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	featureHandler := getHandlersForFeature(configv1alpha1.FeatureResources)

	remoteRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndCAPIClusterClient(ctx, applicant, c, logger)
	if err != nil {
		return err
	}

	logger = logger.WithValues("clustersummary", clusterSummary.Name)

	currentPolicies := make(map[string]configv1alpha1.Resource, 0)
	refs := featureHandler.getRefs(clusterSummary)

	var referencedObjects []client.Object
	referencedObjects, err = collectReferencedObjects(ctx, c, refs, logger)
	if err != nil {
		return err
	}

	var deployed []configv1alpha1.Resource
	deployed, err = deployReferencedObjects(ctx, c, remoteRestConfig, configv1alpha1.FeatureResources,
		referencedObjects, clusterSummary, logger)
	if err != nil {
		return err
	}

	for i := range deployed {
		key := getPolicyInfo(&deployed[i])
		currentPolicies[key] = deployed[i]
	}

	err = undeployStaleResources(ctx, remoteRestConfig, remoteClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureResources), currentPolicies, logger)
	if err != nil {
		return err
	}
	return nil
}

func undeployResources(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := c.Get(ctx, types.NamespacedName{Name: applicant}, clusterSummary); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger = logger.WithValues("clustersummary", clusterSummary.Name)

	clusterClient, err := getKubernetesClient(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	clusterRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	err = undeployStaleResources(ctx, clusterRestConfig, clusterClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeatureResources),
		map[string]configv1alpha1.Resource{}, logger)
	if err != nil {
		return err
	}

	clusterFeatureOwnerRef, err := configv1alpha1.GetOwnerClusterFeatureName(clusterSummary)
	if err != nil {
		return err
	}

	err = updateClusterConfiguration(ctx, c, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterFeatureOwnerRef, configv1alpha1.FeatureResources, nil, nil)
	if err != nil {
		return err
	}
	return nil
}

// resourcesHash returns the hash of all the ClusterSummary referenced ResourceRefs.
func resourcesHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	for i := range clusterSummary.Spec.ClusterFeatureSpec.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.PolicyRefs[i]
		var err error
		if reference.Kind == string(configv1alpha1.ConfigMapReferencedResourceKind) {
			configmap := &corev1.ConfigMap{}
			err = c.Get(ctx, types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configmap)
			if err == nil {
				config += render.AsCode(configmap.Data)
			}
		} else {
			secret := &corev1.Secret{}
			err = c.Get(ctx, types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, secret)
			if err == nil {
				config += render.AsCode(secret.Data)
			}
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("%s %s/%s does not exist yet",
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
	return clusterSummary.Spec.ClusterFeatureSpec.PolicyRefs
}
