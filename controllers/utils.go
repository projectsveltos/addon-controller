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
	"fmt"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

const separator = "--"

// GetClusterSummaryName returns the ClusterSummary name given a ClusterFeature name and
// CAPI cluster Namespace/Name
func GetClusterSummaryName(clusterFeatureName, clusterNamespace, clusterName string) string {
	return fmt.Sprintf("%s%s%s%s%s", clusterFeatureName, separator, clusterNamespace, separator, clusterName)
}

// getClusterFeatureOwner returns the ClusterFeature owning this clusterSummary.
// Returns nil if ClusterFeature does not exist anymore.
func getClusterFeatureOwner(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary) (*configv1alpha1.ClusterFeature, error) {
	for _, ref := range clusterSummary.OwnerReferences {
		if ref.Kind != "ClusterFeature" {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if gv.Group == configv1alpha1.GroupVersion.Group {
			clusterFeature := &configv1alpha1.ClusterFeature{}
			err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, clusterFeature)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, nil
				}
				return nil, err
			}
			return clusterFeature, nil
		}
	}
	return nil, nil
}
