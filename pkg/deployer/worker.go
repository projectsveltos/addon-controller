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
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A "request" represents the need to deploy a feature in a CAPI cluster.
//
// When a request arrives, the flow is following:
// - when a request to configure feature in a CAPI cluster arrives,
// it is first added to the dirty set or dropped if it already present in the
// dirty set;
// - pushed to the jobQueue only if it is not presented in inProgress.
//
// When a worker is ready to serve a request, it gets the request from the
// front of the jobQueue.
// The request is also added to the inProgress set and removed from the dirty set.
//
// If a request, currently in the inProgress arrives again, such request is only added
// to the dirty set, not to the queue. This guarantees that a request to deploy a feature
// in a CAPI cluster is never process more than once in parallel.
//
// When worker is done, the request is removed from the inProgress set.
// If the same request is also present in the dirty set, it is added back to the back of the jobQueue.

const (
	separator = ":::"
)

type requestParams struct {
	key     string
	handler RequestHandler
}

type responseParams struct {
	requestParams
	err error
}

var (
	controlClusterClient client.Client
)

// startWorkloadWorkers initializes all internal structures and starts
// pool of workers
// - numWorker is number of requested workers
// - c is the kubernetes client to access control cluster
func (d *deployer) startWorkloadWorkers(ctx context.Context, numOfWorker int, logger logr.Logger) {
	d.mu = &sync.Mutex{}
	d.dirty = make([]string, 0)
	d.inProgress = make([]string, 0)
	d.jobQueue = make([]requestParams, 0)
	d.results = make(map[string]error)
	d.features = make(map[string]bool)

	for i := 0; i < numOfWorker; i++ {
		go processRequests(d, i, logger.WithValues("worker", fmt.Sprintf("%d", i)))
	}
}

// GetKey returns a unique ID for a request provided:
// - clusterNamespace and clusterName which are the namespace/name of the CAPI
// cluster where feature needs to be deployed;
// - featureID is a unique identifier for the feature that needs to be deployed.
func GetKey(clusterNamespace, clusterName, applicant, featureID string) string {
	return clusterNamespace + separator + clusterName + separator + applicant + separator + featureID
}

// getFromKey given a unique request key, returns:
// - clusterNamespace and clusterName which are the namespace/name of the CAPI
// cluster where feature needs to be deployed;
// - featureID is a unique identifier for the feature that needs to be deployed.
func getFromKey(key string) (namespace, name, applicant, featureID string) {
	info := strings.Split(key, separator)
	namespace = info[0]
	name = info[1]
	applicant = info[2]
	featureID = info[3]
	return
}

func processRequests(d *deployer, i int, logger logr.Logger) {
	id := i
	var params *requestParams

	logger.V(1).Info(fmt.Sprintf("started worker %d", id))

	for {
		if params != nil {
			l := logger.WithValues("key", params.key)
			ns, name, applicant, featureID := getFromKey(params.key)
			l.Info(fmt.Sprintf("worker: %d processing request", id))
			err := params.handler(d.ctx, controlClusterClient,
				ns, name, applicant, featureID,
				l)
			storeResult(d, params.key, err, params.handler, logger)
		}
		params = nil
		select {
		case <-time.After(1 * time.Second):
			d.mu.Lock()
			if len(d.jobQueue) > 0 {
				// take a request from queue and remove it from queue
				params = &requestParams{key: d.jobQueue[0].key, handler: d.jobQueue[0].handler}
				d.jobQueue = d.jobQueue[1:]
				l := logger.WithValues("key", params.key)
				l.V(10).Info("take from jobQueue")
				// Add to inProgress
				l.V(10).Info("add to inProgress")
				d.inProgress = append(d.inProgress, params.key)
				// If present remove from dirty
				for i := range d.dirty {
					if d.dirty[i] == params.key {
						l.V(10).Info("remove from dirty")
						d.dirty = removeFromSlice(d.dirty, i)
						break
					}
				}
			}
			d.mu.Unlock()
		case <-d.ctx.Done():
			logger.V(1).Info("context cancelled")
			return
		}
	}
}

// doneProcessing does following:
// - set results for further in time lookup
// - remove key from inProgress
// - if key is in dirty, remove it from there and add it to the back of the jobQueue
func storeResult(d *deployer, key string, err error, handler RequestHandler, logger logr.Logger) {
	d.mu.Lock()

	// Remove from inProgress
	for i := range d.inProgress {
		if d.inProgress[i] != key {
			continue
		}
		logger.V(10).Info("remove from inProgress")
		d.inProgress = removeFromSlice(d.inProgress, i)
		break
	}

	if err != nil {
		logger.V(5).Info(fmt.Sprintf("added to result with err %s", err.Error()))
	} else {
		logger.V(5).Info("added to result")
	}
	d.results[key] = err

	// if key is in dirty, remove from there and push to jobQueue
	for i := range d.dirty {
		if d.dirty[i] != key {
			continue
		}
		logger.V(10).Info("add to jobQueue")
		d.jobQueue = append(d.jobQueue, requestParams{key: d.dirty[i], handler: handler})
		logger.V(10).Info("remove from dirty")
		d.dirty = removeFromSlice(d.dirty, i)
		logger.V(10).Info("remove result")
		delete(d.results, key)
		break
	}

	d.mu.Unlock()
}

// getRequestStatus gets requests status.
// If result is available it returns the result.
// If request is still queued, responseParams is nil and an error is nil.
// If result is not available and request is neither queued nor already processed, it returns an error to indicate that.
func getRequestStatus(d *deployer, clusterNamespace, clusterName, applicant, featureID string) (*responseParams, error) {

	key := GetKey(clusterNamespace, clusterName, applicant, featureID)

	logger := d.log.WithValues("key", key)

	d.mu.Lock()
	defer d.mu.Unlock()

	logger.V(5).Info("searching result")
	if _, ok := d.results[key]; ok {
		logger.V(5).Info("request already processed, result present. returning result.")
		if d.results[key] != nil {
			logger.V(5).Info("returning a response with an error")
		}
		resp := responseParams{
			requestParams: requestParams{
				key: key,
			},
			err: d.results[key],
		}
		logger.V(5).Info("removing result")
		delete(d.results, key)
		return &resp, nil
	}

	for i := range d.inProgress {
		if d.inProgress[i] == key {
			logger.V(5).Info("request is still in inProgress, so being processed")
			return nil, nil
		}
	}

	for i := range d.jobQueue {
		if d.jobQueue[i].key == key {
			logger.V(5).Info("request is still in jobQueue, so waiting to be processed.")
			return nil, nil
		}
	}

	// if we get here it means, we have no response for this workload cluster, nor the
	// request is queued or being processed
	logger.V(5).Info("request has not been processed nor is currently queued.")
	return nil, fmt.Errorf("request has not been processed nor is currently queued")
}

func removeFromSlice(s []string, i int) []string {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}
