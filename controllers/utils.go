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
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/pkg/errors"
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
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	"github.com/projectsveltos/libsveltos/lib/utils"
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

var (
	version string
)

type NonRetriableError struct {
	Message string
}

func (r *NonRetriableError) Error() string {
	return r.Message
}

func InitScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := configv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1alpha1.AddToScheme(s); err != nil {
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

func getPrefix(clusterType libsveltosv1alpha1.ClusterType) string {
	prefix := "capi"
	if clusterType == libsveltosv1alpha1.ClusterTypeSveltos {
		prefix = "sveltos"
	}
	return prefix
}

// GetClusterSummaryName returns the ClusterSummary name given a ClusterProfile/Profile kind/name and
// cluster type/Name.
func GetClusterSummaryName(profileKind, profileName, clusterName string, isSveltosCluster bool) string {
	clusterType := libsveltosv1alpha1.ClusterTypeCapi
	if isSveltosCluster {
		clusterType = libsveltosv1alpha1.ClusterTypeSveltos
	}
	prefix := getPrefix(clusterType)
	if profileKind == configv1alpha1.ClusterProfileKind {
		// For backward compatibility (code before addition of Profiles) do not change this
		return fmt.Sprintf("%s-%s-%s", profileName, prefix, clusterName)
	}

	return fmt.Sprintf("p--%s-%s-%s", profileName, prefix, clusterName)
}

// getClusterSummary returns the ClusterSummary instance created by a specific
// ClusterProfile/Profile for a specific Cluster
func getClusterSummary(ctx context.Context, c client.Client,
	profileKind, profileName string, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType) (*configv1alpha1.ClusterSummary, error) {

	profileLabel := ClusterProfileLabelName
	if profileKind == configv1alpha1.ProfileKind {
		profileLabel = ProfileLabelName
	}

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			profileLabel:                    profileName,
			configv1alpha1.ClusterNameLabel: clusterName,
			configv1alpha1.ClusterTypeLabel: string(clusterType),
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := c.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1alpha1.GroupVersion.Group, Resource: configv1alpha1.ClusterSummaryKind}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s %s",
			clusterNamespace, clusterName, profileKind, profileName)
	}

	return &clusterSummaryList.Items[0], nil
}

// getClusterConfiguration returns the ClusterConfiguration instance for a specific CAPI Cluster
func getClusterConfiguration(ctx context.Context, c client.Client,
	clusterNamespace, clusterConfigurationName string) (*configv1alpha1.ClusterConfiguration, error) {

	clusterConfiguration := &configv1alpha1.ClusterConfiguration{}
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

func getClusterReportName(profileKind, profileName, clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
	// TODO: shorten this value
	prefix := "" // For backward compatibility (before addition of Profile) leave this empty for ClusterProfiles
	if profileKind == configv1alpha1.ProfileKind {
		prefix = "p--"
	}
	return prefix + profileName + nameSeparator + strings.ToLower(string(clusterType)) +
		nameSeparator + clusterName
}

func getClusterConfigurationName(clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
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

// collectMgmtResources collects clusterSummary.Spec.ClusterProfileSpec.MgmtClusterResources
// from management cluster
func collectMgmtResources(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
) (map[string]*unstructured.Unstructured, error) {

	if clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs == nil {
		return nil, nil
	}

	restConfig := getManagementClusterConfig()

	result := make(map[string]*unstructured.Unstructured)
	for i := range clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs {
		ref := &clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs[i]
		// If namespace is not defined, default to cluster namespace
		namespace := ref.Resource.Namespace
		if namespace == "" {
			namespace = clusterSummary.Namespace
		}
		dr, err := utils.GetDynamicResourceInterface(restConfig, ref.Resource.GroupVersionKind(), namespace)
		if err != nil {
			return nil, err
		}

		instantiatedName, err := getMgmtResourceName(clusterSummary, ref)
		if err != nil {
			return nil, err
		}

		var u *unstructured.Unstructured
		u, err = dr.Get(ctx, instantiatedName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}

		result[ref.Identifier] = u
	}

	return result, nil
}

// Resources referenced in the management cluster can have their name expressed in function
// of cluster information (clusterNamespace, clusterName, clusterType)
func getMgmtResourceName(clusterSummary *configv1alpha1.ClusterSummary,
	ref *configv1alpha1.TemplateResourceRef) (string, error) {

	// Accept name that are templates
	templateName := getTemplateName(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		string(clusterSummary.Spec.ClusterType))
	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(sprig.FuncMap()).Parse(ref.Resource.Name)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer

	if err := tmpl.Execute(&buffer,
		struct{ ClusterNamespace, ClusterName string }{
			ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
			ClusterName:      clusterSummary.Spec.ClusterName}); err != nil {
		return "", errors.Wrapf(err, "error executing template")
	}
	return buffer.String(), nil
}

// isCluterSummaryProvisioned returns true if ClusterSummary is currently fully deployed.
func isCluterSummaryProvisioned(clusterSumary *configv1alpha1.ClusterSummary) bool {
	hasHelmCharts := false
	hasRawYAMLs := false
	hasKustomize := false

	if clusterSumary.Spec.ClusterProfileSpec.HelmCharts != nil &&
		len(clusterSumary.Spec.ClusterProfileSpec.HelmCharts) != 0 {

		hasHelmCharts = true
	}

	if clusterSumary.Spec.ClusterProfileSpec.PolicyRefs != nil &&
		len(clusterSumary.Spec.ClusterProfileSpec.PolicyRefs) != 0 {

		hasRawYAMLs = true
	}

	if clusterSumary.Spec.ClusterProfileSpec.KustomizationRefs != nil &&
		len(clusterSumary.Spec.ClusterProfileSpec.KustomizationRefs) != 0 {

		hasKustomize = true
	}

	deployedHelmCharts := false
	deployedRawYAMLs := false
	deployedKustomize := false

	for i := range clusterSumary.Status.FeatureSummaries {
		fs := &clusterSumary.Status.FeatureSummaries[i]
		if fs.Status != configv1alpha1.FeatureStatusProvisioned {
			return false
		}
		switch fs.FeatureID {
		case configv1alpha1.FeatureHelm:
			deployedHelmCharts = true
		case configv1alpha1.FeatureResources:
			deployedRawYAMLs = true
		case configv1alpha1.FeatureKustomize:
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

func SetVersion(v string) {
	version = v
}

func getVersion() string {
	return version
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
