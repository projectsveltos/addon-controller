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
	"fmt"
	"sort"
	"strings"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

//+kubebuilder:rbac:groups=extension.projectsveltos.io,resources=yttsources,verbs=get;list;watch
//+kubebuilder:rbac:groups=extension.projectsveltos.io,resources=jsonnetsources,verbs=get;list;watch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=debuggingconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;impersonate

const (
	nameSeparator = "--"
	clusterKind   = "Cluster"
)

func InitScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := configv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1b2.AddToScheme(s); err != nil {
		return nil, err
	}

	return s, nil
}

// getClusterConfiguration returns the ClusterConfiguration instance for a specific CAPI Cluster
func getClusterConfiguration(ctx context.Context, c client.Client,
	clusterNamespace, clusterConfigurationName string) (*configv1beta1.ClusterConfiguration, error) {

	clusterConfiguration := &configv1beta1.ClusterConfiguration{}
	if err := c.Get(ctx,
		types.NamespacedName{
			Namespace: clusterNamespace,
			Name:      clusterConfigurationName,
		},
		clusterConfiguration); err != nil {
		return nil, err
	}

	return clusterConfiguration, nil
}

func getEntryKey(resourceKind, resourceNamespace, resourceName string) string {
	if resourceNamespace != "" {
		return fmt.Sprintf("%s-%s-%s", resourceKind, resourceNamespace, resourceName)
	}
	return fmt.Sprintf("%s-%s", resourceKind, resourceName)
}

func getClusterConfigurationName(clusterName string, clusterType libsveltosv1beta1.ClusterType) string {
	// TODO: shorten this value
	return strings.ToLower(string(clusterType)) + nameSeparator + clusterName
}

// getKeyFromObject returns the Key that can be used in the internal reconciler maps.
func getKeyFromObject(scheme *runtime.Scheme, obj client.Object) *corev1.ObjectReference {
	addTypeInformationToObject(scheme, obj)

	apiVersion, kind := obj.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()

	return &corev1.ObjectReference{
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		Kind:       kind,
		APIVersion: apiVersion,
	}
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		panic(1)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}
}

// isCluterSummaryProvisioned returns true if ClusterSummary is currently fully deployed.
func isCluterSummaryProvisioned(clusterSumary *configv1beta1.ClusterSummary) bool {
	hasHelmCharts := false
	hasRawYAMLs := false
	hasKustomize := false

	if len(clusterSumary.Spec.ClusterProfileSpec.HelmCharts) != 0 {
		hasHelmCharts = true
	}

	if len(clusterSumary.Spec.ClusterProfileSpec.PolicyRefs) != 0 {
		hasRawYAMLs = true
	}

	if len(clusterSumary.Spec.ClusterProfileSpec.KustomizationRefs) != 0 {
		hasKustomize = true
	}

	deployedHelmCharts := false
	deployedRawYAMLs := false
	deployedKustomize := false

	for i := range clusterSumary.Status.FeatureSummaries {
		fs := &clusterSumary.Status.FeatureSummaries[i]
		if fs.Status != libsveltosv1beta1.FeatureStatusProvisioned {
			return false
		}
		switch fs.FeatureID {
		case libsveltosv1beta1.FeatureHelm:
			deployedHelmCharts = true
		case libsveltosv1beta1.FeatureResources:
			deployedRawYAMLs = true
		case libsveltosv1beta1.FeatureKustomize:
			deployedKustomize = true
		}
	}

	if hasHelmCharts {
		if !deployedHelmCharts {
			return false
		}
	}

	if hasRawYAMLs {
		if !deployedRawYAMLs {
			return false
		}
	}

	if hasKustomize {
		if !deployedKustomize {
			return false
		}
	}

	return true
}

func isNamespaced(r *unstructured.Unstructured, config *rest.Config) (bool, error) {
	gvk := schema.GroupVersionKind{
		Group:   r.GroupVersionKind().Group,
		Kind:    r.GetKind(),
		Version: r.GroupVersionKind().Version,
	}

	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return false, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

	var mapping *meta.RESTMapping
	mapping, err = mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, err
	}
	return mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// removeDuplicates removes duplicates entries in the references slice
func removeDuplicates(references []corev1.ObjectReference) []corev1.ObjectReference {
	set := libsveltosset.Set{}
	for i := range references {
		set.Insert(&references[i])
	}

	return set.Items()
}

func getConfigMapHash(configmap *corev1.ConfigMap) string {
	var config string
	if configmap.Annotations != nil {
		config += getDataSectionHash(configmap.Annotations)
	}

	config += getDataSectionHash(configmap.Data)
	config += getDataSectionHash(configmap.BinaryData)

	return config
}

func getSecretHash(secret *corev1.Secret) string {
	var config string
	if secret.Annotations != nil {
		config += getDataSectionHash(secret.Annotations)
	}

	config += getDataSectionHash(secret.Data)
	config += getDataSectionHash(secret.StringData)

	return config
}

// getDataSectionHash sorts map and return the hash
func getDataSectionHash[T any](data map[string]T) string {
	var keys []string
	for k := range data {
		keys = append(keys, k)
	}

	// Sort keys (ascending order)
	sort.Strings(keys)

	var config string
	for i := range keys {
		config += render.AsCode(data[keys[i]])
	}

	return config
}

// stringifyMap converts a map[string]string to a string representation
func stringifyMap(data map[string]string) (string, error) {
	jsonData, err := yaml.Marshal(data)
	if err != nil {
		return "", err
	}
	// Return the JSON string representation
	return string(jsonData), nil
}

// parseMapFromString converts a string representation back to map[string]string
func parseMapFromString(data string) (map[string]string, error) {
	// Create an empty map to store the parsed data
	result := map[string]string{}
	// Unmarshal the JSON string into the map
	err := yaml.Unmarshal([]byte(data), &result)
	if err != nil {
		return nil, err
	}
	// Return the parsed map
	return result, nil
}

// Function to remove duplicates from a slice
func unique[T comparable](input []T) []T {
	seen := make(map[T]bool)
	unique := []T{}

	for _, element := range input {
		if !seen[element] {
			unique = append(unique, element)
			seen[element] = true
		}
	}

	return unique
}

// Return FeatureSummaries for featureID
func getFeatureSummaryForFeatureID(clusterSummay *configv1beta1.ClusterSummary, fID libsveltosv1beta1.FeatureID,
) *configv1beta1.FeatureSummary {

	for i := range clusterSummay.Status.FeatureSummaries {
		if clusterSummay.Status.FeatureSummaries[i].FeatureID == fID {
			return &clusterSummay.Status.FeatureSummaries[i]
		}
	}

	return nil
}

// Return FeatureDeploymentInfo for featureID
func getFeatureDeploymentInfoForFeatureID(clusterSummay *configv1beta1.ClusterSummary,
	fID libsveltosv1beta1.FeatureID) *libsveltosv1beta1.FeatureDeploymentInfo {

	for i := range clusterSummay.Status.DeployedGVKs {
		if clusterSummay.Status.DeployedGVKs[i].FeatureID == fID {
			return &clusterSummay.Status.DeployedGVKs[i]
		}
	}

	return nil
}

// Identifies and removes drift detection deployments that are no longer associated with active clusters
// Identifies and removes resourceSummary instances from clusters that are no longer existing
func removeStaleDriftDetectionResources(ctx context.Context, logger logr.Logger) {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			driftDetectionFeatureLabelKey: driftDetectionFeatureLabelValue,
		},
	}

	for {
		time.Sleep(time.Minute)

		c := getManagementClusterClient()
		driftDetectionDeployments := &appsv1.DeploymentList{}
		err := c.List(ctx, driftDetectionDeployments, listOptions...)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect driftDetection deployment: %v", err))
			continue
		}

		for i := range driftDetectionDeployments.Items {
			depl := &driftDetectionDeployments.Items[i]

			exist, clusterNs, clusterName, clusterType := deplAssociatedClusterExist(ctx, c, depl, logger)
			if !exist {
				// find resourceSummaries from this cluster and remove those.
				// Remove deployment only after this one succeed
				err = removeStaleResourceSummary(ctx, clusterNs, clusterName, clusterType, logger)
				if err != nil {
					continue
				}

				logger.V(logs.LogInfo).Info(fmt.Sprintf("deleting driftDetection deployment %s/%s",
					depl.Namespace, depl.Name))
				_ = c.Delete(ctx, depl)
			}
		}
	}
}

func removeStaleResourceSummary(ctx context.Context, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) error {

	c := getManagementClusterClient()

	rsListOptions := []client.ListOption{
		client.MatchingLabels{
			sveltos_upgrade.ClusterNameLabel: clusterName,
			sveltos_upgrade.ClusterTypeLabel: strings.ToLower(string(clusterType)),
		},
	}

	resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
	err := c.List(ctx, resourceSummaries, rsListOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to collect resourceSummary instances: %v", err))
		return err
	}

	for i := range resourceSummaries.Items {
		rs := &resourceSummaries.Items[i]

		ns, ok := getClusterSummaryNamespaceFromResourceSummary(rs, logger)
		if !ok {
			continue
		}
		if ns != clusterNamespace {
			continue
		}

		err = c.Delete(ctx, rs)
		if err != nil {
			logger.V(logs.LogInfo).Info(
				fmt.Sprintf("failed to delete resourceSummary instance: %v", err))
			return err
		}
	}

	return nil
}

func deplAssociatedClusterExist(ctx context.Context, c client.Client, depl *appsv1.Deployment,
	logger logr.Logger) (exist bool, clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType) {

	if depl.Labels == nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("driftDetection %s/%s has no label",
			depl.Namespace, depl.Name))
		return true, "", "", ""
	}

	clusterNamespace, ok := depl.Labels[driftDetectionClusterNamespaceLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("driftDetection %s/%s has no %s label",
			depl.Namespace, depl.Name, driftDetectionClusterNamespaceLabel))
		return true, "", "", ""
	}

	clusterName, ok = depl.Labels[driftDetectionClusterNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("driftDetection %s/%s has no %s label",
			depl.Namespace, depl.Name, driftDetectionClusterNameLabel))
		return true, "", "", ""
	}

	clusterTypeString, ok := depl.Labels[driftDetectionClusterTypeLabel]
	if !ok {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("driftDetection %s/%s has no %s label",
			depl.Namespace, depl.Name, driftDetectionClusterTypeLabel))
		return true, "", "", ""
	}

	if strings.EqualFold(clusterTypeString, string(libsveltosv1beta1.ClusterTypeSveltos)) {
		clusterType = libsveltosv1beta1.ClusterTypeSveltos
	} else if strings.EqualFold(clusterTypeString, string(libsveltosv1beta1.ClusterTypeCapi)) {
		clusterType = libsveltosv1beta1.ClusterTypeCapi
	}

	_, err := clusterproxy.GetCluster(ctx, c, clusterNamespace, clusterName, clusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, clusterNamespace, clusterName, clusterType
		}
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get cluster %s:%s/%s: %v",
			clusterNamespace, clusterName, clusterTypeString, err))
	}

	return true, clusterNamespace, clusterName, clusterType
}

func convertPointerSliceToValueSlice(pointerSlice []*unstructured.Unstructured) []unstructured.Unstructured {
	valueSlice := make([]unstructured.Unstructured, len(pointerSlice))
	for i, ptr := range pointerSlice {
		if ptr != nil {
			valueSlice[i] = *ptr
		}
	}
	return valueSlice
}

// hasAnnotation returns true if the object has the specified annotation.
func hasAnnotation(o metav1.Object, annotation string) bool {
	annotations := o.GetAnnotations()
	if annotations == nil {
		return false
	}
	_, ok := annotations[annotation]
	return ok
}

func resourceToDeployedResource(resource *libsveltosv1beta1.Resource,
	deploymentType configv1beta1.DeploymentType) *configv1beta1.DeployedResource {

	return &configv1beta1.DeployedResource{
		Kind:            resource.Kind,
		Group:           resource.Group,
		Version:         resource.Version,
		DeploymentType:  deploymentType,
		Name:            resource.Name,
		Namespace:       resource.Namespace,
		LastAppliedTime: resource.LastAppliedTime,
	}
}

type ClusterSummaryDeletedError struct {
	Message string
}

func (r *ClusterSummaryDeletedError) Error() string {
	return r.Message
}
