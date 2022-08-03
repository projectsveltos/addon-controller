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
	b64 "encoding/base64"
	"fmt"
	"strings"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	kyvernoapi "github.com/kyverno/kyverno/api/kyverno/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/kyverno"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

func deployKyverno(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := c.Get(ctx, types.NamespacedName{Name: applicant}, clusterSummary); err != nil {
		return err
	}

	if !clusterSummary.DeletionTimestamp.IsZero() {
		logger.V(1).Info("ClusterSummary is marked for deletion. Nothing to do.")
		// if clusterSummary is marked for deletion, there is nothing to deploy
		return fmt.Errorf("clustersummary is marked for deletion")
	}

	// Get CAPI Cluster
	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, cluster); err != nil {
		return err
	}

	clusterClient, err := getKubernetesClient(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	// First verify if kyverno is installed, if not install it
	present, ready, err := isKyvernoReady(ctx, clusterClient, logger)
	if err != nil {
		logger.V(1).Error(err, "Failed to verify presence of kyverno deployment")
		return err
	}

	if !present {
		if err := deployKyvernoInWorklaodCluster(ctx, clusterClient,
			clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.Replicas, logger); err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("kyverno deployment is not ready yet")
	}

	clusterRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	currentKyvernos := make(map[string]bool, 0)
	if clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration != nil {
		for i := range clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs {
			reference := &clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs[i]
			configMap := &corev1.ConfigMap{}
			err := c.Get(ctx,
				types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configMap)
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.V(1).Info(fmt.Sprintf("configMap %s/%s does not exist yet",
						reference.Namespace, reference.Name))
					continue
				}
				return err
			}

			if err := deployKyvernoPolicy(ctx, clusterRestConfig, clusterClient,
				configMap, clusterSummary, currentKyvernos, logger); err != nil {
				return err
			}
		}
	}

	// Remove all policies/clusterPolicies previously deployed by this ClusterSummary and not referenced anymores
	if err = undeployStaleKyvernoResources(ctx, clusterClient, clusterSummary, currentKyvernos, logger); err != nil {
		return err
	}

	return nil
}

func unDeployKyverno(ctx context.Context, c client.Client,
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

	listOptions := []client.ListOption{
		client.MatchingLabels{clusterSummaryLabelName: clusterSummary.Name},
	}

	clusterPolicies := &kyvernoapi.ClusterPolicyList{}
	err = clusterClient.List(ctx, clusterPolicies, listOptions...)
	if err != nil {
		logger.Error(err, "failed to list kyverno clusterpolicies")
		return err
	}

	for i := range clusterPolicies.Items {
		cp := &clusterPolicies.Items[i]
		if err = clusterClient.Delete(ctx, cp); err != nil {
			return err
		}
	}

	policies := &kyvernoapi.PolicyList{}
	err = clusterClient.List(ctx, policies, listOptions...)
	if err != nil {
		logger.Error(err, "failed to list kyverno policies")
		return err
	}

	for i := range policies.Items {
		p := &policies.Items[i]
		if err = clusterClient.Delete(ctx, p); err != nil {
			return err
		}
	}

	return nil
}

// kyvernoHash returns the hash of all the Kyverno referenced configmaps.
func kyvernoHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {
	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration == nil {
		return h.Sum(nil), nil
	}
	for i := range clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs[i]
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

// isKyvernoReady checks whether kyverno deployment is present and ready
func isKyvernoReady(ctx context.Context, c client.Client, logger logr.Logger) (present, ready bool, err error) {
	logger = logger.WithValues("kyvernonamespace", kyverno.Namespace, "kyvernodeployment", kyverno.Deployment)
	present = false
	ready = false
	depl := &appsv1.Deployment{}
	err = c.Get(ctx, types.NamespacedName{Namespace: kyverno.Namespace, Name: kyverno.Deployment}, depl)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(5).Info("Kyverno deployment not found")
			err = nil
			return
		}
		return
	}

	present = true

	if depl.Status.ReadyReplicas != *depl.Spec.Replicas {
		logger.V(5).Info("Not all replicas are ready for Kyverno deployment")
		return
	}

	ready = true
	return
}

func changeReplicas(content string, r uint) string {
	replicas := "replicas: 1"

	index := strings.Index(content, replicas)
	if index == -1 {
		panic(fmt.Errorf("did not find proper replicas set"))
	}

	newReplicas := fmt.Sprintf("replicas: %d", r)
	content = strings.ReplaceAll(content, replicas, newReplicas)
	return content
}

func deployKyvernoInWorklaodCluster(ctx context.Context, c client.Client, replicas uint, logger logr.Logger) error {
	separator := "---\n"
	kyvernoYAML := changeReplicas(string(kyverno.KyvernoYAML), replicas)
	elements := strings.Split(kyvernoYAML, separator)
	for i := range elements {
		element, err := getUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(1).Error(err, "failed to convert to unstructured")
			return err
		}
		err = c.Create(ctx, element)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			logger.V(1).Error(err, "failed to post object")
			return fmt.Errorf("error creating %s %s: %w", element.GetKind(), element.GetName(), err)
		}
	}

	return nil
}

func deployKyvernoPolicy(ctx context.Context, config *rest.Config, c client.Client,
	configMap *corev1.ConfigMap, clusterSummary *configv1alpha1.ClusterSummary,
	currentKyvernos map[string]bool,
	logger logr.Logger) error {
	l := logger.WithValues("configMap", fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name))
	for k := range configMap.Data {
		// Base64 decode policy
		policyDecoded, err := b64.StdEncoding.DecodeString(configMap.Data[k])
		if err != nil {
			l.Error(err, "failed to base64 decode policy from Data")
			return err
		}

		policy, err := getUnstructured(policyDecoded)
		if err != nil {
			l.Error(err, fmt.Sprintf("failed to get policy from Data %.75s", string(policyDecoded)))
			return err
		}

		if policy == nil {
			l.Error(err, fmt.Sprintf("failed to get policy from Data %.75s", string(policyDecoded)))
			return fmt.Errorf("failed to get policy from Data %.75s", string(policyDecoded))
		}

		addClusterSummaryLabel(policy, clusterSummary.Name)
		name := getKyvernoPolicyName(getUnstructuredName(policy), clusterSummary)
		policy.SetName(name)
		currentKyvernos[name] = true

		// If policy is namespaced, create namespace if not already existing
		if err := createNamespace(ctx, c, policy.GetNamespace()); err != nil {
			return err
		}

		// If policy already exists, just get current version and update it by overridding
		// all metadata and spec.
		// If policy does not exist already, create it
		dr, err := getDynamicResourceInterface(config, policyDecoded)
		if err != nil {
			return err
		}
		if err = preprareObjectForUpdate(ctx, dr, policy); err != nil {
			return err
		}

		if policy.GetResourceVersion() != "" {
			err = c.Update(ctx, policy)
		} else {
			err = c.Create(ctx, policy)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func undeployStaleKyvernoResources(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	currentKyvernos map[string]bool, logger logr.Logger) error {
	listOptions := []client.ListOption{
		client.MatchingLabels{clusterSummaryLabelName: clusterSummary.Name},
	}

	policies := &kyvernoapi.PolicyList{}
	err := c.List(ctx, policies, listOptions...)
	if err != nil {
		logger.Error(err, "failed to list policies")
		return err
	}

	for i := range policies.Items {
		if err := deleteIfNotExistant(ctx, &policies.Items[i], c, currentKyvernos); err != nil {
			return err
		}
	}

	clusterPolicies := &kyvernoapi.ClusterPolicyList{}
	err = c.List(ctx, clusterPolicies, listOptions...)
	if err != nil {
		logger.Error(err, "failed to list clusterPolicies")
		return err
	}

	for i := range clusterPolicies.Items {
		if err := deleteIfNotExistant(ctx, &clusterPolicies.Items[i], c, currentKyvernos); err != nil {
			return err
		}
	}

	return nil
}

func getKyvernoPolicyName(policyName string, clusterSummary *configv1alpha1.ClusterSummary) string {
	return clusterSummary.Status.KyvernoPolicyPrefix + "-" + policyName
}

func deleteIfNotExistant(ctx context.Context, policy client.Object, c client.Client, currentKyvernos map[string]bool) error {
	if _, ok := currentKyvernos[policy.GetName()]; !ok {
		if err := c.Delete(ctx, policy); err != nil {
			return err
		}
	}

	return nil
}
