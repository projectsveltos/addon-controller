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
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// ClusterSummaryFinalizer allows ClusterSummaryReconciler to clean up resources associated with
	// ClusterSummary before removing it from the apiserver.
	ClusterSummaryFinalizer = "clustersummaryfinalizer.projectsveltos.io"

	ClusterSummaryKind = "ClusterSummary"
)

// +kubebuilder:validation:Enum:=Resources;Helm
type FeatureID string

const (
	// FeatureResources is the identifier for generic Resources feature
	FeatureResources = FeatureID("Resources")

	// FeatureHelm is the identifier for Helm feature
	FeatureHelm = FeatureID("Helm")
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

	// DeployedGroupVersionKind contains all GroupVersionKinds deployed in the workload
	// cluster because of this feature. Each element has format kind.version.group
	// +optional
	DeployedGroupVersionKind []string `json:"deployedGroupVersionKind,omitempty"`

	// LastAppliedTime is the time feature was last reconciled
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`
}

// HelChartStatus specifies whether ClusterSummary is successfully managing
// an helm chart or not
//+kubebuilder:validation:Enum:=Managing;Conflict
type HelChartStatus string

const (
	// HelChartStatusManaging indicates helm chart is successfully being managed
	HelChartStatusManaging = HelChartStatus("Managing")

	// HelChartStatusConflict indicates there is a conflict with another
	// ClusterSummary to manage the helm chart
	HelChartStatusConflict = HelChartStatus("Conflict")
)

type HelmChartSummary struct {
	// ReleaseName is the chart release
	// +kubebuilder:validation:MinLength=1
	ReleaseName string `json:"releaseName"`

	// ReleaseNamespace is the namespace release will be installed
	// +kubebuilder:validation:MinLength=1
	ReleaseNamespace string `json:"releaseNamespace"`

	// Status indicates whether ClusterSummary can manage the helm
	// chart or there is a conflict
	Status HelChartStatus `json:"status"`

	// Status indicates whether ClusterSummary can manage the helm
	// chart or there is a conflict
	// +optional
	ConflictMessage string `json:"conflictMessage,omitempty"`
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
	FeatureSummaries []FeatureSummary `json:"featureSummaries,omitempty"`

	// HelmReleaseSummaries reports the status of each helm chart
	// directly managed by ClusterFeature.
	// +listType=atomic
	// +optional
	HelmReleaseSummaries []HelmChartSummary `json:"helmReleaseSummaries,omitempty"`
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

// GetOwnerClusterFeatureName returns the ClusterFeature owning a given ClusterSummary
func GetOwnerClusterFeatureName(clusterSummary *ClusterSummary) (*metav1.OwnerReference, error) {
	for _, ref := range clusterSummary.OwnerReferences {
		if ref.Kind != ClusterFeatureKind {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if gv.Group == GroupVersion.Group {
			return &ref, nil
		}
	}

	return nil, fmt.Errorf("ClusterFeature owner not found")
}
