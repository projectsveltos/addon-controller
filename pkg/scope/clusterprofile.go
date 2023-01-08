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

package scope

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

// ClusterProfileScopeParams defines the input parameters used to create a new ClusterProfile Scope.
type ClusterProfileScopeParams struct {
	Client         client.Client
	Logger         logr.Logger
	ClusterProfile *configv1alpha1.ClusterProfile
	ControllerName string
}

// NewClusterProfileScope creates a new ClusterProfile Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewClusterProfileScope(params ClusterProfileScopeParams) (*ClusterProfileScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a ClusterProfileScope")
	}
	if params.ClusterProfile == nil {
		return nil, errors.New("failed to generate new scope from nil ClusterProfile")
	}

	helper, err := patch.NewHelper(params.ClusterProfile, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}
	return &ClusterProfileScope{
		Logger:         params.Logger,
		client:         params.Client,
		ClusterProfile: params.ClusterProfile,
		patchHelper:    helper,
		controllerName: params.ControllerName,
	}, nil
}

// ClusterProfileScope defines the basic context for an actuator to operate upon.
type ClusterProfileScope struct {
	logr.Logger
	client         client.Client
	patchHelper    *patch.Helper
	ClusterProfile *configv1alpha1.ClusterProfile
	controllerName string
}

// PatchObject persists the feature configuration and status.
func (s *ClusterProfileScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.ClusterProfile,
	)
}

// Close closes the current scope persisting the clusterprofile configuration and status.
func (s *ClusterProfileScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Name returns the ClusterProfile name.
func (s *ClusterProfileScope) Name() string {
	return s.ClusterProfile.Name
}

// ControllerName returns the name of the controller that
// created the ClusterProfileScope.
func (s *ClusterProfileScope) ControllerName() string {
	return s.controllerName
}

// GetSelector returns the ClusterSelector
func (s *ClusterProfileScope) GetSelector() string {
	return string(s.ClusterProfile.Spec.ClusterSelector)
}

// SetMatchingClusterRefs sets the feature status.
func (s *ClusterProfileScope) SetMatchingClusterRefs(matchingClusters []corev1.ObjectReference) {
	s.ClusterProfile.Status.MatchingClusterRefs = matchingClusters
}

// IsContinuousSync returns true if ClusterProfile is set to keep updating workload cluster
func (s *ClusterProfileScope) IsContinuousSync() bool {
	return s.ClusterProfile.Spec.SyncMode == configv1alpha1.SyncModeContinuous ||
		s.ClusterProfile.Spec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection
}

// IsOneTimeSync returns true if ClusterProfile sync mod is set to one time
func (s *ClusterProfileScope) IsOneTimeSync() bool {
	return s.ClusterProfile.Spec.SyncMode == configv1alpha1.SyncModeOneTime
}

// IsDryRunSync returns true if ClusterProfile sync mod is set to dryRun
func (s *ClusterProfileScope) IsDryRunSync() bool {
	return s.ClusterProfile.Spec.SyncMode == configv1alpha1.SyncModeDryRun
}
