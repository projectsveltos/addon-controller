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
	b64 "encoding/base64"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
)

const (
	separator = "---\n"
)

func createNamespace(ctx context.Context, clusterClient client.Client, namespaceName string) error {
	if namespaceName == "" {
		return nil
	}

	currentNs := &corev1.Namespace{}
	if err := clusterClient.Get(ctx, client.ObjectKey{Name: namespaceName}, currentNs); err != nil {
		if apierrors.IsNotFound(err) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceName,
				},
			}
			return clusterClient.Create(ctx, ns)
		}
		return err
	}
	return nil
}

// deployContentOfConfigMap deploys policies contained in a ConfigMap.
// ConfigMap.Data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfConfigMap(ctx context.Context, config *rest.Config, c client.Client,
	configMap *corev1.ConfigMap, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) ([]string, error) {

	l := logger.WithValues("configMap", fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name))
	referencedPolicies, err := collectContentOfConfigMap(configMap, l)
	if err != nil {
		return nil, err
	}

	policies := make([]string, 0)
	for i := range referencedPolicies {
		policy := referencedPolicies[i]
		addLabel(policy, ClusterSummaryLabelName, clusterSummary.Name)
		name := getPolicyName(policy.GetName(), clusterSummary)
		policy.SetName(name)

		// If policy is namespaced, create namespace if not already existing
		err := createNamespace(ctx, c, policy.GetNamespace())
		if err != nil {
			return nil, err
		}

		// If policy already exists, just get current version and update it by overridding
		// all metadata and spec.
		// If policy does not exist already, create it
		dr, err := getDynamicResourceInterface(config, policy)
		if err != nil {
			return nil, err
		}
		err = preprareObjectForUpdate(ctx, dr, policy)
		if err != nil {
			return nil, err
		}

		if policy.GetResourceVersion() != "" {
			err = c.Update(ctx, policy)
		} else {
			err = c.Create(ctx, policy)
		}

		if err != nil {
			return nil, err
		}

		policies = append(policies, getPolicyInfo(policy))
	}

	return policies, nil
}

// collectContentOfConfigMap collect policies contained in a ConfigMap.
// ConfigMap.Data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice of *unstructured.Unstructured.
func collectContentOfConfigMap(configMap *corev1.ConfigMap, logger logr.Logger) ([]*unstructured.Unstructured, error) {
	policies := make([]*unstructured.Unstructured, 0)

	l := logger.WithValues("configMap", fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name))
	for k := range configMap.Data {
		// Base64 decode policy
		policyDecoded, err := b64.StdEncoding.DecodeString(configMap.Data[k])
		if err != nil {
			l.Error(err, fmt.Sprintf("failed to base64 decode policy from Data. Key: %s", k))
			return nil, err
		}

		elements := strings.Split(string(policyDecoded), separator)
		for i := range elements {
			policy, err := getUnstructured([]byte(elements[i]))
			if err != nil {
				l.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
				return nil, err
			}

			if policy == nil {
				l.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
				return nil, fmt.Errorf("failed to get policy from Data %.100s", elements[i])
			}

			policies = append(policies, policy)
		}
	}

	return policies, nil
}

func getPolicyName(policyName string, clusterSummary *configv1alpha1.ClusterSummary) string {
	return clusterSummary.Status.PolicyPrefix + "-" + policyName
}

func getPolicyInfo(policy client.Object) string {
	return fmt.Sprintf("%s.%s:%s:%s",
		policy.GetObjectKind().GroupVersionKind().Kind,
		policy.GetObjectKind().GroupVersionKind().Group,
		policy.GetNamespace(),
		policy.GetName())
}

// getClusterSummaryAndCAPIClusterClient gets ClusterSummary and the client to access the associated
// CAPI Cluster.
// Returns an err if ClusterSummary or associated CAPI Cluster are marked for deletion, or if an
// error occurs while getting resources.
func getClusterSummaryAndCAPIClusterClient(ctx context.Context, clusterSummaryName string,
	c client.Client, logger logr.Logger) (*configv1alpha1.ClusterSummary, client.Client, error) {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := c.Get(ctx, types.NamespacedName{Name: clusterSummaryName}, clusterSummary); err != nil {
		return nil, nil, err
	}

	if !clusterSummary.DeletionTimestamp.IsZero() {
		logger.V(logs.LogInfo).Info("ClusterSummary is marked for deletion. Nothing to do.")
		// if clusterSummary is marked for deletion, there is nothing to deploy
		return nil, nil, fmt.Errorf("clustersummary is marked for deletion")
	}

	// Get CAPI Cluster
	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterSummary.Spec.ClusterNamespace, Name: clusterSummary.Spec.ClusterName},
		cluster); err != nil {
		return nil, nil, err
	}

	if !cluster.DeletionTimestamp.IsZero() {
		logger.V(logs.LogInfo).Info("cluster is marked for deletion. Nothing to do.")
		// if cluster is marked for deletion, there is nothing to deploy
		return nil, nil, fmt.Errorf("cluster is marked for deletion")
	}

	clusterClient, err := getKubernetesClient(ctx, logger, c,
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	if err != nil {
		return nil, nil, err
	}

	return clusterSummary, clusterClient, nil
}

func deployDoc(ctx context.Context, c client.Client, doc []byte, logger logr.Logger) error {
	elements := strings.Split(string(doc), separator)
	for i := range elements {
		if elements[i] == "" {
			continue
		}

		logger.V(logs.LogVerbose).Info(fmt.Sprintf("element: %s", elements[i]))

		element, err := getUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to convert to unstructured")
			return err
		}

		if element.IsList() {
			var list *unstructured.UnstructuredList
			list, err = element.ToList()
			if err != nil {
				return err
			}
			for i := range list.Items {
				u := &list.Items[i]
				err = c.Create(ctx, u)
				if err != nil && !apierrors.IsAlreadyExists(err) {
					logger.V(logs.LogInfo).Error(err, "failed to post object")
					return fmt.Errorf("error creating %s %s: %w", element.GetKind(), element.GetName(), err)
				}
			}
		} else {
			err = c.Create(ctx, element)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				logger.V(logs.LogInfo).Error(err, "failed to post object")
				return fmt.Errorf("error creating %s %s: %w", element.GetKind(), element.GetName(), err)
			}
		}
	}

	return nil
}

// collectConfigMaps collects all referenced configMaps in control cluster
func collectConfigMaps(ctx context.Context, controlClusterClient client.Client,
	references []corev1.ObjectReference, logger logr.Logger) ([]corev1.ConfigMap, error) {

	configMaps := make([]corev1.ConfigMap, 0)
	for i := range references {
		reference := &references[i]
		configMap := &corev1.ConfigMap{}
		if err := controlClusterClient.Get(ctx,
			types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configMap); err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("configMap %s/%s does not exist yet",
					reference.Namespace, reference.Name))
				continue
			}
			return nil, err
		}
		configMaps = append(configMaps, *configMap)
	}

	return configMaps, nil
}

// deployConfigMaps deploys in a CAPI Cluster the policies contained in the Data section of each passed ConfigMap
func deployConfigMaps(ctx context.Context, configMaps []corev1.ConfigMap, clusterSummary *configv1alpha1.ClusterSummary,
	capiClusterClient client.Client, capiClusterConfig *rest.Config,
	logger logr.Logger) ([]string, error) {

	deployed := make([]string, 0)

	for i := range configMaps {
		configMap := &configMaps[i]

		tmpDeployed, err := deployContentOfConfigMap(ctx, capiClusterConfig, capiClusterClient, configMap, clusterSummary, logger)
		if err != nil {
			return nil, err
		}

		deployed = append(deployed, tmpDeployed...)
	}
	return deployed, nil
}

func undeployStaleResources(ctx context.Context, clusterConfig *rest.Config, clusterClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	deployedGVKs []schema.GroupVersionKind,
	currentPolicies map[string]bool) error {

	// Do not use due to metav1.Selector limitation
	// labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{ClusterSummaryLabelName: clusterSummary.Name}}
	// listOptions := metav1.ListOptions{
	//	LabelSelector: labelSelector.String(),
	// }

	dc := discovery.NewDiscoveryClientForConfigOrDie(clusterConfig)
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	d := dynamic.NewForConfigOrDie(clusterConfig)

	for i := range deployedGVKs {
		mapping, err := mapper.RESTMapping(deployedGVKs[i].GroupKind(), deployedGVKs[i].Version)
		if err != nil {
			return err
		}

		resourceId := schema.GroupVersionResource{
			Group:    deployedGVKs[i].Group,
			Version:  deployedGVKs[i].Version,
			Resource: mapping.Resource.Resource,
		}

		list, err := d.Resource(resourceId).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}

		for j := range list.Items {
			r := list.Items[j]
			if !hasLabel(&r, ClusterSummaryLabelName, clusterSummary.Name) {
				continue
			}
			err = deleteIfNotExistant(ctx, &r, clusterClient, currentPolicies)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func deleteIfNotExistant(ctx context.Context, policy client.Object, c client.Client, currentPolicies map[string]bool) error {
	name := getPolicyInfo(policy)
	if _, ok := currentPolicies[name]; !ok {
		if err := c.Delete(ctx, policy); err != nil {
			return err
		}
	}

	return nil
}

func hasLabel(u *unstructured.Unstructured, key, value string) bool {
	lbls := u.GetLabels()
	if lbls == nil {
		return false
	}
	return lbls[key] == value
}

func getDeployedGroupVersionKinds(clusterSummary *configv1alpha1.ClusterSummary,
	featureID configv1alpha1.FeatureID) []schema.GroupVersionKind {

	gvks := make([]schema.GroupVersionKind, 0)
	for i := range clusterSummary.Status.FeatureSummaries {
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == featureID {
			for j := range clusterSummary.Status.FeatureSummaries[i].DeployedGroupVersionKind {
				gvk, _ := schema.ParseKindArg(clusterSummary.Status.FeatureSummaries[i].DeployedGroupVersionKind[j])
				gvks = append(gvks, *gvk)
			}
		}
	}

	return gvks
}
