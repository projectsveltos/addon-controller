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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ClusterConfigurationKind = "ClusterConfiguration"
)

type Resource struct {
	// Name of the resource deployed in the CAPI Cluster.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the resource deployed in the CAPI Cluster.
	// Empty for resources scoped at cluster level.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Group of the resource deployed in the CAPI Cluster.
	Group string `json:"group"`

	// Kind of the resource deployed in the CAPI Cluster.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// LastAppliedTime identifies when this resource was last applied to the cluster.
	LastAppliedTime *metav1.Time `json:"lastAppliedTime"`

	// Owner is the list of ConfigMap/Secret containing this resource.
	Owner corev1.ObjectReference `json:"owner"`
}

type Feature struct {
	// FeatureID is an indentifier of the feature whose status is reported
	FeatureID FeatureID `json:"featureID"`

	// Resources is a list of resources deployed in the CAPI Cluster.
	// +optional
	Resources []Resource `json:"resources,omitempty"`
}

// ClusterFeatureResource keeps info on all of the resources deployed in this CAPI Cluster
// due to a given ClusterFeature
type ClusterFeatureResource struct {
	// ClusterFeatureName is the name of the ClusterFeature matching the CAPI Cluster.
	ClusterFeatureName string `json:"clusterFeatureName"`

	// Features contains the list of policies deployed in the CAPI Cluster because
	// of a given feature
	// +optional
	Features []Feature `json:"Features,omitempty"`
}

// ClusterConfigurationStatus defines the observed state of ClusterConfiguration
type ClusterConfigurationStatus struct {
	// ClusterFeatureResources is the list of resources currently deployed in a CAPI Cluster due
	// to ClusterFeatures
	// +optional
	ClusterFeatureResources []ClusterFeatureResource `json:"clusterFeatureResources,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=clusterconfigurations,scope=Namespaced
//+kubebuilder:subresource:status

// ClusterConfiguration is the Schema for the clusterconfigurations API
type ClusterConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Status ClusterConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterConfigurationList contains a list of ClusterConfiguration
type ClusterConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterConfiguration `json:"items"`
}

// nolint: gochecknoinits // forced pattern, can't workaround
func init() {
	SchemeBuilder.Register(&ClusterConfiguration{}, &ClusterConfigurationList{})
}

// GetClusterConfigurationSectionIndex returns Status.ClusterFeatureResources index for given ClusterFeature.
// If not found, returns an error
func GetClusterConfigurationSectionIndex(clusterConfiguration *ClusterConfiguration, clusterFeatureName string) (int, error) {
	for i := range clusterConfiguration.Status.ClusterFeatureResources {
		if clusterConfiguration.Status.ClusterFeatureResources[i].ClusterFeatureName == clusterFeatureName {
			return i, nil
		}
	}

	return -1, fmt.Errorf("section for ClusterFeature %s not present in clusterConfiguration %s/%s",
		clusterFeatureName, clusterConfiguration.Namespace, clusterConfiguration.Name)
}
