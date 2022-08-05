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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ClusterSummaryFinalizer allows ClusterSummaryReconciler to clean up resources associated with
	// ClusterSummary before removing it from the apiserver.
	ClusterSummaryFinalizer = "clustersummaryfinalizer.projectsveltos.io"
)

// +kubebuilder:validation:Enum:=Kyverno;Role
type FeatureID string

const (
	// FeatureKyverno is the identifier for Kyverno feature
	FeatureKyverno = FeatureID("Kyverno")

	// FeatureRole is the identifier for ClusterRole/Role feature
	FeatureRole = FeatureID("Role")
)

// +kubebuilder:validation:Enum:=Provisioning;Provisioned;Failed;Removing;Removed
type FeatureStatus string

const (
	// FeatureStatusProvisioning indicates that feature is being
	// provisioned in the workload cluster
	FeatureStatusProvisioning = FeatureStatus("Provisioning")

	// FeatureStatusProvisioned indicates that feature has being
	// provisioned in the workload cluster
	FeatureStatusProvisioned = FeatureStatus("Provisioned")

	// FeatureStatusFailed indicates that configuring the feature
	// in the workload cluster failed
	FeatureStatusFailed = FeatureStatus("Failed")

	// FeatureStatusRemoving indicates that feature is being
	// removed
	FeatureStatusRemoving = FeatureStatus("Removing")

	// FeatureStatusRemoved indicates that feature is removed
	FeatureStatusRemoved = FeatureStatus("Removed")
)

// FeatureSummary contains a summary of the state of a workload
// cluster feature.
type FeatureSummary struct {
	// FeatureID is an indentifier of the feature whose status is reported
	FeatureID FeatureID `json:"featureID"`

	// Hash represents of a unique value for a feature at a fixed point in
	// time
	// +optional
	Hash []byte `json:"hash,omitempty"`

	// Status represents the state of the feature in the workload cluster
	Status FeatureStatus `json:"status"`

	// FailureReason indicates the type of error that occurred.
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// FailureMessage provides more information about the error.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// ClusterSummarySpec defines the desired state of ClusterSummary
type ClusterSummarySpec struct {
	// ClusterNamespace is the namespace of the workload Cluster this
	// ClusterSummary is for.
	ClusterNamespace string `json:"clusterNamespace"`

	// ClusterName is the name of the workload Cluster this ClusterSummary is for.
	ClusterName string `json:"clusterName"`

	// ClusterFeatureSpec represent the configuration that will be applied to
	// the workload cluster.
	ClusterFeatureSpec ClusterFeatureSpec `json:"clusterFeatureSpec,omitempty"`
}

// ClusterSummaryStatus defines the observed state of ClusterSummary
type ClusterSummaryStatus struct {
	// FeatureSummaries reports the status of each workload cluster feature
	// directly managed by ClusterFeature.
	// +listType=atomic
	// +optional
	FeatureSummaries []FeatureSummary `json:"clusterSummaries,omitempty"`

	// PolicyPrefix is the prefix added to policies deployed by ClusterSummary
	// instance in a CAPI Cluster
	PolicyPrefix string `json:"policyPrefix,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=clustersummaries,scope=Cluster
//+kubebuilder:subresource:status

// ClusterSummary is the Schema for the clustersummaries API
type ClusterSummary struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterSummarySpec   `json:"spec,omitempty"`
	Status ClusterSummaryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterSummaryList contains a list of ClusterSummary
type ClusterSummaryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterSummary `json:"items"`
}

// nolint: gochecknoinits // forced pattern, can't workaround
func init() {
	SchemeBuilder.Register(&ClusterSummary{}, &ClusterSummaryList{})
}
