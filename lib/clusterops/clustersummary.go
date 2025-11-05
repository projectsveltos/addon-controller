/*
Copyright 2025. projectsveltos.io. All rights reserved.

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

package clusterops

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// ClusterSummaryLabelName is added to each policy deployed by a ClusterSummary
	// instance to a CAPI Cluster
	ClusterSummaryLabelName = "projectsveltos.io/cluster-summary-name"

	// ClusterProfileLabelName is added to all ClusterSummary instances created
	// by a ClusterProfile instance
	ClusterProfileLabelName = "projectsveltos.io/cluster-profile-name"

	// ProfileLabelName is added to all ClusterSummary instances created
	// by a Profile instance
	ProfileLabelName = "projectsveltos.io/profile-name"
)

// GetClusterSummary returns the ClusterSummary instance created by a specific
// ClusterProfile/Profile for a specific Cluster
func GetClusterSummary(ctx context.Context, c client.Client,
	profileKind, profileName string, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) (*configv1beta1.ClusterSummary, error) {

	profileLabel := ClusterProfileLabelName
	if profileKind == configv1beta1.ProfileKind {
		profileLabel = ProfileLabelName
	}

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			profileLabel:                   profileName,
			configv1beta1.ClusterNameLabel: clusterName,
			configv1beta1.ClusterTypeLabel: string(clusterType),
		},
	}

	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	if err := c.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1beta1.GroupVersion.Group, Resource: configv1beta1.ClusterSummaryKind}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s %s",
			clusterNamespace, clusterName, profileKind, profileName)
	}

	return &clusterSummaryList.Items[0], nil
}

// GetClusterSummaryName returns the ClusterSummary name given a ClusterProfile/Profile kind/name and
// cluster type/Name.
func GetClusterSummaryName(profileKind, profileName, clusterName string, isSveltosCluster bool) string {
	clusterType := libsveltosv1beta1.ClusterTypeCapi
	if isSveltosCluster {
		clusterType = libsveltosv1beta1.ClusterTypeSveltos
	}
	prefix := GetPrefix(clusterType)
	if profileKind == configv1beta1.ClusterProfileKind {
		// For backward compatibility (code before addition of Profiles) do not change this
		return fmt.Sprintf("%s-%s-%s", profileName, prefix, clusterName)
	}

	return fmt.Sprintf("p--%s-%s-%s", profileName, prefix, clusterName)
}

func GetPrefix(clusterType libsveltosv1beta1.ClusterType) string {
	prefix := "capi"
	if clusterType == libsveltosv1beta1.ClusterTypeSveltos {
		prefix = "sveltos"
	}
	return prefix
}
