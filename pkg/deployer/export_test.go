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

var (
	GetClusterFromKey               = getClusterFromKey
	GetApplicatantAndFeatureFromKey = getApplicatantAndFeatureFromKey
	GetIsCleanupFromKey             = getIsCleanupFromKey
	RemoveFromSlice                 = removeFromSlice

	StoreResult      = storeResult
	GetRequestStatus = getRequestStatus
	ProcessRequests  = processRequests
)

func (d *deployer) SetInProgress(inProgress []string) {
	d.inProgress = inProgress
}

func (d *deployer) GetInProgress() []string {
	return d.inProgress
}

func (d *deployer) SetDirty(dirty []string) {
	d.dirty = dirty
}

func (d *deployer) GetDirty() []string {
	return d.dirty
}

func (d *deployer) SetJobQueue(key string, handler RequestHandler) {
	reqParam := requestParams{
		key:     key,
		handler: handler,
	}
	d.jobQueue = []requestParams{reqParam}
}

func (d *deployer) GetJobQueue() []requestParams {
	return d.jobQueue
}

func (d *deployer) SetResults(results map[string]error) {
	d.results = results
}

func (d *deployer) ClearInternalStruct() {
	d.dirty = make([]string, 0)
	d.inProgress = make([]string, 0)
	d.jobQueue = make([]requestParams, 0)
	d.results = make(map[string]error)
	d.features = make(map[string]bool)
}

func (d *deployer) GetResults() map[string]error {
	return d.results
}

func IsResponseDeployed(resp *responseParams) bool {
	return resp != nil && resp.err == nil
}

func IsResponseFailed(resp *responseParams) bool {
	return resp != nil && resp.err != nil
}
