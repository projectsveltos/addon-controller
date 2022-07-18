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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RoleType specifies whether role is namespaced or cluster wide.
//+kubebuilder:validation:Enum:=Namespaced;Cluster
type RoleType string

const (
	RoleTypeNamespaced RoleType = "Namespaced"
	RoleTypeCluster    RoleType = "Cluster"
)

// WorkloadRoleSpec defines the desired state of WorkloadRole
type WorkloadRoleSpec struct {
	// Type specifies whether rules are cluster or namespaced wide
	// +kubebuilder:default:=Namespaced
	Type RoleType `json:"type"`

	// Namespace is the namespace where corresponding role will be created.
	// +optional
	Namespace *string `json:"namespace,omitempty"`

	// Rules holds all the PolicyRules for this ClusterRole
	// +optional
	Rules []rbacv1.PolicyRule `json:"rules,omitempty"`

	// AggregationRule is an optional field that describes how to build the Rules for this ClusterRole.
	// If AggregationRule is set, then the Rules are controller managed and direct changes to Rules will be
	// stomped by the controller.
	// +optional
	AggregationRule *rbacv1.AggregationRule `json:"aggregationRule,omitempty"`
}

// WorkloadRoleStatus defines the observed state of WorkloadRole
type WorkloadRoleStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=workloadroles,scope=Cluster
//+kubebuilder:subresource:status

// WorkloadRole is the Schema for the workloadroles API
type WorkloadRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadRoleSpec   `json:"spec,omitempty"`
	Status WorkloadRoleStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkloadRoleList contains a list of WorkloadRole
type WorkloadRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadRole `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadRole{}, &WorkloadRoleList{})
}
