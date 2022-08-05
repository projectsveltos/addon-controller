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

package fake

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"

	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
)

// fakeDeployer is a fake provider that implements the DeployerInterface
type fakeDeployer struct {
	client.Client

	// inProgress contains all request that are currently being served.
	inProgress []string

	// results contains results for processed request
	results map[string]error

	// features contains currently registered feature ID
	features map[string]bool
}

// GetClient return a deployer client, implementing the DeployerInterface
func GetClient(ctx context.Context, _ logr.Logger, c client.Client) *fakeDeployer {
	return &fakeDeployer{
		Client:     c,
		inProgress: make([]string, 0),
		results:    make(map[string]error),
		features:   make(map[string]bool),
	}
}

func (d *fakeDeployer) RegisterFeatureID(
	featureID string,
) error {

	return nil
}

// Deploy simply adds request to in progress.
// Registered handler is never invoked. Use StoreResult to pretend
// getting a result
func (d *fakeDeployer) Deploy(
	ctx context.Context,
	clusterNamespace, clusterName, applicant, featureID string,
	cleanup bool,
	f deployer.RequestHandler,
) error {

	key := deployer.GetKey(clusterNamespace, clusterName, applicant, featureID, cleanup)
	d.inProgress = append(d.inProgress, key)
	return nil
}

// GetResult returns result.
// If request was marked as in progress, return InProgress.
// If request result was stored, return Deployed (if stored with no error) or
// Failed (if sotred with an error)
// Otherwise it returns Unavailable
func (d *fakeDeployer) GetResult(
	ctx context.Context,
	clusterNamespace, clusterName, applicant, featureID string,
	cleanup bool,
) deployer.Result {

	key := deployer.GetKey(clusterNamespace, clusterName, applicant, featureID, cleanup)
	v, ok := d.results[key]
	result := deployer.Result{}
	if !ok {
		result.ResultStatus = deployer.Unavailable
		if d.IsKeyInProgress(key) {
			result.ResultStatus = deployer.InProgress
		}
	} else if v != nil {
		result.ResultStatus = deployer.Failed
		result.Err = v
	} else {
		result.ResultStatus = deployer.Deployed
	}
	return result
}

func (d *fakeDeployer) IsInProgress(
	clusterNamespace, clusterName, applicant, featureID string,
	cleanup bool,
) bool {

	key := deployer.GetKey(clusterNamespace, clusterName, applicant, featureID, cleanup)
	for i := range d.inProgress {
		if d.inProgress[i] == key {
			return true
		}
	}
	return false
}

func (d *fakeDeployer) CleanupEntries(
	clusterNamespace, clusterName, applicant, featureID string,
	cleanup bool) {

	key := deployer.GetKey(clusterNamespace, clusterName, applicant, featureID, cleanup)

	// Remove any entry we might have for this cluster/feature
	delete(d.results, key)
}

// StoreResult store request result
func (d *fakeDeployer) StoreResult(
	clusterNamespace, clusterName, applicant, featureID string,
	cleanup bool,
	err error,
) {

	key := deployer.GetKey(clusterNamespace, clusterName, applicant, featureID, cleanup)
	d.results[key] = err
}

// StoreInProgress marks request as in progress
func (d *fakeDeployer) StoreInProgress(
	clusterNamespace, clusterName, applicant, featureID string,
	cleanup bool,
) {

	key := deployer.GetKey(clusterNamespace, clusterName, applicant, featureID, cleanup)
	d.inProgress = append(d.inProgress, key)
}

// IsInProgress returns true if key is currently InProgress
func (d *fakeDeployer) IsKeyInProgress(key string) bool {
	for i := range d.inProgress {
		if d.inProgress[i] == key {
			return true
		}
	}
	return false
}
