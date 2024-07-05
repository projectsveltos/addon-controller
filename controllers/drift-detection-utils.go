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

package controllers

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// When this annotation is set, resource will be excluded from configuration
	// drift detection
	driftDetectionIgnoreAnnotation = "projectsveltos.io/driftDetectionIgnore"
)

// hasIgnoreConfigurationDriftAnnotation verifies whether resource has
// `projectsveltos.io/driftDetectionIgnore` annotation. Any resource with such
// annotation set won't be tracked for configuration drift.
func hasIgnoreConfigurationDriftAnnotation(resource *unstructured.Unstructured) bool {
	annotations := resource.GetAnnotations()
	if annotations != nil {
		if _, ok := annotations[driftDetectionIgnoreAnnotation]; ok {
			return true
		}
	}

	return false
}
