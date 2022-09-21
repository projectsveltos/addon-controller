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
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
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
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfConfigMap(ctx context.Context, remoteConfig *rest.Config, remoteClient client.Client,
	configMap *corev1.ConfigMap, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) ([]configv1alpha1.Resource, error) {

	l := logger.WithValues(string(configv1alpha1.ConfigMapReferencedResourceKind),
		fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name))
	return deployContent(ctx, remoteConfig, remoteClient, configMap, configMap.Data, clusterSummary, l)
}

// deployContentOfSecret deploys policies contained in a Secret.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfSecret(ctx context.Context, remoteConfig *rest.Config, remoteClient client.Client,
	secret *corev1.Secret, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) ([]configv1alpha1.Resource, error) {

	l := logger.WithValues(string(configv1alpha1.SecretReferencedResourceKind),
		fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))
	data := make(map[string]string)
	var err error
	for key, value := range secret.Data {
		data[key], err = decode(value)
		if err != nil {
			return nil, err
		}
	}

	return deployContent(ctx, remoteConfig, remoteClient, secret, data, clusterSummary, l)
}

// deployContent deploys policies contained in a ConfigMap/Secret.
// data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContent(ctx context.Context, remoteConfig *rest.Config, remoteClient client.Client,
	referencedObject client.Object, data map[string]string, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) ([]configv1alpha1.Resource, error) {

	referencedPolicies, err := collectContent(ctx, clusterSummary, data, logger)
	if err != nil {
		return nil, err
	}

	policies := make([]configv1alpha1.Resource, 0)
	for i := range referencedPolicies {
		policy := referencedPolicies[i]
		addLabel(policy, ReferenceLabelKind, referencedObject.GetObjectKind().GroupVersionKind().Kind)
		addLabel(policy, ReferenceLabelName, referencedObject.GetName())
		addLabel(policy, ReferenceLabelNamespace, referencedObject.GetNamespace())
		name := getPolicyName(policy.GetName(), clusterSummary)
		policy.SetName(name)

		// If policy is namespaced, create namespace if not already existing
		err := createNamespace(ctx, remoteClient, policy.GetNamespace())
		if err != nil {
			return nil, err
		}

		// If policy already exists, just get current version and update it by overridding
		// all metadata and spec.
		// If policy does not exist already, create it
		dr, err := getDynamicResourceInterface(remoteConfig, policy)
		if err != nil {
			return nil, err
		}
		policy, err = preprareObjectForUpdate(ctx, dr, policy,
			referencedObject.GetObjectKind().GroupVersionKind().Kind, referencedObject.GetNamespace(), referencedObject.GetName())
		if err != nil {
			return nil, err
		}

		addOwnerReference(policy, clusterSummary)

		l := logger.WithValues("resourceNamespace", policy.GetNamespace(), "resourceName", policy.GetName())
		l.V(logs.LogDebug).Info("deploying policy")

		if policy.GetResourceVersion() != "" {
			err = remoteClient.Update(ctx, policy)
		} else {
			err = remoteClient.Create(ctx, policy)
		}

		if err != nil {
			return nil, err
		}

		policies = append(policies, configv1alpha1.Resource{
			Name:            policy.GetName(),
			Namespace:       policy.GetNamespace(),
			Kind:            policy.GetKind(),
			Group:           policy.GetObjectKind().GroupVersionKind().Group,
			LastAppliedTime: &metav1.Time{Time: time.Now()},
			Owner: corev1.ObjectReference{
				Namespace: referencedObject.GetNamespace(),
				Name:      referencedObject.GetName(),
				Kind:      referencedObject.GetObjectKind().GroupVersionKind().Kind,
			},
		})
	}

	return policies, nil
}

// collectContent collect policies contained in a ConfigMap/Secret.
// ConfigMap/Secret Data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice of *unstructured.Unstructured.
func collectContent(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	data map[string]string, logger logr.Logger) ([]*unstructured.Unstructured, error) {

	policies := make([]*unstructured.Unstructured, 0)

	for k := range data {
		elements := strings.Split(data[k], separator)
		for i := range elements {
			policy, err := getUnstructured([]byte(elements[i]))
			if err != nil {
				logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
				return nil, err
			}

			if isTemplate(policy) {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("policy %s/%s is a template",
					policy.GetNamespace(), policy.GetName()))
				var instance string
				// If policy is a template, instantiate it given current state of system, then deploy
				instance, err = instantiateTemplate(ctx, getManagementClusterClient(), getManagementClusterConfig(),
					clusterSummary, elements[i], logger)
				if err != nil {
					return nil, err
				}

				policy, err = getUnstructured([]byte(instance))
				if err != nil {
					logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
					return nil, err
				}
			}

			if policy == nil {
				logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
				return nil, fmt.Errorf("failed to get policy from Data %.100s", elements[i])
			}

			policies = append(policies, policy)
		}
	}

	return policies, nil
}

func getPolicyName(policyName string, _ *configv1alpha1.ClusterSummary) string {
	return policyName
}

func getPolicyInfo(policy *configv1alpha1.Resource) string {
	return fmt.Sprintf("%s.%s:%s:%s",
		policy.Kind,
		policy.Group,
		policy.Namespace,
		policy.Name)
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

// collectReferencedObjects collects all referenced configMaps/secrets in control cluster
func collectReferencedObjects(ctx context.Context, controlClusterClient client.Client,
	references []configv1alpha1.PolicyRef, logger logr.Logger) ([]client.Object, error) {

	objects := make([]client.Object, 0)
	for i := range references {
		var err error
		var object client.Object
		reference := &references[i]
		if reference.Kind == string(configv1alpha1.ConfigMapReferencedResourceKind) {
			configMap := &corev1.ConfigMap{}
			err = controlClusterClient.Get(ctx,
				types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configMap)
			object = configMap
		} else {
			secret := &corev1.Secret{}
			err = controlClusterClient.Get(ctx,
				types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, secret)
			object = secret
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s %s/%s does not exist yet",
					reference.Kind, reference.Namespace, reference.Name))
				continue
			}
			return nil, err
		}
		objects = append(objects, object)
	}

	return objects, nil
}

// deployReferencedObjects deploys in a CAPI Cluster the policies contained in the Data section of each passed ConfigMap
func deployReferencedObjects(ctx context.Context, c client.Client, remoteConfig *rest.Config,
	featureID configv1alpha1.FeatureID, referencedObject []client.Object, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) ([]configv1alpha1.Resource, error) {

	deployed := make([]configv1alpha1.Resource, 0)

	remoteClient, err := client.New(remoteConfig, client.Options{})
	if err != nil {
		return nil, err
	}

	for i := range referencedObject {
		var tmpDeployed []configv1alpha1.Resource
		if referencedObject[i].GetObjectKind().GroupVersionKind().Kind == string(configv1alpha1.ConfigMapReferencedResourceKind) {
			configMap := referencedObject[i].(*corev1.ConfigMap)
			l := logger.WithValues("configMapNamespace", configMap.Namespace, "configMapName", configMap.Name)
			l.V(logs.LogDebug).Info("deploying ConfigMap content")
			tmpDeployed, err = deployContentOfConfigMap(ctx, remoteConfig, remoteClient, configMap, clusterSummary, l)
		} else {
			secret := referencedObject[i].(*corev1.Secret)
			l := logger.WithValues("secretNamespace", secret.Namespace, "secretName", secret.Name)
			l.V(logs.LogDebug).Info("deploying Secret content")
			tmpDeployed, err = deployContentOfSecret(ctx, remoteConfig, remoteClient, secret, clusterSummary, l)
		}

		if err != nil {
			return nil, err
		}
		deployed = append(deployed, tmpDeployed...)
	}

	clusterFeatureOwnerRef, err := configv1alpha1.GetOwnerClusterFeatureName(clusterSummary)
	if err != nil {
		return nil, err
	}

	err = updateClusterConfiguration(ctx, c, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterFeatureOwnerRef, featureID, deployed, nil)
	if err != nil {
		return nil, err
	}

	return deployed, nil
}

func undeployStaleResources(ctx context.Context, clusterConfig *rest.Config, clusterClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	deployedGVKs []schema.GroupVersionKind,
	currentPolicies map[string]configv1alpha1.Resource, logger logr.Logger) error {

	// Do not use due to metav1.Selector limitation
	// labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{ClusterSummaryLabelName: clusterSummary.Name}}
	// listOptions := metav1.ListOptions{
	//	LabelSelector: labelSelector.String(),
	// }

	logger.V(logs.LogDebug).Info("removing stale resources")

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
			// if CRDs does not exist anymore, ignore error. No instances of
			// such CRD can be left anyway.
			if meta.IsNoMatchError(err) {
				continue
			}
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
			logger.V(logs.LogVerbose).Info("considering %s/%s", r.GetNamespace(), r.GetName())
			// Verify if this policy was deployed because of a clustersummary (ReferenceLabelName
			// is present as label in such a case).
			if !hasLabel(&r, ReferenceLabelName, "") {
				continue
			}

			logger.V(logs.LogVerbose).Info("remove owner reference %s/%s", r.GetNamespace(), r.GetName())
			removeOwnerReference(&r, clusterSummary)

			if len(r.GetOwnerReferences()) != 0 {
				// Other ClusterSummary are still deploying this very same policy
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

func deleteIfNotExistant(ctx context.Context, policy client.Object, c client.Client, currentPolicies map[string]configv1alpha1.Resource) error {
	name := getPolicyInfo(&configv1alpha1.Resource{
		Kind:      policy.GetObjectKind().GroupVersionKind().Kind,
		Group:     policy.GetObjectKind().GroupVersionKind().Group,
		Name:      policy.GetName(),
		Namespace: policy.GetNamespace(),
	})
	if _, ok := currentPolicies[name]; !ok {
		if err := c.Delete(ctx, policy); err != nil {
			return err
		}
	}

	return nil
}

// hasLabel search if key is one of the label.
// If value is empty, returns true if key is present.
// If value is not empty, returns true if key is present and value is a match.
func hasLabel(u *unstructured.Unstructured, key, value string) bool {
	lbls := u.GetLabels()
	if lbls == nil {
		return false
	}

	v, ok := lbls[key]

	if value == "" {
		return ok
	}

	return v == value
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

func updateClusterConfiguration(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string,
	clusterFeatureOwnerRef *metav1.OwnerReference,
	featureID configv1alpha1.FeatureID,
	policyDeployed []configv1alpha1.Resource,
	chartDeployed []configv1alpha1.Chart) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get ClusterConfiguration for CAPI Cluster
		clusterConfiguration := &configv1alpha1.ClusterConfiguration{}
		err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, clusterConfiguration)
		if err != nil {
			return err
		}

		var index int
		index, err = configv1alpha1.GetClusterConfigurationSectionIndex(clusterConfiguration, clusterFeatureOwnerRef.Name)
		if err != nil {
			return err
		}

		isPresent := false
		for i := range clusterConfiguration.Status.ClusterFeatureResources[index].Features {
			if clusterConfiguration.Status.ClusterFeatureResources[index].Features[i].FeatureID == featureID {
				if policyDeployed != nil {
					clusterConfiguration.Status.ClusterFeatureResources[index].Features[i].Resources = policyDeployed
				}
				if chartDeployed != nil {
					clusterConfiguration.Status.ClusterFeatureResources[index].Features[i].Charts = chartDeployed
				}
				isPresent = true
				break
			}
		}

		if !isPresent {
			if clusterConfiguration.Status.ClusterFeatureResources[index].Features == nil {
				clusterConfiguration.Status.ClusterFeatureResources[index].Features = make([]configv1alpha1.Feature, 0)
			}
			clusterConfiguration.Status.ClusterFeatureResources[index].Features = append(clusterConfiguration.Status.ClusterFeatureResources[index].Features,
				configv1alpha1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
			)
		}

		clusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences, *clusterFeatureOwnerRef)

		return c.Status().Update(ctx, clusterConfiguration)
	})

	return err
}

func decode(encoded []byte) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		return "", err
	}

	return string(decoded), nil
}
