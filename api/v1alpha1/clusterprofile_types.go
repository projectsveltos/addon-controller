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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	// ClusterProfileFinalizer allows ClusterProfileReconciler to clean up resources associated with
	// ClusterProfile before removing it from the apiserver.
	ClusterProfileFinalizer = "clusterprofilefinalizer.projectsveltos.io"

	ClusterProfileKind = "ClusterProfile"
)

type Selector string

// ReferencedResourceKind is a string representation of allowed kind of resources
// that can be referenced in a ClusterProfile
type ReferencedResourceKind string

// Define the ReferencedResourceKind constants.
const (
	SecretReferencedResourceKind    ReferencedResourceKind = "Secret"
	ConfigMapReferencedResourceKind ReferencedResourceKind = "ConfigMap"
)

type DryRunReconciliationError struct{}

func (m *DryRunReconciliationError) Error() string {
	return "mode is DryRun. Nothing is reconciled"
}

// SyncMode specifies how features are synced in a workload cluster.
// +kubebuilder:validation:Enum:=OneTime;Continuous;DryRun
type SyncMode string

const (
	// SyncModeOneTime indicates feature sync should happen only once
	SyncModeOneTime = SyncMode("OneTime")

	// SyncModeContinuous indicates feature sync should continuously happen
	SyncModeContinuous = SyncMode("Continuous")

	// SyncModeDryRun indicates feature sync should continuously happen
	// no feature will be updated in the CAPI Cluster though.
	SyncModeDryRun = SyncMode("DryRun")
)

// HelmChartAction specifies action on an helm chart
// +kubebuilder:validation:Enum:=Install;Uninstall
type HelmChartAction string

const (
	// HelmChartActionInstall will cause Helm chart to be installed
	HelmChartActionInstall = HelmChartAction("Install")

	// HelmChartActionUninstall will cause Helm chart to be removed
	HelmChartActionUninstall = HelmChartAction("Uninstall")
)

type HelmChart struct {
	// RepositoryURL is the URL helm chart repository
	// +kubebuilder:validation:MinLength=1
	RepositoryURL string `json:"repositoryURL"`

	// RepositoryName is the name helm chart repository
	// +kubebuilder:validation:MinLength=1
	RepositoryName string `json:"repositoryName"`

	// ChartName is the chart name
	// +kubebuilder:validation:MinLength=1
	ChartName string `json:"chartName"`

	// ChartVersion is the chart version
	// +kubebuilder:validation:MinLength=1
	ChartVersion string `json:"chartVersion"`

	// ReleaseName is the chart release
	// +kubebuilder:validation:MinLength=1
	ReleaseName string `json:"releaseName"`

	// ReleaseNamespace is the namespace release will be installed
	// +kubebuilder:validation:MinLength=1
	ReleaseNamespace string `json:"releaseNamespace"`

	// Values holds the values for this Helm release.
	// Go templating with the values from the referenced CAPI Cluster.
	// Currently following can be referenced:
	// Cluster => CAPI Cluster for instance  {{ index .Cluster.Spec.ClusterNetwork.Pods.CIDRBlocks 0 }}
	// KubeadmControlPlane => the CAPI Cluster controlPlaneRef
	// InfrastructureProvider => the CAPI cluster infrastructure provider
	// +optional
	Values string `json:"values,omitempty"`

	// HelmChartAction is the action that will be taken on the helm chart
	// +kubebuilder:default:=Install
	// +optional
	HelmChartAction HelmChartAction `json:"helmChartAction,omitempty"`
}

// StopMatchingBehavior indicates what will happen when Cluster stops matching
// a ClusterProfile. By default, withdrawpolicies, deployed Helm charts and Kubernetes
// resources will be removed from Cluster. LeavePolicy instead leaves Helm charts
// and Kubernetes policies in the Cluster.
type StopMatchingBehavior string

// Define the StopMatchingBehavior constants.
const (
	WithdrawPolicies StopMatchingBehavior = "WithdrawPolicies"
	LeavePolicies    StopMatchingBehavior = "LeavePolicies"
)

// ClusterProfileSpec defines the desired state of ClusterProfile
type ClusterProfileSpec struct {
	// ClusterSelector identifies ClusterAPI clusters to associate to.
	ClusterSelector Selector `json:"clusterSelector"`

	// SyncMode specifies how features are synced in a matching workload cluster.
	// - OneTime means, first time a workload cluster matches the ClusterProfile,
	// features will be deployed in such cluster. Any subsequent feature configuration
	// change won't be applied into the matching workload clusters;
	// - Continuous means first time a workload cluster matches the ClusterProfile,
	// features will be deployed in such a cluster. Any subsequent feature configuration
	// change will be applied into the matching workload clusters.
	// - DryRun means no change will be propagated to any matching cluster. A report
	// instead will be generated summarizing what would happen in any matching cluster
	// because of the changes made to ClusterProfile while in DryRun mode.
	// +kubebuilder:default:=Continuous
	// +optional
	SyncMode SyncMode `json:"syncMode,omitempty"`

	// StopMatchingBehavior indicates what behavior should be when a Cluster stop matching
	// the ClusterProfile. By default all deployed Helm charts and Kubernetes resources will
	// be withdrawn from Cluster. Setting StopMatchingBehavior to LeavePolicies will instead
	// leave ClusterProfile deployed policies in the Cluster.
	// +kubebuilder:default:=WithdrawPolicies
	// +optional
	StopMatchingBehavior StopMatchingBehavior `json:"stopMatchingBehavior,omitempty"`

	// PolicyRefs references all the ConfigMaps containing kubernetes resources
	// that need to be deployed in the matching CAPI clusters.
	// +optional
	PolicyRefs []libsveltosv1alpha1.PolicyRef `json:"policyRefs,omitempty"`

	// Helm charts
	HelmCharts []HelmChart `json:"helmCharts,omitempty"`
}

// ClusterProfileStatus defines the observed state of ClusterProfile
type ClusterProfileStatus struct {
	// MatchingClusterRefs reference all the cluster-api Cluster currently matching
	// ClusterProfile ClusterSelector
	MatchingClusterRefs []corev1.ObjectReference `json:"matchinClusters,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=clusterprofiles,scope=Cluster
//+kubebuilder:subresource:status

// ClusterProfile is the Schema for the clusterprofiles API
type ClusterProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterProfileSpec   `json:"spec,omitempty"`
	Status ClusterProfileStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterProfileList contains a list of ClusterProfile
type ClusterProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterProfile{}, &ClusterProfileList{})
}
