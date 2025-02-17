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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	"github.com/projectsveltos/libsveltos/lib/deployer"
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

// Requeue a ClusterSummary for reconciliation. This method resets status causing reconciliation to happen
func requeueClusterSummary(ctx context.Context, featureID configv1beta1.FeatureID,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	c := getManagementClusterClient()

	// TODO: change deployer to have an initialize method and a GetDeployer
	// At this point deployer has been already initialized, so the argurment of GetClient are not important
	d := deployer.GetClient(ctx, logger, c, 1)

	if d.IsInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(featureID), clusterSummary.Spec.ClusterType, false) {

		logger.V(logs.LogDebug).Info(fmt.Sprintf("profile %s:%s/%s deploying is in progress",
			clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName))
		// If a profile with conflict is still in progress return an error.
		// Let's consider an example:
		// 1. profile 1 (with tier 100) is deploying a resource (helm chart for instance)
		// 2. profile 2 (with tier 10) gets deployed while #1 is in progress
		// 3. profile 2 wins the conflict, so this method is invoked
		// 4. Even if we were to requeue profile 1 at this point, if deploying for profile 1
		// was in progress and completed before the end of this method, profile 1 at next reconciliation
		// would mark itself as Provisioned (nothing changed for profile 1 and profile 1 was able to
		// find in deployer a successful result)
		//
		// So return an error here
		return fmt.Errorf("profile with conflict is still in progress")
	}

	// Result here does not matter. This call is just to make sure if a result is available, to get it
	// and so remove it.
	// As explained in the example above, we need to make sure profile 1, once requeued, goes through
	// deployment process again. Otherwise if a result was available (a successful one) even requeuing
	// profile 1 would end with profile 1 marking itself as provisioned
	_ = d.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(featureID), clusterSummary.Spec.ClusterType, false)

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
	clusterSummaryScope.SetFeatureStatus(featureID, configv1beta1.FeatureStatusProvisioning, nil, nil)

	return c.Status().Update(ctx, clusterSummaryScope.ClusterSummary)
}
