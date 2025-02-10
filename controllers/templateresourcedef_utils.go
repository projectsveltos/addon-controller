/*
Copyright 2024. projectsveltos.io. All rights reserved.

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
	"text/template"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/funcmap"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

// The TemplateResource namespace can be specified or it will inherit the cluster namespace
func getTemplateResourceNamespace(clusterSummary *configv1beta1.ClusterSummary,
	ref *configv1beta1.TemplateResourceRef) (string, error) {

	namespace := ref.Resource.Namespace
	if namespace == "" {
		// Use cluster namespace
		return clusterSummary.Spec.ClusterNamespace, nil
	}

	// Accept namespaces that are templates
	templateName := getTemplateName(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		string(clusterSummary.Spec.ClusterType))
	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(
		funcmap.SveltosFuncMap(funcmap.HasTextTemplateAnnotation(clusterSummary.Annotations))).Parse(ref.Resource.Namespace)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer

	// Cluster namespace and name can be used to instantiate the name of the resource that
	// needs to be fetched from the management cluster. Defined an unstructured with namespace and name set
	u := &unstructured.Unstructured{}
	u.SetNamespace(clusterSummary.Spec.ClusterNamespace)
	u.SetName(clusterSummary.Spec.ClusterName)
	u.SetKind(string(clusterSummary.Spec.ClusterType))

	if err := tmpl.Execute(&buffer,
		struct {
			Cluster map[string]interface{}
			// deprecated. This used to be original format which was different than rest of templating
			ClusterNamespace, ClusterName string
		}{
			Cluster:          u.UnstructuredContent(),
			ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
			ClusterName:      clusterSummary.Spec.ClusterName}); err != nil {
		return "", fmt.Errorf("error executing template: %w", err)
	}
	return buffer.String(), nil
}

// Resources referenced in the management cluster can have their name expressed in function
// of cluster information:
// clusterNamespace => .Cluster.metadata.namespace
// clusterName => .Cluster.metadata.name
// clusterType => .Cluster.kind
func getTemplateResourceName(clusterSummary *configv1beta1.ClusterSummary,
	ref *configv1beta1.TemplateResourceRef) (string, error) {

	// Accept name that are templates
	templateName := getTemplateName(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		string(clusterSummary.Spec.ClusterType))
	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(
		funcmap.SveltosFuncMap(funcmap.HasTextTemplateAnnotation(clusterSummary.Annotations))).Parse(ref.Resource.Name)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer

	// Cluster namespace and name can be used to instantiate the name of the resource that
	// needs to be fetched from the management cluster. Defined an unstructured with namespace and name set
	u := &unstructured.Unstructured{}
	u.SetNamespace(clusterSummary.Spec.ClusterNamespace)
	u.SetName(clusterSummary.Spec.ClusterName)
	u.SetKind(string(clusterSummary.Spec.ClusterType))

	if err := tmpl.Execute(&buffer,
		struct {
			Cluster map[string]interface{}
			// deprecated. This used to be original format which was different than rest of templating
			ClusterNamespace, ClusterName string
		}{
			Cluster:          u.UnstructuredContent(),
			ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
			ClusterName:      clusterSummary.Spec.ClusterName}); err != nil {
		return "", fmt.Errorf("error executing template: %w", err)
	}
	return buffer.String(), nil
}

// collectTemplateResourceRefs collects clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs
// from management cluster
func collectTemplateResourceRefs(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
) (map[string]*unstructured.Unstructured, error) {

	if clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs == nil {
		return nil, nil
	}

	restConfig := getManagementClusterConfig()

	result := make(map[string]*unstructured.Unstructured)
	for i := range clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs {
		ref := clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs[i]
		var err error
		ref.Resource.Namespace, err = getTemplateResourceNamespace(clusterSummary, &ref)
		if err != nil {
			return nil, err
		}
		ref.Resource.Name, err = getTemplateResourceName(clusterSummary, &ref)
		if err != nil {
			return nil, err
		}

		dr, err := k8s_utils.GetDynamicResourceInterface(restConfig, ref.Resource.GroupVersionKind(), ref.Resource.Namespace)
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
