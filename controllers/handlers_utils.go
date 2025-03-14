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
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/patcher"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

const (
	separator                = "---\n"
	reasonLabel              = "projectsveltos.io/reason"
	clusterSummaryAnnotation = "projectsveltos.io/clustersummary"
	subresourcesAnnotation   = "projectsveltos.io/subresources"
	pathAnnotation           = "path"
)

func getClusterSummaryAnnotationValue(clusterSummary *configv1beta1.ClusterSummary) string {
	prefix := getPrefix(clusterSummary.Spec.ClusterType)
	return fmt.Sprintf("%s-%s-%s", prefix, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName)
}

// createNamespace creates a namespace if it does not exist already
// No action in DryRun mode.
func createNamespace(ctx context.Context, clusterClient client.Client,
	clusterSummary *configv1beta1.ClusterSummary, namespaceName string) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	if namespaceName == "" {
		return nil
	}

	currentNs := &corev1.Namespace{}
	if err := clusterClient.Get(ctx, client.ObjectKey{Name: namespaceName}, currentNs); err != nil {
		if apierrors.IsNotFound(err) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceName,
				},
			}
			return clusterClient.Create(ctx, ns)
		}
		return err
	}
	return nil
}

// deployContentOfConfigMap deploys policies contained in a ConfigMap.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfConfigMap(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, configMap *corev1.ConfigMap, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) ([]configv1beta1.ResourceReport, error) {

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
) ([]configv1beta1.ResourceReport, error) {

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
) ([]configv1beta1.ResourceReport, error) {

	s := source.(sourcev1.Source)

	tmpDir, err := prepareFileSystemWithFluxSource(s, logger)
	if err != nil {
		return nil, err
	}

	if tmpDir == "" {
		return nil, nil
	}

	defer os.RemoveAll(tmpDir)

	// Path can be expressed as a template and instantiate using Cluster fields.
	instantiatedPath, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
		clusterSummary, clusterSummary.GetName(), path, nil, logger)
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

func applySubresources(ctx context.Context, dr dynamic.ResourceInterface,
	object *unstructured.Unstructured, subresources []string, options *metav1.PatchOptions) error {

	if len(subresources) == 0 {
		return nil
	}

	object.SetManagedFields(nil)
	object.SetResourceVersion("")
	data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, object)
	if err != nil {
		return err
	}

	_, err = dr.Patch(ctx, object.GetName(), types.ApplyPatchType, data, *options, subresources...)
	return err
}

func removeDriftExclusionsFields(ctx context.Context, dr dynamic.ResourceInterface,
	clusterSummary *configv1beta1.ClusterSummary, object *unstructured.Unstructured) (bool, error) {

	// When operating in SyncModeContinuousWithDriftDetection mode and DriftExclusions are specified,
	// avoid resetting certain object fields if the object is being redeployed (i.e, object already exists)
	// For example, consider a Deployment with an Autoscaler. Since the Autoscaler manages the spec.replicas
	// field, Sveltos is requested to deploy the Deployment and spec.replicas is specified as a field to ignore during
	// configuration drift evaluation.
	// If Sveltos is redeploying the deployment (for instance deployment image tag was changed), Sveltos must not
	// override spec.replicas.
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		if clusterSummary.Spec.ClusterProfileSpec.DriftExclusions != nil {
			_, err := dr.Get(ctx, object.GetName(), metav1.GetOptions{})
			if err == nil {
				// Resource exist. We are in drift detection mode and with driftExclusions.
				// Remove fields in driftExclusions before applying an update
				return true, nil
			} else if apierrors.IsNotFound(err) {
				// Object does not exist. We can apply it as it is. Since the object does
				// not exist, nothing will be overridden
				return false, nil
			} else {
				return false, err
			}
		}
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun &&
		clusterSummary.Spec.ClusterProfileSpec.DriftExclusions != nil {
		// When evaluating diff in DryRun mode, exclude fields
		return true, nil
	}

	return false, nil
}

// updateResource creates or updates a resource in a Cluster.
// No action in DryRun mode.
func updateResource(ctx context.Context, dr dynamic.ResourceInterface,
	clusterSummary *configv1beta1.ClusterSummary, object *unstructured.Unstructured, subresources []string,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	forceConflict := true
	options := metav1.PatchOptions{
		FieldManager: "application/apply-patch",
		Force:        &forceConflict,
	}

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		// Set dryRun option. Still proceed further so diff can be properly evaluated
		options.DryRun = []string{metav1.DryRunAll}
	}

	l := logger.WithValues("resourceNamespace", object.GetNamespace(), "resourceName", object.GetName(),
		"resourceGVK", object.GetObjectKind().GroupVersionKind(), "subresources", subresources)
	l.V(logs.LogDebug).Info("deploying policy")

	removeFields, err := removeDriftExclusionsFields(ctx, dr, clusterSummary, object)
	if err != nil {
		return nil, err
	}

	if removeFields {
		patches := transformDriftExclusionsToPatches(clusterSummary.Spec.ClusterProfileSpec.DriftExclusions)
		p := &patcher.CustomPatchPostRenderer{Patches: patches}
		var patchedObjects []*unstructured.Unstructured
		patchedObjects, err = p.RunUnstructured([]*unstructured.Unstructured{object})
		if err != nil {
			return nil, err
		}
		object = patchedObjects[0]
	}

	data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, object)
	if err != nil {
		return nil, err
	}

	updatedObject, err := dr.Patch(ctx, object.GetName(), types.ApplyPatchType, data, options)
	if err != nil {
		if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun &&
			apierrors.IsNotFound(err) {
			// In DryRun mode, if resource is namespaced and namespace is not present,
			// patch will fail with namespace not found. Treat this error as resoruce
			// would be created
			return object, nil
		}
		return nil, err
	}

	return updatedObject, applySubresources(ctx, dr, object, subresources, &options)
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

// deployContent deploys policies contained in a ConfigMap/Secret.
// data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContent(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config, destClient client.Client,
	referencedObject client.Object, data map[string]string, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []configv1beta1.ResourceReport, err error) {

	subresources := getSubresources(referencedObject)
	instantiateTemplate := instantiateTemplate(referencedObject, logger)
	instantiateLua := instantiateWithLua(referencedObject, logger)
	resources, err := collectContent(ctx, clusterSummary, mgmtResources, data, instantiateTemplate, instantiateLua, logger)
	if err != nil {
		return nil, err
	}

	ref := &corev1.ObjectReference{
		Kind:      referencedObject.GetObjectKind().GroupVersionKind().Kind,
		Namespace: referencedObject.GetNamespace(),
		Name:      referencedObject.GetName(),
	}

	return deployUnstructured(ctx, deployingToMgmtCluster, destConfig, destClient, resources, ref,
		configv1beta1.FeatureResources, clusterSummary, mgmtResources, subresources, logger)
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
	featureID configv1beta1.FeatureID, clusterSummary *configv1beta1.ClusterSummary, mgmtResources map[string]*unstructured.Unstructured,
	subresources []string, logger logr.Logger) (reports []configv1beta1.ResourceReport, err error) {

	profile, profileTier, err := configv1beta1.GetProfileOwnerAndTier(ctx, getManagementClusterClient(), clusterSummary)
	if err != nil {
		return nil, err
	}

	referencedUnstructured, err = applyPatches(ctx, clusterSummary, referencedUnstructured, mgmtResources, logger)
	if err != nil {
		return nil, err
	}

	conflictErrorMsg := ""
	errorMsg := ""
	reports = make([]configv1beta1.ResourceReport, 0)
	for i := range referencedUnstructured {
		policy := referencedUnstructured[i]

		err := adjustNamespace(policy, destConfig)
		if err != nil {
			if clusterSummary.Spec.ClusterProfileSpec.ContinueOnError {
				errorMsg += fmt.Sprintf("%v", err)
				continue
			}
			return nil, err
		}

		if !isResourceNamespaceValid(profile, policy, deployingToMgmtCluster) {
			return nil, fmt.Errorf("profile can only deploy resource in same namespace in the management cluster")
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("deploying resource %s %s/%s (deploy to management cluster: %v)",
			policy.GetKind(), policy.GetNamespace(), policy.GetName(), deployingToMgmtCluster))

		resource, policyHash := getResource(policy, hasIgnoreConfigurationDriftAnnotation(policy), referencedObject, profileTier, featureID, logger)

		// If policy is namespaced, create namespace if not already existing
		err = createNamespace(ctx, destClient, clusterSummary, policy.GetNamespace())
		if err != nil {
			return nil, err
		}

		dr, err := k8s_utils.GetDynamicResourceInterface(destConfig, policy.GroupVersionKind(), policy.GetNamespace())
		if err != nil {
			return nil, err
		}

		var resourceInfo *deployer.ResourceInfo
		var requeue bool
		resourceInfo, requeue, err = canDeployResource(ctx, dr, policy, referencedObject, profile, profileTier, logger)
		if err != nil {
			var conflictErr *deployer.ConflictError
			ok := errors.As(err, &conflictErr)
			if ok {
				conflictResourceReport := generateConflictResourceReport(ctx, dr, resource)
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
			return reports, err
		}

		addMetadata(policy, resourceInfo.GetResourceVersion(), profile,
			clusterSummary.Spec.ClusterProfileSpec.ExtraLabels, clusterSummary.Spec.ClusterProfileSpec.ExtraAnnotations)

		if deployingToMgmtCluster {
			// When deploying resources in the management cluster, just setting (Cluster)Profile as OwnerReference is
			// not enough. We also need to track which ClusterSummary is creating the resource. Otherwise while
			// trying to clean stale resources those objects will be incorrectly removed.
			// An extra annotation is added here to indicate the clustersummary, so the managed cluster, this
			// resource was created for
			value := getClusterSummaryAnnotationValue(clusterSummary)
			addAnnotation(policy, clusterSummaryAnnotation, value)
		}

		if requeue {
			if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
				// No action required. Even though ClusterProfile has higher priority, it is in DryRun
				// mode. So what's already deployed stays as it is.
				err = requeueAllOldOwners(ctx, resourceInfo.GetOwnerReferences(), featureID, clusterSummary, logger)
				if err != nil {
					return reports, err
				}
			}
		}

		policy, err = updateResource(ctx, dr, clusterSummary, policy, subresources, logger)
		resource.LastAppliedTime = &metav1.Time{Time: time.Now()}
		reports = append(reports, *generateResourceReport(policyHash, resourceInfo, policy, resource))
		if err != nil {
			if clusterSummary.Spec.ClusterProfileSpec.ContinueOnError {
				errorMsg += fmt.Sprintf("%v", err)
				continue
			}
			return reports, err
		}
	}

	return reports, handleDeployUnstructuredErrors(conflictErrorMsg, errorMsg, clusterSummary)
}

func handleDeployUnstructuredErrors(conflictErrorMsg, errorMsg string, clusterSummary *configv1beta1.ClusterSummary,
) error {

	if conflictErrorMsg != "" {
		if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
			// if in DryRun mode, ignore conflicts
			return deployer.NewConflictError(conflictErrorMsg)
		}
	}

	if errorMsg != "" {
		if clusterSummary.Spec.ClusterProfileSpec.SyncMode != configv1beta1.SyncModeDryRun {
			// if in DryRun mode, ignore errors
			return fmt.Errorf("%s", errorMsg)
		}
	}

	return nil
}

func addMetadata(policy *unstructured.Unstructured, resourceVersion string, profile client.Object,
	extraLabels, extraAnnotations map[string]string) {

	// The canDeployResource function validates if objects can be deployed. It achieves this by
	// fetching the object from the managed cluster and using its metadata to detect and potentially
	// resolve conflicts. If a conflict is detected, and the decision favors current clusterSummary instance,
	// the policy is updated.
	// However, it's crucial to ensure that between the time canDeployResource runs and the policy update,
	// no other modifications occur to the object. This includes updates from other ClusterSummary instances.
	// To guarantee consistency, we leverage the object's resourceVersion obtained by canDeployResource when
	// fetching the object. Setting the resource version during policy update acts as an optimistic locking mechanism.
	// If the object has been modified since the canDeployResource call, setting the resource version will fail,
	// invalidating previous conflict call and preventing unintended overwrites.
	// This approach ensures that conflict resolution decisions made by canDeployResource remain valid during the policy update.
	policy.SetResourceVersion(resourceVersion)

	k8s_utils.AddOwnerReference(policy, profile)

	addExtraLabels(policy, extraLabels)
	addExtraAnnotations(policy, extraAnnotations)
}

// requeueAllOldOwners gets the list of all ClusterProfile/Profile instances currently owning the resource in the
// managed cluster (profiles). For each one, it finds the corresponding ClusterSummary and via requeueOldOwner reset
// the Status so a new reconciliation happens.
func requeueAllOldOwners(ctx context.Context, profileOwners []corev1.ObjectReference,
	featureID configv1beta1.FeatureID, clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	c := getManagementClusterClient()
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
			profileName = types.NamespacedName{Namespace: clusterSummary.Namespace, Name: profileOwners[i].Name}
		default:
			continue
		}

		if err != nil {
			return err
		}

		// Get ClusterSummary that deployed the resource.
		var ownerClusterSummary *configv1beta1.ClusterSummary
		ownerClusterSummary, err = getClusterSummary(ctx, c, profileKind, profileName.Name,
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

// canDeployResource verifies whether resource can be deployed. Following checks are performed:
//
// - if resource is currently already deployed in the managed cluster, if owned by this (Cluster)Profile/referenced
// resource => it can be updated
// - if resource is currently already deployed in the managed cluster and owned by same (Cluster)Profile but different
// referenced resource => it cannot be updated
// - if resource is currently already deployed in the managed cluster but owned by different (Cluster)Profile
// => it can be updated only if current (Cluster)Profile tier is lower than profile currently deploying the resource
//
// If resource cannot be deployed, return a ConflictError.
// If any other error occurs while doing those verification, the error is returned
func canDeployResource(ctx context.Context, dr dynamic.ResourceInterface, policy *unstructured.Unstructured,
	referencedObject *corev1.ObjectReference, profile client.Object, profileTier int32, logger logr.Logger,
) (resourceInfo *deployer.ResourceInfo, requeueOldOwner bool, err error) {

	l := logger.WithValues("resource",
		fmt.Sprintf("%s:%s/%s", referencedObject.Kind, referencedObject.Namespace, referencedObject.Name))
	resourceInfo, err = deployer.ValidateObjectForUpdate(ctx, dr, policy,
		referencedObject.Kind, referencedObject.Namespace, referencedObject.Name, profile)
	if err != nil {
		var conflictErr *deployer.ConflictError
		ok := errors.As(err, &conflictErr)
		if ok {
			// There is a conflict.
			if hasHigherOwnershipPriority(getTier(resourceInfo.OwnerTier), profileTier) {
				l.V(logs.LogDebug).Info("conflict detected but resource ownership can change")
				// Because of tier, ownership must change. Which also means current ClusterProfile/Profile
				// owning the resource must be requeued for reconciliation
				return resourceInfo, true, nil
			}
			l.V(logs.LogDebug).Info("conflict detected")
			// Conflict cannot be resolved in favor of the clustersummary being reconciled. So report the conflict
			// error
			return resourceInfo, false, conflictErr
		}
		return nil, false, err
	}

	// There was no conflict. Resource can be deployed.
	return resourceInfo, false, nil
}

func generateResourceReport(policyHash string, resourceInfo *deployer.ResourceInfo,
	policy *unstructured.Unstructured, resource *configv1beta1.Resource) *configv1beta1.ResourceReport {

	resourceReport := &configv1beta1.ResourceReport{Resource: *resource}
	if resourceInfo == nil {
		resourceReport.Action = string(configv1beta1.CreateResourceAction)
	} else if policyHash != resourceInfo.Hash {
		resourceReport.Action = string(configv1beta1.UpdateResourceAction)
		diff, err := evaluateResourceDiff(resourceInfo.CurrentResource, policy)
		if err == nil {
			resourceReport.Message = diff
		}
	} else {
		resourceReport.Action = string(configv1beta1.NoResourceAction)
		resourceReport.Message = "Object already deployed. And policy referenced by ClusterProfile has not changed since last deployment."
	}

	return resourceReport
}

// evaluateResourceDiff evaluates and returns diff
func evaluateResourceDiff(from, to *unstructured.Unstructured) (string, error) {
	objectInfo := fmt.Sprintf("%s %s", from.GroupVersionKind().Kind, from.GetName())

	// Remove managedFields, status and the hash annotation added by Sveltos.
	from = omitManagedFields(from)
	from = omitGeneratation(from)
	from = omitStatus(from)
	from = omitHashAnnotation(from)

	to = omitManagedFields(to)
	to = omitGeneratation(to)
	to = omitStatus(to)
	to = omitHashAnnotation(to)

	fromTempFile, err := os.CreateTemp("", "from-temp-file-")
	if err != nil {
		return "", err
	}
	defer os.Remove(fromTempFile.Name()) // Clean up the file after use
	fromWriter := io.Writer(fromTempFile)
	err = printUnstructured(from, fromWriter)
	if err != nil {
		return "", err
	}

	toTempFile, err := os.CreateTemp("", "to-temp-file-")
	if err != nil {
		return "", err
	}
	defer os.Remove(toTempFile.Name()) // Clean up the file after use
	toWriter := io.Writer(toTempFile)
	err = printUnstructured(to, toWriter)
	if err != nil {
		return "", err
	}

	fromContent, err := os.ReadFile(fromTempFile.Name())
	if err != nil {
		return "", err
	}

	toContent, err := os.ReadFile(toTempFile.Name())
	if err != nil {
		return "", err
	}

	edits := myers.ComputeEdits(span.URIFromPath(objectInfo), string(fromContent), string(toContent))

	diff := fmt.Sprint(gotextdiff.ToUnified(fmt.Sprintf("deployed: %s", objectInfo),
		fmt.Sprintf("proposed: %s", objectInfo), string(fromContent), edits))

	return diff, nil
}

func omitManagedFields(u *unstructured.Unstructured) *unstructured.Unstructured {
	a, err := meta.Accessor(u)
	if err != nil {
		// The object is not a `metav1.Object`, ignore it.
		return u
	}
	a.SetManagedFields(nil)
	return u
}

func omitGeneratation(u *unstructured.Unstructured) *unstructured.Unstructured {
	a, err := meta.Accessor(u)
	if err != nil {
		// The object is not a `metav1.Object`, ignore it.
		return u
	}
	a.SetGeneration(0)
	return u
}

func omitStatus(u *unstructured.Unstructured) *unstructured.Unstructured {
	content := u.UnstructuredContent()
	if _, ok := content["status"]; ok {
		content["status"] = ""
	}
	u.SetUnstructuredContent(content)
	return u
}

func omitHashAnnotation(u *unstructured.Unstructured) *unstructured.Unstructured {
	annotations := u.GetAnnotations()
	if annotations != nil {
		delete(annotations, deployer.PolicyHash)
	}
	u.SetAnnotations(annotations)
	return u
}

// Print the object inside the writer w.
func printUnstructured(obj runtime.Object, w io.Writer) error {
	if obj == nil {
		return nil
	}
	data, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// addExtraLabels adds ExtraLabels to policy.
// If policy already has a label with a key present in `ExtraLabels`, the value from `ExtraLabels` will
// override the existing value.
func addExtraLabels(policy *unstructured.Unstructured, extraLabels map[string]string) {
	if extraLabels == nil {
		return
	}

	if len(extraLabels) == 0 {
		return
	}

	lbls := policy.GetLabels()
	if lbls == nil {
		lbls = map[string]string{}
	}
	for k := range extraLabels {
		lbls[k] = extraLabels[k]
	}

	policy.SetLabels(lbls)
}

// addExtraAnnotations adds ExtraAnnotations to policy.
// If policy already has an annotation with a key present in `ExtraAnnotations`, the value from `ExtraAnnotations`
// will override the existing value.
func addExtraAnnotations(policy *unstructured.Unstructured, extraAnnotations map[string]string) {
	if extraAnnotations == nil {
		return
	}

	if len(extraAnnotations) == 0 {
		return
	}

	annotations := policy.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	for k := range extraAnnotations {
		annotations[k] = extraAnnotations[k]
	}

	policy.SetAnnotations(annotations)
}

// getResource returns sveltos Resource and the resource hash hash
func getResource(policy *unstructured.Unstructured, ignoreForConfigurationDrift bool,
	referencedObject *corev1.ObjectReference, tier int32, featureID configv1beta1.FeatureID, logger logr.Logger,
) (resource *configv1beta1.Resource, policyHash string) {

	resource = &configv1beta1.Resource{
		Name:      policy.GetName(),
		Namespace: policy.GetNamespace(),
		Kind:      policy.GetKind(),
		Group:     policy.GetObjectKind().GroupVersionKind().Group,
		Version:   policy.GetObjectKind().GroupVersionKind().Version,
		Owner: corev1.ObjectReference{
			Namespace: referencedObject.Namespace,
			Name:      referencedObject.Name,
			Kind:      referencedObject.Kind,
		},
		IgnoreForConfigurationDrift: ignoreForConfigurationDrift,
	}

	var err error
	policyHash, err = computePolicyHash(policy)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to compute policy hash %v", err))
		policyHash = ""
	}

	// Get policy hash of referenced policy
	addLabel(policy, deployer.ReferenceKindLabel, referencedObject.GetObjectKind().GroupVersionKind().Kind)
	addLabel(policy, deployer.ReferenceNameLabel, referencedObject.Name)
	addLabel(policy, deployer.ReferenceNamespaceLabel, referencedObject.Namespace)
	addLabel(policy, reasonLabel, string(featureID))
	addAnnotation(policy, deployer.PolicyHash, policyHash)
	addAnnotation(policy, deployer.OwnerTier, fmt.Sprintf("%d", tier))

	return resource, policyHash
}

// removeCommentsAndEmptyLines removes any line containing just YAML comments
// and any empty lines
func removeCommentsAndEmptyLines(text string) string {
	commentLine := regexp.MustCompile(`(?m)^\s*#([^#].*?)$`)
	result := commentLine.ReplaceAllString(text, "")
	emptyLine := regexp.MustCompile(`(?m)^\s*$`)
	result = emptyLine.ReplaceAllString(result, "")
	return result
}

func customSplit(text string) ([]string, error) {
	section := removeCommentsAndEmptyLines(text)
	if section == "" {
		return nil, nil
	}

	if !strings.Contains(text, "---") {
		return []string{text}, nil
	}

	result := []string{}

	dec := yaml.NewDecoder(bytes.NewReader([]byte(text)))

	for {
		var value interface{}
		err := dec.Decode(&value)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		valueBytes, err := yaml.Marshal(value)
		if err != nil {
			return nil, err
		}
		if valueBytes == nil {
			continue
		}
		result = append(result, string(valueBytes))
	}

	return result, nil
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

	for k := range data {
		section := data[k]

		if instantiateTemplate {
			instance, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
				clusterSummary, clusterSummary.GetName(), section, mgmtResources, logger)
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

		elements, err := customSplit(section)
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to split Data %.100s", section))
			return nil, err
		}

		for i := range elements {
			section := elements[i]
			// Section can contain multiple resources separated by ---
			policy, err := getUnstructured([]byte(section), logger)
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

func getUnstructured(section []byte, logger logr.Logger) ([]*unstructured.Unstructured, error) {
	elements, err := customSplit(string(section))
	if err != nil {
		return nil, err
	}
	policies := make([]*unstructured.Unstructured, 0, len(elements))
	for i := range elements {
		policy, err := k8s_utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
			return nil, err
		}

		if policy == nil {
			logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", elements[i]))
			return nil, fmt.Errorf("failed to get policy from Data %.100s", elements[i])
		}

		policies = append(policies, policy)
	}

	return policies, nil
}

func getPolicyInfo(policy *configv1beta1.Resource) string {
	return fmt.Sprintf("%s.%s:%s:%s",
		policy.Kind,
		policy.Group,
		policy.Namespace,
		policy.Name)
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

	// Get CAPI Cluster
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

		namespace := libsveltostemplate.GetReferenceResourceNamespace(
			clusterSummary.Namespace, references[i].Namespace)

		name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), references[i].Name)
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
				return nil, nil, &NonRetriableError{Message: msg}
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
func deployReferencedObjects(ctx context.Context, c client.Client, remoteConfig *rest.Config,
	clusterSummary *configv1beta1.ClusterSummary, objectsToDeployLocally, objectsToDeployRemotely []client.Object,
	logger logr.Logger) (localReports, remoteReports []configv1beta1.ResourceReport, err error) {

	remoteClient, err := client.New(remoteConfig, client.Options{})
	if err != nil {
		return nil, nil, err
	}

	var mgmtResources map[string]*unstructured.Unstructured
	mgmtResources, err = collectTemplateResourceRefs(ctx, clusterSummary)
	if err != nil {
		return nil, nil, err
	}

	var tmpResourceReports []configv1beta1.ResourceReport

	// Assume that if objects are deployed in the management clusters, those are needed before any
	// resource is deployed in the managed cluster. So try to deploy those first if any.

	localConfig := rest.CopyConfig(getManagementClusterConfig())
	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	if adminName != "" {
		localConfig.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", adminNamespace, adminName),
		}
	}
	tmpResourceReports, err = deployObjects(ctx, true, c, localConfig, objectsToDeployLocally, clusterSummary,
		mgmtResources, logger)
	localReports = append(localReports, tmpResourceReports...)
	if err != nil {
		return localReports, nil, err
	}

	// Deploy all resources that need to be deployed in the managed cluster
	tmpResourceReports, err = deployObjects(ctx, false, remoteClient, remoteConfig, objectsToDeployRemotely, clusterSummary,
		mgmtResources, logger)
	remoteReports = append(remoteReports, tmpResourceReports...)
	if err != nil {
		return localReports, remoteReports, err
	}

	return localReports, remoteReports, nil
}

// deployObjects deploys content of referencedObjects
func deployObjects(ctx context.Context, deployingToMgmtCluster bool, destClient client.Client, destConfig *rest.Config,
	referencedObjects []client.Object, clusterSummary *configv1beta1.ClusterSummary,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []configv1beta1.ResourceReport, err error) {

	reports = make([]configv1beta1.ResourceReport, 0, len(referencedObjects))
	for i := range referencedObjects {
		var tmpResourceReports []configv1beta1.ResourceReport
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

func undeployStaleResources(ctx context.Context, isMgmtCluster bool,
	remoteConfig *rest.Config, remoteClient client.Client, featureID configv1beta1.FeatureID,
	clusterSummary *configv1beta1.ClusterSummary, deployedGVKs []schema.GroupVersionKind,
	currentPolicies map[string]configv1beta1.Resource, logger logr.Logger) ([]configv1beta1.ResourceReport, error) {

	logger.V(logs.LogDebug).Info("removing stale resources")

	if !isMgmtCluster {
		cluster, err := clusterproxy.GetCluster(ctx, getManagementClusterClient(), clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, err
		}

		if !cluster.GetDeletionTimestamp().IsZero() {
			// if cluster is marked for deletion, no need to worry about removing resources deployed
			// there. This check applies only for managed cluster. Resources deployed in the management
			// cluster are still removed
			return nil, nil
		}
	}

	profile, _, err := configv1beta1.GetProfileOwnerAndTier(ctx, getManagementClusterClient(), clusterSummary)
	if err != nil {
		return nil, err
	}

	undeployed := make([]configv1beta1.ResourceReport, 0)

	dc := discovery.NewDiscoveryClientForConfigOrDie(remoteConfig)
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	d := dynamic.NewForConfigOrDie(remoteConfig)

	labelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{reasonLabel: string(featureID)},
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

		for j := range list.Items {
			r := list.Items[j]
			rr, err := undeployStaleResource(ctx, isMgmtCluster, remoteClient, profile, clusterSummary,
				r, currentPolicies, logger)
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

func undeployStaleResource(ctx context.Context, isMgmtCluster bool, remoteClient client.Client,
	profile client.Object, clusterSummary *configv1beta1.ClusterSummary, r unstructured.Unstructured,
	currentPolicies map[string]configv1beta1.Resource, logger logr.Logger) (*configv1beta1.ResourceReport, error) {

	logger.V(logs.LogVerbose).Info(fmt.Sprintf("considering %s/%s", r.GetNamespace(), r.GetName()))
	// Verify if this policy was deployed because of a projectsveltos (ReferenceLabelName
	// is present as label in such a case).
	if !hasLabel(&r, deployer.ReferenceNameLabel, "") {
		return nil, nil
	}

	if isMgmtCluster {
		// When deploying resources in the management cluster, just setting ClusterProfile as OwnerReference is
		// not enough. We also need to track which ClusterSummary is creating the resource. Otherwise while
		// trying to clean stale resources those objects will be incorrectly removed.
		// An extra annotation is added to indicate the clustersummary, so the managed cluster, this
		// resource was created for. Check if annotation is present.
		value := getClusterSummaryAnnotationValue(clusterSummary)
		if !hasAnnotation(&r, clusterSummaryAnnotation, value) {
			return nil, nil
		}
	}

	var resourceReport *configv1beta1.ResourceReport = nil
	// If in DryRun do not withdrawn any policy.
	// If this ClusterSummary is the only OwnerReference and it is not deploying this policy anymore,
	// policy would be withdrawn
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		if canDelete(&r, currentPolicies) && k8s_utils.IsOnlyOwnerReference(&r, profile) &&
			!isLeavePolicies(clusterSummary, logger) {

			resourceReport = &configv1beta1.ResourceReport{
				Resource: configv1beta1.Resource{
					Kind: r.GetObjectKind().GroupVersionKind().Kind, Namespace: r.GetNamespace(), Name: r.GetName(),
					Group: r.GroupVersionKind().Group, Version: r.GroupVersionKind().Version,
				},
				Action: string(configv1beta1.DeleteResourceAction),
			}
		}
	} else if canDelete(&r, currentPolicies) {
		logger.V(logs.LogVerbose).Info(fmt.Sprintf("remove owner reference %s/%s", r.GetNamespace(), r.GetName()))

		k8s_utils.RemoveOwnerReference(&r, profile)

		if len(r.GetOwnerReferences()) != 0 {
			// Other ClusterSummary are still deploying this very same policy
			return nil, nil
		}

		err := handleResourceDelete(ctx, remoteClient, &r, clusterSummary, logger)
		if err != nil {
			return nil, err
		}
	}

	return resourceReport, nil
}

func handleResourceDelete(ctx context.Context, remoteClient client.Client, policy client.Object,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	// If mode is set to LeavePolicies, leave policies in the workload cluster.
	// Remove all labels added by Sveltos.
	if isLeavePolicies(clusterSummary, logger) {
		l := policy.GetLabels()
		delete(l, deployer.ReferenceKindLabel)
		delete(l, deployer.ReferenceNameLabel)
		delete(l, deployer.ReferenceNamespaceLabel)
		policy.SetLabels(l)
		return remoteClient.Update(ctx, policy)
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("removing resource %s %s/%s",
		policy.GetObjectKind().GroupVersionKind().Kind, policy.GetNamespace(), policy.GetName()))
	return remoteClient.Delete(ctx, policy)
}

// canDelete returns true if a policy can be deleted. For a policy to be deleted:
// - policy is not part of currentReferencedPolicies
func canDelete(policy client.Object, currentReferencedPolicies map[string]configv1beta1.Resource) bool {
	name := getPolicyInfo(&configv1beta1.Resource{
		Kind:      policy.GetObjectKind().GroupVersionKind().Kind,
		Group:     policy.GetObjectKind().GroupVersionKind().Group,
		Version:   policy.GetObjectKind().GroupVersionKind().Version,
		Name:      policy.GetName(),
		Namespace: policy.GetNamespace(),
	})
	if _, ok := currentReferencedPolicies[name]; ok {
		return false
	}

	return true
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

// hasLabel search if key is one of the label.
// If value is empty, returns true if key is present.
// If value is not empty, returns true if key is present and value is a match.
func hasLabel(u *unstructured.Unstructured, key, value string) bool {
	lbls := u.GetLabels()
	if lbls == nil {
		return false
	}

	v, ok := lbls[key]

	if value == "" {
		return ok
	}

	return v == value
}

// hasAnnotation search if key is one of the annotation.
// If value is empty, returns true if key is present.
// If value is not empty, returns true if key is present and value is a match.
func hasAnnotation(u *unstructured.Unstructured, key, value string) bool {
	annotations := u.GetAnnotations()
	if annotations == nil {
		return false
	}

	v, ok := annotations[key]

	if value == "" {
		return ok
	}

	return v == value
}

func getDeployedGroupVersionKinds(clusterSummary *configv1beta1.ClusterSummary,
	featureID configv1beta1.FeatureID) []schema.GroupVersionKind {

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

// No action in DryRun mode.
func updateClusterConfiguration(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary,
	profileOwnerRef *metav1.OwnerReference,
	featureID configv1beta1.FeatureID,
	policyDeployed []configv1beta1.Resource,
	chartDeployed []configv1beta1.Chart) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get ClusterConfiguration for CAPI Cluster
		clusterConfiguration := &configv1beta1.ClusterConfiguration{}
		err := c.Get(ctx,
			types.NamespacedName{
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Name:      getClusterConfigurationName(clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType),
			},
			clusterConfiguration)
		if err != nil {
			if apierrors.IsNotFound(err) && !clusterSummary.DeletionTimestamp.IsZero() {
				return nil
			}
			return err
		}

		var index int
		index, err = configv1beta1.GetClusterConfigurationSectionIndex(clusterConfiguration, profileOwnerRef.Kind,
			profileOwnerRef.Name)
		if err != nil {
			return err
		}

		if profileOwnerRef.Kind == configv1beta1.ClusterProfileKind {
			return updateClusterProfileResources(ctx, c, profileOwnerRef, clusterConfiguration,
				index, featureID, policyDeployed, chartDeployed)
		} else {
			return updateProfileResources(ctx, c, profileOwnerRef, clusterConfiguration,
				index, featureID, policyDeployed, chartDeployed)
		}
	})

	return err
}

func updateClusterProfileResources(ctx context.Context, c client.Client, profileOwnerRef *metav1.OwnerReference,
	clusterConfiguration *configv1beta1.ClusterConfiguration, index int,
	featureID configv1beta1.FeatureID, policyDeployed []configv1beta1.Resource,
	chartDeployed []configv1beta1.Chart) error {

	isPresent := false

	profileResources := &clusterConfiguration.Status.ClusterProfileResources[index]
	for i := range profileResources.Features {
		if profileResources.Features[i].FeatureID == featureID {
			if policyDeployed != nil {
				profileResources.Features[i].Resources = policyDeployed
			}
			if chartDeployed != nil {
				profileResources.Features[i].Charts = chartDeployed
			}
			isPresent = true
			break
		}
	}

	if !isPresent {
		if profileResources.Features == nil {
			profileResources.Features = make([]configv1beta1.Feature, 0)
		}
		profileResources.Features = append(
			profileResources.Features,
			configv1beta1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
		)
	}

	clusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences, *profileOwnerRef)

	return c.Status().Update(ctx, clusterConfiguration)
}

func updateProfileResources(ctx context.Context, c client.Client, profileOwnerRef *metav1.OwnerReference,
	clusterConfiguration *configv1beta1.ClusterConfiguration, index int,
	featureID configv1beta1.FeatureID, policyDeployed []configv1beta1.Resource,
	chartDeployed []configv1beta1.Chart) error {

	isPresent := false

	profileResources := &clusterConfiguration.Status.ProfileResources[index]
	for i := range profileResources.Features {
		if profileResources.Features[i].FeatureID == featureID {
			if policyDeployed != nil {
				profileResources.Features[i].Resources = policyDeployed
			}
			if chartDeployed != nil {
				profileResources.Features[i].Charts = chartDeployed
			}
			isPresent = true
			break
		}
	}

	if !isPresent {
		if profileResources.Features == nil {
			profileResources.Features = make([]configv1beta1.Feature, 0)
		}
		profileResources.Features = append(
			profileResources.Features,
			configv1beta1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
		)
	}

	clusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences, *profileOwnerRef)

	return c.Status().Update(ctx, clusterConfiguration)
}

// computePolicyHash compute policy hash.
func computePolicyHash(policy *unstructured.Unstructured) (string, error) {
	b, err := policy.MarshalJSON()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = hash.Write(b)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
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

func generateConflictResourceReport(ctx context.Context, dr dynamic.ResourceInterface,
	resource *configv1beta1.Resource) *configv1beta1.ResourceReport {

	conflictReport := &configv1beta1.ResourceReport{
		Resource: *resource,
		Action:   string(configv1beta1.ConflictResourceAction),
	}
	message, err := deployer.GetOwnerMessage(ctx, dr, resource.Name)
	if err == nil {
		conflictReport.Message = message
	}
	return conflictReport
}

func updateDeployedGroupVersionKind(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	featureID configv1beta1.FeatureID, localResourceReports, remoteResourceReports []configv1beta1.ResourceReport,
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
	featureID configv1beta1.FeatureID) {

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
		clusterSummary.Status.DeployedGVKs = make([]configv1beta1.FeatureDeploymentInfo, 0)
	}

	clusterSummary.Status.DeployedGVKs = append(
		clusterSummary.Status.DeployedGVKs,
		configv1beta1.FeatureDeploymentInfo{
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
		namespace := libsveltostemplate.GetReferenceResourceNamespace(
			clusterSummary.Namespace, valuesFrom[i].Namespace)

		name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), valuesFrom[i].Name)
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
		namespace := libsveltostemplate.GetReferenceResourceNamespace(
			clusterSummary.Namespace, valuesFrom[i].Namespace)

		name, err := libsveltostemplate.GetReferenceResourceName(clusterSummary.Spec.ClusterNamespace,
			clusterSummary.Spec.ClusterName, string(clusterSummary.Spec.ClusterType), valuesFrom[i].Name)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to instantiate name for %s %s/%s: %v",
				valuesFrom[i].Kind, valuesFrom[i].Namespace, valuesFrom[i].Name, err))
			return nil, nil, err
		}

		if valuesFrom[i].Kind == string(libsveltosv1beta1.ConfigMapReferencedResourceKind) {
			configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: namespace, Name: name})
			if err != nil {
				msg := fmt.Sprintf("failed to get ConfigMap %s/%s", namespace, name)
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s: %v", msg, err))
				if apierrors.IsNotFound(err) {
					msg := fmt.Sprintf("Referenced resource: %s %s/%s does not exist",
						libsveltosv1beta1.ConfigMapReferencedResourceKind, namespace, name)
					logger.V(logs.LogInfo).Info(msg)
					return nil, nil, &NonRetriableError{Message: msg}
				}
				return nil, nil, fmt.Errorf("%s: %w", msg, err)
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
				msg := fmt.Sprintf("failed to get Secret %s/%s", namespace, name)
				logger.V(logs.LogInfo).Info(fmt.Sprintf("%s: %v", msg, err))
				if apierrors.IsNotFound(err) {
					msg := fmt.Sprintf("Referenced resource: %s %s/%s does not exist",
						libsveltosv1beta1.SecretReferencedResourceKind, namespace, name)
					logger.V(logs.LogInfo).Info(msg)
					return nil, nil, &NonRetriableError{Message: msg}
				}
				return nil, nil, fmt.Errorf("%s: %w", msg, err)
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

	for k := range instantiatedPatches {
		instantiatedPatch, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
			clusterSummary, requestor, instantiatedPatches[k].Patch, mgmtResources, logger)
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

	// If Reloader changes, Reloader needs to be deployed or undeployed
	// So consider it in the hash
	config += fmt.Sprintf("%v", clusterProfileSpec.Reloader)

	// If Tier changes, conflicts might be resolved differently
	// So consider it in the hash
	config += fmt.Sprintf("%d", clusterProfileSpec.Tier)
	config += fmt.Sprintf("%t", clusterProfileSpec.ContinueOnConflict)

	if clusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {
		// Use the version. This will cause drift-detection, Sveltos CRDs
		// to be redeployed on upgrade
		config += getVersion()
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
