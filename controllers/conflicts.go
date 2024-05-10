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
	"fmt"
	"strconv"

	"github.com/go-logr/logr"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// Any Helm chart can be managed by only one ClusterProfile/Profile instance
// Any Kubernetes resources can be managed by only one ClusterProfile/Profile instance
// A conflict arises when a ClusterProfile or Profile tries to manage a resource (chart
// or Kubernetes object) already controlled by another. These conflicts are resolved based on tier.
// Each ClusterProfile or Profile belongs to a specific tier, with lower tiers taking precedence.
// In case of a tie (same tier), the existing owner (the Profile/ClusterProfile currently managing
// the resource) retains control.

// Determines if a ClusterProfile/Profile can take ownership of a resource
// currently managed by another ClusterProfile/Profile.
// This function considers the tier of the claiming ClusterProfile/Profile
// (represented by 'claimingTier') and the current owner information
// (`currentOwner`). Ownership can be transferred if the claiming profile
// belongs to a lower tier than the current owner. In case of tiers being
// equal, the function returns false to maintain the current ownership.
//
// Args:
//
//	currentOwnerTier:  The tier of the ClusterProfile/Profile currently with ownership
//	claimingTier: The tier of the ClusterProfile/Profile trying to take ownership
//
// Returns:
//   - true if the claiming ClusterProfile/Profile has higher ownership priority (lower tier),
//   - false otherwise.
func hasHigherOwnershipPriority(currentOwnerTier, claimingTier int32) bool {
	return claimingTier < currentOwnerTier
}

func getTier(tierData string) int32 {
	value, err := strconv.ParseInt(tierData, 10, 32)
	if err != nil {
		// Return tier max value
		return 1<<31 - 1
	}

	// Tier minimun value is 1. So this indicates a
	// corrupted value.
	if value == 0 {
		// Return tier max value
		return 1<<31 - 1
	}

	return int32(value)
}

// Resource Ownership change. Queue old owner for reconciliation
func requeueOldOwner(ctx context.Context, featureID configv1alpha1.FeatureID, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	c := getManagementClusterClient()

	clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
		Client:         c,
		Logger:         logger,
		ClusterSummary: clusterSummary,
		ControllerName: "clustersummary",
	})

	if err != nil {
		return err
	}

	// Reset the hash a deployment happens again
	logger.V(logs.LogDebug).Info(fmt.Sprintf("reset status of ClusterSummary %s/%s",
		clusterSummary.Namespace, clusterSummary.Name))
	clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusProvisioning, nil)

	return c.Status().Update(ctx, clusterSummaryScope.ClusterSummary)
}
