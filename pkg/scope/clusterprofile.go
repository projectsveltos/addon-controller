/*
Copyright 2022-23. projectsveltos.io. All rights reserved.

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

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
)

// ProfileScopeParams defines the input parameters used to create a new Profile Scope.
type ProfileScopeParams struct {
	Client         client.Client
	Logger         logr.Logger
	Profile        client.Object
	ControllerName string
}

// NewProfileScope creates a new Profile Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewProfileScope(params ProfileScopeParams) (*ProfileScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a ProfileScope")
	}
	if params.Profile == nil {
		return nil, errors.New("failed to generate new scope from nil Profile")
	}

	helper, err := patch.NewHelper(params.Profile, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}

	if params.Profile.GetObjectKind().GroupVersionKind().Kind != configv1alpha1.ClusterProfileKind &&
		params.Profile.GetObjectKind().GroupVersionKind().Kind != configv1alpha1.ProfileKind {

		return nil, errors.Wrap(err, "only ClusterProfile or Profile can be used")
	}

	return &ProfileScope{
		Logger:         params.Logger,
		client:         params.Client,
		Profile:        params.Profile,
		patchHelper:    helper,
		controllerName: params.ControllerName,
	}, nil
}

// ProfileScope defines the basic context for an actuator to operate upon.
type ProfileScope struct {
	logr.Logger
	client         client.Client
	patchHelper    *patch.Helper
	Profile        client.Object
	controllerName string
}

// PatchObject persists the feature configuration and status.
func (s *ProfileScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.Profile,
	)
}

// Close closes the current scope persisting the Profile configuration and status.
func (s *ProfileScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Namespace returns the Profile namespace.
func (s *ProfileScope) Namespace() string {
	return s.Profile.GetNamespace()
}

// Name returns the Profile name.
func (s *ProfileScope) Name() string {
	return s.Profile.GetName()
}

// ControllerName returns the name of the controller that
// created the ProfileScope.
func (s *ProfileScope) ControllerName() string {
	return s.controllerName
}

// GetSelector returns the ClusterSelector
func (s *ProfileScope) GetSelector() string {
	spec := s.GetSpec()
	return string(spec.ClusterSelector)
}

// SetMatchingClusterRefs sets the feature status.
func (s *ProfileScope) SetMatchingClusterRefs(matchingClusters []corev1.ObjectReference) {
	status := s.GetStatus()
	status.MatchingClusterRefs = matchingClusters
}

// IsContinuousSync returns true if Profile is set to keep updating workload cluster
func (s *ProfileScope) IsContinuousSync() bool {
	spec := s.GetSpec()
	return spec.SyncMode == configv1alpha1.SyncModeContinuous ||
		spec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection
}

// IsOneTimeSync returns true if Profile sync mod is set to one time
func (s *ProfileScope) IsOneTimeSync() bool {
	spec := s.GetSpec()
	return spec.SyncMode == configv1alpha1.SyncModeOneTime
}

// IsDryRunSync returns true if Profile sync mod is set to dryRun
func (s *ProfileScope) IsDryRunSync() bool {
	spec := s.GetSpec()
	return spec.SyncMode == configv1alpha1.SyncModeDryRun
}

func (s *ProfileScope) GetSpec() *configv1alpha1.Spec {
	switch s.Profile.GetObjectKind().GroupVersionKind().Kind {
	case configv1alpha1.ClusterProfileKind:
		clusterProfile := s.Profile.(*configv1alpha1.ClusterProfile)
		return &clusterProfile.Spec
	case configv1alpha1.ProfileKind:
		profile := s.Profile.(*configv1alpha1.Profile)
		return &profile.Spec
	}

	// This will never happen as there is a validation creating scope
	return nil
}

func (s *ProfileScope) GetStatus() *configv1alpha1.Status {
	switch s.Profile.GetObjectKind().GroupVersionKind().Kind {
	case configv1alpha1.ClusterProfileKind:
		clusterProfile := s.Profile.(*configv1alpha1.ClusterProfile)
		return &clusterProfile.Status
	case configv1alpha1.ProfileKind:
		profile := s.Profile.(*configv1alpha1.Profile)
		return &profile.Status
	}

	// This will never happen as there is a validation creating scope
	return nil
}

func (s *ProfileScope) GetClusterProfile() *configv1alpha1.ClusterProfile {
	return s.Profile.(*configv1alpha1.ClusterProfile)
}

func (s *ProfileScope) GetProfile() *configv1alpha1.Profile {
	return s.Profile.(*configv1alpha1.Profile)
}

func (s *ProfileScope) GetKind() string {
	switch s.Profile.GetObjectKind().GroupVersionKind().Kind {
	case configv1alpha1.ClusterProfileKind:
		return configv1alpha1.ClusterProfileKind
	case configv1alpha1.ProfileKind:
		return configv1alpha1.ProfileKind
	}

	return ""
}
