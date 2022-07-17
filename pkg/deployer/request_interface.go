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
)

type ResultStatus int64

const (
	Deployed ResultStatus = iota
	InProgress
	Failed
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
	case Unavailable:
		return "unavailable"
	}
	return "unavailable"
}

type Result struct {
	ResultStatus
	Err error
}

type DeployerInterface interface {
	// RegisterFeatureID allows registering a feature ID.
	// If a featureID is already registered, it returns an error.
	RegisterFeatureID(
		featureID string,
	) error

	// Deploy creates a request to deploy a feature in a given
	// CAPI cluster.
	// When worker is available to fulfill such request, requestHandler
	// will be invoked in the worker context.
	// If featureID is not registered, an error will be returned.
	Deploy(
		ctx context.Context,
		clusterNamespace, clusterName, featureID string,
		f requestHandler,
	) error

	// GetResult returns result for a given request.
	GetResult(
		ctx context.Context,
		clusterNamespace, clusterName, featureID string,
	) Result
}
