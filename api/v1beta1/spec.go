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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// ClusterNameLabel is the label set on:
	// - ClusterSummary instances created by a ClusterProfile instance for a given cluster;
	// - ClusterConfiguration instances created by a ClusterProfile instance for a given cluster;
	// - ClusterReport instances created by a ClusterProfile instance for a given cluster;
	ClusterNameLabel = "projectsveltos.io/cluster-name"

	// ClusterTypeLabel is the label set on:
	// - ClusterSummary instances created by a ClusterProfile instance for a given cluster;
	// - ClusterConfiguration instances created by a ClusterProfile instance for a given cluster;
	// - ClusterReport instances created by a ClusterProfile instance for a given cluster;
	ClusterTypeLabel = "projectsveltos.io/cluster-type"
)

type DryRunReconciliationError struct{}

func (m *DryRunReconciliationError) Error() string {
	return "mode is DryRun. Nothing is reconciled"
}

// SyncMode specifies how features are synced in a workload cluster.
// +kubebuilder:validation:Enum:=OneTime;Continuous;ContinuousWithDriftDetection;DryRun
type SyncMode string

const (
	// SyncModeOneTime indicates feature sync should happen only once
	SyncModeOneTime = SyncMode("OneTime")

	// SyncModeContinuous indicates feature sync should continuously happen
	SyncModeContinuous = SyncMode("Continuous")

	// SyncModeContinuousWithDriftDetection indicates that feature synchronization
	// should occur continuously. If configuration drift is detected in the managed
	// cluster, it will be overridden.
	SyncModeContinuousWithDriftDetection = SyncMode("ContinuousWithDriftDetection")

	// SyncModeDryRun indicates feature sync should continuously happen
	// no feature will be updated in the CAPI Cluster though.
	SyncModeDryRun = SyncMode("DryRun")
)

// DeploymentType indicates whether resources need to be deployed
// into the management cluster (local) or the managed cluster (remote)
// +kubebuilder:validation:Enum:=Local;Remote
type DeploymentType string

const (
	// DeploymentTypeLocal indicates resource deployment need to
	// be in the management cluster
	DeploymentTypeLocal = DeploymentType("Local")

	// DeploymentTypeRemote indicates resource deployment need to
	// be in the managed cluster
	DeploymentTypeRemote = DeploymentType("Remote")
)

type ValueFrom struct {
	// Namespace of the referenced resource.
	// For ClusterProfile namespace can be left empty. In such a case, namespace will
	// be implicit set to cluster's namespace.
	// For Profile namespace must be left empty. The Profile namespace will be used.
	// Namespace can be expressed as a template and instantiate using any cluster field.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name of the referenced resource.
	// Name can be expressed as a template and instantiate using any cluster field.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind of the resource. Supported kinds are:
	// - ConfigMap/Secret
	// +kubebuilder:validation:Enum=ConfigMap;Secret
	Kind string `json:"kind"`

	// Optional indicates that the referenced resource is not mandatory.
	// If set to true and the resource is not found, the error will be ignored,
	// and Sveltos will continue processing other ValueFroms.
	// +kubebuilder:default:=false
	// +optional
	Optional bool `json:"optional,omitempty"`
}

type RegistryCredentialsConfig struct {
	// CredentialsSecretRef references a secret containing credentials
	// For ClusterProfile namespace can be left empty. In such a case, namespace will
	// be implicit set to cluster's namespace.
	// +optional
	CredentialsSecretRef *corev1.SecretReference `json:"credentials,omitempty"`

	// Key specifies the key within the CredentialsSecretRef containing the data
	// If not specified, it defaults to the only key in the secret if there's just one.
	// +optional
	Key string `json:"key,omitempty"`

	// CASecretRef references a secret containing the TLS CA certificate
	// For ClusterProfile namespace can be left empty. In such a case, namespace will
	// be implicit set to cluster's namespace.
	// key: ca.crt
	// +optional
	CASecretRef *corev1.SecretReference `json:"ca,omitempty"`

	// InsecureSkipTLSVerify controls server certificate verification.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`

	// PlainHTTP indicates to use insecure HTTP connections for the chart download
	// +optional
	PlainHTTP bool `json:"plainHTTP,omitempty"`
}

// HelmChartAction specifies action on an helm chart
// +kubebuilder:validation:Enum:=Install;Uninstall
type HelmChartAction string

const (
	// HelmChartActionInstall will cause Helm chart to be installed
	HelmChartActionInstall = HelmChartAction("Install")

	// HelmChartActionUninstall will cause Helm chart to be removed
	HelmChartActionUninstall = HelmChartAction("Uninstall")
)

type HelmOptions struct {
	// SkipCRDs controls whether CRDs should be installed during install/upgrade operation.
	// By default, CRDs are installed if not already present.
	// +kubebuilder:default:=false
	// +optional
	SkipCRDs bool `json:"skipCRDs,omitempty"`

	// SkipSchemaValidation determines if JSON schema validation is disabled.
	// +kubebuilder:default:=false
	// +optional
	SkipSchemaValidation bool `json:"skipSchemaValidation,omitempty"`

	// if set, will wait until all Pods, PVCs, Services, and minimum number of Pods of a Deployment, StatefulSet, or ReplicaSet
	// are in a ready state before marking the release as successful. It will wait for as long as --timeout
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	Wait bool `json:"wait,omitempty"`

	// if set and --wait enabled, will wait until all Jobs have been completed before marking the release as successful.
	// It will wait for as long as --timeout
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	WaitForJobs bool `json:"waitForJobs,omitempty"`

	// time to wait for any individual Kubernetes operation (like Jobs for hooks) (default 5m0s)
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// prevent hooks from running during install/upgrade/uninstall
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	DisableHooks bool `json:"disableHooks,omitempty"`

	// if set, the installation process will not validate rendered templates against the Kubernetes OpenAPI Schema
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	DisableOpenAPIValidation bool `json:"disableOpenAPIValidation,omitempty"`

	// if set, the installation process deletes the installation/upgrades on failure.
	// The --wait flag will be set automatically if --atomic is used
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	Atomic bool `json:"atomic,omitempty"`

	// update dependencies if they are missing before installing the chart
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	DependencyUpdate bool `json:"dependencyUpdate,omitempty"`

	// Labels that would be added to release metadata.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// EnableClientCache is a flag to enable Helm client cache. If it is not specified, it will be set to false.
	// +kubebuilder:default=false
	// +optional
	EnableClientCache bool `json:"enableClientCache,omitempty"`

	// Description is the description of an helm operation
	// +optional
	Description string `json:"description,omitempty"`

	// HelmInstallOptions are options specific to helm install
	// +optional
	InstallOptions HelmInstallOptions `json:"installOptions,omitempty"`

	// HelmUpgradeOptions are options specific to helm upgrade
	// +optional
	UpgradeOptions HelmUpgradeOptions `json:"upgradeOptions,omitempty"`

	// HelmUninstallOptions are options specific to helm uninstall
	// +optional
	UninstallOptions HelmUninstallOptions `json:"uninstallOptions,omitempty"`
}

type HelmInstallOptions struct {
	// Create the release namespace if not present. Defaults to true
	// +kubebuilder:default:=true
	// +optional
	CreateNamespace bool `json:"createNamespace,omitempty"`

	// Replaces if set indicates to replace an older release with this one
	// +kubebuilder:default:=true
	// +optional
	Replace bool `json:"replace,omitempty"`

	// prevent hooks from running during install. If set to true, overrides
	// DisableHooks in HelmOptions. Use this one when you want to selective
	// disable hooks on install
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	DisableHooks bool `json:"disableHooks,omitempty"`
}

type HelmUpgradeOptions struct {
	// Force will, if set to `true`, ignore certain warnings and perform the upgrade anyway.
	// This should be used with caution.
	// +kubebuilder:default:=false
	// +optional
	Force bool `json:"force,omitempty"`

	// ResetValues will reset the values to the chart's built-ins rather than merging with existing.
	// +kubebuilder:default:=false
	// +optional
	ResetValues bool `json:"resetValues,omitempty"`

	// ReuseValues copies values from the current release to a new release if the
	// new release does not have any values. If the request already has values,
	// or if there are no values in the current release, this does nothing.
	// This is skipped if the ResetValues flag is set, in which case the
	// request values are not altered.
	// +kubebuilder:default:=false
	// +optional
	ReuseValues bool `json:"reuseValues,omitempty"`

	// ResetThenReuseValues will reset the values to the chart's built-ins then merge with user's last supplied values.
	// +kubebuilder:default:=false
	// +optional
	ResetThenReuseValues bool `json:"resetThenReuseValues,omitempty"`

	// Recreate will (if true) recreate pods after a rollback.
	// +kubebuilder:default:=false
	// +optional
	Recreate bool `json:"recreate,omitempty"`

	// MaxHistory limits the maximum number of revisions saved per release
	// Default to 2
	// +kubebuilder:default=2
	// +optional
	MaxHistory int `json:"maxHistory,omitempty"`

	// CleanupOnFail will, if true, cause the upgrade to delete newly-created resources on a failed update.
	// +kubebuilder:default:=false
	// +optional
	CleanupOnFail bool `json:"cleanupOnFail,omitempty"`

	// SubNotes determines whether sub-notes are rendered in the chart.
	// +kubebuilder:default:=false
	// +optional
	SubNotes bool `json:"subNotes,omitempty"`

	// UpgradeCRDs upgrade CRDs from the Helm Chart's crds directory
	// By default, CRDs are not applied during Helm upgrade action by Helm
	// https://helm.sh/docs/chart_best_practices/custom_resource_definitions/
	// +kubebuilder:default:=false
	// +optional
	UpgradeCRDs bool `json:"upgradeCRDs,omitempty"`

	// prevent hooks from running during install. If set to true, overrides
	// DisableHooks in HelmOptions. Use this one when you want to selective
	// disable hooks on upgrade
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	DisableHooks bool `json:"disableHooks,omitempty"`
}

type HelmUninstallOptions struct {
	// When uninstall a chart with this flag, Helm removes the resources associated with the chart,
	// but it keeps the release information. This allows to see details about the uninstalled release
	// using the helm history command.
	// +optional
	KeepHistory bool `json:"keepHistory,omitempty"`

	// DeletionPropagation
	// +kubebuilder:validation:Enum:=orphan;foreground;background
	// +optional
	DeletionPropagation string `json:"deletionPropagation,omitempty"`

	// prevent hooks from running during install. If set to true, overrides
	// DisableHooks in HelmOptions. Use this one when you want to selective
	// disable hooks on uninstall
	// Default to false
	// +kubebuilder:default:=false
	// +optional
	DisableHooks bool `json:"disableHooks,omitempty"`
}

//nolint: lll // marker
// +kubebuilder:validation:XValidation:rule="self.repositoryURL.startsWith('http') ? size(self.chartName) >= 1 : true",message="ChartName must be defined"
// +kubebuilder:validation:XValidation:rule="self.repositoryURL.startsWith('oci') ? size(self.chartName) >= 1 : true",message="ChartName must be defined"
// +kubebuilder:validation:XValidation:rule="self.repositoryURL.startsWith('http') ? size(self.chartVersion) >= 1 : true",message="ChartVersion must be defined"
// +kubebuilder:validation:XValidation:rule="self.repositoryURL.startsWith('oci') ? size(self.chartVersion) >= 1 : true",message="ChartVersion must be defined"
// +kubebuilder:validation:XValidation:rule="self.repositoryURL.startsWith('http') ? size(self.repositoryName) >= 1 : true",message="RepositoryName must be defined"
// +kubebuilder:validation:XValidation:rule="self.repositoryURL.startsWith('oci') ? size(self.repositoryName) >= 1 : true",message="RepositoryName must be defined"

type HelmChart struct {
	// RepositoryURL is the URL helm chart repository
	// It can point to a traditional HTTP-based repository or an OCI registry.
	// Examples:
	//   - HTTP Repository: https://charts.example.com/stable
	//   - OCI Registry: oci://registry.example.com/namespace/charts
	//   - GitHub Pages: https://<username>.github.io/<repository>/
	//   - Any other valid URL where Helm charts are hosted.
	//
	// Alternatively, it can be a Flux source reference:
	//   <flux source kind>://<flux source namespace>/<flux source name>/<path>
	//
	// Where:
	//   - <flux source kind> is the type of Flux source (gitrepository, ocirepository, bucket).
	//   - <flux source namespace> and <flux source name> are the namespace and name of the Flux Source in the management cluster.
	//   - <path> is the path within the source repository where the Helm charts are located.
	//
	// For instance:
	//   gitrepository://flux-system/flux-system/charts/projectsveltos
	//
	// Assuming there is a Flux GitRepository named 'flux-system' in the 'flux-system' namespace
	// syncing 'https://github.com/projectsveltos/helm-charts.git/', and the Helm charts are
	// located in the 'charts/projectsveltos' directory of that repository.
	// +kubebuilder:validation:MinLength=1
	RepositoryURL string `json:"repositoryURL"`

	// RepositoryName is the name helm chart repository
	// This field is used only when RepositoryURL points to a traditional HTTP or OCI repository.
	// It is ignored if RepositoryURL references a Flux Source.
	// Must be defined otherwise.
	// +optional
	RepositoryName string `json:"repositoryName,omitempty"`

	// ChartName is the chart name
	// This field is used only when RepositoryURL points to a traditional HTTP or OCI repository.
	// Must be defined otherwise.
	// +optional
	ChartName string `json:"chartName,omitempty"`

	// ChartVersion is the chart version
	// This field is used only when RepositoryURL points to a traditional HTTP or OCI repository.
	// It is ignored if RepositoryURL references a Flux Source.
	// Must be defined otherwise.
	// +optional
	ChartVersion string `json:"chartVersion,omitempty"`

	// ReleaseName is the chart release
	// +kubebuilder:validation:MinLength=1
	ReleaseName string `json:"releaseName"`

	// ReleaseNamespace is the namespace release will be installed
	// +kubebuilder:validation:MinLength=1
	ReleaseNamespace string `json:"releaseNamespace"`

	// Values field allows to define configuration for the Helm release.
	// These values can be static or leverage Go templates for dynamic customization.
	// When expressed as templates, the values are filled in using information from
	// resources within the management cluster before deployment (Cluster and TemplateResourceRefs)
	// +optional
	Values string `json:"values,omitempty"`

	// ValuesFrom can reference ConfigMap/Secret instances. Within the ConfigMap or Secret data,
	// it is possible to store configuration for the Helm release.
	// These values can be static or leverage Go templates for dynamic customization.
	// When expressed as templates, the values are filled in using information from
	// resources within the management cluster before deployment (Cluster and TemplateResourceRefs)
	// +optional
	ValuesFrom []ValueFrom `json:"valuesFrom,omitempty"`

	// HelmChartAction is the action that will be taken on the helm chart
	// +kubebuilder:default:=Install
	// +optional
	HelmChartAction HelmChartAction `json:"helmChartAction,omitempty"`

	// Options allows to set flags which are used during installation.
	// +optional
	Options *HelmOptions `json:"options,omitempty"`

	// RegistryCredentialsConfig is an optional configuration for credentials,
	// including information to connect to private registries.
	// +optional
	RegistryCredentialsConfig *RegistryCredentialsConfig `json:"registryCredentialsConfig,omitempty"`
}

type KustomizationRef struct {
	// Namespace of the referenced resource.
	// For ClusterProfile namespace can be left empty. In such a case, namespace will
	// be implicit set to cluster's namespace.
	// For Profile namespace must be left empty. The Profile namespace will be used.
	// Namespace can be expressed as a template and instantiate using any cluster field.
	Namespace string `json:"namespace"`

	// Name of the referenced resource.
	// Name can be expressed as a template and instantiate using any cluster field.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind of the resource. Supported kinds are:
	// - flux GitRepository;OCIRepository;Bucket
	// - ConfigMap/Secret
	// +kubebuilder:validation:Enum=GitRepository;OCIRepository;Bucket;ConfigMap;Secret
	Kind string `json:"kind"`

	// Path to the directory containing the kustomization.yaml file, or the
	// set of plain YAMLs a kustomization.yaml should be generated for.
	// Defaults to 'None', which translates to the root path of the SourceRef.
	// These values can be static or leverage Go templates for dynamic customization.
	// When expressed as templates, the values are filled in using information from
	// resources within the management cluster before deployment (Cluster)
	// +optional
	Path string `json:"path,omitempty"`

	// Optional indicates that the referenced resource is not mandatory.
	// If set to true and the resource is not found, the error will be ignored,
	// and Sveltos will continue processing other ValueFroms.
	// +kubebuilder:default:=false
	// +optional
	Optional bool `json:"optional,omitempty"`

	// TargetNamespace sets or overrides the namespace in the
	// kustomization.yaml file.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Optional
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`

	// DeploymentType indicates whether resources need to be deployed
	// into the management cluster (local) or the managed cluster (remote)
	// +kubebuilder:default:=Remote
	// +optional
	DeploymentType DeploymentType `json:"deploymentType,omitempty"`

	// Values is a map[string]string type that allows to define a set of key-value pairs.
	// These key-value pairs can optionally leverage Go templates for further processing.
	// With Sveltos, you can define key-value pairs where the values can be Go templates.
	// These templates have access to management cluster information during deployment. This allows
	// to do more than just replace placeholders. Variables can be used to dynamically
	// construct values based on other resources or variables within the Kustomize output.
	// For example, imagine you have a Region key with a template value like:
	// '{{ index .Cluster.metadata.labels "region" }}'.
	// This template retrieves the region label from the cluster instance metadata.
	// Finally, Sveltos uses these processed values to fill placeholders in the Kustomize output.
	// The output itself can also contain templates, like:
	// region: '{{ default "west" .Region }}'.
	// This way, the final output from Kustomize will have the region set dynamically based on
	// the actual region retrieved earlier.
	// +optional
	Values map[string]string `json:"values,omitempty"`

	// ValuesFrom can reference ConfigMap/Secret instances. Within the ConfigMap or Secret data,
	// it is possible to define key-value pairs. These key-value pairs can optionally leverage
	// Go templates for further processing.
	// With Sveltos, you can define key-value pairs where the values can be Go templates.
	// These templates have access to management cluster information during deployment. This allows
	// to do more than just replace placeholders. Variables can be used to dynamically
	// construct values based on other resources or variables within the Kustomize output.
	// For example, imagine you have a Region key with a template value like:
	// '{{ index .Cluster.metadata.labels "region" }}'.
	// This template retrieves the region label from the cluster instance metadata.
	// Finally, Sveltos uses these processed values to fill placeholders in the Kustomize output.
	// The output itself can also contain templates, like:
	// region: '{{ default "west" .Region }}'.
	// This way, the final output from Kustomize will have the region set dynamically based on
	// the actual region retrieved earlier.
	// +optional
	ValuesFrom []ValueFrom `json:"valuesFrom,omitempty"`
}

// StopMatchingBehavior indicates what will happen when Cluster stops matching
// a ClusterProfile. By default, withdrawpolicies, deployed Helm charts and Kubernetes
// resources will be removed from Cluster. LeavePolicy instead leaves Helm charts
// and Kubernetes policies in the Cluster.
// +kubebuilder:validation:Enum:=WithdrawPolicies;LeavePolicies
type StopMatchingBehavior string

// Define the StopMatchingBehavior constants.
const (
	WithdrawPolicies StopMatchingBehavior = "WithdrawPolicies"
	LeavePolicies    StopMatchingBehavior = "LeavePolicies"
)

type TemplateResourceRef struct {
	// Resource references a Kubernetes instance in the management
	// cluster to fetch and use during template instantiation.
	// For ClusterProfile namespace can be left empty. In such a case, namespace will
	// be implicit set to cluster's namespace.
	// Name and namespace can be expressed as a template and instantiate using any cluster field.
	Resource corev1.ObjectReference `json:"resource"`

	// Identifier is how the resource will be referred to in the
	// template
	Identifier string `json:"identifier"`

	// Optional indicates that the referenced resource is not mandatory.
	// If set to true and the resource is not found, the error will be ignored,
	// and Sveltos will continue processing other TemplateResourceRefs.
	// +kubebuilder:default:=false
	// +optional
	Optional bool `json:"optional,omitempty"`
}

type PolicyRef struct {
	// Namespace of the referenced resource.
	// For ClusterProfile namespace can be left empty. In such a case, namespace will
	// be implicit set to cluster's namespace.
	// For Profile namespace must be left empty. Profile namespace will be used.
	// Namespace can be expressed as a template and instantiate using any cluster field.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name of the referenced resource.
	// Name can be expressed as a template and instantiate using any cluster field.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind of the resource. Supported kinds are:
	// - ConfigMap/Secret
	// - flux GitRepository;OCIRepository;Bucket
	// +kubebuilder:validation:Enum=GitRepository;OCIRepository;Bucket;ConfigMap;Secret
	Kind string `json:"kind"`

	// Path to the directory containing the YAML files.
	// Defaults to 'None', which translates to the root path of the SourceRef.
	// Used only for GitRepository;OCIRepository;Bucket
	// +optional
	Path string `json:"path,omitempty"`

	// DeploymentType indicates whether resources need to be deployed
	// into the management cluster (local) or the managed cluster (remote)
	// +kubebuilder:default:=Remote
	// +optional
	DeploymentType DeploymentType `json:"deploymentType,omitempty"`

	// Optional indicates that the referenced resource is not mandatory.
	// If set to true and the resource is not found, the error will be ignored,
	// and Sveltos will continue processing other PolicyRefs.
	// +kubebuilder:default:=false
	// +optional
	Optional bool `json:"optional,omitempty"`
}

type Clusters struct {
	// Hash represents of a unique value for ClusterProfile Spec at
	// a fixed point in time
	// +optional
	Hash []byte `json:"hash,omitempty"`

	// Clusters reference all the clusters currently matching
	// ClusterProfile ClusterSelector and already updated/being updated
	// to ClusterProfile Spec
	Clusters []corev1.ObjectReference `json:"clusters,omitempty"`
}

type Spec struct {
	// ClusterSelector identifies clusters to associate to.
	// +optional
	ClusterSelector libsveltosv1beta1.Selector `json:"clusterSelector,omitempty"`

	// ClusterRefs identifies clusters to associate to.
	// +optional
	ClusterRefs []corev1.ObjectReference `json:"clusterRefs,omitempty"`

	// SetRefs identifies referenced (cluster)Sets.
	// - ClusterProfile can reference ClusterSet;
	// - Profile can reference Set;
	// +optional
	SetRefs []string `json:"setRefs,omitempty"`

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

	// DependsOn specifies a list of other ClusterProfiles that this instance depends on.
	// In any managed cluster that matches this ClusterProfile, the add-ons and applications
	// defined in this instance will not be deployed until all add-ons and applications in the
	// ClusterProfiles listed as dependencies are deployed.
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

	// ExtraLabels: These labels will be added by Sveltos to all Kubernetes resources deployed in
	// a managed cluster based on this ClusterProfile/Profile instance.
	// **Important:** If a resource deployed by Sveltos already has a label with a key present in
	// `ExtraLabels`, the value from `ExtraLabels` will override the existing value.
	// (Deprecated use Patches instead)
	// +optional
	ExtraLabels map[string]string `json:"extraLabels,omitempty"`

	// ExtraAnnotations: These annotations will be added by Sveltos to all Kubernetes resources
	// deployed in a managed cluster based on this ClusterProfile/Profile instance.
	// **Important:** If a resource deployed by Sveltos already has a annotation with a key present in
	// `ExtraAnnotations`, the value from `ExtraAnnotations` will override the existing value.
	// (Deprecated use Patches instead)
	// +optional
	ExtraAnnotations map[string]string `json:"extraAnnotations,omitempty"`
}
