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

package v1beta1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	ClusterConfigurationKind = "ClusterConfiguration"
)

type Chart struct {
	// RepoURL URL of the repo containing the helm chart deployed
	// in the Cluster.
	// +kubebuilder:validation:MinLength=1
	RepoURL string `json:"repoURL"`

	// ReleaseName name of the release deployed in the Cluster.
	// +kubebuilder:validation:MinLength=1
	ReleaseName string `json:"releaseName"`

	// Namespace where chart is deployed in the Cluster.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ChartVersion is the version of the helm chart deployed in the Cluster.
	ChartVersion string `json:"chartVersion"`

	// AppVersion is the version of the app deployed in the Cluster.
	// +optional
	AppVersion string `json:"appVersion,omitempty"`

	// The URL to an icon file.
	Icon string `json:"icon,omitempty"`

	// LastAppliedTime identifies when this resource was last applied to the cluster.
	LastAppliedTime *metav1.Time `json:"lastAppliedTime"`
}

type Feature struct {
	// FeatureID is an indentifier of the feature whose status is reported
	FeatureID libsveltosv1beta1.FeatureID `json:"featureID"`

	// Resources is a list of resources deployed in the Cluster.
	// +optional
	Resources []libsveltosv1beta1.Resource `json:"resources,omitempty"`

	// Charts is a list of helm charts deployed in the Cluster.
	// +optional
	Charts []Chart `json:"charts,omitempty"`
}

// ProfileResource keeps info on all of the resources deployed in this Cluster
// due to a given Profile
type ProfileResource struct {
	// ProfileName is the name of the Profile matching the Cluster.
	ProfileName string `json:"profileName"`

	// Features contains the list of policies deployed in the Cluster because
	// of a given feature
	// +optional
	Features []Feature `json:"Features,omitempty"`
}

// ClusterProfileResource keeps info on all of the resources deployed in this Cluster
// due to a given ClusterProfile
type ClusterProfileResource struct {
	// ProfileName is the name of the ClusterProfile matching the Cluster.
	ClusterProfileName string `json:"clusterProfileName"`

	// Features contains the list of policies deployed in the Cluster because
	// of a given feature
	// +optional
	Features []Feature `json:"Features,omitempty"`
}

// ClusterConfigurationStatus defines the observed state of ClusterConfiguration
type ClusterConfigurationStatus struct {
	// ClusterProfileResources is the list of resources currently deployed in a Cluster due
	// to ClusterProfiles
	// +optional
	ClusterProfileResources []ClusterProfileResource `json:"clusterProfileResources,omitempty"`

	// ProfileResources is the list of resources currently deployed in a Cluster due
	// to Profiles
	// +optional
	ProfileResources []ProfileResource `json:"profileResources,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=clusterconfigurations,scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:storageversion

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

func init() {
	SchemeBuilder.Register(&ClusterConfiguration{}, &ClusterConfigurationList{})
}

// GetClusterConfigurationSectionIndex returns Status.ClusterProfileResources index for given ClusterProfile.
// If not found, returns an error
func GetClusterConfigurationSectionIndex(clusterConfiguration *ClusterConfiguration,
	profileKind, profileName string) (int, error) {

	if profileKind == ClusterProfileKind {
		for i := range clusterConfiguration.Status.ClusterProfileResources {
			if clusterConfiguration.Status.ClusterProfileResources[i].ClusterProfileName == profileName {
				return i, nil
			}
		}
	} else {
		for i := range clusterConfiguration.Status.ProfileResources {
			if clusterConfiguration.Status.ProfileResources[i].ProfileName == profileName {
				return i, nil
			}
		}
	}

	return -1, fmt.Errorf("section for %s/%s not present in clusterConfiguration %s/%s",
		profileKind, profileName, clusterConfiguration.Namespace, clusterConfiguration.Name)
}
