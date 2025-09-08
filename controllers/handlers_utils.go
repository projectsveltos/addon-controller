/*
Copyright 2022-24. projectsveltos.io. All rights reserved.

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

package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/patcher"
	"github.com/projectsveltos/libsveltos/lib/pullmode"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

const (
	separator                = "---\n"
	clusterSummaryAnnotation = "projectsveltos.io/clustersummary"
	subresourcesAnnotation   = "projectsveltos.io/subresources"
	pathAnnotation           = "path"
)

func getClusterSummaryAnnotationValue(clusterSummary *configv1beta1.ClusterSummary) string {
	prefix := clusterops.GetPrefix(clusterSummary.Spec.ClusterType)
	return fmt.Sprintf("%s-%s-%s", prefix, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName)
}

// deployContentOfConfigMap deploys policies contained in a ConfigMap.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfConfigMap(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, configMap *corev1.ConfigMap, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) ([]libsveltosv1beta1.ResourceReport, error) {

	return deployContent(ctx, deployingToMgmtCluster, destConfig, destClient, configMap, configMap.Data,
		clusterSummary, mgmtResources, logger)
}

// deployContentOfSecret deploys policies contained in a Secret.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfSecret(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, secret *corev1.Secret, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) ([]libsveltosv1beta1.ResourceReport, error) {

	data := make(map[string]string)
	for key, value := range secret.Data {
		data[key] = string(value)
	}

	return deployContent(ctx, deployingToMgmtCluster, destConfig, destClient, secret, data,
		clusterSummary, mgmtResources, logger)
}

func deployContentOfSource(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, source client.Object, path string, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) ([]libsveltosv1beta1.ResourceReport, error) {

	s := source.(sourcev1.Source)

	tmpDir, err := prepareFileSystemWithFluxSource(s, logger)
	if err != nil {
		return nil, err
	}

	if tmpDir == "" {
		return nil, nil
	}

	defer os.RemoveAll(tmpDir)

	objects, err := fecthClusterObjects(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, err
	}

	// Path can be expressed as a template and instantiate using Cluster fields.
	instantiatedPath, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary, clusterSummary.GetName(), path, objects, nil, logger)
	if err != nil {
		return nil, err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("using path %s", instantiatedPath))

	// check build path exists
	dirPath := filepath.Join(tmpDir, instantiatedPath)
	_, err = os.Stat(dirPath)
	if err != nil {
		logger.Error(err, "source path not found")
		return nil, err
	}

	var content map[string]string
	content, err = readFiles(dirPath)
	if err != nil {
		logger.Error(err, "failed to read content")
		return nil, err
	}

	return deployContent(ctx, deployingToMgmtCluster, destConfig, destClient, source, content,
		clusterSummary, mgmtResources, logger)
}

func readFiles(dir string) (map[string]string, error) {
	files := make(map[string]string)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[filepath.Base(path)] = string(content)
		}
		return nil
	})
	return files, err
}

func instantiateTemplate(referencedObject client.Object, logger logr.Logger) bool {
	annotations := referencedObject.GetAnnotations()
	if annotations != nil {
		if _, ok := annotations[libsveltosv1beta1.PolicyTemplateAnnotation]; ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("referencedObject %s %s/%s is a template",
				referencedObject.GetObjectKind().GroupVersionKind().Kind, referencedObject.GetNamespace(), referencedObject.GetName()))
			return true
		}
	}

	return false
}

func instantiateWithLua(referencedObject client.Object, logger logr.Logger) bool {
	annotations := referencedObject.GetAnnotations()
	if annotations != nil {
		if _, ok := annotations[libsveltosv1beta1.PolicyLuaAnnotation]; ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("referencedObject %s %s/%s contains a lua script",
				referencedObject.GetObjectKind().GroupVersionKind().Kind, referencedObject.GetNamespace(), referencedObject.GetName()))
			return true
		}
	}

	return false
}

func getSubresources(referencedObject client.Object) []string {
	annotations := referencedObject.GetAnnotations()
	if annotations != nil {
		value, exists := annotations[subresourcesAnnotation]
		if exists {
			subresources := strings.Split(value, ",")
			return subresources
		}
	}
	return nil
}

// return a slice of ResourceReports. Only Resource is set within each report
func prepareReports(resources []*unstructured.Unstructured) []libsveltosv1beta1.ResourceReport {
	reports := make([]libsveltosv1beta1.ResourceReport, len(resources))

	for i := range reports {
		reports[i] = libsveltosv1beta1.ResourceReport{
			Resource: libsveltosv1beta1.Resource{
				Name:      resources[i].GetName(),
				Namespace: resources[i].GetNamespace(),
				Kind:      resources[i].GetKind(),
				Group:     resources[i].GroupVersionKind().Group,
				Version:   resources[i].GroupVersionKind().Version,
			},
		}
	}

	return reports
}

// deployContent deploys policies contained in a ConfigMap/Secret.
// data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContent(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config, destClient client.Client,
	referencedObject client.Object, data map[string]string, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []libsveltosv1beta1.ResourceReport, err error) {

	subresources := getSubresources(referencedObject)
	instantiateTemplate := instantiateTemplate(referencedObject, logger)
	instantiateLua := instantiateWithLua(referencedObject, logger)
	resources, err := collectContent(ctx, clusterSummary, mgmtResources, data, instantiateTemplate, instantiateLua, logger)
	if err != nil {
		return nil, err
	}

	resources, err = applyPatches(ctx, clusterSummary, resources, mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	ref := &corev1.ObjectReference{
		Kind:      referencedObject.GetObjectKind().GroupVersionKind().Kind,
		Namespace: referencedObject.GetNamespace(),
		Name:      referencedObject.GetName(),
	}

	// Only for SveltosCluster in pull mode and when the content must be deployed in the managed cluster
	// If deployingToMgmtCluster is true, we are deploying to the management cluster so proceed with deployUnstructured
	if destConfig == nil && !deployingToMgmtCluster {
		bundleResources := make(map[string][]unstructured.Unstructured)
		key := fmt.Sprintf("%s-%s-%s", ref.Kind, ref.Namespace, ref.Name)
		bundleResources[key] = convertPointerSliceToValueSlice(resources)

		// In pull mode we return reports with action Create. Those will only be used to update deployed GVK.
		// sveltos-applier will take care of sending proper reports

		return prepareReports(resources),
			pullmode.StageResourcesForDeployment(ctx, getManagementClusterClient(),
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
				configv1beta1.ClusterSummaryKind, clusterSummary.Name, string(libsveltosv1beta1.FeatureResources),
				bundleResources, false, logger)
	}

	return deployUnstructured(ctx, deployingToMgmtCluster, destConfig, destClient, resources, ref,
		libsveltosv1beta1.FeatureResources, clusterSummary, subresources, logger)
}

// adjustNamespace fixes namespace.
// - sets namespace to "default" for namespaced resource with unset namespace
// - unsets namespace for cluster-wide resources with namespace set
func adjustNamespace(policy *unstructured.Unstructured, destConfig *rest.Config) error {
	isResourceNamespaced, err := isNamespaced(policy, destConfig)
	if err != nil {
		return err
	}

	if isResourceNamespaced {
		if policy.GetNamespace() == "" {
			policy.SetNamespace("default")
		}
	} else {
		if policy.GetNamespace() != "" {
			policy.SetNamespace("")
		}
	}

	return nil
}

// isResourceNamespaceValid validates the resource namespace.
// A Profile, when deploying resources locally, i.e, to the management cluster, can
// only deploy resources in the same namespace
func isResourceNamespaceValid(profile client.Object, policy *unstructured.Unstructured,
	deployingToMgmtCluster bool) bool {

	if profile.GetObjectKind().GroupVersionKind().Kind == configv1beta1.ProfileKind {
		if deployingToMgmtCluster && policy.GetNamespace() != profile.GetNamespace() {
			return false
		}
	}

	return true
}

func applyPatches(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	referencedUnstructured []*unstructured.Unstructured, mgmtResources map[string]*unstructured.Unstructured,
	logger logr.Logger) ([]*unstructured.Unstructured, error) {

	patches, err := initiatePatches(ctx, clusterSummary, "patch", mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	if len(patches) > 0 {
		p := &patcher.CustomPatchPostRenderer{Patches: patches}
		referencedUnstructured, err = p.RunUnstructured(referencedUnstructured)
		if err != nil {
			return nil, err
		}
	}

	return referencedUnstructured, nil
}

// deployUnstructured deploys referencedUnstructured objects.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
//
//nolint:funlen // requires a lot of arguments because kustomize and plain resources are using this function
func deployUnstructured(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, referencedUnstructured []*unstructured.Unstructured, referencedObject *corev1.ObjectReference,
	featureID libsveltosv1beta1.FeatureID, clusterSummary *configv1beta1.ClusterSummary,
	subresources []string, logger logr.Logger) (reports []libsveltosv1beta1.ResourceReport, err error) {

	profile, profileTier, err := configv1beta1.GetProfileOwnerAndTier(ctx, getManagementClusterClient(), clusterSummary)
	if err != nil {
		return nil, err
	}

	conflictErrorMsg := ""
	errorMsg := ""
	reports = make([]libsveltosv1beta1.ResourceReport, 0)
	for i := range referencedUnstructured {
		policy := referencedUnstructured[i]

		errorPrefix := fmt.Sprintf("deploying resource %s %s/%s (deploy to management cluster: %v) failed",
			policy.GetKind(), policy.GetNamespace(), policy.GetName(), deployingToMgmtCluster)

		err := adjustNamespace(policy, destConfig)
		if err != nil {
			if clusterSummary.Spec.ClusterProfileSpec.ContinueOnError {
				errorMsg += fmt.Sprintf("%v", err)
				continue
			}
			return nil, fmt.Errorf("%s: %w", errorPrefix, err)
		}

		if !isResourceNamespaceValid(profile, policy, deployingToMgmtCluster) {
			return nil, fmt.Errorf("profile can only deploy resource in same namespace in the management cluster")
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("deploying resource %s %s/%s (deploy to management cluster: %v)",
			policy.GetKind(), policy.GetNamespace(), policy.GetName(), deployingToMgmtCluster))

		resource, policyHash := deployer.GetResource(policy, deployer.HasIgnoreConfigurationDriftAnnotation(policy),
			referencedObject, profile, profileTier, string(featureID), logger)

		// If policy is namespaced, create namespace if not already existing
		err = deployer.CreateNamespace(ctx, destClient, clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun,
			policy.GetNamespace())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errorPrefix, err)
		}

		dr, err := k8s_utils.GetDynamicResourceInterface(destConfig, policy.GroupVersionKind(), policy.GetNamespace())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", errorPrefix, err)
		}

		var resourceInfo *deployer.ResourceInfo
		var requeue bool
		resourceInfo, requeue, err = deployer.CanDeployResource(ctx, dr, policy, referencedObject, profile, profileTier, logger)
		if err != nil {
			var conflictErr *deployer.ConflictError
			ok := errors.As(err, &conflictErr)
			if ok {
				conflictResourceReport := deployer.GenerateConflictResourceReport(ctx, dr, resource)
				if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
					reports = append(reports, *conflictResourceReport)
					continue
				} else {
					conflictErrorMsg += conflictResourceReport.Message
					if clusterSummary.Spec.ClusterProfileSpec.ContinueOnConflict {
						continue
					}
					return reports, deployer.NewConflictError(conflictErrorMsg)
				}
			}
			return reports, fmt.Errorf("%s: %w", errorPrefix, err)
		}

		deployer.AddMetadata(policy, resourceInfo.GetResourceVersion(), profile,
			clusterSummary.Spec.ClusterProfileSpec.ExtraLabels, clusterSummary.Spec.ClusterProfileSpec.ExtraAnnotations)

		if deployingToMgmtCluster {
			// When deploying resources in the management cluster, just setting (Cluster)Profile as OwnerReference is
			// not enough. We also need to track which ClusterSummary is creating the resource. Otherwise while
			// trying to clean stale resources those objects will be incorrectly removed.
			// An extra annotation is added here to indicate the clustersummary, so the managed cluster, this
			// resource was created for
			value := getClusterSummaryAnnotationValue(clusterSummary)
			deployer.AddAnnotation(policy, clusterSummaryAnnotation, value)
		}

		if requeue {
			if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
				// No action required. Even though ClusterProfile has higher priority, it is in DryRun
				// mode. So what's already deployed stays as it is.
				err = requeueAllOldOwners(ctx, resourceInfo, featureID, clusterSummary, logger)
				if err != nil {
					return reports, fmt.Errorf("%s: %w", errorPrefix, err)
				}
			}
		}

		updatedPolicy, err := deployer.UpdateResource(ctx, dr, clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection,
			clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			policy, subresources, logger)
		if updatedPolicy != nil {
			resource.LastAppliedTime = &metav1.Time{Time: time.Now()}
			reports = append(reports, *deployer.GenerateResourceReport(policyHash, resourceInfo, updatedPolicy, resource))
		}

		if err != nil {
			if clusterSummary.Spec.ClusterProfileSpec.ContinueOnError {
				errorMsg += fmt.Sprintf("%v", err)
				continue
			}
			return reports, fmt.Errorf("%s: %w", errorPrefix, err)
		}
	}

	return reports, deployer.HandleDeployUnstructuredErrors(conflictErrorMsg, errorMsg,
		clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun)
}

// requeueAllOldOwners gets the list of all ClusterProfile/Profile instances currently owning the resource in the
// managed cluster (profiles). For each one, it finds the corresponding ClusterSummary and via requeueOldOwner reset
// the Status so a new reconciliation happens.
func requeueAllOldOwners(ctx context.Context, resourceInfo *deployer.ResourceInfo,
	featureID libsveltosv1beta1.FeatureID, clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	c := getManagementClusterClient()

	profileOwners := resourceInfo.GetOwnerReferences()

	annotations := resourceInfo.CurrentResource.GetAnnotations()

	// Since release v0.52.1 we replaced OwnerReference with annotations
	if annotations != nil && annotations[deployer.OwnerName] != "" {
		profileOwners = append(profileOwners, corev1.ObjectReference{
			Name: annotations[deployer.OwnerName],
			Kind: annotations[deployer.OwnerKind],
		})
	}

	// Since release v0.30.0 only one profile instance can deploy a resource in a managed
	// cluster. Before that though multiple instances could have deployed same resource
	// provided all those instances were referencing same ConfigMap/Secret.
	// Here we walk over ownerReferences just for backward compatibility
	for i := range profileOwners {
		var err error
		var profileKind string
		var profileName types.NamespacedName
		switch profileOwners[i].Kind {
		case configv1beta1.ClusterProfileKind:
			profileKind = configv1beta1.ClusterProfileKind
			profileName = types.NamespacedName{Name: profileOwners[i].Name}
		case configv1beta1.ProfileKind:
			profileKind = configv1beta1.ProfileKind
			profileName = types.NamespacedName{Name: profileOwners[i].Name}
		default:
			continue
		}

		if err != nil {
			return err
		}

		// Get ClusterSummary that deployed the resource.
		var ownerClusterSummary *configv1beta1.ClusterSummary
		ownerClusterSummary, err = clusterops.GetClusterSummary(ctx, c, profileKind, profileName.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}

		err = requeueClusterSummary(ctx, featureID, ownerClusterSummary, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

// collectContent collect policies contained in a ConfigMap/Secret.
// ConfigMap/Secret Data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice of *unstructured.Unstructured.
func collectContent(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, data map[string]string,
	instantiateTemplate, instantiateLua bool, logger logr.Logger,
) ([]*unstructured.Unstructured, error) {

	policies := make([]*unstructured.Unstructured, 0, len(data))

	objects, err := fecthClusterObjects(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, err
	}

	for k := range data {
		section := data[k]

		if instantiateTemplate {
			instance, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
				clusterSummary, clusterSummary.GetName(), section, objects, mgmtResources, logger)
			if err != nil {
				logger.Error(err, fmt.Sprintf("failed to instantiate policy from Data %.100s", section))
				return nil, err
			}

			section = instance
		} else if instantiateLua {
			instance, err := instantiateWithLuaScript(ctx, getManagementClusterConfig(), getManagementClusterClient(),
				clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
				section, mgmtResources, logger)
			if err != nil {
				logger.Error(err, fmt.Sprintf("failed to instantiate policy from Data %.100s", section))
				return nil, err
			}
			logger.V(logs.LogInfo).Info(fmt.Sprintf("lua output %q", instance))
			section = instance
		}

		elements, err := deployer.CustomSplit(section)
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to split Data %.100s", section))
			return nil, err
		}

		for i := range elements {
			section := elements[i]
			// Section can contain multiple resources separated by ---
			policy, err := deployer.GetUnstructured([]byte(section), logger)
			if err != nil {
				logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", section))
				return nil, err
			}

			if policy == nil {
				continue
			}

			policies = append(policies, policy...)
		}
	}

	return policies, nil
}

// getClusterSummaryAdmin returns the name of the admin that created the ClusterProfile
// instance owing this ClusterProfile instance
func getClusterSummaryAdmin(clusterSummary *configv1beta1.ClusterSummary) (namespace, name string) {
	if clusterSummary.Labels == nil {
		return "", ""
	}

	namespace = clusterSummary.Labels[libsveltosv1beta1.ServiceAccountNamespaceLabel]
	name = clusterSummary.Labels[libsveltosv1beta1.ServiceAccountNameLabel]
	return
}

// getClusterSummaryAndClusterClient gets ClusterSummary and the client to access the associated
// CAPI/Sveltos Cluster.
// Returns an err if ClusterSummary or associated CAPI Cluster are marked for deletion, or if an
// error occurs while getting resources.
func getClusterSummaryAndClusterClient(ctx context.Context, clusterNamespace, clusterSummaryName string,
	c client.Client, logger logr.Logger) (*configv1beta1.ClusterSummary, client.Client, error) {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1beta1.ClusterSummary{}
	if err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterNamespace, Name: clusterSummaryName}, clusterSummary); err != nil {
		return nil, nil, err
	}

	if !clusterSummary.DeletionTimestamp.IsZero() {
		logger.V(logs.LogInfo).Info("ClusterSummary is marked for deletion. Nothing to do.")
		// if clusterSummary is marked for deletion, there is nothing to deploy
		return nil, nil, fmt.Errorf("clustersummary is marked for deletion")
	}

	// Get Cluster
	cluster, err := clusterproxy.GetCluster(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
	if err != nil {
		return nil, nil, err
	}

	if !cluster.GetDeletionTimestamp().IsZero() {
		logger.V(logs.LogInfo).Info("cluster is marked for deletion. Nothing to do.")
		// if cluster is marked for deletion, there is nothing to deploy
		return nil, nil, fmt.Errorf("cluster is marked for deletion")
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	clusterClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, nil, err
	}

	return clusterSummary, clusterClient, nil
}

func appendPathAnnotations(object client.Object, reference *configv1beta1.PolicyRef) {
	if object == nil {
		return
	}
	annotations := object.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[pathAnnotation] = reference.Path
	// Path is needed when we need to collect resources.
	object.SetAnnotations(annotations)
}

// collectReferencedObjects collects all referenced configMaps/secrets in control cluster
// local contains all configMaps/Secrets whose content need to be deployed locally (in the management cluster)
// remote contains all configMap/Secrets whose content need to be deployed remotely (in the managed cluster)
func collectReferencedObjects(ctx context.Context, controlClusterClient client.Client,
	clusterSummary *configv1beta1.ClusterSummary, references []configv1beta1.PolicyRef,
	logger logr.Logger) (local, remote []client.Object, err error) {

	local = make([]client.Object, 0, len(references))
	remote = make([]client.Object, 0, len(references))
	for i := range references {
		var object client.Object
		reference := &references[i]

		namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, getManagementClusterClient(),
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, references[i].Namespace,
			clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate namespace for %s %s/%s: %v",
				reference.Kind, reference.Namespace, reference.Name, err))
			return nil, nil, err
		}

		name, err := libsveltostemplate.GetReferenceResourceName(ctx, getManagementClusterClient(),
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, references[i].Name,
			clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s %s/%s: %v",
				reference.Kind, reference.Namespace, reference.Name, err))
			return nil, nil, err
		}

		if reference.Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			object, err = getConfigMap(ctx, controlClusterClient,
				types.NamespacedName{Namespace: namespace, Name: name})
		} else if reference.Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
			object, err = getSecret(ctx, controlClusterClient,
				types.NamespacedName{Namespace: namespace, Name: name})
		} else {
			object, err = getSource(ctx, controlClusterClient, namespace, name, reference.Kind)
			appendPathAnnotations(object, reference)
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				msg := fmt.Sprintf("Referenced resource: %s %s/%s does not exist",
					reference.Kind, reference.Namespace, name)
				logger.V(logs.LogInfo).Info(msg)
				if reference.Optional {
					continue
				}
				return nil, nil, &configv1beta1.NonRetriableError{Message: msg}
			}
			return nil, nil, err
		}

		if reference.DeploymentType == configv1beta1.DeploymentTypeLocal {
			local = append(local, object)
		} else {
			remote = append(remote, object)
		}
	}

	return local, remote, nil
}

// deployReferencedObjects deploys in a managed Cluster the resources contained in each referenced ConfigMap
// - objectsToDeployLocally is a list of ConfigMaps/Secrets whose content need to be deployed
// in the management cluster
// - objectsToDeployRemotely is a list of ConfigMaps/Secrets whose content need to be deployed
// in the managed cluster
func deployReferencedObjects(ctx context.Context, c client.Client, remoteConfig *rest.Config, remoteClient client.Client,
	clusterSummary *configv1beta1.ClusterSummary, objectsToDeployLocally, objectsToDeployRemotely []client.Object,
	logger logr.Logger) (localReports, remoteReports []libsveltosv1beta1.ResourceReport, err error) {

	var mgmtResources map[string]*unstructured.Unstructured
	mgmtResources, err = collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(err.Error())
			return nil, nil, &configv1beta1.NonRetriableError{Message: err.Error()}
		}
		return nil, nil, err
	}

	// Assume that if objects are deployed in the management clusters, those are needed before any
	// resource is deployed in the managed cluster. So try to deploy those first if any.

	localConfig := rest.CopyConfig(getManagementClusterConfig())
	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	if adminName != "" {
		localConfig.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", adminNamespace, adminName),
		}
	}
	localReports, err = deployObjects(ctx, true, c, localConfig, objectsToDeployLocally, clusterSummary,
		mgmtResources, logger)
	if err != nil {
		return localReports, nil, err
	}

	// Deploy all resources that need to be deployed in the managed cluster
	remoteReports, err = deployObjects(ctx, false, remoteClient, remoteConfig, objectsToDeployRemotely, clusterSummary,
		mgmtResources, logger)
	if err != nil {
		return localReports, remoteReports, err
	}

	return localReports, remoteReports, nil
}

// deployObjects deploys content of referencedObjects
func deployObjects(ctx context.Context, deployingToMgmtCluster bool, destClient client.Client, destConfig *rest.Config,
	referencedObjects []client.Object, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []libsveltosv1beta1.ResourceReport, err error) {

	reports = make([]libsveltosv1beta1.ResourceReport, 0, len(referencedObjects))
	for i := range referencedObjects {
		var tmpResourceReports []libsveltosv1beta1.ResourceReport
		if referencedObjects[i].GetObjectKind().GroupVersionKind().Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			configMap := referencedObjects[i].(*corev1.ConfigMap)
			l := logger.WithValues("configMapNamespace", configMap.Namespace, "configMapName", configMap.Name)
			l.V(logs.LogDebug).Info("deploying ConfigMap content")
			tmpResourceReports, err =
				deployContentOfConfigMap(ctx, deployingToMgmtCluster, destConfig, destClient, configMap,
					clusterSummary, mgmtResources, l)
		} else if referencedObjects[i].GetObjectKind().GroupVersionKind().Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
			secret := referencedObjects[i].(*corev1.Secret)
			l := logger.WithValues("secretNamespace", secret.Namespace, "secretName", secret.Name)
			l.V(logs.LogDebug).Info("deploying Secret content")
			tmpResourceReports, err =
				deployContentOfSecret(ctx, deployingToMgmtCluster, destConfig, destClient, secret,
					clusterSummary, mgmtResources, l)
		} else {
			source := referencedObjects[i]
			logger.V(logs.LogDebug).Info("deploying Source content")
			annotations := source.GetAnnotations()
			path := annotations[pathAnnotation]
			tmpResourceReports, err =
				deployContentOfSource(ctx, deployingToMgmtCluster, destConfig, destClient, source, path,
					clusterSummary, mgmtResources, logger)
		}

		if tmpResourceReports != nil {
			reports = append(reports, tmpResourceReports...)
		}

		if err != nil {
			return reports, err
		}
	}

	return reports, nil
}

func isManagedClusterDeleted(ctx context.Context, isMgmtCluster bool, clusterSummary *configv1beta1.ClusterSummary) (bool, error) {
	if !isMgmtCluster {
		cluster, err := clusterproxy.GetCluster(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}

		if !cluster.GetDeletionTimestamp().IsZero() {
			// if cluster is marked for deletion, no need to worry about removing resources deployed
			// there. This check applies only for managed cluster. Resources deployed in the management
			// cluster are still removed
			return true, nil
		}
	}
	return false, nil
}

func setupRemoteConfigAndClient(isMgmtCluster bool, remoteConfig *rest.Config, remoteClient client.Client,
	clusterSummary *configv1beta1.ClusterSummary) (*rest.Config, client.Client, error) {

	if isMgmtCluster {
		localConfig := rest.CopyConfig(getManagementClusterConfig())
		adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
		if adminName != "" {
			localConfig.Impersonate = rest.ImpersonationConfig{
				UserName: fmt.Sprintf("system:serviceaccount:%s:%s", adminNamespace, adminName),
			}
		}
		localClient, err := client.New(localConfig, client.Options{Scheme: remoteClient.Scheme()})
		if err != nil {
			return nil, nil, err
		}
		return localConfig, localClient, nil
	}
	return remoteConfig, remoteClient, nil
}

func processDeployedGVKs(ctx context.Context, isMgmtCluster bool, remoteConfig *rest.Config, remoteClient client.Client,
	featureID libsveltosv1beta1.FeatureID, clusterSummary *configv1beta1.ClusterSummary, deployedGVKs []schema.GroupVersionKind,
	currentPolicies map[string]libsveltosv1beta1.Resource, profile client.Object, logger logr.Logger,
) ([]libsveltosv1beta1.ResourceReport, error) {

	undeployed := make([]libsveltosv1beta1.ResourceReport, 0)

	localConfig, localClient, err := setupRemoteConfigAndClient(isMgmtCluster, remoteConfig, remoteClient, clusterSummary)
	if err != nil {
		return nil, err
	}

	dc := discovery.NewDiscoveryClientForConfigOrDie(localConfig)
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	d := dynamic.NewForConfigOrDie(remoteConfig)

	labelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{deployer.ReasonLabel: string(featureID)},
	}

	listOptions := metav1.ListOptions{
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
	}

	for i := range deployedGVKs {
		// TODO: move this to separate method
		logger.V(logs.LogDebug).Info(fmt.Sprintf("removing stale resources for GVK %s", deployedGVKs[i].String()))
		mapping, err := mapper.RESTMapping(deployedGVKs[i].GroupKind(), deployedGVKs[i].Version)
		if err != nil {
			// if CRDs does not exist anymore, ignore error. No instances of
			// such CRD can be left anyway.
			if errors.Is(err, &meta.NoKindMatchError{}) {
				logger.V(logs.LogDebug).Info(fmt.Sprintf("removing stale resources for GVK %s failed with NoKindMatchError",
					deployedGVKs[i].String()))
				continue
			}
			return nil, err
		}

		resourceId := schema.GroupVersionResource{
			Group:    deployedGVKs[i].Group,
			Version:  deployedGVKs[i].Version,
			Resource: mapping.Resource.Resource,
		}

		list, err := d.Resource(resourceId).List(ctx, listOptions)
		if err != nil {
			return nil, err
		}

		leavePolicies := isLeavePolicies(clusterSummary, logger)
		isDryRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		var skipAnnotationKey, skipAnnotationValue string
		if isMgmtCluster {
			skipAnnotationValue = getClusterSummaryAnnotationValue(clusterSummary)
			skipAnnotationKey = clusterSummaryAnnotation
		}

		for j := range list.Items {
			r := list.Items[j]
			rr, err := deployer.UndeployStaleResource(ctx, skipAnnotationKey, skipAnnotationValue, localClient,
				profile, leavePolicies, isDryRun, r, currentPolicies, logger)
			if err != nil {
				return nil, err
			}

			if rr != nil {
				undeployed = append(undeployed, *rr)
			}
		}
	}

	return undeployed, nil
}

func undeployStaleResources(ctx context.Context, isMgmtCluster bool,
	remoteConfig *rest.Config, remoteClient client.Client, featureID libsveltosv1beta1.FeatureID,
	clusterSummary *configv1beta1.ClusterSummary, deployedGVKs []schema.GroupVersionKind,
	currentPolicies map[string]libsveltosv1beta1.Resource, logger logr.Logger) ([]libsveltosv1beta1.ResourceReport, error) {

	logger.V(logs.LogDebug).Info("removing stale resources")

	isClusterDeleted, err := isManagedClusterDeleted(ctx, isMgmtCluster, clusterSummary)
	if err != nil {
		return nil, err
	}
	if isClusterDeleted {
		return nil, nil
	}

	profile, _, err := configv1beta1.GetProfileOwnerAndTier(ctx, getManagementClusterClient(), clusterSummary)
	if err != nil {
		return nil, err
	}

	return processDeployedGVKs(ctx, isMgmtCluster, remoteConfig, remoteClient, featureID, clusterSummary, deployedGVKs,
		currentPolicies, profile, logger)
}

// isLeavePolicies returns true if:
// - ClusterSummary is marked for deletion
// - StopMatchingBehavior is set to LeavePolicies
func isLeavePolicies(clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) bool {
	if !clusterSummary.DeletionTimestamp.IsZero() &&
		clusterSummary.Spec.ClusterProfileSpec.StopMatchingBehavior == configv1beta1.LeavePolicies {

		logger.V(logs.LogInfo).Info("ClusterProfile StopMatchingBehavior set to LeavePolicies")
		return true
	}
	return false
}

func getDeployedGroupVersionKinds(clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID) []schema.GroupVersionKind {

	gvks := make([]schema.GroupVersionKind, 0)
	// For backward compatible we still look at this field.
	// New code set only FeatureDeploymentInfo
	fs := getFeatureSummaryForFeatureID(clusterSummary, featureID)
	if fs != nil {
		for j := range fs.DeployedGroupVersionKind {
			gvk, _ := schema.ParseKindArg(fs.DeployedGroupVersionKind[j])
			gvks = append(gvks, *gvk)
		}
	}

	fdi := getFeatureDeploymentInfoForFeatureID(clusterSummary, featureID)
	if fdi != nil {
		for j := range fdi.DeployedGroupVersionKind {
			gvk, _ := schema.ParseKindArg(fdi.DeployedGroupVersionKind[j])
			gvks = append(gvks, *gvk)
		}
	}

	return gvks
}

// getConfigMap retrieves any ConfigMap from the given name and namespace.
func getConfigMap(ctx context.Context, c client.Client, configmapName types.NamespacedName) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	if err := c.Get(ctx, configmapName, configMap); err != nil {
		return nil, err
	}

	addTypeInformationToObject(c.Scheme(), configMap)

	return configMap, nil
}

// getSecret retrieves any Secret from the given secret name and namespace.
func getSecret(ctx context.Context, c client.Client, secretName types.NamespacedName) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, secretName, secret); err != nil {
		return nil, err
	}

	if secret.Type != libsveltosv1beta1.ClusterProfileSecretType {
		return nil, libsveltosv1beta1.ErrSecretTypeNotSupported
	}

	addTypeInformationToObject(c.Scheme(), secret)

	return secret, nil
}

func updateDeployedGroupVersionKind(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	featureID libsveltosv1beta1.FeatureID, localResourceReports, remoteResourceReports []libsveltosv1beta1.ResourceReport,
	logger logr.Logger) (*configv1beta1.ClusterSummary, error) {

	logger.V(logs.LogDebug).Info("update status with deployed GroupVersionKinds")
	reports := localResourceReports
	reports = append(reports, remoteResourceReports...)

	if len(reports) == 0 {
		return clusterSummary, nil
	}

	c := getManagementClusterClient()

	currentClusterSummary := &configv1beta1.ClusterSummary{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := c.Get(ctx,
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)
		if err != nil {
			return err
		}

		gvks := make([]schema.GroupVersionKind, 0)
		gvkMap := make(map[schema.GroupVersionKind]bool)
		for i := range reports {
			gvk := schema.GroupVersionKind{
				Group:   reports[i].Resource.Group,
				Version: reports[i].Resource.Version,
				Kind:    reports[i].Resource.Kind,
			}
			if _, ok := gvkMap[gvk]; !ok {
				gvks = append(gvks, gvk)
				gvkMap[gvk] = true
			}
		}

		// update status with list of GroupVersionKinds deployed in a Managed and Management Cluster
		appendDeployedGroupVersionKinds(currentClusterSummary, gvks, featureID)

		return getManagementClusterClient().Status().Update(ctx, currentClusterSummary)
	})

	return currentClusterSummary, err
}

// appendDeployedGroupVersionKinds appends the list of deployed GroupVersionKinds to current list
func appendDeployedGroupVersionKinds(clusterSummary *configv1beta1.ClusterSummary, gvks []schema.GroupVersionKind,
	featureID libsveltosv1beta1.FeatureID) {

	fdi := getFeatureDeploymentInfoForFeatureID(clusterSummary, featureID)
	if fdi != nil {
		fdi.DeployedGroupVersionKind = append(
			fdi.DeployedGroupVersionKind,
			tranformGroupVersionKindToString(gvks)...)
		// Remove duplicates
		fdi.DeployedGroupVersionKind = unique(fdi.DeployedGroupVersionKind)
		return
	}

	if fdi == nil {
		clusterSummary.Status.DeployedGVKs = make([]libsveltosv1beta1.FeatureDeploymentInfo, 0)
	}

	clusterSummary.Status.DeployedGVKs = append(
		clusterSummary.Status.DeployedGVKs,
		libsveltosv1beta1.FeatureDeploymentInfo{
			FeatureID:                featureID,
			DeployedGroupVersionKind: tranformGroupVersionKindToString(gvks),
		},
	)
}

func tranformGroupVersionKindToString(gvks []schema.GroupVersionKind) []string {
	result := make([]string, 0)
	tmpMap := make(map[string]bool)

	for i := range gvks {
		key := fmt.Sprintf("%s.%s.%s", gvks[i].Kind, gvks[i].Version, gvks[i].Group)
		if _, ok := tmpMap[key]; !ok {
			tmpMap[key] = true
			result = append(result, key)
		}
	}

	return result
}

// getRestConfig returns restConfig to access remote cluster
func getRestConfig(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) (*rest.Config, logr.Logger, error) {

	clusterNamespace := clusterSummary.Spec.ClusterNamespace
	clusterName := clusterSummary.Spec.ClusterName

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName)).
		WithValues("clusterSummary", clusterSummary.Name).WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	logger.V(logs.LogDebug).Info("get remote restConfig")
	cacheMgr := clustercache.GetManager()
	remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, logger, err
	}

	return remoteRestConfig, logger, nil
}

func getValuesFromResourceHash(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	valuesFrom []configv1beta1.ValueFrom, logger logr.Logger) (string, error) {

	var config string
	for i := range valuesFrom {
		namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, valuesFrom[i].Namespace,
			clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate namespace for %s %s/%s: %v",
				valuesFrom[i].Kind, valuesFrom[i].Namespace, valuesFrom[i].Name, err))
			return "", err
		}

		name, err := libsveltostemplate.GetReferenceResourceName(ctx, c,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, valuesFrom[i].Name,
			clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s %s/%s: %v",
				valuesFrom[i].Kind, valuesFrom[i].Namespace, valuesFrom[i].Name, err))
			return "", err
		}

		if valuesFrom[i].Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			configMap, err := getConfigMap(ctx, c,
				types.NamespacedName{Namespace: namespace, Name: name})
			if err == nil {
				config += getDataSectionHash(configMap.Data)
				config += getDataSectionHash(configMap.BinaryData)
			}
		} else if valuesFrom[i].Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
			secret, err := getSecret(ctx, c,
				types.NamespacedName{Namespace: namespace, Name: name})
			if err == nil {
				config += getDataSectionHash(secret.Data)
				config += getDataSectionHash(secret.StringData)
			}
		}
	}

	return config, nil
}

// getValuesFrom function retrieves key-value pairs from referenced ConfigMaps or Secrets.
//
// - `valuesFrom`: A slice of `ValueFrom` objects specifying the ConfigMaps or Secrets to use.
// - `overrideKeys`: Controls how existing keys are handled:
//   - `true`: Existing keys in the output map will be overwritten with new values from references.
//   - `false`: Values from references will be appended to existing keys in the output map using the `addToMap` function.
//
// It returns a map containing the collected key-value pairs and any encountered error.
func getValuesFrom(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	valuesFrom []configv1beta1.ValueFrom, overrideKeys bool, logger logr.Logger) (template, nonTemplate map[string]string, err error) {

	template = make(map[string]string)
	nonTemplate = make(map[string]string)
	for i := range valuesFrom {
		namespace, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, c,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, valuesFrom[i].Namespace,
			clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate namespace for %s %s/%s: %v",
				valuesFrom[i].Kind, valuesFrom[i].Namespace, valuesFrom[i].Name, err))
			return nil, nil, err
		}

		name, err := libsveltostemplate.GetReferenceResourceName(ctx, c, clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, valuesFrom[i].Name, clusterSummary.Spec.ClusterType)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s %s/%s: %v",
				valuesFrom[i].Kind, valuesFrom[i].Namespace, valuesFrom[i].Name, err))
			return nil, nil, err
		}

		if valuesFrom[i].Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
			if err != nil {
				err = handleReferenceError(err, valuesFrom[i].Kind, namespace, name, valuesFrom[i].Optional, logger)
				if err == nil {
					continue
				}
				return nil, nil, err
			}

			if instantiateTemplate(configMap, logger) {
				for key, value := range configMap.Data {
					if overrideKeys {
						template[key] = value
					} else {
						addToMap(template, key, value)
					}
				}
			} else {
				for key, value := range configMap.Data {
					if overrideKeys {
						nonTemplate[key] = value
					} else {
						addToMap(nonTemplate, key, value)
					}
				}
			}
		} else if valuesFrom[i].Kind == string(libsveltosv1beta1.SecretReferencedResourceKind) {
			secret, err := getSecret(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
			if err != nil {
				err = handleReferenceError(err, valuesFrom[i].Kind, namespace, name, valuesFrom[i].Optional, logger)
				if err == nil {
					continue
				}
				return nil, nil, err
			}
			if instantiateTemplate(secret, logger) {
				for key, value := range secret.Data {
					if overrideKeys {
						template[key] = string(value)
					} else {
						addToMap(template, key, string(value))
					}
				}
			} else {
				for key, value := range secret.Data {
					if overrideKeys {
						nonTemplate[key] = string(value)
					} else {
						addToMap(nonTemplate, key, string(value))
					}
				}
			}
		}
	}

	return template, nonTemplate, nil
}

func handleReferenceError(err error, kind, namespace, name string, optional bool,
	logger logr.Logger) error {

	msg := fmt.Sprintf("Referenced resource: %s %s/%s", kind, namespace, name)

	if apierrors.IsNotFound(err) {
		msg += " does not exist"
		logger.V(logs.LogInfo).Info(msg)
		if optional {
			return nil
		}
		return &configv1beta1.NonRetriableError{Message: msg}
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("%s: %v", msg, err))
	return fmt.Errorf("%s: %w", msg, err)
}

func addToMap(m map[string]string, key, value string) {
	// Check if the key exists in the map
	if existingValue, ok := m[key]; ok {
		// Concatenate the new value to the existing one
		m[key] = existingValue + "\n" + value
	} else {
		// Key doesn't exist, add it with the value
		m[key] = value
	}
}

// Return Templated Patch Objects
func initiatePatches(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	requestor string, mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (instantiatedPatches []libsveltosv1beta1.Patch, err error) {

	if len(clusterSummary.Spec.ClusterProfileSpec.Patches) == 0 {
		return
	}

	instantiatedPatches = clusterSummary.Spec.ClusterProfileSpec.Patches

	objects, err := fecthClusterObjects(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, err
	}

	for k := range instantiatedPatches {
		instantiatedPatch, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
			clusterSummary, requestor, instantiatedPatches[k].Patch, objects, mgmtResources, logger)
		if err != nil {
			return nil, err
		}

		instantiatedPatches[k].Patch = instantiatedPatch
	}

	return
}

func getClusterProfileSpecHash(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary) ([]byte, error) {
	h := sha256.New()
	var config string

	clusterProfileSpec := clusterSummary.Spec.ClusterProfileSpec
	// If SyncMode changes (from not ContinuousWithDriftDetection to ContinuousWithDriftDetection
	// or viceversa) reconcile.
	config += fmt.Sprintf("%v", clusterProfileSpec.SyncMode)

	// When using ContinuousWithDriftDetection in agentless mode, ResourceSummary instances are now managed in the management cluster.
	// This addition ensures the ClusterSummary is redeployed due to the change in deployment location.
	if clusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection && getAgentInMgmtCluster() {
		config += ("agentless")
	}

	// If Reloader changes, Reloader needs to be deployed or undeployed
	// So consider it in the hash
	config += fmt.Sprintf("%v", clusterProfileSpec.Reloader)

	// If Tier changes, conflicts might be resolved differently
	// So consider it in the hash
	config += fmt.Sprintf("%d", clusterProfileSpec.Tier)
	config += fmt.Sprintf("%t", clusterProfileSpec.ContinueOnConflict)

	if clusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Drift detection is now upgraded on its own. v1.0.1 was last release triggering
		// the upgrade via ClusterSummary redeployment. v1.0.1 is still added here to make
		// sure hash does not change
		config += "v1.0.1"
	}

	mgmtResourceHash, err := getTemplateResourceRefHash(ctx, clusterSummary)
	if err != nil {
		return nil, err
	}

	config += string(mgmtResourceHash)

	if clusterProfileSpec.Patches != nil {
		config += render.AsCode(clusterProfileSpec.Patches)
	}

	// If drift-detectionmanager configuration is in a ConfigMap. fetch ConfigMap and use its Data
	// section in the hash evaluation.
	if driftDetectionConfigMap := getDriftDetectionConfigMap(); driftDetectionConfigMap != "" {
		configMap, err := collectDriftDetectionConfigMap(ctx)
		if err != nil {
			return nil, err
		}
		config += render.AsCode(configMap.Data)
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getTemplateResourceRefHash(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
) ([]byte, error) {

	mgmtResources, err := collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		return nil, err
	}

	if len(mgmtResources) == 0 {
		return []byte(""), nil
	}

	var keys []string
	for k := range mgmtResources {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var config string
	for i := range keys {
		config += render.AsCode(mgmtResources[keys[i]])
	}

	h := sha256.New()
	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func prepareSetters(clusterSummary *configv1beta1.ClusterSummary, featureID libsveltosv1beta1.FeatureID,
	profileRef *corev1.ObjectReference, configurationHash []byte) []pullmode.Option {

	setters := make([]pullmode.Option, 0)
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		setters = append(setters, pullmode.WithDriftDetection(),
			pullmode.WithDriftDetectionPatches(clusterSummary.Spec.ClusterProfileSpec.DriftExclusions))
	}
	if clusterSummary.Spec.ClusterProfileSpec.Reloader {
		setters = append(setters, pullmode.WithReloader())
	}
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		setters = append(setters, pullmode.WithDryRun())
	}
	if clusterSummary.Spec.ClusterProfileSpec.MaxConsecutiveFailures != nil {
		setters = append(setters, pullmode.WithMaxConsecutiveFailures(*clusterSummary.Spec.ClusterProfileSpec.MaxConsecutiveFailures))
	}
	if clusterSummary.Spec.ClusterProfileSpec.StopMatchingBehavior == configv1beta1.LeavePolicies {
		setters = append(setters, pullmode.WithLeavePolicies())
	}

	if !clusterSummary.DeletionTimestamp.IsZero() {
		setters = append(setters, pullmode.WithSourceStatus(libsveltosv1beta1.SourceStatusDeleted))
	} else {
		setters = append(setters, pullmode.WithSourceStatus(libsveltosv1beta1.SourceStatusActive))
	}

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	if adminName != "" {
		setters = append(setters, pullmode.WithServiceAccount(adminNamespace, adminName))
	}

	setters = append(setters, pullmode.WithRequestorHash(configurationHash))
	deployedGVKs := getDeployedGroupVersionKinds(clusterSummary, featureID)
	gvks := tranformGroupVersionKindToString(deployedGVKs)

	setters = append(setters, pullmode.WithTier(clusterSummary.Spec.ClusterProfileSpec.Tier),
		pullmode.WithContinueOnConflict(clusterSummary.Spec.ClusterProfileSpec.ContinueOnConflict),
		pullmode.WithContinueOnError(clusterSummary.Spec.ClusterProfileSpec.ContinueOnError),
		pullmode.WithValidateHealths(clusterSummary.Spec.ClusterProfileSpec.ValidateHealths),
		pullmode.WithDeployedGVKs(gvks))

	// Do not check on profileOwnerRef being not nil. It must always be passed
	sourceRef := corev1.ObjectReference{
		APIVersion: profileRef.APIVersion,
		Kind:       profileRef.Kind,
		Name:       profileRef.Name,
		UID:        profileRef.UID,
	}

	if profileRef.Kind == configv1beta1.ProfileKind {
		sourceRef.Namespace = clusterSummary.Namespace
	}

	setters = append(setters, pullmode.WithSourceRef(&sourceRef))

	return setters
}

func updateReloaderWithDeployedResources(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	profileRef *corev1.ObjectReference, feature libsveltosv1beta1.FeatureID, resources []corev1.ObjectReference,
	removeReloader bool, logger logr.Logger) error {

	reloaderClient, err := getReloaderClient(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	return clusterops.UpdateReloaderWithDeployedResources(ctx, reloaderClient, profileRef, feature, resources,
		removeReloader, logger)
}

// Reloader instances reside in the same cluster as the sveltos-agent component.
// This function dynamically selects the appropriate Kubernetes client:
// - Management cluster's client if Sveltos agents are deployed there.
// - A managed cluster's client (obtained via clusterproxy) if Sveltos agents are in a managed cluster.
func getReloaderClient(ctx context.Context, clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) (client.Client, error) {

	if getAgentInMgmtCluster() {
		return getManagementClusterClient(), nil
	}

	// ResourceSummary is a Sveltos resource created in managed clusters.
	// Sveltos resources are always created using cluster-admin so that admin does not need to be
	// given such permissions.
	return clusterproxy.GetKubernetesClient(ctx, getManagementClusterClient(),
		clusterNamespace, clusterName, "", "", clusterType, logger)
}

func getFileWithKubeconfig(ctx context.Context, c client.Client, clusterSummary *configv1beta1.ClusterSummary,
	logger logr.Logger) (fileName string, closer func(), err error) {

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s",
		clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName))
	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger = logger.WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	kubeconfigContent, err := clusterproxy.GetSecretData(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return "", nil, err
	}

	fileName, closer, err = clusterproxy.CreateKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return "", nil, err
	}
	return fileName, closer, nil
}
