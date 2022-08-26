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
	"k8s.io/apimachinery/pkg/api/resource"
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

	// SyncModeContinuous indicates feature sync should continuously happen
	SyncModeContinuous = SyncMode("Continuous")
)

// MGIANLUC: Kyverno generate ClusterRoleBinding https://kyverno.io/docs/writing-policies/generate/

type KyvernoConfiguration struct {
	// +kubebuilder:default:=1
	// Replicas is the number of kyverno replicas required
	Replicas uint `json:"replicas,omitempty"`

	// PolicyRef references ConfigMaps containing the Kyverno policies
	// that need to be deployed in the workload cluster.
	PolicyRefs []corev1.ObjectReference `json:"policyRef,omitempty"`
}

type GatekeeperConfiguration struct {
	// PolicyRef references ConfigMaps containing the Gatekeeper policies
	// that need to be deployed in the workload cluster. This includes and it is not limited
	// to contrainttemplates, configs, etc.
	// +optional
	PolicyRefs []corev1.ObjectReference `json:"policyRef,omitempty"`

	// AuditInterval is the audit interval time.
	// Disable audit interval by setting 0
	// +kubebuilder:default:=60
	// +optional
	AuditInterval uint `json:"auditInterval,omitempty"`

	// AuditChunkSize is the Kubernetes API chunking List results when retrieving
	// cluster resources using discovery client
	// +kubebuilder:default:=500
	// +optional
	AuditChunkSize uint `json:"auditChunkSize,omitempty"`

	// AuditFromCache, if set to true, pull resources from OPA cache when auditing.
	// Note that this requires replication of Kubernetes resources into OPA before
	// they can be evaluated against the enforced policies
	// +kubebuilder:default:=false
	// +optional
	AuditFromCache bool `json:"auditFromCache,omitempty"`
}

// PrometheusInstallationMode specifies how prometheus is deployed in a CAPI Cluster.
//+kubebuilder:validation:Enum:=KubeStateMetrics;KubePrometheus;Custom
type PrometheusInstallationMode string

const (
	// InstallationModeCustom will cause Prometheus Operator to be installed
	// and any PolicyRefs.
	PrometheusInstallationModeCustom = PrometheusInstallationMode("Custom")

	// InstallationModeKubeStateMetrics will cause Prometheus Operator to be installed
	// and any PolicyRefs. On top of that, KubeStateMetrics will also be installed
	// and a Promethus CRD instance will be created to scrape KubeStateMetrics metrics.
	PrometheusInstallationModeKubeStateMetrics = PrometheusInstallationMode("KubeStateMetrics")

	// InstallationModeKubePrometheus will cause the Kube-Prometheus stack to be deployed.
	// Any PolicyRefs will be installed after that.
	// Kube-Prometheus stack includes KubeStateMetrics.
	PrometheusInstallationModeKubePrometheus = PrometheusInstallationMode("KubePrometheus")
)

type PrometheusConfiguration struct {
	// InstallationMode indicates what type of resources will be deployed in a
	// CAPI Cluster.
	// +kubebuilder:default:=Custom
	// +optional
	InstallationMode PrometheusInstallationMode `json:"installationMode,omitempty"`

	// storageClassName is the name of the StorageClass Prometheus will use to claim storage.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// StorageQuantity indicates the amount of storage Prometheus will request from storageclass
	// if defined. (40Gi for instance)
	// If not defined and StorageClassName is defined, 40Gi will be used.
	// +optional
	StorageQuantity *resource.Quantity `json:"storageQuantity,omitempty"`

	// PolicyRef references ConfigMaps containing the Prometheus operator policies
	// that need to be deployed in the workload cluster. This includes:
	// - Prometheus, Alertmanager, ThanosRuler, ServiceMonitor, PodMonitor, Probe,
	// PrometheusRule, AlertmanagerConfig CRD instances;
	// -  Any other configuration needed for prometheus (like storageclass configuration)
	PolicyRefs []corev1.ObjectReference `json:"policyRefs,omitempty"`
}

// ContourInstallationMode specifies how contour is deployed in a CAPI Cluster.
//+kubebuilder:validation:Enum:=Contour;Gateway
type ContourInstallationMode string

const (
	// ContourInstallationModeGateway will cause Contour Gateway Provisioner to
	// be deployed.
	ContourInstallationModeGateway = ContourInstallationMode("Gateway")

	// ContourInstallationModeContour will cause Contour to be deployed.
	ContourInstallationModeContour = ContourInstallationMode("Contour")
)

type ContourConfiguration struct {
	// InstallationMode indicates what type of resources will be deployed in a
	// CAPI Cluster.
	// +kubebuilder:default:=Contour
	// +optional
	InstallationMode ContourInstallationMode `json:"installationMode,omitempty"`

	// PolicyRef references ConfigMaps containing the Contour policies
	// that need to be deployed in the workload cluster. This includes:
	// - ContourConfiguration, ContourDeployment, HTTPProxy, GatewayClass, Gateway,
	// HTTPRoute, etc.
	// -  Any other configuration needed for Contour
	PolicyRefs []corev1.ObjectReference `json:"policyRefs,omitempty"`
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

	// ResourceRefs references all the ConfigMaps containing kubernetes resources
	// that need to be deployed in the matching CAPI clusters.
	// +optional
	ResourceRefs []corev1.ObjectReference `json:"resourceRefs,omitempty"`

	// KyvernoConfiguration contains the Kyverno configuration.
	// If not nil, Kyverno will be deployed in the workload cluster along with, if any,
	// specified Kyverno policies.
	// +optional
	KyvernoConfiguration *KyvernoConfiguration `json:"kyvernoConfiguration,omitempty"`

	// GatekeeperConfiguration contains the Gatekeeper configuration.
	// If not nil, Gatekeeper will be deployed in the workload cluster along with, if any,
	// specified Gatekeeper policies.
	// +optional
	GatekeeperConfiguration *GatekeeperConfiguration `json:"gatekeeperConfiguration,omitempty"`

	// PrometheusConfiguration contains the Prometheus configuration.
	// If not nil, at the very least Prometheus operator will be deployed in the workload cluster
	// +optional
	PrometheusConfiguration *PrometheusConfiguration `json:"prometheusConfiguration,omitempty"`

	// ContourConfiguration contains the Contour configuration.
	// If not nil, contour will be deployed in the workload cluster
	// +optional
	ContourConfiguration *ContourConfiguration `json:"contourConfiguration,omitempty"`
}

// ClusterFeatureStatus defines the observed state of ClusterFeature
type ClusterFeatureStatus struct {
	// MatchingClusterRefs reference all the cluster-api Cluster currently matching
	// ClusterFeature ClusterSelector
	MatchingClusterRefs []corev1.ObjectReference `json:"matchinClusters,omitempty"`
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

// nolint: gochecknoinits // forced pattern, can't workaround
func init() {
	SchemeBuilder.Register(&ClusterFeature{}, &ClusterFeatureList{})
}
