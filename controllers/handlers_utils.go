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
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
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

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

const (
	separator                = "---\n"
	reasonLabel              = "projectsveltos.io/reason"
	clusterSummaryAnnotation = "projectsveltos.io/clustersummary"
)

func getClusterSummaryAnnotationValue(clusterSummary *configv1alpha1.ClusterSummary) string {
	prefix := getPrefix(clusterSummary.Spec.ClusterType)
	return fmt.Sprintf("%s-%s-%s", prefix, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName)
}

// createNamespace creates a namespace if it does not exist already
// No action in DryRun mode.
func createNamespace(ctx context.Context, clusterClient client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, namespaceName string) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
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
	destClient client.Client, configMap *corev1.ConfigMap, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []configv1alpha1.ResourceReport, err error) {

	reports, err =
		deployContent(ctx, deployingToMgmtCluster, destConfig, destClient, configMap, configMap.Data, clusterSummary,
			mgtmResources, logger)
	return
}

// deployContentOfSecret deploys policies contained in a Secret.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContentOfSecret(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, secret *corev1.Secret, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []configv1alpha1.ResourceReport, err error) {

	data := make(map[string]string)
	for key, value := range secret.Data {
		data[key] = string(value)
	}

	reports, err =
		deployContent(ctx, deployingToMgmtCluster, destConfig, destClient, secret, data, clusterSummary,
			mgtmResources, logger)
	return
}

// updateResource creates or updates a resource in a CAPI Cluster.
// No action in DryRun mode.
func updateResource(ctx context.Context, dr dynamic.ResourceInterface,
	clusterSummary *configv1alpha1.ClusterSummary, object *unstructured.Unstructured,
	logger logr.Logger) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	l := logger.WithValues("resourceNamespace", object.GetNamespace(),
		"resourceName", object.GetName(), "resourceGVK", object.GetObjectKind().GroupVersionKind())
	l.V(logs.LogDebug).Info("deploying policy")

	data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, object)
	if err != nil {
		return err
	}

	forceConflict := true
	options := metav1.PatchOptions{
		FieldManager: "application/apply-patch",
		Force:        &forceConflict,
	}
	_, err = dr.Patch(ctx, object.GetName(), types.ApplyPatchType, data, options)
	return err
}

func instantiateTemplate(referencedObject client.Object, logger logr.Logger) bool {
	annotations := referencedObject.GetAnnotations()
	if annotations != nil {
		if _, ok := annotations[libsveltosv1alpha1.PolicyTemplateAnnotation]; ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("referencedObject %s %s/%s is a template",
				referencedObject.GetObjectKind().GroupVersionKind().Kind, referencedObject.GetNamespace(), referencedObject.GetName()))
			return true
		}
	}

	return false
}

// deployContent deploys policies contained in a ConfigMap/Secret.
// data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployContent(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config, destClient client.Client,
	referencedObject client.Object, data map[string]string, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []configv1alpha1.ResourceReport, err error) {

	instantiateTemplate := instantiateTemplate(referencedObject, logger)
	resources, err := collectContent(ctx, clusterSummary, mgtmResources, data, instantiateTemplate, logger)
	if err != nil {
		return nil, err
	}

	ref := &corev1.ObjectReference{
		Kind:      referencedObject.GetObjectKind().GroupVersionKind().Kind,
		Namespace: referencedObject.GetNamespace(),
		Name:      referencedObject.GetName(),
	}

	err = validateUnstructred(ctx, deployingToMgmtCluster, resources, clusterSummary, logger)
	if err != nil {
		return nil, err
	}

	return deployUnstructured(ctx, deployingToMgmtCluster, destConfig, destClient, resources, ref,
		configv1alpha1.FeatureResources, clusterSummary, logger)
}

func validateUnstructred(ctx context.Context, deployingToMgmtCluster bool,
	resources []*unstructured.Unstructured, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	err := validateUnstructredAgainstLuaPolicies(ctx, deployingToMgmtCluster, resources,
		clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info("resources are not compliant with LUA policies")
		return err
	}

	return nil
}

// validateUnstructredAgainstLuaPolicies validates all resources against all lua policies currently
// enforced for the managed cluster where resources need to be applied.
// Lua policies can be written to validate single resources (each deployment replica must be at least 3)
// or combined resources (each deployment must be exposed by a service).
func validateUnstructredAgainstLuaPolicies(ctx context.Context, deployingToMgmtCluster bool,
	referencedUnstructured []*unstructured.Unstructured, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	// validations are only enforced when posting to managed clusters
	if deployingToMgmtCluster {
		return nil
	}

	var luaPolicies map[string][]byte
	luaPolicies, err := getLuaValidations(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		&clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("validating resource against %d lua policies",
		len(luaPolicies)))

	err = runLuaValidations(ctx, luaPolicies, referencedUnstructured, logger)
	if err != nil {
		return err
	}

	return nil
}

// deployUnstructured deploys referencedUnstructured objects.
// Returns an error if one occurred. Otherwise it returns a slice containing the name of
// the policies deployed in the form of kind.group:namespace:name for namespaced policies
// and kind.group::name for cluster wide policies.
func deployUnstructured(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, referencedUnstructured []*unstructured.Unstructured, referencedObject *corev1.ObjectReference,
	featureID configv1alpha1.FeatureID, clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger,
) (reports []configv1alpha1.ResourceReport, err error) {

	profile, err := configv1alpha1.GetProfileOwner(ctx, getManagementClusterClient(), clusterSummary)
	if err != nil {
		return nil, err
	}

	reports = make([]configv1alpha1.ResourceReport, 0)
	for i := range referencedUnstructured {
		policy := referencedUnstructured[i]

		logger.V(logs.LogDebug).Info(fmt.Sprintf("deploying resource %s %s/%s (deploy to management cluster: %v)",
			policy.GetKind(), policy.GetNamespace(), policy.GetName(), deployingToMgmtCluster))

		resource, policyHash := getResource(policy, referencedObject, featureID, logger)

		// If policy is namespaced, create namespace if not already existing
		err = createNamespace(ctx, destClient, clusterSummary, policy.GetNamespace())
		if err != nil {
			return nil, err
		}

		// If policy already exists, just get current version and update it by overridding
		// all metadata and spec.
		// If policy does not exist already, create it
		dr, err := utils.GetDynamicResourceInterface(destConfig, policy.GroupVersionKind(), policy.GetNamespace())
		if err != nil {
			return nil, err
		}

		var exist bool
		var currentHash string
		exist, currentHash, err = deployer.ValidateObjectForUpdate(ctx, dr, policy,
			referencedObject.Kind, referencedObject.Namespace, referencedObject.Name)
		if err != nil {
			var conflictErr *deployer.ConflictError
			ok := errors.As(err, &conflictErr)
			// In DryRun mode do not stop here, but report the conflict.
			if ok && clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
				conflictResourceReport := generateConflictResourceReport(ctx, dr, resource)
				reports = append(reports, *conflictResourceReport)
				continue
			}
			return nil, err
		}

		deployer.AddOwnerReference(policy, profile)

		if deployingToMgmtCluster {
			// When deploying resources in the management cluster, just setting ClusterProfile as OwnerReference is
			// not enough. We also need to track which ClusterSummary is creating the resource. Otherwise while
			// trying to clean stale resources those objects will be incorrectly removed.
			// An extra annotation is added here to indicate the clustersummary, so the managed cluster, this
			// resource was created for
			value := getClusterSummaryAnnotationValue(clusterSummary)
			addAnnotation(policy, clusterSummaryAnnotation, value)
		}

		err = updateResource(ctx, dr, clusterSummary, policy, logger)
		if err != nil {
			return nil, err
		}

		resource.LastAppliedTime = &metav1.Time{Time: time.Now()}

		if !exist {
			reports = append(reports,
				configv1alpha1.ResourceReport{Resource: *resource, Action: string(configv1alpha1.CreateResourceAction)})
		} else if policyHash != currentHash {
			reports = append(reports,
				configv1alpha1.ResourceReport{Resource: *resource, Action: string(configv1alpha1.UpdateResourceAction)})
		} else {
			reports = append(reports,
				configv1alpha1.ResourceReport{Resource: *resource, Action: string(configv1alpha1.NoResourceAction),
					Message: "Object already deployed. And policy referenced by ClusterProfile has not changed since last deployment."})
		}
	}

	return reports, nil
}

// getResource returns sveltos Resource and the resource hash hash
func getResource(policy *unstructured.Unstructured, referencedObject *corev1.ObjectReference,
	featureID configv1alpha1.FeatureID, logger logr.Logger) (resource *configv1alpha1.Resource, policyHash string) {

	resource = &configv1alpha1.Resource{
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

// collectContent collect policies contained in a ConfigMap/Secret.
// ConfigMap/Secret Data might have one or more keys. Each key might contain a single policy
// or multiple policies separated by '---'
// Returns an error if one occurred. Otherwise it returns a slice of *unstructured.Unstructured.
func collectContent(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, data map[string]string,
	instantiateTemplate bool, logger logr.Logger,
) ([]*unstructured.Unstructured, error) {

	policies := make([]*unstructured.Unstructured, 0)

	for k := range data {
		elements := strings.Split(data[k], separator)
		for i := range elements {
			section := removeCommentsAndEmptyLines(elements[i])
			if section == "" {
				continue
			}

			section = elements[i]

			if instantiateTemplate {
				instance, err := instantiateTemplateValues(ctx, getManagementClusterConfig(), getManagementClusterClient(),
					clusterSummary.Spec.ClusterType, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
					clusterSummary.GetName(), section, mgtmResources, logger)
				if err != nil {
					return nil, err
				}

				section = instance
			}

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
	policies := make([]*unstructured.Unstructured, 0)
	elements := strings.Split(string(section), separator)

	for i := range elements {
		section := removeCommentsAndEmptyLines(elements[i])
		if section == "" {
			continue
		}

		section = elements[i]

		policy, err := utils.GetUnstructured([]byte(section))
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", section))
			return nil, err
		}

		if policy == nil {
			logger.Error(err, fmt.Sprintf("failed to get policy from Data %.100s", section))
			return nil, fmt.Errorf("failed to get policy from Data %.100s", section)
		}

		policies = append(policies, policy)
	}

	return policies, nil
}

func getPolicyInfo(policy *configv1alpha1.Resource) string {
	return fmt.Sprintf("%s.%s:%s:%s",
		policy.Kind,
		policy.Group,
		policy.Namespace,
		policy.Name)
}

// getClusterSummaryAdmin returns the name of the admin that created the ClusterProfile
// instance owing this ClusterProfile instance
func getClusterSummaryAdmin(clusterSummary *configv1alpha1.ClusterSummary) (namespace, name string) {
	if clusterSummary.Labels == nil {
		return "", ""
	}

	namespace = clusterSummary.Labels[libsveltosv1alpha1.ServiceAccountNamespaceLabel]
	name = clusterSummary.Labels[libsveltosv1alpha1.ServiceAccountNameLabel]
	return
}

// getClusterSummaryAndClusterClient gets ClusterSummary and the client to access the associated
// CAPI/Sveltos Cluster.
// Returns an err if ClusterSummary or associated CAPI Cluster are marked for deletion, or if an
// error occurs while getting resources.
func getClusterSummaryAndClusterClient(ctx context.Context, clusterNamespace, clusterSummaryName string,
	c client.Client, logger logr.Logger) (*configv1alpha1.ClusterSummary, client.Client, error) {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
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

// getReferenceResourceNamespace returns the namespace to use for a referenced resource.
// If namespace is set on referencedResource, that namespace will be used.
// If namespace is not set, cluster namespace will be used
func getReferenceResourceNamespace(clusterNamespace, referencedResourceNamespace string) string {
	if referencedResourceNamespace != "" {
		return referencedResourceNamespace
	}

	return clusterNamespace
}

// collectReferencedObjects collects all referenced configMaps/secrets in control cluster
// local contains all configMaps/Secrets whose content need to be deployed locally (in the management cluster)
// remote contains all configMap/Secrets whose content need to be deployed remotely (in the managed cluster)
func collectReferencedObjects(ctx context.Context, controlClusterClient client.Client, clusterNamespace string,
	references []configv1alpha1.PolicyRef, logger logr.Logger) (local, remote []client.Object, err error) {

	local = make([]client.Object, 0)
	remote = make([]client.Object, 0)
	for i := range references {
		var object client.Object
		reference := &references[i]

		namespace := getReferenceResourceNamespace(clusterNamespace, references[i].Namespace)

		if reference.Kind == string(libsveltosv1alpha1.ConfigMapReferencedResourceKind) {
			object, err = getConfigMap(ctx, controlClusterClient,
				types.NamespacedName{Namespace: namespace, Name: reference.Name})
		} else {
			object, err = getSecret(ctx, controlClusterClient,
				types.NamespacedName{Namespace: namespace, Name: reference.Name})
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				msg := fmt.Sprintf("Referenced resource: %s %s/%s does not exist",
					reference.Kind, reference.Namespace, reference.Name)
				logger.V(logs.LogInfo).Info(msg)
				return nil, nil, &NonRetriableError{Message: msg}
			}
			return nil, nil, err
		}

		if reference.DeploymentType == configv1alpha1.DeploymentTypeLocal {
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
	clusterSummary *configv1alpha1.ClusterSummary, objectsToDeployLocally, objectsToDeployRemotely []client.Object,
	logger logr.Logger) (localReports, remoteReports []configv1alpha1.ResourceReport, err error) {

	remoteClient, err := client.New(remoteConfig, client.Options{})
	if err != nil {
		return nil, nil, err
	}

	var mgtmResources map[string]*unstructured.Unstructured
	mgtmResources, err = collectMgmtResources(ctx, clusterSummary)
	if err != nil {
		return nil, nil, err
	}

	var tmpResourceReports []configv1alpha1.ResourceReport

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
		mgtmResources, logger)
	localReports = append(localReports, tmpResourceReports...)
	if err != nil {
		return localReports, nil, err
	}

	// Deploy all resources that need to be deployed in the managed cluster
	tmpResourceReports, err = deployObjects(ctx, false, remoteClient, remoteConfig, objectsToDeployRemotely, clusterSummary,
		mgtmResources, logger)
	remoteReports = append(remoteReports, tmpResourceReports...)
	if err != nil {
		return localReports, remoteReports, err
	}

	return localReports, remoteReports, nil
}

// deployObjects deploys content of referencedObjects
func deployObjects(ctx context.Context, deployingToMgmtCluster bool, destClient client.Client, destConfig *rest.Config,
	referencedObjects []client.Object, clusterSummary *configv1alpha1.ClusterSummary,
	mgtmResources map[string]*unstructured.Unstructured, logger logr.Logger,
) (reports []configv1alpha1.ResourceReport, err error) {

	for i := range referencedObjects {
		var tmpResourceReports []configv1alpha1.ResourceReport
		if referencedObjects[i].GetObjectKind().GroupVersionKind().Kind == string(libsveltosv1alpha1.ConfigMapReferencedResourceKind) {
			configMap := referencedObjects[i].(*corev1.ConfigMap)
			l := logger.WithValues("configMapNamespace", configMap.Namespace, "configMapName", configMap.Name)
			l.V(logs.LogDebug).Info("deploying ConfigMap content")
			tmpResourceReports, err =
				deployContentOfConfigMap(ctx, deployingToMgmtCluster, destConfig, destClient, configMap,
					clusterSummary, mgtmResources, l)
		} else {
			secret := referencedObjects[i].(*corev1.Secret)
			l := logger.WithValues("secretNamespace", secret.Namespace, "secretName", secret.Name)
			l.V(logs.LogDebug).Info("deploying Secret content")
			tmpResourceReports, err =
				deployContentOfSecret(ctx, deployingToMgmtCluster, destConfig, destClient, secret,
					clusterSummary, mgtmResources, l)
		}

		if err != nil {
			return nil, err
		}
		reports = append(reports, tmpResourceReports...)
	}

	return reports, nil
}

func undeployStaleResources(ctx context.Context, isMgmtCluster bool,
	remoteConfig *rest.Config, remoteClient client.Client, featureID configv1alpha1.FeatureID,
	clusterSummary *configv1alpha1.ClusterSummary, deployedGVKs []schema.GroupVersionKind,
	currentPolicies map[string]configv1alpha1.Resource, logger logr.Logger) ([]configv1alpha1.ResourceReport, error) {

	logger.V(logs.LogDebug).Info("removing stale resources")

	profile, err := configv1alpha1.GetProfileOwner(ctx, getManagementClusterClient(), clusterSummary)
	if err != nil {
		return nil, err
	}

	undeployed := make([]configv1alpha1.ResourceReport, 0)

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
		mapping, err := mapper.RESTMapping(deployedGVKs[i].GroupKind(), deployedGVKs[i].Version)
		if err != nil {
			// if CRDs does not exist anymore, ignore error. No instances of
			// such CRD can be left anyway.
			if meta.IsNoMatchError(err) {
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
	profile client.Object, clusterSummary *configv1alpha1.ClusterSummary, r unstructured.Unstructured,
	currentPolicies map[string]configv1alpha1.Resource, logger logr.Logger) (*configv1alpha1.ResourceReport, error) {

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

	var resourceReport *configv1alpha1.ResourceReport = nil
	// If in DryRun do not withdrawn any policy.
	// If this ClusterSummary is the only OwnerReference and it is not deploying this policy anymore,
	// policy would be withdrawn
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		if canDelete(&r, currentPolicies) && deployer.IsOnlyOwnerReference(&r, profile) &&
			!isLeavePolicies(clusterSummary, logger) {

			resourceReport = &configv1alpha1.ResourceReport{
				Resource: configv1alpha1.Resource{
					Kind: r.GetObjectKind().GroupVersionKind().Kind, Namespace: r.GetNamespace(), Name: r.GetName(),
					Group: r.GroupVersionKind().Group, Version: r.GroupVersionKind().Version,
				},
				Action: string(configv1alpha1.DeleteResourceAction),
			}
		}
	} else {
		logger.V(logs.LogVerbose).Info(fmt.Sprintf("remove owner reference %s/%s", r.GetNamespace(), r.GetName()))

		deployer.RemoveOwnerReference(&r, profile)

		if len(r.GetOwnerReferences()) != 0 {
			// Other ClusterSummary are still deploying this very same policy
			return nil, nil
		}

		if canDelete(&r, currentPolicies) {
			err := handleResourceDelete(ctx, remoteClient, &r, clusterSummary, logger)
			if err != nil {
				return nil, err
			}
		}
	}

	return resourceReport, nil
}

func handleResourceDelete(ctx context.Context, remoteClient client.Client, policy client.Object,
	clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) error {

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
func canDelete(policy client.Object, currentReferencedPolicies map[string]configv1alpha1.Resource) bool {
	name := getPolicyInfo(&configv1alpha1.Resource{
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
func isLeavePolicies(clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) bool {
	if !clusterSummary.DeletionTimestamp.IsZero() &&
		clusterSummary.Spec.ClusterProfileSpec.StopMatchingBehavior == configv1alpha1.LeavePolicies {

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

func getDeployedGroupVersionKinds(clusterSummary *configv1alpha1.ClusterSummary,
	featureID configv1alpha1.FeatureID) []schema.GroupVersionKind {

	gvks := make([]schema.GroupVersionKind, 0)
	for i := range clusterSummary.Status.FeatureSummaries {
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == featureID {
			for j := range clusterSummary.Status.FeatureSummaries[i].DeployedGroupVersionKind {
				gvk, _ := schema.ParseKindArg(clusterSummary.Status.FeatureSummaries[i].DeployedGroupVersionKind[j])
				gvks = append(gvks, *gvk)
			}
		}
	}

	return gvks
}

// No action in DryRun mode.
func updateClusterConfiguration(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	profileOwnerRef *metav1.OwnerReference,
	featureID configv1alpha1.FeatureID,
	policyDeployed []configv1alpha1.Resource,
	chartDeployed []configv1alpha1.Chart) error {

	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get ClusterConfiguration for CAPI Cluster
		clusterConfiguration := &configv1alpha1.ClusterConfiguration{}
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
		index, err = configv1alpha1.GetClusterConfigurationSectionIndex(clusterConfiguration, profileOwnerRef.Kind,
			profileOwnerRef.Name)
		if err != nil {
			return err
		}

		if profileOwnerRef.Kind == configv1alpha1.ClusterProfileKind {
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
	clusterConfiguration *configv1alpha1.ClusterConfiguration, index int,
	featureID configv1alpha1.FeatureID, policyDeployed []configv1alpha1.Resource,
	chartDeployed []configv1alpha1.Chart) error {

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
			profileResources.Features = make([]configv1alpha1.Feature, 0)
		}
		profileResources.Features = append(
			profileResources.Features,
			configv1alpha1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
		)
	}

	clusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences, *profileOwnerRef)

	return c.Status().Update(ctx, clusterConfiguration)
}

func updateProfileResources(ctx context.Context, c client.Client, profileOwnerRef *metav1.OwnerReference,
	clusterConfiguration *configv1alpha1.ClusterConfiguration, index int,
	featureID configv1alpha1.FeatureID, policyDeployed []configv1alpha1.Resource,
	chartDeployed []configv1alpha1.Chart) error {

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
			profileResources.Features = make([]configv1alpha1.Feature, 0)
		}
		profileResources.Features = append(
			profileResources.Features,
			configv1alpha1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
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
	configMapKey := client.ObjectKey{
		Namespace: configmapName.Namespace,
		Name:      configmapName.Name,
	}
	if err := c.Get(ctx, configMapKey, configMap); err != nil {
		return nil, err
	}

	return configMap, nil
}

// getSecret retrieves any Secret from the given secret name and namespace.
func getSecret(ctx context.Context, c client.Client, secretName types.NamespacedName) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: secretName.Namespace,
		Name:      secretName.Name,
	}
	if err := c.Get(ctx, secretKey, secret); err != nil {
		return nil, err
	}

	if secret.Type != libsveltosv1alpha1.ClusterProfileSecretType {
		return nil, libsveltosv1alpha1.ErrSecretTypeNotSupported
	}

	return secret, nil
}

func generateConflictResourceReport(ctx context.Context, dr dynamic.ResourceInterface,
	resource *configv1alpha1.Resource) *configv1alpha1.ResourceReport {

	conflictReport := &configv1alpha1.ResourceReport{
		Resource: *resource,
		Action:   string(configv1alpha1.ConflictResourceAction),
	}
	message, err := deployer.GetOwnerMessage(ctx, dr, resource.Name)
	if err == nil {
		conflictReport.Message = message
	}
	return conflictReport
}

func updateDeployedGroupVersionKind(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary,
	featureID configv1alpha1.FeatureID, localResourceReports, remoteResourceReports []configv1alpha1.ResourceReport,
	logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("update status with deployed GroupVersionKinds")
	reports := localResourceReports
	reports = append(reports, remoteResourceReports...)

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
	setDeployedGroupVersionKind(clusterSummary, gvks, featureID)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return getManagementClusterClient().Status().Update(ctx, clusterSummary)
	})
	return err
}

// setDeployedGroupVersionKind sets the list of deployed GroupVersionKinds
func setDeployedGroupVersionKind(clusterSummary *configv1alpha1.ClusterSummary, gvks []schema.GroupVersionKind,
	featureID configv1alpha1.FeatureID) {

	for i := range clusterSummary.Status.FeatureSummaries {
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == featureID {
			setDeployedGroupVersionKindField(&clusterSummary.Status.FeatureSummaries[i], gvks)
			return
		}
	}

	if clusterSummary.Status.FeatureSummaries == nil {
		clusterSummary.Status.FeatureSummaries = make([]configv1alpha1.FeatureSummary, 0)
	}

	clusterSummary.Status.FeatureSummaries = append(
		clusterSummary.Status.FeatureSummaries,
		configv1alpha1.FeatureSummary{
			FeatureID: featureID,
		},
	)

	for i := range clusterSummary.Status.FeatureSummaries {
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == featureID {
			setDeployedGroupVersionKindField(&clusterSummary.Status.FeatureSummaries[i], gvks)
			return
		}
	}
}

func setDeployedGroupVersionKindField(fs *configv1alpha1.FeatureSummary, gvks []schema.GroupVersionKind) {
	tmp := make([]string, 0)

	// Preserve the order
	current := make(map[string]bool)
	for _, k := range fs.DeployedGroupVersionKind {
		current[k] = true
		tmp = append(tmp, k)
	}

	for i := range gvks {
		key := fmt.Sprintf("%s.%s.%s", gvks[i].Kind, gvks[i].Version, gvks[i].Group)
		if _, ok := current[key]; !ok {
			current[key] = true
			tmp = append(tmp, key)
		}
	}

	fs.DeployedGroupVersionKind = tmp
}

// getRestConfig returns restConfig to access remote cluster
func getRestConfig(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) (*rest.Config, logr.Logger, error) {

	clusterNamespace := clusterSummary.Spec.ClusterNamespace
	clusterName := clusterSummary.Spec.ClusterName

	adminNamespace, adminName := getClusterSummaryAdmin(clusterSummary)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", clusterNamespace, clusterName)).
		WithValues("clusterSummary", clusterSummary.Name).WithValues("admin", fmt.Sprintf("%s/%s", adminNamespace, adminName))

	logger.V(logs.LogDebug).Info("get remote restConfig")
	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, c, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return nil, logger, err
	}

	return remoteRestConfig, logger, nil
}
