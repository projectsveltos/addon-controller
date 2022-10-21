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
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// PolicyRef specifies a resource containing one or more policy
// to deploy in matching CAPI Clusters.
type PolicyRef struct {
	// Namespace of the referenced resource.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Name of the rreferenced resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind of the resource. Supported kinds are: Secrets and ConfigMaps.
	// +kubebuilder:validation:Enum=Secret;ConfigMap
	Kind string `json:"kind"`
}

func (r PolicyRef) String() string {
	return r.Kind + "-" + r.Namespace + "-" + r.Name
}

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
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// HelmChartAction is the action that will be taken on the helm chart
	// +kubebuilder:default:=Install
	// +optional
	HelmChartAction HelmChartAction `json:"helmChartAction,omitempty"`
}

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
	// +kubebuilder:default:=Continuous
	// +optional
	SyncMode SyncMode `json:"syncMode,omitempty"`

	// PolicyRefs references all the ConfigMaps containing kubernetes resources
	// that need to be deployed in the matching CAPI clusters.
	// +optional
	PolicyRefs []PolicyRef `json:"policyRefs,omitempty"`

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

// GetValues unmarshals the raw values to a map[string]interface{} and returns
// the result.
func (in *HelmChart) GetValues() (map[string]interface{}, error) {
	var values map[string]interface{}
	if in.Values != nil {
		if err := json.Unmarshal(in.Values.Raw, &values); err != nil {
			return nil, err
		}
	}
	return values, nil
}
