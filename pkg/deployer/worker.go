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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
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
	controlClusterClient = d.Client

	for i := 0; i < numOfWorker; i++ {
		go processRequests(ctx, d, i, logger.WithValues("worker", fmt.Sprintf("%d", i)))
	}
}

// GetKey returns a unique ID for a request provided:
// - clusterNamespace and clusterName which are the namespace/name of the CAPI
// cluster where feature needs to be deployed;
// - featureID is a unique identifier for the feature that needs to be deployed.
func GetKey(clusterNamespace, clusterName, applicant, featureID string, cleanup bool) string {
	return clusterNamespace + separator + clusterName + separator + applicant + separator + featureID + separator + strconv.FormatBool(cleanup)
}

// getClusterFromKey given a unique request key, returns:
// - clusterNamespace and clusterName which are the namespace/name of the CAPI
// cluster where feature needs to be deployed;
func getClusterFromKey(key string) (namespace, name string, err error) {
	info := strings.Split(key, separator)
	const length = 5
	if len(info) != length {
		err = fmt.Errorf("key: %s is malformed", key)
		return
	}
	namespace = info[0]
	name = info[1]
	return
}

// getApplicatantAndFeatureFromKey given a unique request key, returns:
// - featureID is a unique identifier for the feature that needs to be deployed;
func getApplicatantAndFeatureFromKey(key string) (applicant, featureID string, err error) {
	info := strings.Split(key, separator)
	const length = 5
	if len(info) != length {
		err = fmt.Errorf("key: %s is malformed", key)
		return
	}
	applicant = info[2]
	featureID = info[3]
	return
}

// getIsCleanupFromKey returns true if the request was for cleanup
func getIsCleanupFromKey(key string) (cleanup bool, err error) {
	info := strings.Split(key, separator)
	const length = 5
	if len(info) != length {
		err = fmt.Errorf("key: %s is malformed", key)
		return
	}
	cleanup, err = strconv.ParseBool(info[4])
	return
}

func processRequests(ctx context.Context, d *deployer, i int, logger logr.Logger) {
	id := i
	var params *requestParams

	logger.V(logs.LogInfo).Info(fmt.Sprintf("started worker %d", id))

	for {
		if params != nil {
			l := logger.WithValues("key", params.key)
			// Get error only from getIsCleanupFromKey as same key is always used
			ns, name, _ := getClusterFromKey(params.key)
			applicant, featureID, _ := getApplicatantAndFeatureFromKey(params.key)
			cleanup, err := getIsCleanupFromKey(params.key)
			l.Info(fmt.Sprintf("worker: %d processing request. cleanup: %t", id, cleanup))
			if err != nil {
				storeResult(d, params.key, err, params.handler, logger)
			} else {
				start := time.Now()
				err = params.handler(ctx, controlClusterClient,
					ns, name, applicant, featureID,
					l)
				storeResult(d, params.key, err, params.handler, logger)
				elapsed := time.Since(start)
				programDuration(elapsed, ns, name, featureID, l)
			}
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
				l.V(logs.LogVerbose).Info("take from jobQueue")
				// Add to inProgress
				l.V(logs.LogVerbose).Info("add to inProgress")
				d.inProgress = append(d.inProgress, params.key)
				// If present remove from dirty
				for i := range d.dirty {
					if d.dirty[i] == params.key {
						l.V(logs.LogVerbose).Info("remove from dirty")
						d.dirty = removeFromSlice(d.dirty, i)
						break
					}
				}
			}
			d.mu.Unlock()
		case <-ctx.Done():
			logger.V(logs.LogInfo).Info("context canceled")
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
		logger.V(logs.LogVerbose).Info("remove from inProgress")
		d.inProgress = removeFromSlice(d.inProgress, i)
		break
	}

	l := logger.WithValues("key", key)

	if err != nil {
		l.V(logs.LogDebug).Info(fmt.Sprintf("added to result with err %s", err.Error()))
	} else {
		l.V(logs.LogDebug).Info("added to result")
	}
	d.results[key] = err

	// if key is in dirty, remove from there and push to jobQueue
	for i := range d.dirty {
		if d.dirty[i] != key {
			continue
		}
		l.V(logs.LogVerbose).Info("add to jobQueue")
		d.jobQueue = append(d.jobQueue, requestParams{key: d.dirty[i], handler: handler})
		l.V(logs.LogVerbose).Info("remove from dirty")
		d.dirty = removeFromSlice(d.dirty, i)
		l.V(logs.LogVerbose).Info("remove result")
		delete(d.results, key)
		break
	}

	d.mu.Unlock()
}

// getRequestStatus gets requests status.
// If result is available it returns the result.
// If request is still queued, responseParams is nil and an error is nil.
// If result is not available and request is neither queued nor already processed, it returns an error to indicate that.
func getRequestStatus(d *deployer, clusterNamespace, clusterName, applicant, featureID string,
	cleanup bool) (*responseParams, error) {

	key := GetKey(clusterNamespace, clusterName, applicant, featureID, cleanup)

	logger := d.log.WithValues("key", key)

	d.mu.Lock()
	defer d.mu.Unlock()

	logger.V(logs.LogDebug).Info("searching result")
	if _, ok := d.results[key]; ok {
		logger.V(logs.LogDebug).Info("request already processed, result present. returning result.")
		if d.results[key] != nil {
			logger.V(logs.LogDebug).Info("returning a response with an error")
		}
		resp := responseParams{
			requestParams: requestParams{
				key: key,
			},
			err: d.results[key],
		}
		logger.V(logs.LogDebug).Info("removing result")
		delete(d.results, key)
		return &resp, nil
	}

	for i := range d.inProgress {
		if d.inProgress[i] == key {
			logger.V(logs.LogDebug).Info("request is still in inProgress, so being processed")
			return nil, nil
		}
	}

	for i := range d.jobQueue {
		if d.jobQueue[i].key == key {
			logger.V(logs.LogDebug).Info("request is still in jobQueue, so waiting to be processed.")
			return nil, nil
		}
	}

	// if we get here it means, we have no response for this workload cluster, nor the
	// request is queued or being processed
	logger.V(logs.LogDebug).Info("request has not been processed nor is currently queued.")
	return nil, fmt.Errorf("request has not been processed nor is currently queued")
}

func removeFromSlice(s []string, i int) []string {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

func programDuration(elapsed time.Duration, clusterNamespace, clusterName, featureID string,
	logger logr.Logger) {

	if featureID == string(configv1alpha1.FeatureResources) {
		programResourceDurationHistogram.Observe(elapsed.Seconds())
		clusterHistogram := newResourceHistogram(clusterNamespace, clusterName, logger)
		if clusterHistogram != nil {
			logger.V(logs.LogVerbose).Info(fmt.Sprintf("register data for %s/%s %s",
				clusterNamespace, clusterName, featureID))
			clusterHistogram.Observe(elapsed.Seconds())
		}
	} else {
		programChartDurationHistogram.Observe(elapsed.Seconds())
		clusterHistogram := newChartHistogram(clusterNamespace, clusterName, logger)
		if clusterHistogram != nil {
			logger.V(logs.LogVerbose).Info(fmt.Sprintf("register data for %s/%s %s",
				clusterNamespace, clusterName, featureID))
			clusterHistogram.Observe(elapsed.Seconds())
		}
	}
}
