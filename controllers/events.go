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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/eventreasons"
)

// recordEventf records an event on regarding, about the related object (can be nil).
// It does nothing when no recorder is set.
// The note is capped, so an oversized message is not rejected by the API server.
func recordEventf(recorder events.EventRecorder, regarding, related runtime.Object, eventType, reason, action,
	format string, args ...any) {

	if recorder == nil {
		return
	}

	note := eventreasons.TruncateNote(fmt.Sprintf(format, args...))
	recorder.Eventf(regarding, related, eventType, reason, action, "%s", note)
}

// featureAction returns the action of an event about a feature: the verb followed by the feature, for
// instance DeployingHelm. The events recorder merges events with the same reason, action and regarding
// object and keeps only the first note, so an action which is the same for every feature would make
// the failures of two features of the same ClusterSummary a single event.
func featureAction(action string, featureID libsveltosv1beta1.FeatureID) string {
	return action + string(featureID)
}

// getActionForFeatureStatus returns the action an event refers to given the status the feature had before:
// a feature being removed is Removing, anything else is Deploying.
func getActionForFeatureStatus(previousStatus *libsveltosv1beta1.FeatureStatus) string {
	if previousStatus != nil &&
		(*previousStatus == libsveltosv1beta1.FeatureStatusRemoving ||
			*previousStatus == libsveltosv1beta1.FeatureStatusAgentRemoving) {

		return eventreasons.ActionRemoving
	}

	return eventreasons.ActionDeploying
}

var (
	// errPullModeLicenseRequired is the failure of a ClusterSummary targeting a SveltosCluster in pull mode which
	// is not covered by a license.
	errPullModeLicenseRequired = errors.New("license is required to manage SveltosCluster in pull mode")
)

// hasOwnEvent returns true for the errors which are already reported by their own event: a failure which
// will not be retried (FeatureFailedNonRetriable) and a missing license (LicenseInvalid).
// FeatureFailed is not recorded for those, or the same failure would be reported twice.
func hasOwnEvent(err error) bool {
	var nonRetriableError *configv1beta1.NonRetriableError
	var templateError *configv1beta1.TemplateInstantiationError

	return errors.As(err, &nonRetriableError) ||
		errors.As(err, &templateError) ||
		errors.Is(err, errPullModeLicenseRequired)
}

// isConflictError returns true when err is, or wraps, a conflict with another profile.
func isConflictError(err error) bool {
	var conflictError *deployer.ConflictError

	return errors.As(err, &conflictError)
}

// recordFailedStatus records the event for a feature which has just been set to Failed with failure: a
// ProfileConflict for a conflict with another profile, a FeatureFailed for any other failure which is
// retried. The failures which have their own event (hasOwnEvent) are not recorded here.
// previousStatus and previousMessage are what the ClusterSummary recorded before this update.
func (r *ClusterSummaryReconciler) recordFailedStatus(clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID, previousStatus *libsveltosv1beta1.FeatureStatus, previousMessage *string,
	failure error) {

	switch {
	case isConflictError(failure):
		r.recordProfileConflict(clusterSummary, previousMessage, failure)
	case !hasOwnEvent(failure):
		r.recordFeatureFailure(clusterSummary, featureID, getActionForFeatureStatus(previousStatus),
			previousMessage, failure)
	}
}

// recordProfileConflict records a Warning ProfileConflict event: a resource this ClusterSummary wants to deploy
// is already deployed by another profile. Nothing is recorded when the ClusterSummary already failed with the
// same message: a retry which hits the same conflict is not news.
func (r *ClusterSummaryReconciler) recordProfileConflict(clusterSummary *configv1beta1.ClusterSummary,
	previousMessage *string, conflict error) {

	message := conflict.Error()
	if previousMessage != nil && *previousMessage == message {
		return
	}

	recordEventf(r.eventRecorder, clusterSummary, nil, corev1.EventTypeWarning, eventreasons.ProfileConflict,
		eventreasons.ActionDeploying, "Conflict detected for cluster %s %s/%s: %s",
		clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, message)
}

// getFeatureFailure returns the status and the failure message the ClusterSummary currently records for
// the feature. Both are nil when the feature has no entry yet.
// Call it before updating the feature status: the failure message of the previous attempt is what tells
// whether a failure is new.
func getFeatureFailure(clusterSummary *configv1beta1.ClusterSummary, featureID libsveltosv1beta1.FeatureID,
) (status *libsveltosv1beta1.FeatureStatus, message *string) {

	featureSummary := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if featureSummary == nil {
		return nil, nil
	}

	currentStatus := featureSummary.Status
	status = &currentStatus
	if featureSummary.FailureMessage != nil {
		currentMessage := *featureSummary.FailureMessage
		message = &currentMessage
	}

	return status, message
}

// recordFeatureFailure records a Warning FeatureFailed event when a feature fails to be deployed, or removed,
// and the operation is going to be retried. Nothing is recorded when the feature already failed with the same
// message (previousMessage): a retry which fails the same way is not news. A different message is recorded.
// The failure message is cleared when the feature succeeds, so a failure after a success is recorded again.
func (r *ClusterSummaryReconciler) recordFeatureFailure(clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID, action string, previousMessage *string, failure error) {

	if failure == nil {
		return
	}

	message := failure.Error()
	if previousMessage != nil && *previousMessage == message {
		return
	}

	recordEventf(r.eventRecorder, clusterSummary, nil, corev1.EventTypeWarning, eventreasons.FeatureFailed,
		featureAction(action, featureID), "Feature %s for cluster %s %s/%s failed: %s",
		featureID, clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, message)
}
