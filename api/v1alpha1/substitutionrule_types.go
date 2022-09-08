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
	ClusterSubstitutionKind = "ClusterSubstitution"
)

// SubstitutionRuleSpec defines the desired state of SubstitutionRule
type SubstitutionRuleSpec struct {
	// Kind of the resource
	Kind string `json:"kind,omitempty"`
	// Namespace of the resource
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name of the resource.
	Name string `json:"name"`
	// API version of the resource.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}

// SubstitutionRuleStatus defines the observed state of SubstitutionRule
type SubstitutionRuleStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=substitutionrules,scope=Cluster
//+kubebuilder:subresource:status

// SubstitutionRule is the Schema for the substitutionrules API
type SubstitutionRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubstitutionRuleSpec   `json:"spec,omitempty"`
	Status SubstitutionRuleStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SubstitutionRuleList contains a list of SubstitutionRule
type SubstitutionRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubstitutionRule `json:"items"`
}

// nolint: gochecknoinits // forced pattern, can't workaround
func init() {
	SchemeBuilder.Register(&SubstitutionRule{}, &SubstitutionRuleList{})
}
