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

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

// ClusterFeatureScopeParams defines the input parameters used to create a new ClusterFeature Scope.
type ClusterFeatureScopeParams struct {
	Client         client.Client
	Logger         logr.Logger
	ClusterFeature *configv1alpha1.ClusterFeature
	ControllerName string
}

// NewClusterFeatureScope creates a new ClusterFeature Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewClusterFeatureScope(params ClusterFeatureScopeParams) (*ClusterFeatureScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a ClusterFeatureScope")
	}
	if params.ClusterFeature == nil {
		return nil, errors.New("failed to generate new scope from nil ClusterFeature")
	}

	helper, err := patch.NewHelper(params.ClusterFeature, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}
	return &ClusterFeatureScope{
		Logger:         params.Logger,
		client:         params.Client,
		ClusterFeature: params.ClusterFeature,
		patchHelper:    helper,
		controllerName: params.ControllerName,
	}, nil
}

// ClusterFeatureScope defines the basic context for an actuator to operate upon.
type ClusterFeatureScope struct {
	logr.Logger
	client         client.Client
	patchHelper    *patch.Helper
	ClusterFeature *configv1alpha1.ClusterFeature
	controllerName string
}

// PatchObject persists the feature configuration and status.
func (s *ClusterFeatureScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.ClusterFeature,
	)
}

// Close closes the current scope persisting the clusterfeature configuration and status.
func (s *ClusterFeatureScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Name returns the ClusterFeature name.
func (s *ClusterFeatureScope) Name() string {
	return s.ClusterFeature.Name
}

// ControllerName returns the name of the controller that
// created the ClusterFeatureScope.
func (s *ClusterFeatureScope) ControllerName() string {
	return s.controllerName
}

// GetSelector returns the ClusterSelector
func (s *ClusterFeatureScope) GetSelector() string {
	return string(s.ClusterFeature.Spec.ClusterSelector)
}

// SetMatchingClusterRefs sets the feature status.
func (s *ClusterFeatureScope) SetMatchingClusterRefs(matchingClusters []corev1.ObjectReference) {
	s.ClusterFeature.Status.MatchingClusterRefs = matchingClusters
}

// IsContinuousSync returns true if ClusterFeature is set to keep updating workload cluster
func (s *ClusterFeatureScope) IsContinuousSync() bool {
	return s.ClusterFeature.Spec.SyncMode == configv1alpha1.SyncModeContinuous
}

// IsOneTimeSync returns true if ClusterFeature sync mod is set to one time
func (s *ClusterFeatureScope) IsOneTimeSync() bool {
	return s.ClusterFeature.Spec.SyncMode == configv1alpha1.SyncModeOneTime
}

// IsDryRunSync returns true if ClusterFeature sync mod is set to dryRun
func (s *ClusterFeatureScope) IsDryRunSync() bool {
	return s.ClusterFeature.Spec.SyncMode == configv1alpha1.SyncModeDryRun
}
