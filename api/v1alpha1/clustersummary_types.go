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

package v1alpha1

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	// ClusterSummaryFinalizer allows ClusterSummaryReconciler to clean up resources associated with
	// ClusterSummary before removing it from the apiserver.
	ClusterSummaryFinalizer = "clustersummaryfinalizer.projectsveltos.io"

	ClusterSummaryKind = "ClusterSummary"
)

// +kubebuilder:validation:Enum:=Resources;Helm;Kustomize
type FeatureID string

const (
	// FeatureResources is the identifier for generic Resources feature
	FeatureResources = FeatureID("Resources")

	// FeatureHelm is the identifier for Helm feature
	FeatureHelm = FeatureID("Helm")

	// FeatureKustomize is the identifier for Kustomize feature
	FeatureKustomize = FeatureID("Kustomize")
)

// +kubebuilder:validation:Enum:=Provisioning;Provisioned;Failed;FailedNonRetriable;Removing;Removed
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

	// FeatureStatusFailedNonRetriable indicates that configuring the feature
	// in the workload cluster failed with a non retriable error
	FeatureStatusFailedNonRetriable = FeatureStatus("FailedNonRetriable")

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
	// +optional
	Status FeatureStatus `json:"status,omitempty"`

	// FailureReason indicates the type of error that occurred.
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// FailureMessage provides more information about the error.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`

	// DeployedGroupVersionKind contains all GroupVersionKinds deployed in either
	// the workload cluster or the management cluster because of this feature.
	// Each element has format kind.version.group
	// +optional
	DeployedGroupVersionKind []string `json:"deployedGroupVersionKind,omitempty"`

	// LastAppliedTime is the time feature was last reconciled
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`
}

// HelChartStatus specifies whether ClusterSummary is successfully managing
// an helm chart or not
// +kubebuilder:validation:Enum:=Managing;Conflict
type HelmChartStatus string

const (
	// HelChartStatusManaging indicates helm chart is successfully being managed
	HelmChartStatusManaging = HelmChartStatus("Managing")

	// HelChartStatusConflict indicates there is a conflict with another
	// ClusterSummary to manage the helm chart
	HelmChartStatusConflict = HelmChartStatus("Conflict")
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
	Status HelmChartStatus `json:"status"`

	// ValuesHash represents of a unique value for the values section
	// +optional
	ValuesHash []byte `json:"valuesHash,omitempty"`

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

	// ClusterType is the type of Cluster
	ClusterType libsveltosv1alpha1.ClusterType `json:"clusterType"`

	// ClusterProfileSpec represent the configuration that will be applied to
	// the workload cluster.
	ClusterProfileSpec Spec `json:"clusterProfileSpec,omitempty"`
}

// ClusterSummaryStatus defines the observed state of ClusterSummary
type ClusterSummaryStatus struct {
	// Dependencies is a summary reporting the status of the dependencies
	// for the associated ClusterProfile
	Dependencies *string `json:"dependencies,omitempty"`

	// FeatureSummaries reports the status of each workload cluster feature
	// directly managed by ClusterProfile.
	// +listType=map
	// +listMapKey=featureID
	// +optional
	FeatureSummaries []FeatureSummary `json:"featureSummaries,omitempty"`

	// HelmReleaseSummaries reports the status of each helm chart
	// directly managed by ClusterProfile.
	// +listType=atomic
	// +optional
	HelmReleaseSummaries []HelmChartSummary `json:"helmReleaseSummaries,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=clustersummaries,scope=Namespaced
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

func init() {
	SchemeBuilder.Register(&ClusterSummary{}, &ClusterSummaryList{})
}

func GetClusterSummary(ctx context.Context, c client.Client, namespace, name string,
) (*ClusterSummary, error) {

	clusterSummary := &ClusterSummary{}
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, clusterSummary)
	if err != nil {
		return nil, err
	}

	return clusterSummary, nil
}

// GetProfileOwnerReference returns the ClusterProfile/Profile owning a given ClusterSummary
func GetProfileOwnerReference(clusterSummary *ClusterSummary) (*metav1.OwnerReference, error) {
	for _, ref := range clusterSummary.OwnerReferences {
		if ref.Kind != ClusterProfileKind &&
			ref.Kind != ProfileKind {

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

	return nil, fmt.Errorf("(Cluster)Profile owner not found")
}

// GetProfileOwnerAndTier returns the (Cluster)Profile owning this clusterSummary and its tier.
// Returns nil if (Cluster)Profile does not exist anymore.
func GetProfileOwnerAndTier(ctx context.Context, c client.Client, clusterSummary *ClusterSummary,
) (client.Object, int32, error) {

	for _, ref := range clusterSummary.OwnerReferences {
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, 0, errors.WithStack(err)
		}
		if gv.Group != GroupVersion.Group {
			continue
		}

		if ref.Kind == ClusterProfileKind {
			clusterProfile := &ClusterProfile{}
			err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, clusterProfile)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, 0, nil
				}
				return nil, 0, err
			}
			return clusterProfile, clusterProfile.Spec.Tier, nil
		} else if ref.Kind == ProfileKind {
			profile := &Profile{}
			err := c.Get(ctx,
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: ref.Name},
				profile)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, 0, nil
				}
				return nil, 0, err
			}
			return profile, profile.Spec.Tier, nil
		}
	}
	return nil, 0, nil
}
