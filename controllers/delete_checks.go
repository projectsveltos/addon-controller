/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

	"github.com/go-logr/logr"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func validatePreDeleteChecks(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID, logger logr.Logger) error {

	l := logger.WithValues("check", "pre-delete")
	return validateDeleteChecks(ctx, clusterSummary, featureID,
		clusterSummary.Spec.ClusterProfileSpec.PreDeleteChecks, l)
}

func validatePostDeleteChecks(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID, logger logr.Logger) error {

	l := logger.WithValues("check", "post-delete")
	return validateDeleteChecks(ctx, clusterSummary, featureID,
		clusterSummary.Spec.ClusterProfileSpec.PostDeleteChecks, l)
}

func validateDeleteChecks(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID, deleteChecks []libsveltosv1beta1.ValidateHealth, logger logr.Logger) error {

	if len(deleteChecks) == 0 {
		return nil
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		logger.V(logs.LogDebug).Error(err, "failed to verify if Cluster is in pull mode")
		return err
	}

	// In pull mode this check is done by Sveltos-applier
	if isPullMode {
		return nil
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	cacheMgr := clustercache.GetManager()
	remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, adminNamespace,
		adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		logger.V(logs.LogDebug).Error(err, "failed to get cluster rest.Config")
		return err
	}

	logger.V(logs.LogDebug).Info("validate delete checks")
	err = clusterops.ValidateHealthPolicies(ctx, remoteRestConfig,
		clusterSummary.Spec.ClusterProfileSpec.PreDeleteChecks, featureID, true, logger)
	if err != nil {
		logger.V(logs.LogDebug).Error(err, "delete check failed")
		return err
	}

	return nil
}
