/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

package clusterops

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// Default JobCheck implementation: reports the feature is unavailable. A Sveltos Enterprise
// build overrides this via SetJobHealthCheckValidator/SetJobHealthCheckResolver before
// starting the manager.
func init() {
	validateJobHealthCheck = func(_ context.Context, _ JobHealthCheckDeps,
		check *libsveltosv1beta1.ValidateHealth, _ logr.Logger) error {

		return fmt.Errorf("JobCheck (%s) requires a Sveltos Enterprise build", check.Name)
	}

	resolveJobHealthCheck = func(_ context.Context, _ JobHealthCheckDeps,
		check *libsveltosv1beta1.ValidateHealth, _ logr.Logger) (*batchv1.Job, error) {

		return nil, fmt.Errorf("JobCheck (%s) requires a Sveltos Enterprise build", check.Name)
	}
}
