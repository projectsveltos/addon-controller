/*
Copyright 2022-26. projectsveltos.io. All rights reserved.

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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

// deploymentContext bundles the per-reconciliation data threaded through most
// deployment functions, avoiding long repeated argument lists.
type deploymentContext struct {
	clusterSummary *configv1beta1.ClusterSummary
	clusterObjects *currentClusterObjects
	mgmtResources  map[string]*unstructured.Unstructured
}
