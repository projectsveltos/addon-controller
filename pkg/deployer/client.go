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
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var getClientLock = &sync.Mutex{}

const (
	numOfWorker = 10
)

// deployer represents a client implementing the DeployerInterface
type deployer struct {
	log logr.Logger
	client.Client

	ctx context.Context

	mu *sync.Mutex

	// A request represents a request to deploy a feature in a CAPI cluster.

	// dirty contains all requests that have requested to configure a feature
	// and are currenlty waiting to be served.
	dirty []string

	// inProgress contains all request that are currently being served.
	inProgress []string

	// jobQueue contains all requests that needs to be served
	jobQueue []requestParams

	// results contains results for processed request
	results map[string]error

	// features contains currently registered feature ID
	features map[string]bool
}

var deployerInstance *deployer

// GetClient return a deployer client, implementing the DeployerInterface
func GetClient(ctx context.Context, l logr.Logger, c client.Client) *deployer {
	if deployerInstance == nil {
		getClientLock.Lock()
		defer getClientLock.Unlock()
		if deployerInstance == nil {
			l.V(1).Info("Creating single instance now.")
			deployerInstance = &deployer{log: l, Client: c, ctx: ctx}
			// numOfWorker is set to 10 by default. This can be overridden
			// using an env variable
			deployerInstance.startWorkloadWorkers(ctx, numOfWorker, l)
		}
	}

	return deployerInstance
}

func (d *deployer) RegisterFeatureID(
	featureID string,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.features[featureID]; ok {
		return fmt.Errorf("featureID %s is already registered", featureID)
	}

	d.features[featureID] = true
	return nil
}

func (d *deployer) Deploy(
	ctx context.Context,
	clusterNamespace, clusterName, featureID string,
	f requestHandler,
) error {
	key := getKey(clusterNamespace, clusterName, featureID)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.features[featureID]; !ok {
		return fmt.Errorf("featureID %s is not registered", featureID)
	}

	// Search if request is in dirty. Drop it if already there
	for i := range d.dirty {
		if d.dirty[i] == key {
			d.log.V(10).Info("request is already present in dirty")
			return nil
		}
	}

	// Since we got a new request, if a result was saved, clear it.
	d.log.V(10).Info("removing result from previous request if any")
	delete(d.results, key)

	d.log.V(10).Info("request added to dirty")
	d.dirty = append(d.dirty, key)

	// Push to queue if not already in progress
	for i := range d.inProgress {
		if d.inProgress[i] == key {
			d.log.V(10).Info("request is already in inProgress")
			return nil
		}
	}

	d.log.V(10).Info("request added to jobQueue")
	req := requestParams{key: key, handler: f}
	d.jobQueue = append(d.jobQueue, req)

	return nil
}

func (d *deployer) GetResult(
	ctx context.Context,
	clusterNamespace, clusterName, featureID string,
) Result {
	responseParam, err := getRequestStatus(d, clusterNamespace, clusterName, featureID)
	if err != nil {
		return Result{
			ResultStatus: Unavailable,
			Err:          nil,
		}
	}

	if responseParam == nil {
		return Result{
			ResultStatus: InProgress,
			Err:          nil,
		}
	}

	if responseParam.err != nil {
		return Result{
			ResultStatus: Failed,
			Err:          responseParam.err,
		}
	}

	return Result{
		ResultStatus: Deployed,
	}
}
