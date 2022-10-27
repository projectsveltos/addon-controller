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
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

const (
	separator = "---\n"
)

// createNamespace creates a namespace if it does not exist already
// No action in DryRun mode.
func createNamespace(ctx context.Context, clusterClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, namespaceName string) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

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
func deployContentOfConfigMap(ctx context.Context, remoteConfig *rest.Config, c, remoteClient client.Client,
	configMap *corev1.ConfigMap, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (reports []configv1alpha1.ResourceReport, err error) {

	reports, err =
		deployContent(ctx, remoteConfig, c, remoteClient, configMap, configMap.Data, clusterSummary, logger)
	return
}

// deployContentOfSecret deploys policies contained in a Secret.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfSecret(ctx context.Context, remoteConfig *rest.Config, c, remoteClient client.Client,
	secret *corev1.Secret, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (reports []configv1alpha1.ResourceReport, err error) {

	data := make(map[string]string)
	for key, value := range secret.Data {
		data[key], err = decode(value)
		if err != nil {
			return nil, err
		}
	}

	reports, err =
		deployContent(ctx, remoteConfig, c, remoteClient, secret, data, clusterSummary, logger)
	return
}

// updateResource creates or updates a resource in a CAPI Cluster.
// No action in DryRun mode.
func updateResource(ctx context.Context, dr dynamic.ResourceInterface,
	clusterSummary *configv1alpha1.ClusterSummary, object *unstructured.Unstructured,
	logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	l := logger.WithValues("resourceNamespace", object.GetNamespace(),
		"resourceName", object.GetName(), "resourceGVK", object.GetObjectKind().GroupVersionKind())
	l.V(logs.LogDebug).Info("deploying policy")

	data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, object)
	if err != nil {
		return err
	}

	forceConflict := true
	options := metav1.PatchOptions{
		FieldManager: "application/apply-patch",
		Force:        &forceConflict,
	}
	_, err = dr.Patch(ctx, object.GetName(), types.ApplyPatchType, data, options)
	return err
}

// deployContent deploys policies contained in a ConfigMap/Secret.
// data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContent(ctx context.Context, remoteConfig *rest.Config, c, remoteClient client.Client,
	referencedObject client.Object, data map[string]string, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (reports []configv1alpha1.ResourceReport, err error) {

	referencedPolicies, err := collectContent(ctx, clusterSummary, data, logger)
	if err != nil {
		return nil, err
	}

	clusterProfile, err := configv1alpha1.GetClusterProfileOwner(ctx, c, clusterSummary)
	if err != nil {
		return nil, err
	}

	reports = make([]configv1alpha1.ResourceReport, 0)
	for i := range referencedPolicies {
		policy := referencedPolicies[i]

		resource := &configv1alpha1.Resource{
			Name:      policy.GetName(),
			Namespace: policy.GetNamespace(),
			Kind:      policy.GetKind(),
			Group:     policy.GetObjectKind().GroupVersionKind().Group,
			Owner: corev1.ObjectReference{
				Namespace: referencedObject.GetNamespace(),
				Name:      referencedObject.GetName(),
				Kind:      referencedObject.GetObjectKind().GroupVersionKind().Kind,
			},
		}

		policyHash, err := computePolicyHash(policy)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to compute policy hash %v", err))
			policyHash = ""
		}

		// Get policy hash of referenced policy
		addLabel(policy, ReferenceLabelKind, referencedObject.GetObjectKind().GroupVersionKind().Kind)
		addLabel(policy, ReferenceLabelName, referencedObject.GetName())
		addLabel(policy, ReferenceLabelNamespace, referencedObject.GetNamespace())
		addAnnotation(policy, PolicyHash, policyHash)

		// If policy is namespaced, create namespace if not already existing
		err = createNamespace(ctx, remoteClient, clusterSummary, policy.GetNamespace())
		if err != nil {
			return nil, err
		}

		// If policy already exists, just get current version and update it by overridding
		// all metadata and spec.
		// If policy does not exist already, create it
		dr, err := utils.GetDynamicResourceInterface(remoteConfig, policy)
		if err != nil {
			return nil, err
		}

		var exist bool
		var currentHash string
		exist, currentHash, err = validateObjectForUpdate(ctx, dr, policy,
			referencedObject.GetObjectKind().GroupVersionKind().Kind, referencedObject.GetNamespace(), referencedObject.GetName())
		if err != nil {
			var conflictErr *conflictError
			ok := errors.As(err, &conflictErr)
			// In DryRun mode do not stop here, but report the conflict.
			if ok && clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
				conflictResourceReport := generateConflictResourceReport(ctx, dr, resource)
				reports = append(reports, *conflictResourceReport)
				continue
			}
			return nil, err
		}

		addOwnerReference(policy, clusterProfile)

		// Update only if object does not exist yet or hash is different
		if !exist || currentHash != policyHash {
			err = updateResource(ctx, dr, clusterSummary, policy, logger)
			if err != nil {
				return nil, err
			}
		}

		resource.LastAppliedTime = &metav1.Time{Time: time.Now()}

		if !exist {
			reports = append(reports,
				configv1alpha1.ResourceReport{Resource: *resource, Action: string(configv1alpha1.CreateResourceAction)})
		} else if policyHash != currentHash {
			reports = append(reports,
				configv1alpha1.ResourceReport{Resource: *resource, Action: string(configv1alpha1.UpdateResourceAction)})
		} else {
			reports = append(reports,
				configv1alpha1.ResourceReport{Resource: *resource, Action: string(configv1alpha1.NoResourceAction),
					Message: "Object already deployed. And policy referenced by ClusterProfile has not changed since last deployment."})
		}
	}

	return reports, nil
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
			if elements[i] == "" {
				continue
			}

			policy, err := utils.GetUnstructured([]byte(elements[i]))
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

				policy, err = utils.GetUnstructured([]byte(instance))
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
func getClusterSummaryAndCAPIClusterClient(ctx context.Context, clusterNamespace, clusterSummaryName string,
	c client.Client, logger logr.Logger) (*configv1alpha1.ClusterSummary, client.Client, error) {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterNamespace, Name: clusterSummaryName}, clusterSummary); err != nil {
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

	s, err := InitScheme()
	if err != nil {
		return nil, nil, err
	}

	clusterClient, err := clusterproxy.GetKubernetesClient(ctx, logger, c, s,
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	if err != nil {
		return nil, nil, err
	}

	return clusterSummary, clusterClient, nil
}

// collectReferencedObjects collects all referenced configMaps/secrets in control cluster
func collectReferencedObjects(ctx context.Context, controlClusterClient client.Client,
	references []libsveltosv1alpha1.PolicyRef, logger logr.Logger) ([]client.Object, error) {

	objects := make([]client.Object, 0)
	for i := range references {
		var err error
		var object client.Object
		reference := &references[i]
		if reference.Kind == string(configv1alpha1.ConfigMapReferencedResourceKind) {
			object, err = getConfigMap(ctx, controlClusterClient,
				types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name})
		} else {
			object, err = getSecret(ctx, controlClusterClient,
				types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name})
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
	referencedObject []client.Object, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (reports []configv1alpha1.ResourceReport, err error) {

	remoteClient, err := client.New(remoteConfig, client.Options{})
	if err != nil {
		return nil, err
	}

	for i := range referencedObject {
		var tmpResourceReports []configv1alpha1.ResourceReport
		if referencedObject[i].GetObjectKind().GroupVersionKind().Kind == string(configv1alpha1.ConfigMapReferencedResourceKind) {
			configMap := referencedObject[i].(*corev1.ConfigMap)
			l := logger.WithValues("configMapNamespace", configMap.Namespace, "configMapName", configMap.Name)
			l.V(logs.LogDebug).Info("deploying ConfigMap content")
			tmpResourceReports, err =
				deployContentOfConfigMap(ctx, remoteConfig, c, remoteClient, configMap, clusterSummary, l)
		} else {
			secret := referencedObject[i].(*corev1.Secret)
			l := logger.WithValues("secretNamespace", secret.Namespace, "secretName", secret.Name)
			l.V(logs.LogDebug).Info("deploying Secret content")
			tmpResourceReports, err =
				deployContentOfSecret(ctx, remoteConfig, c, remoteClient, secret, clusterSummary, l)
		}

		if err != nil {
			return nil, err
		}
		reports = append(reports, tmpResourceReports...)
	}

	return reports, nil
}

func undeployStaleResources(ctx context.Context, remoteConfig *rest.Config, c, remoteClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	deployedGVKs []schema.GroupVersionKind,
	currentPolicies map[string]configv1alpha1.Resource, logger logr.Logger) ([]configv1alpha1.ResourceReport, error) {

	// Do not use due to metav1.Selector limitation
	// labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{ClusterSummaryLabelName: clusterSummary.Name}}
	// listOptions := metav1.ListOptions{
	//	LabelSelector: labelSelector.String(),
	// }

	logger.V(logs.LogDebug).Info("removing stale resources")

	clusterProfile, err := configv1alpha1.GetClusterProfileOwner(ctx, c, clusterSummary)
	if err != nil {
		return nil, err
	}

	undeployed := make([]configv1alpha1.ResourceReport, 0)

	dc := discovery.NewDiscoveryClientForConfigOrDie(remoteConfig)
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	d := dynamic.NewForConfigOrDie(remoteConfig)

	for i := range deployedGVKs {
		mapping, err := mapper.RESTMapping(deployedGVKs[i].GroupKind(), deployedGVKs[i].Version)
		if err != nil {
			// if CRDs does not exist anymore, ignore error. No instances of
			// such CRD can be left anyway.
			if meta.IsNoMatchError(err) {
				continue
			}
			return nil, err
		}

		resourceId := schema.GroupVersionResource{
			Group:    deployedGVKs[i].Group,
			Version:  deployedGVKs[i].Version,
			Resource: mapping.Resource.Resource,
		}

		list, err := d.Resource(resourceId).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		for j := range list.Items {
			r := list.Items[j]
			logger.V(logs.LogVerbose).Info("considering %s/%s", r.GetNamespace(), r.GetName())
			// Verify if this policy was deployed because of a clustersummary (ReferenceLabelName
			// is present as label in such a case).
			if !hasLabel(&r, ReferenceLabelName, "") {
				continue
			}

			// If in DryRun do not withdrawn any policy.
			// If this ClusterSummary is the only OwnerReference and it is not deploying this policy anymore,
			// policy would be withdrawn
			if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
				if canDelete(&r, currentPolicies) && isOnlyhOwnerReference(&r, clusterProfile) {
					undeployed = append(undeployed, configv1alpha1.ResourceReport{
						Resource: configv1alpha1.Resource{
							Kind: r.GetObjectKind().GroupVersionKind().Kind, Namespace: r.GetNamespace(), Name: r.GetName(),
							Group: r.GroupVersionKind().Group,
						},
						Action: string(configv1alpha1.DeleteResourceAction),
					})
				}
			} else {
				logger.V(logs.LogVerbose).Info("remove owner reference %s/%s", r.GetNamespace(), r.GetName())

				removeOwnerReference(&r, clusterProfile)

				if len(r.GetOwnerReferences()) != 0 {
					// Other ClusterSummary are still deploying this very same policy
					continue
				}

				if canDelete(&r, currentPolicies) {
					err = remoteClient.Delete(ctx, &r)
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}

	return undeployed, nil
}

// canDelete returns true if a policy can be deleted. For a policy to be deleted:
// - policy is not part of currentReferencedPolicies
func canDelete(policy client.Object, currentReferencedPolicies map[string]configv1alpha1.Resource) bool {
	name := getPolicyInfo(&configv1alpha1.Resource{
		Kind:      policy.GetObjectKind().GroupVersionKind().Kind,
		Group:     policy.GetObjectKind().GroupVersionKind().Group,
		Name:      policy.GetName(),
		Namespace: policy.GetNamespace(),
	})
	if _, ok := currentReferencedPolicies[name]; ok {
		return false
	}
	return true
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

// No action in DryRun mode.
func updateClusterConfiguration(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	clusterProfileOwnerRef *metav1.OwnerReference,
	featureID configv1alpha1.FeatureID,
	policyDeployed []configv1alpha1.Resource,
	chartDeployed []configv1alpha1.Chart) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get ClusterConfiguration for CAPI Cluster
		clusterConfiguration := &configv1alpha1.ClusterConfiguration{}
		err := c.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Spec.ClusterNamespace, Name: clusterSummary.Spec.ClusterName},
			clusterConfiguration)
		if err != nil {
			if apierrors.IsNotFound(err) && !clusterSummary.DeletionTimestamp.IsZero() {
				return nil
			}
			return err
		}

		var index int
		index, err = configv1alpha1.GetClusterConfigurationSectionIndex(clusterConfiguration, clusterProfileOwnerRef.Name)
		if err != nil {
			return err
		}

		isPresent := false
		for i := range clusterConfiguration.Status.ClusterProfileResources[index].Features {
			if clusterConfiguration.Status.ClusterProfileResources[index].Features[i].FeatureID == featureID {
				if policyDeployed != nil {
					clusterConfiguration.Status.ClusterProfileResources[index].Features[i].Resources = policyDeployed
				}
				if chartDeployed != nil {
					clusterConfiguration.Status.ClusterProfileResources[index].Features[i].Charts = chartDeployed
				}
				isPresent = true
				break
			}
		}

		if !isPresent {
			if clusterConfiguration.Status.ClusterProfileResources[index].Features == nil {
				clusterConfiguration.Status.ClusterProfileResources[index].Features = make([]configv1alpha1.Feature, 0)
			}
			clusterConfiguration.Status.ClusterProfileResources[index].Features = append(clusterConfiguration.Status.ClusterProfileResources[index].Features,
				configv1alpha1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
			)
		}

		clusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences, *clusterProfileOwnerRef)

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

// computePolicyHash compute policy hash.
func computePolicyHash(policy *unstructured.Unstructured) (string, error) {
	b, err := policy.MarshalJSON()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = hash.Write(b)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

// getConfigMap retrieves any ConfigMap from the given name and namespace.
func getConfigMap(ctx context.Context, c client.Client, configmapName types.NamespacedName) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	configMapKey := client.ObjectKey{
		Namespace: configmapName.Namespace,
		Name:      configmapName.Name,
	}
	if err := c.Get(ctx, configMapKey, configMap); err != nil {
		return nil, err
	}

	return configMap, nil
}

// getSecret retrieves any Secret from the given secret name and namespace.
func getSecret(ctx context.Context, c client.Client, secretName types.NamespacedName) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: secretName.Namespace,
		Name:      secretName.Name,
	}
	if err := c.Get(ctx, secretKey, secret); err != nil {
		return nil, err
	}

	return secret, nil
}

func generateConflictResourceReport(ctx context.Context, dr dynamic.ResourceInterface,
	resource *configv1alpha1.Resource) *configv1alpha1.ResourceReport {

	conflictReport := &configv1alpha1.ResourceReport{
		Resource: *resource,
		Action:   string(configv1alpha1.ConflictResourceAction),
	}
	message, err := getOwnerMessage(ctx, dr, resource.Name)
	if err == nil {
		conflictReport.Message = message
	}
	return conflictReport
}
