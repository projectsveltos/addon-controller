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
	"strings"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

//+kubebuilder:rbac:groups=extension.projectsveltos.io,resources=yttsources,verbs=get;list;watch
//+kubebuilder:rbac:groups=extension.projectsveltos.io,resources=jsonnetsources,verbs=get;list;watch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=debuggingconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;impersonate

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

// GetClusterSummaryName returns the ClusterSummary name given a ClusterProfile name and
// CAPI cluster Namespace/Name.
// This method does not guarantee that name is not already in use. Caller of this method needs
// to handle that scenario
func GetClusterSummaryName(clusterProfileName, clusterName string, isSveltosCluster bool) string {
	prefix := "capi"
	if isSveltosCluster {
		prefix = "sveltos"
	}
	return fmt.Sprintf("%s-%s-%s", clusterProfileName, prefix, clusterName)
}

// getClusterSummary returns the ClusterSummary instance created by a specific ClusterProfile for a specific
// CAPI Cluster
func getClusterSummary(ctx context.Context, c client.Client,
	clusterProfileName, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType) (*configv1alpha1.ClusterSummary, error) {

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			ClusterProfileLabelName:         clusterProfileName,
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
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s",
			clusterNamespace, clusterName, clusterProfileName)
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

func getClusterReportName(clusterProfileName, clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
	// TODO: shorten this value
	return clusterProfileName + "--" + strings.ToLower(string(clusterType)) + "--" + clusterName
}

func getClusterConfigurationName(clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
	// TODO: shorten this value
	return strings.ToLower(string(clusterType)) + "--" + clusterName
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
		ref := clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs[i]
		// If namespace is not defined, default to cluster namespace
		namespace := ref.Resource.Namespace
		if namespace == "" {
			namespace = clusterSummary.Namespace
		}
		dr, err := utils.GetDynamicResourceInterface(restConfig, ref.Resource.GroupVersionKind(), namespace)
		if err != nil {
			return nil, err
		}

		var u *unstructured.Unstructured
		u, err = dr.Get(ctx, ref.Resource.Name, metav1.GetOptions{})
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
