/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	"k8s.io/apimachinery/pkg/util/intstr"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// ClusterPromotionFinalizer allows ClusterPromotionReconciler to clean up resources associated with
	// ClusterPromotion before removing it from the apiserver.
	ClusterPromotionFinalizer = "config.projectsveltos.io/clusterpromotion"

	ClusterPromotionKind = "ClusterPromotion"
)

type ProfileSpec struct {
	// SyncMode specifies how features are synced in a matching workload cluster.
	// - OneTime means, first time a workload cluster matches the ClusterProfile,
	// features will be deployed in such cluster. Any subsequent feature configuration
	// change won't be applied into the matching workload clusters;
	// - Continuous mode ensures that the first time a workload cluster matches a ClusterProfile,
	// the specified features are deployed. Subsequent changes to the feature configuration are also
	// automatically applied to all matching workload clusters.
	// _ SyncModeContinuousWithDriftDetection operates similarly to Continuous mode, but also monitors
	// matching managed clusters for configuration drift. If drift is detected, a reconciliation is
	// triggered to ensure the managed cluster's configuration aligns with the ClusterProfile.
	// - DryRun means no change will be propagated to any matching cluster. A report
	// instead will be generated summarizing what would happen in any matching cluster
	// because of the changes made to ClusterProfile while in DryRun mode.
	// +kubebuilder:default:=Continuous
	// +optional
	SyncMode SyncMode `json:"syncMode,omitempty"`

	// Tier controls the order of deployment for ClusterProfile or Profile resources targeting
	// the same cluster resources.
	// Imagine two configurations (ClusterProfiles or Profiles) trying to deploy the same resource (a Kubernetes
	// resource or an helm chart). By default, the first one to reach the cluster "wins" and deploys it.
	// Tier allows you to override this. When conflicts arise, the ClusterProfile or Profile with the **lowest**
	// Tier value takes priority and deploys the resource.
	// Higher Tier values represent lower priority. The default Tier value is 100.
	// Using Tiers provides finer control over resource deployment within your cluster, particularly useful
	// when multiple configurations manage the same resources.
	// +kubebuilder:default:=100
	// +kubebuilder:validation:Minimum=1
	// +optional
	Tier int32 `json:"tier,omitempty"`

	// By default (when ContinueOnConflict is unset or set to false), Sveltos stops deployment after
	// encountering the first conflict (e.g., another ClusterProfile already deployed the resource).
	// If set to true, Sveltos will attempt to deploy remaining resources in the ClusterProfile even
	// if conflicts are detected for previous resources.
	// +kubebuilder:default:=false
	// +optional
	ContinueOnConflict bool `json:"continueOnConflict,omitempty"`

	// By default (when ContinueOnError is unset or set to false), Sveltos stops deployment after
	// encountering the first error.
	// If set to true, Sveltos will attempt to deploy remaining resources in the ClusterProfile even
	// if errors are detected for previous resources.
	// +kubebuilder:default:=false
	// +optional
	ContinueOnError bool `json:"continueOnError,omitempty"`

	// The maximum number of clusters that can be updated concurrently.
	// Value can be an absolute number (ex: 5) or a percentage of desired cluster (ex: 10%).
	// Defaults to 100%.
	// Example: when this is set to 30%, when list of add-ons/applications in ClusterProfile
	// changes, only 30% of matching clusters will be updated in parallel. Only when updates
	// in those cluster succeed, other matching clusters are updated.
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:validation:Pattern="^((100|[0-9]{1,2})%|[0-9]+)$"
	// +optional
	MaxUpdate *intstr.IntOrString `json:"maxUpdate,omitempty"`

	// StopMatchingBehavior indicates what behavior should be when a Cluster stop matching
	// the ClusterProfile. By default all deployed Helm charts and Kubernetes resources will
	// be withdrawn from Cluster. Setting StopMatchingBehavior to LeavePolicies will instead
	// leave ClusterProfile deployed policies in the Cluster.
	// +kubebuilder:default:=WithdrawPolicies
	// +optional
	StopMatchingBehavior StopMatchingBehavior `json:"stopMatchingBehavior,omitempty"`

	// Reloader indicates whether Deployment/StatefulSet/DaemonSet instances deployed
	// by Sveltos and part of this ClusterProfile need to be restarted via rolling upgrade
	// when a ConfigMap/Secret instance mounted as volume is modified.
	// When set to true, when any mounted ConfigMap/Secret is modified, Sveltos automatically
	// starts a rolling upgrade for Deployment/StatefulSet/DaemonSet instances mounting it.
	// +kubebuilder:default:=false
	// +optional
	Reloader bool `json:"reloader,omitempty"`

	// TemplateResourceRefs is a list of resource to collect from the management cluster.
	// Those resources' values will be used to instantiate templates
	// +patchMergeKey=identifier
	// +patchStrategy=merge,retainKeys
	// +listType=map
	// +listMapKey=identifier
	// +optional
	TemplateResourceRefs []TemplateResourceRef `json:"templateResourceRefs,omitempty" patchStrategy:"merge" patchMergeKey:"identifier"`

	// DependsOn specifies a list of other profiles that this instance depends on.
	// A ClusterProfile can only depend on other ClusterProfiles, and a Profile can
	// only depend on other Profiles.
	// The add-ons and applications defined in this instance will not be deployed
	// until all add-ons and applications in the profiles listed as dependencies are deployed.
	DependsOn []string `json:"dependsOn,omitempty"`

	// PolicyRefs references all the ConfigMaps/Secrets/Flux Sources containing kubernetes resources
	// that need to be deployed in the matching managed clusters.
	// The values contained in those resources can be static or leverage Go templates for dynamic customization.
	// When expressed as templates, the values are filled in using information from
	// resources within the management cluster before deployment (Cluster and TemplateResourceRefs)
	// +listType=atomic
	// +optional
	PolicyRefs []PolicyRef `json:"policyRefs,omitempty"`

	// Helm charts is a list of helm charts that need to be deployed
	// +listType=atomic
	// +optional
	HelmCharts []HelmChart `json:"helmCharts,omitempty"`

	// Kustomization refs is a list of kustomization paths. Kustomization will
	// be run on those paths and the outcome will be deployed.
	// +listType=atomic
	// +optional
	KustomizationRefs []KustomizationRef `json:"kustomizationRefs,omitempty"`

	// ValidateHealths is a slice of Lua functions to run against
	// the managed cluster to validate the state of those add-ons/applications
	// is healthy
	// +listType=atomic
	// +optional
	ValidateHealths []libsveltosv1beta1.ValidateHealth `json:"validateHealths,omitempty"`

	// Define additional Kustomize inline Patches applied for all resources on this profile
	// Within the Patch Spec you can use templating
	// +listType=atomic
	// +optional
	Patches []libsveltosv1beta1.Patch `json:"patches,omitempty"`

	// PatchesFrom can reference ConfigMap/Secret instances. Within the ConfigMap or Secret data,
	// it is possible to store additional Kustomize inline Patches applied for all resources on this profile
	// These values can be static or leverage Go templates for dynamic customization.
	// When expressed as templates, the values are filled in using information from
	// resources within the management cluster before deployment (Cluster and TemplateResourceRefs)
	// +listType=atomic
	// +optional
	PatchesFrom []ValueFrom `json:"patchesFrom,omitempty"`

	// DriftExclusions is a list of configuration drift exclusions to be applied when syncMode is
	// set to ContinuousWithDriftDetection. Each exclusion specifies JSON6902 paths to ignore
	// when evaluating drift, optionally targeting specific resources and features.
	// +listType=atomic
	// +optional
	DriftExclusions []libsveltosv1beta1.DriftExclusion `json:"driftExclusions,omitempty"`

	// The maximum number of consecutive deployment failures that Sveltos will permit.
	// After this many consecutive failures, the deployment will be considered failed, and Sveltos will stop retrying.
	// This setting applies only to feature deployments, not resource removal.
	// This field is optional. If not set, Sveltos default behavior is to keep retrying.
	// +optional
	MaxConsecutiveFailures *uint `json:"maxConsecutiveFailures,omitempty"`
}

// AutoTrigger defines the conditions for an automatic promotion.
type AutoTrigger struct {
	// Delay is an optional time duration to wait after the WaitForStatus condition
	// is met before proceeding with the promotion.
	// +optional
	Delay *metav1.Duration `json:"delay,omitempty"`

	// PreHealthCheckDeployment is a slice of resources Sveltos will deploy after the Delay
	// period has elapsed and before running PostDelayHealthChecks.
	// This can be used, for example, to deploy a Job that performs validation tasks.
	// The PostDelayHealthChecks can then validate the successful completion of these resources (e.g., a Job).
	// +optional
	PreHealthCheckDeployment []PolicyRef `json:"preHealthCheckDeployment,omitempty"`

	// PostDelayHealthChecks is a slice of health checks Sveltos will run after the delay
	// period has elapsed.
	// +optional
	PostDelayHealthChecks []libsveltosv1beta1.ValidateHealth `json:"postDelayHealthChecks,omitempty"`

	// PromotionWindow defines the recurring time window during which the
	// automatic promotion is permitted. The controller evaluates this window
	// *only after* the Delay (if defined) has elapsed and all PostDelayHealthChecks
	// have passed. The controller will then wait until the window defined by From/To
	// is open before proceeding.
	// +optional
	PromotionWindow *TimeWindow `json:"promotionWindow,omitempty"`
}

// TimeWindow defines a recurring time range when an operation is permitted.
// The time range is defined using cron expressions for the start and end of the window.
type TimeWindow struct {
	// From is a cron expression defining the recurring time(s) when the promotion
	// window opens. The promotion will be blocked until the time defined by 'From'
	// is reached.
	// +kubebuilder:validation:MinLength=1
	From string `json:"from"`

	// To is a cron expression defining the recurring time(s) when the promotion
	// window closes. If the promotion process is not complete when 'To' is reached,
	// the controller should pause and wait for the next 'From' time.
	// +kubebuilder:validation:MinLength=1
	To string `json:"to"`
}

// ManualTrigger is a placeholder to represent a manual trigger.
type ManualTrigger struct {
	// Approved, when set to true, signals to the controller that
	// promotion to the next stage is approved.
	// +optional
	Approved *bool `json:"approved,omitempty"`

	// AutomaticReset, when set to true, instructs the controller to automatically
	// reset the 'Approved' field to nil/false after successfully promoting
	// to the next stage. This prevents unintended immediate advancement
	// past the next manual stage. Defaults to true.
	// +kubebuilder:default:=true
	// +optional
	AutomaticReset bool `json:"automaticReset,omitempty"`
}

// Trigger defines the condition for promotion to the next stage.
// A Trigger can be either manual or automatic, but not both.
// +kubebuilder:validation:XValidation:rule="has(self.manual) != has(self.auto)",message="A trigger must have either a 'manual' or 'auto' field, but not both."
type Trigger struct {
	// Manual is a manual trigger. When set, promotion requires a human signal.
	// +optional
	Manual *ManualTrigger `json:"manual,omitempty"`

	// Auto is an automatic trigger. When set, promotion occurs automatically
	// based on defined conditions.
	// +optional
	Auto *AutoTrigger `json:"auto,omitempty"`
}

// Stage defines a single step in the promotion pipeline
type Stage struct {
	// Name of the stage, e.g., "qa", "staging", "production".
	Name string `json:"name"`

	// ClusterSelector specifies which clusters this stage's resources will be deployed to.
	ClusterSelector libsveltosv1beta1.Selector `json:"clusterSelector,omitempty"`

	// Trigger for promoting to the next stage in the pipeline.
	// This field is only applicable to stages that are not the last stage.
	// +optional
	Trigger *Trigger `json:"trigger,omitempty"`
}

// ClusterPromotionSpec defines the desired state of ClusterPromotion
type ClusterPromotionSpec struct {
	// ProfileSpec contains the common configuration for the Sveltos ClusterProfiles
	// that will be created at each stage.
	ProfileSpec ProfileSpec `json:"profileSpec,omitempty"`

	// Stages is a list of environments in the promotion pipeline.
	// The pipeline will progress through these stages in the order they are defined.
	// +kubebuilder:validation:MinItems=1
	// +patchMergeKey=name
	// +patchStrategy=merge,retainKeys
	// +listType=map
	// +listMapKey=name
	Stages []Stage `json:"stages"`

	// PreserveClusterProfilesOnDelete, if true, prevents the controller from deleting
	// the associated ClusterProfiles when this ClusterPromotion is deleted.
	// The ClusterProfiles will remain, and will need to be manually cleaned up.
	// +optional
	PreserveClusterProfilesOnDelete bool `json:"preserveClusterProfilesOnDelete,omitempty"`
}

// StageStatus defines the status for a given stage in a progressive deployment.
type StageStatus struct {
	// Name of the stage, e.g., "qa", "staging", "production".
	Name string `json:"name"`

	// LastUpdateReconciledTime is the time the ClusterPromotion controller most recently
	// created or updated the corresponding ClusterProfile resource for this stage.
	// This indicates when the desired state was last applied to the system.
	// +optional
	LastUpdateReconciledTime *metav1.Time `json:"lastUpdateReconciledTime,omitempty"`

	// LastSuccessfulAppliedTime is the time this stage was fully and successfully deployed.
	// This means all clusters matching the stage have reported a 'Provisioned' status.
	// +optional
	LastSuccessfulAppliedTime *metav1.Time `json:"lastSuccessfulAppliedTime,omitempty"`

	// LastStatusCheckTime is the time the ClusterPromotion controller last checked the
	// statuses of the matching clusters and their provisioned state.
	// This is updated frequently while waiting for provisioning to complete.
	// +optional
	LastStatusCheckTime *metav1.Time `json:"lastStatusCheckTime,omitempty"`

	// FailureMessage reports a detailed error message if a failure occurred.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`

	// CurrentStatusDescription provides a high-level description of where the stage is
	// in its progression (e.g., "Waiting for cluster profiles to be created",
	// "Waiting for all clusters to be Provisioned", "Running post-deployment health checks").
	// This helps users understand the current blocking state.
	// +optional
	CurrentStatusDescription *string `json:"currentStatusDescription,omitempty"`
}

// ClusterPromotionStatus defines the observed state of ClusterPromotion.
type ClusterPromotionStatus struct {
	// Stages tracks the status of each configured promotion stage
	// (e.g., "qa", "staging", "production").
	Stages []StageStatus `json:"stages,omitempty"`

	// ProfileSpecHash represents a unique value for the entire ProfileSpec configuration
	// at a fixed point in time. This is used to detect configuration changes.
	// +optional
	ProfileSpecHash []byte `json:"profileSpecHash,omitempty"`

	// StagesHash represents a unique value for the entire list of Stages.
	// This hash changes if any stage is added, removed, or if the ordering
	// or configuration (ClusterSelector, Trigger) of any stage changes.
	// +optional
	StagesHash []byte `json:"stagesHash,omitempty"`

	//  If the pipeline is currently running, this is the name of the stage
	// being processed. If the pipeline is paused or completed, this is the name
	// of the last stage that reached a target state.
	// +optional
	CurrentStageName string `json:"currentStageName,omitempty"`

	// LastPromotionTime is the time the entire promotion process was successfully completed.
	// +optional
	LastPromotionTime *metav1.Time `json:"lastPromotionTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=clusterpromotions,scope=Cluster
// +kubebuilder:subresource:status

// ClusterPromotion is the Schema for the ClusterPromotions API
type ClusterPromotion struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of ClusterPromotion
	// +required
	Spec ClusterPromotionSpec `json:"spec"`

	// status defines the observed state of ClusterPromotion
	// +optional
	Status ClusterPromotionStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterPromotionList contains a list of ClusterPromotion
type ClusterPromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterPromotion `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterPromotion{}, &ClusterPromotionList{})
}
