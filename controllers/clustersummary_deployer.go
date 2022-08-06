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
	"reflect"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

type getCurrentHash func(cctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error)

type feature struct {
	id          configv1alpha1.FeatureID
	currentHash getCurrentHash
	deploy      deployer.RequestHandler
}

func (r *ClusterSummaryReconciler) deployFeature(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature, logger logr.Logger) error {

	clusterSummary := clusterSummaryScope.ClusterSummary

	logger = logger.WithValues("clusternamespace", clusterSummary.Spec.ClusterNamespace,
		"clustername", clusterSummary.Spec.ClusterNamespace,
		"applicant", clusterSummary.Name,
		"feature", string(f.id))
	logger.V(logs.LogDebug).Info("request to deploy")

	// If undeploying feature is in progress, wait for it to complete.
	// Otherwise, if we redeploy feature while same feature is still being cleaned up, if two workers process those request in
	// parallel some resources might end up missing.
	if r.Deployer.IsInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(f.id), true) {

		logger.V(logs.LogDebug).Info("cleanup is in progress")
		return fmt.Errorf("cleanup of %s still in progress. Wait before redeploying", string(f.id))
	}

	// Get hash of current configuration (at this very precise moment)
	currentHash, err := f.currentHash(ctx, r.Client, clusterSummaryScope, logger)
	if err != nil {
		return err
	}
	hash := r.getHash(clusterSummaryScope, f.id)
	isConfigSame := reflect.DeepEqual(hash, currentHash)
	if !isConfigSame {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("configuration has changed. Current hash %q. Previous hash %q",
			currentHash, hash))
	}

	deployed := r.isFeatureDeployed(clusterSummaryScope, f.id)
	if deployed && isConfigSame {
		// feature is deployed and nothing has changed. Nothing to do.
		logger.V(logs.LogDebug).Info("feature is deployed and hash has not changed")
		return nil
	}

	var status *configv1alpha1.FeatureStatus
	var resultError error

	// Feature is not deployed yet
	if isConfigSame {
		logger.V(logs.LogDebug).Info("hash has not changed")
		result := r.Deployer.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(f.id), false)
		status = r.convertResultStatus(result)
		resultError = result.Err
	}

	if status != nil {
		logger.V(logs.LogDebug).Info("result is available. updating status.")
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, currentHash, resultError, logger)
		if *status == configv1alpha1.FeatureStatusProvisioned {
			return nil
		}
		if *status == configv1alpha1.FeatureStatusProvisioning {
			return fmt.Errorf("feature is still being provisioned")
		}
	} else {
		logger.V(logs.LogDebug).Info("no result is available. mark status as provisioning")
		s := configv1alpha1.FeatureStatusProvisioning
		status = &s
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, currentHash, nil, logger)
	}

	// Getting here means either feature failed to be deployed or configuration has changed.
	// Feature must be (re)deployed.
	logger.V(logs.LogDebug).Info("queueing request to deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), false, f.deploy); err != nil {
		return err
	}

	return fmt.Errorf("request is queued")
}

func (r *ClusterSummaryReconciler) undeployFeature(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature, logger logr.Logger) error {

	clusterSummary := clusterSummaryScope.ClusterSummary

	logger = logger.WithValues("clusternamespace", clusterSummary.Spec.ClusterNamespace,
		"clustername", clusterSummary.Spec.ClusterNamespace,
		"applicant", clusterSummary.Name)
	logger.V(logs.LogDebug).Info("request to un-deploy")

	// If deploying feature is in progress, wait for it to complete.
	// Otherwise, if we cleanup feature while same feature is still being provisioned, if two workers process those request in
	// parallel some resources might be left over.
	if r.Deployer.IsInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(f.id), false) {

		logger.V(logs.LogDebug).Info("provisioning is in progress")
		return fmt.Errorf("deploying %s still in progress. Wait before cleanup", string(f.id))
	}

	if r.isFeatureRemoved(clusterSummaryScope, f.id) {
		logger.V(logs.LogDebug).Info("feature is removed")
		// feature is removed. Nothing to do.
		return nil
	}

	result := r.Deployer.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummaryScope.Name(), string(f.id), true)
	status := r.convertResultStatus(result)

	if status != nil {
		if *status == configv1alpha1.FeatureStatusProvisioning {
			s := configv1alpha1.FeatureStatusRemoving
			status = &s
			r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
			return fmt.Errorf("feature is still being removed")
		}
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
		if *status == configv1alpha1.FeatureStatusRemoved {
			return nil
		}
	} else {
		logger.V(logs.LogDebug).Info("no result is available. mark status as removing")
		s := configv1alpha1.FeatureStatusRemoving
		status = &s
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, nil, logger)
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), true, f.deploy); err != nil {
		return err
	}

	return fmt.Errorf("cleanup request is queued")
}

// isFeatureDeployed returns true if feature is marked as deployed (present in FeatureSummaries and status
// is set to Provisioned).
func (r *ClusterSummaryReconciler) isFeatureDeployed(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1alpha1.FeatureID) bool {

	clusterSummary := clusterSummaryScope.ClusterSummary

	for i := range clusterSummary.Status.FeatureSummaries {
		fs := clusterSummary.Status.FeatureSummaries[i]
		if fs.FeatureID == featureID {
			if fs.Status == configv1alpha1.FeatureStatusProvisioned {
				return true
			}
		}
	}
	return false
}

// isFeatureRemoved returns true if feature is marked as removed (present in FeatureSummaries and status
// is set to Removed).
func (r *ClusterSummaryReconciler) isFeatureRemoved(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1alpha1.FeatureID) bool {

	clusterSummary := clusterSummaryScope.ClusterSummary

	for i := range clusterSummary.Status.FeatureSummaries {
		fs := clusterSummary.Status.FeatureSummaries[i]
		if fs.FeatureID == featureID {
			if fs.Status == configv1alpha1.FeatureStatusRemoved {
				return true
			}
		}
	}
	return false
}

// getHash returns, if available, the hash corresponding to the featureID configuration last time it
// was processed.
func (r *ClusterSummaryReconciler) getHash(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1alpha1.FeatureID) []byte {

	clusterSummary := clusterSummaryScope.ClusterSummary

	for i := range clusterSummary.Status.FeatureSummaries {
		fs := clusterSummary.Status.FeatureSummaries[i]
		if fs.FeatureID == featureID {
			return fs.Hash
		}
	}
	return nil
}

func (r *ClusterSummaryReconciler) updateFeatureStatus(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1alpha1.FeatureID, status *configv1alpha1.FeatureStatus, hash []byte, statusError error,
	logger logr.Logger) {

	if status == nil {
		return
	}
	logger = logger.WithValues("hash", hash, "status", *status)
	logger.V(logs.LogDebug).Info("updating clustersummary status")
	switch *status {
	case configv1alpha1.FeatureStatusProvisioned:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusProvisioned, hash)
		clusterSummaryScope.SetFailureMessage(featureID, nil)
	case configv1alpha1.FeatureStatusRemoved:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusRemoved, hash)
		clusterSummaryScope.SetFailureMessage(featureID, nil)
	case configv1alpha1.FeatureStatusProvisioning:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusProvisioning, hash)
	case configv1alpha1.FeatureStatusRemoving:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusRemoving, hash)
	case configv1alpha1.FeatureStatusFailed:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusFailed, hash)
		err := statusError.Error()
		clusterSummaryScope.SetFailureMessage(featureID, &err)
	}
}

func (r *ClusterSummaryReconciler) convertResultStatus(result deployer.Result) *configv1alpha1.FeatureStatus {
	switch result.ResultStatus {
	case deployer.Deployed:
		s := configv1alpha1.FeatureStatusProvisioned
		return &s
	case deployer.Failed:
		s := configv1alpha1.FeatureStatusFailed
		return &s
	case deployer.InProgress:
		s := configv1alpha1.FeatureStatusProvisioning
		return &s
	case deployer.Removed:
		s := configv1alpha1.FeatureStatusRemoved
		return &s
	case deployer.Unavailable:
		return nil
	}

	return nil
}
