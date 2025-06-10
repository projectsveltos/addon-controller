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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// HelmAction represents the type of action on a give resource or helm release
type HelmAction string

// Define the HelmAction constants.
const (
	NoHelmAction           HelmAction = "No Action"
	UpdateHelmValuesAction HelmAction = "Update Values"
	InstallHelmAction      HelmAction = "Install"
	UpgradeHelmAction      HelmAction = "Upgrade"
	UninstallHelmAction    HelmAction = "Delete"
	ConflictHelmAction     HelmAction = "Conflict"
)

type ReleaseReport struct {
	// ReleaseName of the release deployed in the CAPI Cluster.
	// +kubebuilder:validation:MinLength=1
	ReleaseName string `json:"chartName"`

	// Namespace where release is deployed in the CAPI Cluster.
	// +kubebuilder:validation:MinLength=1
	ReleaseNamespace string `json:"releaseNamespace"`

	// ChartVersion is the version of the helm chart deployed
	// in the CAPI Cluster.
	ChartVersion string `json:"chartVersion"`

	// Action represent the type of operation on the Helm Chart
	// +kubebuilder:validation:Enum=No Action;Install;Upgrade;Delete;Conflict;Update Values
	// +optional
	Action string `json:"action,omitempty"`

	// Message is for any message that needs to added to better
	// explain the action.
	// +optional
	Message string `json:"message,omitempty"`
}

// ClusterReportSpec defines the desired state of ClusterReport
type ClusterReportSpec struct {
	// ClusterNamespace is the namespace of the CAPI Cluster this
	// ClusterReport is for.
	ClusterNamespace string `json:"clusterNamespace"`

	// ClusterName is the name of the CAPI Cluster this ClusterReport
	// is for.
	ClusterName string `json:"clusterName"`
}

// ClusterReportStatus defines the observed state of ClusterReport
type ClusterReportStatus struct {
	// ReleaseReports contains report on helm releases
	// +optional
	ReleaseReports []ReleaseReport `json:"releaseReports,omitempty"`

	// HelmResourceReports contains report on helm resources (when in pull mode, helm template resources are
	// deployed directly)
	// +optional
	HelmResourceReports []libsveltosv1beta1.ResourceReport `json:"helmResourceReports,omitempty"`

	// ResourceReports contains report on Kubernetes resources
	// deployed because of PolicyRefs
	// +optional
	ResourceReports []libsveltosv1beta1.ResourceReport `json:"resourceReports,omitempty"`

	// KustomizeResourceReports contains report on Kubernetes resources
	// deployed because of KustomizationRefs
	// +optional
	KustomizeResourceReports []libsveltosv1beta1.ResourceReport `json:"kustomizeResourceReports,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=clusterreports,scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:storageversion

// ClusterReport is the Schema for the clusterreports API
type ClusterReport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterReportSpec   `json:"spec,omitempty"`
	Status ClusterReportStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterReportList contains a list of ClusterReport
type ClusterReportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterReport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterReport{}, &ClusterReportList{})
}
