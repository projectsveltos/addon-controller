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
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

// The TemplateResource namespace can be specified or it will inherit the cluster namespace
func getTemplateResourceNamespace(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	ref *configv1beta1.TemplateResourceRef) (string, error) {

	return libsveltostemplate.GetReferenceResourceNamespace(ctx, getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, ref.Resource.Namespace,
		clusterSummary.Spec.ClusterType)
}

// Resources referenced in the management cluster can have their name expressed in function
// of cluster field
func getTemplateResourceName(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	ref *configv1beta1.TemplateResourceRef) (string, error) {

	return libsveltostemplate.GetReferenceResourceName(ctx, getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, ref.Resource.Name,
		clusterSummary.Spec.ClusterType)
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
		ref.Resource.Namespace, err = getTemplateResourceNamespace(ctx, clusterSummary, &ref)
		if err != nil {
			return nil, err
		}
		ref.Resource.Name, err = getTemplateResourceName(ctx, clusterSummary, &ref)
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
			if apierrors.IsNotFound(err) && ref.Optional {
				continue
			}
			return nil, err
		}

		if ref.IgnoreStatusChanges {
			unstructured.RemoveNestedField(u.Object, "status")
		}

		result[ref.Identifier] = u
	}

	return result, nil
}
