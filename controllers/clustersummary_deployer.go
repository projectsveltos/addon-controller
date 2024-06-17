/*
Copyright 2022-24. projectsveltos.io. All rights reserved.

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
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	driftDetectionInMgtmCluster = "driftDetectionInMgtmCluster"
)

func startDriftDetectionInMgmtCluster(o deployer.Options) bool {
	if o.HandlerOptions == nil {
		return false
	}

	runInMgtmCluster := false
	if _, ok := o.HandlerOptions[driftDetectionInMgtmCluster]; ok {
		runInMgtmCluster = true
	}

	return runInMgtmCluster
}

type getCurrentHash func(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error)

type getPolicyRefs func(clusterSummary *configv1beta1.ClusterSummary) []configv1beta1.PolicyRef

type feature struct {
	id          configv1beta1.FeatureID
	currentHash getCurrentHash
	deploy      deployer.RequestHandler
	undeploy    deployer.RequestHandler
	getRefs     getPolicyRefs
}

func (r *ClusterSummaryReconciler) deployFeature(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature, logger logr.Logger) error {

	clusterSummary := clusterSummaryScope.ClusterSummary

	logger = logger.WithValues("clusternamespace", clusterSummary.Spec.ClusterNamespace,
		"clustername", clusterSummary.Spec.ClusterNamespace,
		"applicant", clusterSummary.Name,
		"feature", string(f.id))
	logger.V(logs.LogDebug).Info("request to deploy")

	r.Deployer.CleanupEntries(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(f.id), clusterSummary.Spec.ClusterType, true)

	// If undeploying feature is in progress, wait for it to complete.
	// Otherwise, if we redeploy feature while same feature is still being cleaned up, if two workers process those request in
	// parallel some resources might end up missing.
	if r.Deployer.IsInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(f.id), clusterSummary.Spec.ClusterType, true) {

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
		logger.V(logs.LogDebug).Info(fmt.Sprintf("configuration has changed. Current hash %x. Previous hash %x",
			currentHash, hash))
	}

	if !r.shouldRedeploy(clusterSummaryScope, f, isConfigSame, logger) {
		logger.V(logs.LogDebug).Info("no need to redeploy")
		return nil
	}

	var status *configv1beta1.FeatureStatus
	var resultError error

	// Feature is not deployed yet
	if isConfigSame {
		logger.V(logs.LogDebug).Info("hash has not changed")
		result := r.Deployer.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, false)
		status = r.convertResultStatus(result)
		resultError = result.Err
	}

	if status != nil {
		logger.V(logs.LogDebug).Info("result is available. updating status.")
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, currentHash, resultError, logger)
		if *status == configv1beta1.FeatureStatusProvisioned {
			return nil
		}
		if resultError != nil {
			// Check if error is a NonRetriableError type
			var nonRetriableError *NonRetriableError
			if errors.As(resultError, &nonRetriableError) {
				nonRetriableStatus := configv1beta1.FeatureStatusFailedNonRetriable
				r.updateFeatureStatus(clusterSummaryScope, f.id, &nonRetriableStatus, currentHash, resultError, logger)
				return nil
			}
		}
		if *status == configv1beta1.FeatureStatusProvisioning {
			return fmt.Errorf("feature is still being provisioned")
		}
	} else {
		logger.V(logs.LogDebug).Info("no result is available. mark status as provisioning")
		s := configv1beta1.FeatureStatusProvisioning
		status = &s
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, currentHash, nil, logger)
	}

	// Getting here means either feature failed to be deployed or configuration has changed.
	// Feature must be (re)deployed.
	options := deployer.Options{HandlerOptions: map[string]string{}}
	if r.AgentInMgmtCluster {
		options.HandlerOptions[driftDetectionInMgtmCluster] = "management"
	}

	logger.V(logs.LogDebug).Info("queueing request to deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, false,
		genericDeploy, programDuration, options); err != nil {
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, currentHash, err, logger)
		return err
	}

	return fmt.Errorf("request is queued")
}

func genericDeploy(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1beta1.ClusterType,
	o deployer.Options, logger logr.Logger) error {

	// Code common to all features
	// Feature specific code (featureHandler.deploy is invoked)
	// Code common to all features

	// Before any per feature specific code

	// Invoking per feature specific code
	featureHandler := getHandlersForFeature(configv1beta1.FeatureID(featureID))
	err := featureHandler.deploy(ctx, c, clusterNamespace, clusterName, applicant, featureID, clusterType, o, logger)
	if err != nil {
		return err
	}

	// After any per feature specific code

	return nil
}

func (r *ClusterSummaryReconciler) undeployFeature(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature, logger logr.Logger) error {

	clusterSummary := clusterSummaryScope.ClusterSummary

	logger = logger.WithValues("clusternamespace", clusterSummary.Spec.ClusterNamespace,
		"clustername", clusterSummary.Spec.ClusterNamespace,
		"applicant", clusterSummary.Name,
		"feature", string(f.id))
	logger.V(logs.LogDebug).Info("request to un-deploy")

	r.Deployer.CleanupEntries(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(f.id), clusterSummary.Spec.ClusterType, false)

	// If deploying feature is in progress, wait for it to complete.
	// Otherwise, if we cleanup feature while same feature is still being provisioned, if two workers process those request in
	// parallel some resources might be left over.
	if r.Deployer.IsInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(f.id), clusterSummary.Spec.ClusterType, false) {

		logger.V(logs.LogDebug).Info("provisioning is in progress")
		return fmt.Errorf("deploying %s still in progress. Wait before cleanup", string(f.id))
	}

	if r.isFeatureRemoved(clusterSummaryScope.ClusterSummary, f.id) {
		logger.V(logs.LogDebug).Info("feature is removed")
		// feature is removed. Nothing to do.
		return nil
	}

	result := r.Deployer.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummaryScope.Name(), string(f.id), clusterSummary.Spec.ClusterType, true)
	status := r.convertResultStatus(result)

	if status != nil {
		if *status == configv1beta1.FeatureStatusProvisioning {
			s := configv1beta1.FeatureStatusRemoving
			status = &s
			r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
			return fmt.Errorf("feature is still being removed")
		}

		// Failure to undeploy because of missing permission is ignored.
		if apierrors.IsForbidden(result.Err) {
			logger.V(logs.LogInfo).Info("undeploying failing because of missing permission.")
			tmpStatus := configv1beta1.FeatureStatusRemoved
			status = &tmpStatus
			r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
			return nil
		}

		r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
		if *status == configv1beta1.FeatureStatusRemoved {
			return nil
		}
	} else {
		logger.V(logs.LogDebug).Info("no result is available. mark status as removing")
		s := configv1beta1.FeatureStatusRemoving
		status = &s
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, nil, logger)
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, true, genericUndeploy, programDuration, deployer.Options{}); err != nil {
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, err, logger)
		return err
	}

	return fmt.Errorf("cleanup request is queued")
}

func genericUndeploy(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	clusterType libsveltosv1beta1.ClusterType, o deployer.Options, logger logr.Logger) error {

	// Code common to all features
	// Feature specific code (featureHandler.undeploy is invoked)
	// Code common to all features

	// Before any per feature specific code

	var err error
	_, err = clusterproxy.GetCluster(ctx, c, clusterNamespace, clusterName, clusterType)

	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("Cluster %s/%s not found. Nothing to cleanup", clusterNamespace, clusterName))
			return nil
		}
		return err
	}

	// Invoking per feature specific code
	featureHandler := getHandlersForFeature(configv1beta1.FeatureID(featureID))
	if err := featureHandler.undeploy(ctx, c, clusterNamespace, clusterName, applicant, featureID, clusterType, o, logger); err != nil {
		return err
	}

	// After any per feature specific code

	return nil
}

// isFeatureStatusPresent returns true if feature status is set.
// That means feature was deployed/being deployed
func (r *ClusterSummaryReconciler) isFeatureStatusPresent(clusterSummary *configv1beta1.ClusterSummary,
	featureID configv1beta1.FeatureID) bool {

	if fs := getFeatureSummaryForFeatureID(clusterSummary, featureID); fs != nil {
		return true
	}

	return false
}

// isFeatureDeployed returns true if feature is marked as deployed (present in FeatureSummaries and status
// is set to Provisioned).
func (r *ClusterSummaryReconciler) isFeatureDeployed(clusterSummary *configv1beta1.ClusterSummary,
	featureID configv1beta1.FeatureID) bool {

	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil && fs.Status == configv1beta1.FeatureStatusProvisioned {
		return true
	}

	return false
}

// isFeatureFailedWithNonRetriableError returns true if feature is marked as failed with a non retriable error
func (r *ClusterSummaryReconciler) isFeatureFailedWithNonRetriableError(clusterSummary *configv1beta1.ClusterSummary,
	featureID configv1beta1.FeatureID) bool {

	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil && fs.Status == configv1beta1.FeatureStatusFailedNonRetriable {
		return true
	}

	return false
}

// isFeatureRemoved returns true if feature is marked as removed (present in FeatureSummaries and status
// is set to Removed).
func (r *ClusterSummaryReconciler) isFeatureRemoved(clusterSummary *configv1beta1.ClusterSummary,
	featureID configv1beta1.FeatureID) bool {

	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil && fs.Status == configv1beta1.FeatureStatusRemoved {
		return true
	}

	return false
}

// getHash returns, if available, the hash corresponding to the featureID configuration last time it
// was processed.
func (r *ClusterSummaryReconciler) getHash(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1beta1.FeatureID) []byte {

	clusterSummary := clusterSummaryScope.ClusterSummary

	if fs := getFeatureSummaryForFeatureID(clusterSummary, featureID); fs != nil {
		return fs.Hash
	}

	return nil
}

func (r *ClusterSummaryReconciler) updateFeatureStatus(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1beta1.FeatureID, status *configv1beta1.FeatureStatus, hash []byte, statusError error,
	logger logr.Logger) {

	if status == nil {
		return
	}
	logger = logger.WithValues("hash", fmt.Sprintf("%x", hash), "status", *status)
	logger.V(logs.LogDebug).Info("updating clustersummary status")
	now := metav1.NewTime(time.Now())

	switch *status {
	case configv1beta1.FeatureStatusProvisioned:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1beta1.FeatureStatusProvisioned, hash)
		clusterSummaryScope.SetFailureMessage(featureID, nil)
	case configv1beta1.FeatureStatusRemoved:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1beta1.FeatureStatusRemoved, hash)
		clusterSummaryScope.SetFailureMessage(featureID, nil)
	case configv1beta1.FeatureStatusProvisioning:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1beta1.FeatureStatusProvisioning, hash)
	case configv1beta1.FeatureStatusRemoving:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1beta1.FeatureStatusRemoving, hash)
	case configv1beta1.FeatureStatusFailed, configv1beta1.FeatureStatusFailedNonRetriable:
		clusterSummaryScope.SetFeatureStatus(featureID, *status, hash)
		err := statusError.Error()
		clusterSummaryScope.SetFailureMessage(featureID, &err)
	}

	clusterSummaryScope.SetLastAppliedTime(featureID, &now)
}

func (r *ClusterSummaryReconciler) convertResultStatus(result deployer.Result) *configv1beta1.FeatureStatus {
	switch result.ResultStatus {
	case deployer.Deployed:
		s := configv1beta1.FeatureStatusProvisioned
		return &s
	case deployer.Failed:
		s := configv1beta1.FeatureStatusFailed
		return &s
	case deployer.InProgress:
		s := configv1beta1.FeatureStatusProvisioning
		return &s
	case deployer.Removed:
		s := configv1beta1.FeatureStatusRemoved
		return &s
	case deployer.Unavailable:
		return nil
	}

	return nil
}

// shouldRedeploy returns true if this feature requires to be redeployed.
func (r *ClusterSummaryReconciler) shouldRedeploy(clusterSummaryScope *scope.ClusterSummaryScope, f feature,
	isConfigSame bool, logger logr.Logger) bool {

	if clusterSummaryScope.IsDryRunSync() {
		logger.V(logs.LogDebug).Info("dry run mode. Always redeploy.")
		return true
	}

	deployed := r.isFeatureDeployed(clusterSummaryScope.ClusterSummary, f.id)
	isErrorNonRetriable := false
	if !deployed {
		isErrorNonRetriable = r.isFeatureFailedWithNonRetriableError(clusterSummaryScope.ClusterSummary, f.id)
	}

	if isErrorNonRetriable && isConfigSame {
		// feature failed with a non retriable error and nothing has changed. Nothing to do.
		logger.V(logs.LogDebug).Info("feature failed with non-retriable error and hash has not changed")
		return false
	}

	if deployed && isConfigSame {
		// feature is deployed and nothing has changed. Nothing to do.
		logger.V(logs.LogDebug).Info("feature is deployed and hash has not changed")
		return false
	}

	return true
}
