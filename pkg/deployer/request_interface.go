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

package deployer

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ResultStatus int64

const (
	Deployed ResultStatus = iota
	InProgress
	Failed
	Removed
	Unavailable
)

func (r ResultStatus) String() string {
	switch r {
	case Deployed:
		return "deployed"
	case InProgress:
		return "in-progress"
	case Failed:
		return "failed"
	case Removed:
		return "removed"
	case Unavailable:
		return "unavailable"
	}
	return "unavailable"
}

type Result struct {
	ResultStatus
	Err error
}

type RequestHandler func(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, featureID string,
	logger logr.Logger) error

type DeployerInterface interface {
	// RegisterFeatureID allows registering a feature ID.
	// If a featureID is already registered, it returns an error.
	RegisterFeatureID(
		featureID string,
	) error

	// Deploy creates a request to deploy/cleanup a feature in a given
	// CAPI cluster (identified by clusterNamespace, clusterName).
	// cleanup indicates whether request is for feature to be provisioned
	// or removed.
	// When worker is available to fulfill such request, RequestHandler
	// will be invoked in the worker context.
	// If featureID is not registered, an error will be returned.
	// Applicant is an identifier of whatever is making this request.
	// It can be left empty (in case there is no need to differentiate between
	// different applicants).
	Deploy(
		ctx context.Context,
		clusterNamespace, clusterName, applicant, featureID string,
		cleanup bool,
		f RequestHandler,
	) error

	// IsInProgress returns true, if featureID for clusterNamespace/clusterName
	// requested by applicant is currently in progress.
	// cleanup indicates whether request for feature to be provisioned or
	// removed is currently in progress.
	IsInProgress(
		clusterNamespace, clusterName, applicant, featureID string,
		cleanup bool,
	) bool

	// GetResult returns result for a given request.
	GetResult(
		ctx context.Context,
		clusterNamespace, clusterName, applicant, featureID string,
		cleanup bool,
	) Result

	// CleanupEntries removes any entry (from any internal data structure) for
	// given feature
	CleanupEntries(clusterNamespace, clusterName, applicant, featureID string,
		cleanup bool)
}
