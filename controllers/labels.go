/*
Copyright 2022-23. projectsveltos.io. All rights reserved.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ClusterSummaryLabelName is added to each policy deployed by a ClusterSummary
	// instance to a CAPI Cluster
	ClusterSummaryLabelName = "projectsveltos.io/cluster-summary-name"

	// ClusterProfileLabelName is added to all ClusterSummary instances created
	// by a ClusterProfile instance
	ClusterProfileLabelName = "projectsveltos.io/cluster-profile-name"

	// clusterLabelNamespace is the label set on ClusterSummary instances created
	// by a ClusterProfile instance for a given cluster
	ClusterLabelNamespace = "projectsveltos.io/cluster-namespace"

	// clusterLabelName is the label set on:
	// - ClusterSummary instances created by a ClusterProfile instance for a given cluster;
	// - ClusterConfiguration instances created by a ClusterProfile instance for a given cluster;
	// - ClusterReport instances created by a ClusterProfile instance for a given cluster;
	ClusterLabelName = "projectsveltos.io/cluster-name"

	// ClusterTypeLabelName is the label set on:
	// - ClusterSummary instances created by a ClusterProfile instance for a given cluster;
	// - ClusterConfiguration instances created by a ClusterProfile instance for a given cluster;
	// - ClusterReport instances created by a ClusterProfile instance for a given cluster;
	ClusterTypeLabelName = "projectsveltos.io/cluster-type"

	// PolicyTemplate is the annotation that must be set on a policy when the
	// policy is a template and needs variable sustitution.
	PolicyTemplate = "projectsveltos.io/template"
)

// addLabel adds label to an object
func addLabel(obj metav1.Object, labelKey, labelValue string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelKey] = labelValue
	obj.SetLabels(labels)
}

// addAnnotation adds annotation to an object
func addAnnotation(obj metav1.Object, annotationKey, annotationValue string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annotationKey] = annotationValue
	obj.SetAnnotations(annotations)
}
