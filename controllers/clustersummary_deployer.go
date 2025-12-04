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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/pullmode"
)

const (
	driftDetectionInMgtmCluster = "driftDetectionInMgtmCluster"
	configurationHash           = "configurationHash"
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

type getCurrentHash func(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) ([]byte, error)

type getPolicyRefs func(clusterSummary *configv1beta1.ClusterSummary) []configv1beta1.PolicyRef

type feature struct {
	id          libsveltosv1beta1.FeatureID
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
	logContentSummary(clusterSummary, f.id, logger)

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
	currentHash, err := f.currentHash(ctx, r.Client, clusterSummary, logger)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	storedHash := r.getHash(clusterSummaryScope, f.id)

	isConfigSame := reflect.DeepEqual(storedHash, currentHash)
	if !isConfigSame {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("configuration has changed. Current %x. Previous %x",
			currentHash, storedHash))
		clusterSummaryScope.ResetConsecutiveFailures(f.id)
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		msg := fmt.Sprintf("failed to verify if Cluster is in pull mode: %v", err)
		logger.V(logs.LogDebug).Info(msg)
		return err
	}

	if !r.shouldRedeploy(ctx, clusterSummaryScope, f, isConfigSame, logger) {
		logger.V(logs.LogDebug).Info("no need to redeploy")
		return nil
	}

	return r.proceedDeployingFeature(ctx, clusterSummaryScope, f, isPullMode, isConfigSame, currentHash, logger)
}

func (r *ClusterSummaryReconciler) proceedDeployingFeature(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature, isPullMode, isConfigSame bool, currentHash []byte, logger logr.Logger) error {

	clusterSummary := clusterSummaryScope.ClusterSummary

	var deployerStatus *libsveltosv1beta1.FeatureStatus
	var deployerError error

	if isConfigSame {
		// Check if feature is deployed
		logger.V(logs.LogDebug).Info("hash has not changed")
		result := r.Deployer.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, false)
		deployerStatus = r.convertResultStatus(result)
		deployerError = result.Err
	}

	if deployerStatus != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("deployer result is available: %v", *deployerStatus))

		if *deployerStatus == libsveltosv1beta1.FeatureStatusProvisioned {
			if isPullMode {
				// provisioned here means configuration for sveltos-applier has been successufully prepared.
				// In pull mode, verify now agent has deployed the configuration.
				provisioning := libsveltosv1beta1.FeatureStatusProvisioning
				r.updateFeatureStatus(clusterSummaryScope, f.id, &provisioning, currentHash, deployerError, logger)
				return r.proceedDeployingFeatureInPullMode(ctx, clusterSummaryScope, f, isConfigSame, currentHash, logger)
			}

			r.updateFeatureStatus(clusterSummaryScope, f.id, deployerStatus, currentHash, deployerError, logger)
			message := fmt.Sprintf("Feature: %s deployed to cluster %s %s/%s", f.id,
				clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
			r.eventRecorder.Eventf(clusterSummary, corev1.EventTypeNormal, "sveltos", message)
			return nil
		}

		r.updateFeatureStatus(clusterSummaryScope, f.id, deployerStatus, currentHash, deployerError, logger)
		if deployerError != nil {
			// Check if error is a NonRetriableError type
			var nonRetriableError *configv1beta1.NonRetriableError
			if errors.As(deployerError, &nonRetriableError) {
				nonRetriableStatus := libsveltosv1beta1.FeatureStatusFailedNonRetriable
				r.updateFeatureStatus(clusterSummaryScope, f.id, &nonRetriableStatus, currentHash, deployerError, logger)
				return nil
			}
			var templateError *configv1beta1.TemplateInstantiationError
			if errors.As(deployerError, &templateError) {
				nonRetriableStatus := libsveltosv1beta1.FeatureStatusFailedNonRetriable
				r.updateFeatureStatus(clusterSummaryScope, f.id, &nonRetriableStatus, currentHash, deployerError, logger)
				return nil
			}
			if r.maxNumberOfConsecutiveFailureReached(clusterSummaryScope, f, logger) {
				nonRetriableStatus := libsveltosv1beta1.FeatureStatusFailedNonRetriable
				resultError := errors.New("the maximum number of consecutive errors has been reached")
				r.updateFeatureStatus(clusterSummaryScope, f.id, &nonRetriableStatus, currentHash, resultError, logger)
				return nil
			}
		}
		if *deployerStatus == libsveltosv1beta1.FeatureStatusProvisioning {
			return fmt.Errorf("feature is still being provisioned")
		}
	} else {
		if isPullMode && pullmode.IsBeingProvisioned(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name, string(f.id),
			logger) {

			return r.proceedDeployingFeatureInPullMode(ctx, clusterSummaryScope, f, isConfigSame, currentHash, logger)
		}
		logger.V(logs.LogDebug).Info("no result is available. mark status as provisioning")
		s := libsveltosv1beta1.FeatureStatusProvisioning
		deployerStatus = &s
		r.updateFeatureStatus(clusterSummaryScope, f.id, deployerStatus, currentHash, nil, logger)
	}

	// Getting here means either feature failed to be deployed or configuration has changed.
	// Feature must be (re)deployed.
	options := deployer.Options{HandlerOptions: make(map[string]any)}
	if getAgentInMgmtCluster() {
		options.HandlerOptions[driftDetectionInMgtmCluster] = "ok"
	}
	options.HandlerOptions[configurationHash] = currentHash
	logger.V(logs.LogDebug).Info("queueing request to deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, false,
		genericDeploy, programDeployMetrics, options); err != nil {
		r.updateFeatureStatus(clusterSummaryScope, f.id, deployerStatus, currentHash, err, logger)
		return err
	}

	return fmt.Errorf("request is queued")
}

func (r *ClusterSummaryReconciler) proceedDeployingFeatureInPullMode(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature, isConfigSame bool, currentHash []byte, logger logr.Logger) error {

	clusterSummary := clusterSummaryScope.ClusterSummary

	var pullmodeStatus *libsveltosv1beta1.FeatureStatus

	if isConfigSame {
		pullmodeHash, err := pullmode.GetRequestorHash(ctx, getManagementClusterClient(),
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			configv1beta1.ClusterSummaryKind, clusterSummary.Name, string(f.id), logger)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				msg := fmt.Sprintf("failed to get pull mode hash: %v", err)
				logger.V(logs.LogDebug).Info(msg)
				return err
			}
		} else {
			isConfigSame = reflect.DeepEqual(pullmodeHash, currentHash)
		}
	}

	if isConfigSame {
		// only if configuration hash matches, check if feature is deployed
		logger.V(logs.LogDebug).Info("hash has not changed")
		pullmodeStatus = r.proceesAgentDeploymentStatus(ctx, clusterSummaryScope, f, logger)
	}

	if pullmodeStatus != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("agent result is available. updating status: %v", *pullmodeStatus))

		switch *pullmodeStatus {
		case libsveltosv1beta1.FeatureStatusProvisioned:
			message := fmt.Sprintf("Feature: %s deployed to cluster %s %s/%s", f.id,
				clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
			r.eventRecorder.Eventf(clusterSummary, corev1.EventTypeNormal, "sveltos", message)
			r.updateFeatureStatus(clusterSummaryScope, f.id, pullmodeStatus, currentHash, nil, logger)
			clusterSummaryScope.SetFailureMessage(f.id, nil)
			now := metav1.NewTime(time.Now())
			clusterSummaryScope.SetLastAppliedTime(f.id, &now)
			return pullmode.TerminateDeploymentTracking(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
				clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name, string(f.id), logger)
		case libsveltosv1beta1.FeatureStatusProvisioning:
			msg := "agent is provisioning the content"
			logger.V(logs.LogDebug).Info(msg)
			r.updateFeatureStatus(clusterSummaryScope, f.id, pullmodeStatus, currentHash, nil, logger)
			return errors.New(msg)
		case libsveltosv1beta1.FeatureStatusFailed:
			logger.V(logs.LogDebug).Info("agent failed provisioning the content")
			if r.maxNumberOfConsecutiveFailureReached(clusterSummaryScope, f, logger) {
				nonRetriableStatus := libsveltosv1beta1.FeatureStatusFailedNonRetriable
				resultError := errors.New("the maximum number of consecutive errors has been reached")
				r.updateFeatureStatus(clusterSummaryScope, f.id, &nonRetriableStatus, currentHash, resultError, logger)
				return pullmode.TerminateDeploymentTracking(ctx, getManagementClusterClient(),
					clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind,
					clusterSummary.Name, string(f.id), logger)
			} else {
				r.updateFeatureStatus(clusterSummaryScope, f.id, pullmodeStatus, currentHash, nil, logger)
			}
		case libsveltosv1beta1.FeatureStatusFailedNonRetriable, libsveltosv1beta1.FeatureStatusRemoving,
			libsveltosv1beta1.FeatureStatusAgentRemoving, libsveltosv1beta1.FeatureStatusRemoved:
			logger.V(logs.LogDebug).Info("proceed deploying")
		}
	} else {
		provisioning := libsveltosv1beta1.FeatureStatusProvisioning
		r.updateFeatureStatus(clusterSummaryScope, f.id, &provisioning, currentHash, nil, logger)
	}

	// Getting here means either agent failed to deploy feature or configuration has changed.
	// Either way, feature must be (re)deployed. Queue so new configuration for agent is prepared.
	options := deployer.Options{HandlerOptions: make(map[string]any)}
	if getAgentInMgmtCluster() {
		options.HandlerOptions[driftDetectionInMgtmCluster] = "management"
	}
	options.HandlerOptions[configurationHash] = currentHash

	logger.V(logs.LogDebug).Info("queueing request to deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, false,
		genericDeploy, programDeployMetrics, options); err != nil {
		return err
	}

	return fmt.Errorf("request to deploy queued")
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
	featureHandler := getHandlersForFeature(libsveltosv1beta1.FeatureID(featureID))
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

	if r.isFeatureRemoved(clusterSummaryScope, f.id) {
		logger.V(logs.LogDebug).Info("feature is removed")
		// feature is removed. Nothing to do.
		return nil
	}

	isPullMode, err := clusterproxy.IsClusterInPullMode(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		msg := fmt.Sprintf("failed to verify if Cluster is in pull mode: %v", err)
		logger.V(logs.LogDebug).Info(msg)
		return err
	}

	if isPullMode {
		// In general:
		// - an undeploy creates a ConfigurationGroup with Action: Remove and result is fetched via GetRemoveStatus
		// - a deploy creates a ConfigurationGroup with Action: Deploy and result if fetched via GetDeploymentStatus
		// but in case of helm, undeploy first creates a ConfigurationGroup with Action: Deploy so that delete
		// hooks can be installed. Cleaning this makes sure we dont confuse ConfigurationGroup.
		if err := cleanPreviousActiveTracking(ctx, clusterSummary, f, logger); err != nil {
			return err
		}
	}

	return r.processUndeployResult(ctx, clusterSummaryScope, f, isPullMode, logger)
}

func (r *ClusterSummaryReconciler) processUndeployResult(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature, isPullMode bool, logger logr.Logger) error {

	clusterSummary := clusterSummaryScope.ClusterSummary

	result := r.Deployer.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummaryScope.Name(), string(f.id), clusterSummary.Spec.ClusterType, true)
	status := r.convertResultStatus(result)

	if status != nil {
		if *status == libsveltosv1beta1.FeatureStatusProvisioning {
			s := libsveltosv1beta1.FeatureStatusRemoving
			status = &s
			r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
			return fmt.Errorf("feature is still being removed")
		}

		// Failure to undeploy because of missing permission is ignored.
		if apierrors.IsForbidden(result.Err) {
			logger.V(logs.LogInfo).Info("undeploying failing because of missing permission.")
			tmpStatus := libsveltosv1beta1.FeatureStatusRemoving
			status = &tmpStatus
			r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
			return nil
		}

		var handOverError *configv1beta1.HandOverError
		if errors.As(result.Err, &handOverError) {
			clusterSummaryScope.ClusterSummary.Status.NextReconcileTime =
				&metav1.Time{Time: time.Now().Add(time.Minute)}
			return result.Err
		}

		var nonRetriableError *configv1beta1.NonRetriableError
		if errors.As(result.Err, &nonRetriableError) {
			removing := libsveltosv1beta1.FeatureStatusRemoving
			r.updateFeatureStatus(clusterSummaryScope, f.id, &removing, nil, result.Err, logger)
			return result.Err
		}
		var templateError *configv1beta1.TemplateInstantiationError
		if errors.As(result.Err, &templateError) {
			removing := libsveltosv1beta1.FeatureStatusRemoving
			r.updateFeatureStatus(clusterSummaryScope, f.id, &removing, nil, result.Err, logger)
			return result.Err
		}

		if *status == libsveltosv1beta1.FeatureStatusRemoved {
			if isPullMode {
				// Removed here means the configuration for agent to undeploy has been successfully
				// created. Since we are in pull mode, verify now whether agent has successfully undeployed
				// the configuration.
				return r.processUndeployResultInPullMode(ctx, clusterSummaryScope, f, logger)
			}

			r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, nil, logger)
			return nil
		} else { // if *status != libsveltosv1beta1.FeatureStatusRemoved
			r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, result.Err, logger)
		}
	}

	if status == nil {
		if isPullMode &&
			r.isFeatureWaitingForAgentRemoval(clusterSummaryScope, f.id) &&
			pullmode.IsBeingRemoved(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
				clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name, string(f.id),
				logger) {

			return r.processUndeployResultInPullMode(ctx, clusterSummaryScope, f, logger)
		}
		logger.V(logs.LogDebug).Info("no result is available. mark status as removing")
		s := libsveltosv1beta1.FeatureStatusRemoving
		status = &s
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, nil, logger)
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, true, genericUndeploy, programDuration,
		deployer.Options{}); err != nil {
		r.updateFeatureStatus(clusterSummaryScope, f.id, status, nil, err, logger)
		return err
	}

	return fmt.Errorf("cleanup request is queued")
}

func cleanPreviousActiveTracking(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary, f feature,
	logger logr.Logger) error {

	// If a ConfigurationGroup exists and was previously managed by this ClusterSummary, remove it.
	// This is done when the source ClusterSummary is no longer in a "deleted" state,
	// indicating a need to clean up an older, potentially obsolete ConfigurationGroup.
	sourceStatus, err := pullmode.GetSourceStatus(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name,
		string(f.id), logger)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}

	if sourceStatus != nil && *sourceStatus == libsveltosv1beta1.SourceStatusActive {
		logger.V(logs.LogInfo).Info("configurationGroup sourceStatus was active. Remove it.")
		_ = pullmode.TerminateDeploymentTracking(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name,
			string(f.id), logger)
		// Return an error to make sure cache is synced by next
		return errors.New("provisioning ConfigurationGroup (active source) was still present")
	}

	return nil
}

func (r *ClusterSummaryReconciler) processUndeployResultInPullMode(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope, f feature, logger logr.Logger) error {

	// ClusterSummary follows a strict state machine for resource removal:
	//
	// 1. Create ConfigurationGroup with action=Remove
	// 2. Set ClusterSummary.Status to FeatureStatusAgentRemoving
	// 3. Monitor ConfigurationGroup status:
	//    - Missing ConfigurationGroup = resources successfully removed
	//    - ConfigurationGroup.Status = Removed = resources successfully removed
	//
	// The FeatureStatusAgentRemoving state is critical - only after reaching this state
	// can a missing ConfigurationGroup be interpreted as successful removal. This prevents
	// race conditions where a missing ConfigurationGroup might be misinterpreted as
	// successful removal when it was never created or failed to deploy.

	clusterSummary := clusterSummaryScope.ClusterSummary

	agentStatus, err := pullmode.GetRemoveStatus(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Kind, clusterSummary.Name, string(f.id), logger)
	if err != nil {
		if apierrors.IsNotFound(err) && r.isFeatureWaitingForAgentRemoval(clusterSummaryScope, f.id) {
			// Status is set to FeatureStatusAgentRemoving only once GetRemoveStatus returns the status
			// which means ConfigurationGroup was created with Action Remove. If ConfigurationGroup is
			// not found anymore, consider this removed.
			removed := libsveltosv1beta1.FeatureStatusRemoved
			r.updateFeatureStatus(clusterSummaryScope, f.id, &removed, nil, nil, logger)
			return nil
		}
		errorMsg := err.Error()
		clusterSummaryScope.SetFailureMessage(f.id, &errorMsg)
	} else {
		if agentStatus != nil {
			if agentStatus.DeploymentStatus != nil && *agentStatus.DeploymentStatus == libsveltosv1beta1.FeatureStatusRemoved {
				logger.V(logs.LogDebug).Info("agent removed content")
				err = pullmode.TerminateDeploymentTracking(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
					clusterSummary.Spec.ClusterName, configv1beta1.ClusterSummaryKind, clusterSummary.Name, string(f.id), logger)
				if err != nil {
					return err
				}
				removed := libsveltosv1beta1.FeatureStatusRemoved
				r.updateFeatureStatus(clusterSummaryScope, f.id, &removed, nil, nil, logger)
				return nil
			} else if agentStatus.FailureMessage != nil {
				clusterSummaryScope.SetFailureMessage(f.id, agentStatus.FailureMessage)
			} else {
				// Verified agent is removing
				agentRemoving := libsveltosv1beta1.FeatureStatusAgentRemoving
				r.updateFeatureStatus(clusterSummaryScope, f.id, &agentRemoving, nil, nil, logger)
				return nil
			}
		} else {
			logger.V(logs.LogDebug).Info("no result is available. mark status as removing")
			removing := libsveltosv1beta1.FeatureStatusRemoving
			r.updateFeatureStatus(clusterSummaryScope, f.id, &removing, nil, nil, logger)
		}
	}

	logger.V(logs.LogDebug).Info("queueing request to un-deploy")
	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), clusterSummary.Spec.ClusterType, true, genericUndeploy, programDuration,
		deployer.Options{}); err != nil {
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
	featureHandler := getHandlersForFeature(libsveltosv1beta1.FeatureID(featureID))
	if err := featureHandler.undeploy(ctx, c, clusterNamespace, clusterName, applicant, featureID, clusterType, o, logger); err != nil {
		return err
	}

	// After any per feature specific code

	return nil
}

// If SveltosCluster is in pull mode, verify whether agent has pulled and successuffly deployed it.
// Updates the ClusterSummary status accordingly
func (r *ClusterSummaryReconciler) proceesAgentDeploymentStatus(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope, f feature, logger logr.Logger,
) *libsveltosv1beta1.FeatureStatus {

	logger.V(logs.LogDebug).Info("Verify if agent has deployed content and process it")

	clusterSummary := clusterSummaryScope.ClusterSummary

	status, err := pullmode.GetDeploymentStatus(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Kind, clusterSummary.Name, string(f.id), logger)

	if status != nil {
		// Some GVK might have been deployed to the management cluster. Always append
		// ConfigurationGroup only contains what has been deployed to the managed cluster
		deployedGVKs := tranformGroupVersionKindToString(getDeployedGroupVersionKinds(clusterSummary, f.id))
		deployedGVKs = append(deployedGVKs, status.DeployedGroupVersionKind...)
		deployedGVKs = unique(deployedGVKs)

		found := false
		for i := range clusterSummary.Status.DeployedGVKs {
			if clusterSummary.Status.DeployedGVKs[i].FeatureID == f.id {
				// Update existing entry
				clusterSummary.Status.DeployedGVKs[i].DeployedGroupVersionKind = deployedGVKs
				found = true
				break
			}
		}

		if !found {
			// Append new entry
			clusterSummary.Status.DeployedGVKs = append(clusterSummary.Status.DeployedGVKs,
				libsveltosv1beta1.FeatureDeploymentInfo{
					FeatureID:                f.id,
					DeployedGroupVersionKind: deployedGVKs,
				})
		}
	}

	if err != nil {
		if pullmode.IsProcessingMismatch(err) {
			provisioning := libsveltosv1beta1.FeatureStatusProvisioning
			return &provisioning
		}
		errorMsg := err.Error()
		clusterSummaryScope.SetFailureMessage(f.id, &errorMsg)
	} else if pullmode.IsActionNotSetToDeploy(err) {
		_ = pullmode.TerminateDeploymentTracking(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, clusterSummary.Kind, clusterSummary.Name, string(f.id), logger)
	} else if status.FailureMessage != nil {
		clusterSummaryScope.SetFailureMessage(f.id, status.FailureMessage)
	}

	return status.DeploymentStatus
}

// isFeatureStatusPresent returns true if feature status is set.
// That means feature was deployed/being deployed
func (r *ClusterSummaryReconciler) isFeatureStatusPresent(clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID) bool {

	if fs := getFeatureSummaryForFeatureID(clusterSummary, featureID); fs != nil {
		return true
	}

	return false
}

// isFeatureDeployed returns true if feature is marked as deployed (present in FeatureSummaries and status
// is set to Provisioned).
func (r *ClusterSummaryReconciler) isFeatureDeployed(clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID) bool {

	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil && fs.Status == libsveltosv1beta1.FeatureStatusProvisioned {
		return true
	}

	return false
}

// isFeatureFailedWithNonRetriableError returns true if feature is marked as failed with a non retriable error
func (r *ClusterSummaryReconciler) isFeatureFailedWithNonRetriableError(clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID) bool {

	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil && fs.Status == libsveltosv1beta1.FeatureStatusFailedNonRetriable {
		return true
	}

	return false
}

// isFeatureRemoved returns true if feature is marked as removed (present in FeatureSummaries and status
// is set to Removed).
func (r *ClusterSummaryReconciler) isFeatureRemoved(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID libsveltosv1beta1.FeatureID) bool {

	clusterSummary := clusterSummaryScope.ClusterSummary
	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil && fs.Status == libsveltosv1beta1.FeatureStatusRemoved {
		return true
	}

	return false
}

// isFeatureWaitingForAgentRemoval returns true if feature is marked as being removed by agent when in pull mode
func (r *ClusterSummaryReconciler) isFeatureWaitingForAgentRemoval(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID libsveltosv1beta1.FeatureID) bool {

	clusterSummary := clusterSummaryScope.ClusterSummary
	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil && fs.Status == libsveltosv1beta1.FeatureStatusAgentRemoving {
		return true
	}

	return false
}

// getHash returns, if available, the hash corresponding to the featureID configuration last time it
// was processed.
func (r *ClusterSummaryReconciler) getHash(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID libsveltosv1beta1.FeatureID) []byte {

	clusterSummary := clusterSummaryScope.ClusterSummary

	if fs := getFeatureSummaryForFeatureID(clusterSummary, featureID); fs != nil {
		return fs.Hash
	}

	return nil
}

// getConsecutiveFailures returns, if available, the number of consecutive failures corresponding to the
// featureID
func (r *ClusterSummaryReconciler) getConsecutiveFailures(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID libsveltosv1beta1.FeatureID) uint {

	clusterSummary := clusterSummaryScope.ClusterSummary

	if fs := getFeatureSummaryForFeatureID(clusterSummary, featureID); fs != nil {
		return fs.ConsecutiveFailures
	}

	return 0
}

func (r *ClusterSummaryReconciler) updateFeatureStatus(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID libsveltosv1beta1.FeatureID, status *libsveltosv1beta1.FeatureStatus, hash []byte, statusError error,
	logger logr.Logger) {

	if status == nil {
		return
	}
	logger = logger.WithValues("hash", fmt.Sprintf("%x", hash), "status", *status)
	logger.V(logs.LogDebug).Info("updating clustersummary status")
	now := metav1.NewTime(time.Now())

	switch *status {
	case libsveltosv1beta1.FeatureStatusProvisioned:
		failed := false
		clusterSummaryScope.SetFeatureStatus(featureID, libsveltosv1beta1.FeatureStatusProvisioned, hash, &failed)
		clusterSummaryScope.SetFailureMessage(featureID, nil)
		clusterSummaryScope.SetLastAppliedTime(featureID, &now)
	case libsveltosv1beta1.FeatureStatusRemoved:
		failed := false
		clusterSummaryScope.SetFeatureStatus(featureID, libsveltosv1beta1.FeatureStatusRemoved, hash, &failed)
		clusterSummaryScope.SetFailureMessage(featureID, nil)
		clusterSummaryScope.SetLastAppliedTime(featureID, &now)
	case libsveltosv1beta1.FeatureStatusProvisioning:
		clusterSummaryScope.SetFeatureStatus(featureID, libsveltosv1beta1.FeatureStatusProvisioning, hash, nil)
	case libsveltosv1beta1.FeatureStatusAgentRemoving:
		clusterSummaryScope.SetFeatureStatus(featureID, libsveltosv1beta1.FeatureStatusAgentRemoving, hash, nil)
	case libsveltosv1beta1.FeatureStatusRemoving:
		clusterSummaryScope.SetFeatureStatus(featureID, libsveltosv1beta1.FeatureStatusRemoving, hash, nil)
	case libsveltosv1beta1.FeatureStatusFailed, libsveltosv1beta1.FeatureStatusFailedNonRetriable:
		failed := true
		clusterSummaryScope.SetFeatureStatus(featureID, *status, hash, &failed)
		if statusError != nil {
			err := statusError.Error()
			clusterSummaryScope.SetFailureMessage(featureID, &err)
		}
		clusterSummaryScope.SetLastAppliedTime(featureID, &now)
	}
}

func (r *ClusterSummaryReconciler) convertResultStatus(result deployer.Result) *libsveltosv1beta1.FeatureStatus {
	switch result.ResultStatus {
	case deployer.Deployed:
		s := libsveltosv1beta1.FeatureStatusProvisioned
		return &s
	case deployer.Failed:
		s := libsveltosv1beta1.FeatureStatusFailed
		return &s
	case deployer.InProgress:
		s := libsveltosv1beta1.FeatureStatusProvisioning
		return &s
	case deployer.Removed:
		s := libsveltosv1beta1.FeatureStatusRemoved
		return &s
	case deployer.Unavailable:
		return nil
	}

	return nil
}

// shouldRedeploy returns true if this feature requires to be redeployed.
func (r *ClusterSummaryReconciler) shouldRedeploy(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope, f feature, isConfigSame bool,
	logger logr.Logger) bool {

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

// maxNumberOfConsecutiveFailureReached returns true if max number of consecutive failures has been reached.
func (r *ClusterSummaryReconciler) maxNumberOfConsecutiveFailureReached(clusterSummaryScope *scope.ClusterSummaryScope, f feature,
	logger logr.Logger) bool {

	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.MaxConsecutiveFailures != nil {
		consecutiveFailures := r.getConsecutiveFailures(clusterSummaryScope, f.id)
		if consecutiveFailures >= *clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.MaxConsecutiveFailures {
			msg := fmt.Sprintf("max number of consecutive failures reached %d", consecutiveFailures)
			logger.V(logs.LogDebug).Info(msg)
			return true
		}
	}

	return false
}

func logContentSummary(clusterSummary *configv1beta1.ClusterSummary, fID libsveltosv1beta1.FeatureID, logger logr.Logger) {
	switch fID {
	case libsveltosv1beta1.FeatureHelm:
		logger.V(logs.LogDebug).Info(fmt.Sprintf("HelmCharts: %d", len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts)))
	case libsveltosv1beta1.FeatureResources:
		logger.V(logs.LogDebug).Info(fmt.Sprintf("PolicyRefs: %d", len(clusterSummary.Spec.ClusterProfileSpec.PolicyRefs)))
	case libsveltosv1beta1.FeatureKustomize:
		logger.V(logs.LogDebug).Info(fmt.Sprintf("KustomizationRefs: %d", len(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs)))
	}
}
