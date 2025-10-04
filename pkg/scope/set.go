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

package scope

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// SetScopeParams defines the input parameters used to create a new Set Scope.
type SetScopeParams struct {
	Client         client.Client
	Logger         logr.Logger
	Set            client.Object
	ControllerName string
}

// NewSetScope creates a new Set Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewSetScope(params SetScopeParams) (*SetScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a SetScope")
	}
	if params.Set == nil {
		return nil, errors.New("failed to generate new scope from nil Set")
	}

	helper, err := patch.NewHelper(params.Set, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}

	if params.Set.GetObjectKind().GroupVersionKind().Kind != libsveltosv1beta1.ClusterSetKind &&
		params.Set.GetObjectKind().GroupVersionKind().Kind != libsveltosv1beta1.SetKind {

		return nil, errors.New(fmt.Sprintf("only ClusterSet or Set can be used (%q)",
			params.Set.GetObjectKind().GroupVersionKind().Kind))
	}

	return &SetScope{
		Logger:         params.Logger,
		client:         params.Client,
		Set:            params.Set,
		patchHelper:    helper,
		controllerName: params.ControllerName,
	}, nil
}

// SetScope defines the basic context for an actuator to operate upon.
type SetScope struct {
	logr.Logger
	client         client.Client
	patchHelper    *patch.Helper
	Set            client.Object
	controllerName string
}

// PatchObject persists the feature configuration and status.
func (s *SetScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.Set,
	)
}

// Close closes the current scope persisting the Set configuration and status.
func (s *SetScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Name returns the Set name.
func (s *SetScope) Name() string {
	return s.Set.GetName()
}

// ControllerName returns the name of the controller that
// created the SetScope.
func (s *SetScope) ControllerName() string {
	return s.controllerName
}

// GetSelector returns the ClusterSelector
func (s *SetScope) GetSelector() *metav1.LabelSelector {
	spec := s.GetSpec()
	return &spec.ClusterSelector.LabelSelector
}

// SetMatchingClusterRefs sets the feature status.
func (s *SetScope) SetMatchingClusterRefs(matchingClusters []corev1.ObjectReference) {
	status := s.GetStatus()
	status.MatchingClusterRefs = matchingClusters
}

// SetSelectedClusters sets the feature status.
func (s *SetScope) SetSelectedClusterRefs(selectedClusters []corev1.ObjectReference) {
	status := s.GetStatus()
	status.SelectedClusterRefs = selectedClusters
}

func (s *SetScope) GetSpec() *libsveltosv1beta1.Spec {
	switch s.Set.GetObjectKind().GroupVersionKind().Kind {
	case libsveltosv1beta1.ClusterSetKind:
		clusterSet := s.Set.(*libsveltosv1beta1.ClusterSet)
		return &clusterSet.Spec
	case libsveltosv1beta1.SetKind:
		set := s.Set.(*libsveltosv1beta1.Set)
		return &set.Spec
	}

	// This will never happen as there is a validation creating scope
	return nil
}

func (s *SetScope) GetStatus() *libsveltosv1beta1.Status {
	switch s.Set.GetObjectKind().GroupVersionKind().Kind {
	case libsveltosv1beta1.ClusterSetKind:
		clusterSet := s.Set.(*libsveltosv1beta1.ClusterSet)
		return &clusterSet.Status
	case libsveltosv1beta1.SetKind:
		profile := s.Set.(*libsveltosv1beta1.Set)
		return &profile.Status
	}

	// This will never happen as there is a validation creating scope
	return nil
}

func (s *SetScope) GetClusterSet() *libsveltosv1beta1.ClusterSet {
	return s.Set.(*libsveltosv1beta1.ClusterSet)
}

func (s *SetScope) GetSet() *libsveltosv1beta1.Set {
	return s.Set.(*libsveltosv1beta1.Set)
}

func (s *SetScope) GetKind() string {
	switch s.Set.GetObjectKind().GroupVersionKind().Kind {
	case libsveltosv1beta1.ClusterSetKind:
		return libsveltosv1beta1.ClusterSetKind
	case libsveltosv1beta1.SetKind:
		return libsveltosv1beta1.SetKind
	}

	return ""
}
