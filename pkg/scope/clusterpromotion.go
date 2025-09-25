/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

// ClusterPromotionScopeParams defines the input parameters used to create a new ClusterPromotion Scope.
type ClusterPromotionScopeParams struct {
	Client           client.Client
	Logger           logr.Logger
	ClusterPromotion *configv1beta1.ClusterPromotion
	ControllerName   string
}

// NewClusterPromotionScope creates a new ClusterPromotion Scope from the supplied parameters.
// This is meant to be called for each reconcile iteration.
func NewClusterPromotionScope(params *ClusterPromotionScopeParams) (*ClusterPromotionScope, error) {
	if params.Client == nil {
		return nil, errors.New("client is required when creating a ClusterPromotionScope")
	}
	if params.ClusterPromotion == nil {
		return nil, errors.New("failed to generate new scope from nil ClusterPromotion")
	}

	helper, err := patch.NewHelper(params.ClusterPromotion, params.Client)
	if err != nil {
		return nil, errors.Wrap(err, "failed to init patch helper")
	}
	return &ClusterPromotionScope{
		Logger:           params.Logger,
		client:           params.Client,
		ClusterPromotion: params.ClusterPromotion,
		patchHelper:      helper,
		controllerName:   params.ControllerName,
	}, nil
}

// ClusterPromotionScope defines the basic context for an actuator to operate upon.
type ClusterPromotionScope struct {
	logr.Logger
	client           client.Client
	patchHelper      *patch.Helper
	ClusterPromotion *configv1beta1.ClusterPromotion
	controllerName   string
}

// PatchObject persists the cluster configuration and status.
func (s *ClusterPromotionScope) PatchObject(ctx context.Context) error {
	return s.patchHelper.Patch(
		ctx,
		s.ClusterPromotion,
	)
}

// Close closes the current scope persisting the clusterprofile configuration and status.
func (s *ClusterPromotionScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}

// Name returns the ClusterPromotion name.
func (s *ClusterPromotionScope) Name() string {
	return s.ClusterPromotion.Name
}

// ProfileSpecHash returns the stored ProfileSpec hash
func (s *ClusterPromotionScope) ProfileSpecHash() []byte {
	return s.ClusterPromotion.Status.ProfileSpecHash
}

// ProfileHash returns the stored Stages Hash
func (s *ClusterPromotionScope) StagesHash() []byte {
	return s.ClusterPromotion.Status.StagesHash
}
