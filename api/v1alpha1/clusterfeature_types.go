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
)

const (
	// ClusterFeatureFinalizer allows ClusterFeatureReconciler to clean up resources associated with
	// ClusterFeature before removing it from the apiserver.
	ClusterFeatureFinalizer = "clusterfeaturefinalizer.projectsveltos.io"
)

type Selector string

// SyncMode specifies how features are synced in a workload cluster.
//+kubebuilder:validation:Enum:=OneTime;Continuous
type SyncMode string

const (
	// SyncModeOneTime indicates feature sync should happen only once
	SyncModeOneTime = SyncMode("OneTime")

	// SyncModeContinuos indicates feature sync should continuously happen
	SyncModeContinuos = SyncMode("Continuous")
)

// MGIANLUC: Kyverno generate ClusterRoleBinding https://kyverno.io/docs/writing-policies/generate/

type KyvernoConfiguration struct {
	// Policies references ConfigMaps containing the Kyverno policies
	// that need to be deployed in the workload cluster.
	Policies []corev1.ObjectReference `json:"policies,omitempty"`
}

// ClusterFeatureSpec defines the desired state of ClusterFeature
type ClusterFeatureSpec struct {
	// ClusterSelector identifies ClusterAPI clusters to associate to.
	ClusterSelector Selector `json:"clusterSelector"`

	// SyncMode specifies how features are synced in a matching workload cluster.
	// - OneTime means, first time a workload cluster matches the ClusterFeature,
	// features will be deployed in such cluster. Any subsequent feature configuration
	// change won't be applied into the matching workload clusters;
	// - Continuous means first time a workload cluster matches the ClusterFeature,
	// features will be deployed in such a cluster. Any subsequent feature configuration
	// change will be applied into the matching workload clusters.
	// +kubebuilder:default:=OneTime
	// +optional
	SyncMode SyncMode `json:"syncMode,omitempty"`

	// WorkloadRoles references all the WorkloadRoles that will be used
	// to create ClusterRole/Role in the workload cluster.
	// +optional
	WorkloadRoles []corev1.ObjectReference `json:"workloadRoles,omitempty"`

	// KyvernoConfiguration contains the Kyverno configuration.
	// If not nil, Kyverno will be deployed in the workload cluster along with, if any,
	// specified Kyverno policies.
	// +optional
	KyvernoConfiguration *KyvernoConfiguration `json:"kyvernoConfiguration,omitempty"`
}

// ClusterFeatureStatus defines the observed state of ClusterFeature
type ClusterFeatureStatus struct {
	// MatchingCluster reference all the cluster-api Cluster currently matching
	// ClusterFeature ClusterSelector
	MatchingClusters []corev1.ObjectReference `json:"matchinClusters,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=clusterfeatures,scope=Cluster
//+kubebuilder:subresource:status

// ClusterFeature is the Schema for the clusterfeatures API
type ClusterFeature struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterFeatureSpec   `json:"spec,omitempty"`
	Status ClusterFeatureStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterFeatureList contains a list of ClusterFeature
type ClusterFeatureList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterFeature `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterFeature{}, &ClusterFeatureList{})
}
