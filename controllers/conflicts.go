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

	"github.com/go-logr/logr"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// Requeue a ClusterSummary for reconciliation. This method resets status causing reconciliation to happen
func requeueClusterSummary(ctx context.Context, featureID libsveltosv1beta1.FeatureID,
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
	clusterSummaryScope.SetFeatureStatus(featureID, libsveltosv1beta1.FeatureStatusProvisioning, nil, nil)

	return c.Status().Update(ctx, clusterSummaryScope.ClusterSummary)
}
