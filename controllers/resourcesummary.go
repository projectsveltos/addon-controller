/*
Copyright 2022-23. projectsveltos.io. All rights reserved.
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
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	driftdetection "github.com/projectsveltos/addon-controller/pkg/drift-detection"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	"github.com/projectsveltos/libsveltos/lib/logsettings"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/patcher"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

const (
	projectsveltos = "projectsveltos"
	deploymentKind = "Deployment"

	driftDetectionClusterNamespaceLabel = "cluster-namespace"
	driftDetectionClusterNameLabel      = "cluster-name"
	driftDetectionClusterTypeLabel      = "cluster-type"
	driftDetectionFeatureLabelKey       = "feature"
	driftDetectionFeatureLabelValue     = "drift-detection"
)

func getResourceSummaryNamespaceInManagedCluster() string {
	return projectsveltos
}

func getDriftDetectionNamespaceInMgmtCluster() string {
	return projectsveltos
}

func getResourceSummaryNameInManagedCluster(clusterSummaryNamespace, clusterSummaryName string) string {
	return fmt.Sprintf("%s--%s", clusterSummaryNamespace, clusterSummaryName)
}

func getResourceSummaryNameInManagemntCluster(clusterSummaryName string) string {
	return clusterSummaryName
}

func deployDriftDetectionManagerInCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant string, clusterType libsveltosv1beta1.ClusterType,
	startInMgmtCluster bool, logger logr.Logger) error {

	logger = logger.WithValues("clustersummary", applicant)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))
	logger.V(logs.LogDebug).Info("deploy drift detection manager: do not send updates mode")

	patches, err := getDriftDetectionManagerPatches(ctx, c, logger)
	if err != nil {
		return err
	}

	err = deployDriftDetectionCRDs(ctx, clusterNamespace, clusterName, clusterType,
		startInMgmtCluster, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("Deploying drift-detection-manager")
	// Deploy DriftDetectionManager
	if startInMgmtCluster {
		restConfig := getManagementClusterConfig()
		return deployDriftDetectionManagerInManagementCluster(ctx, restConfig, clusterNamespace,
			clusterName, "do-not-send-updates", clusterType, patches, logger)
	}

	return deployDriftDetectionManagerInManagedCluster(ctx, clusterNamespace,
		clusterName, "do-not-send-updates", clusterType, patches, logger)
}

func deployResourceSummaryInCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant string, clusterType libsveltosv1beta1.ClusterType,
	resources []libsveltosv1beta1.Resource, kustomizeResources []libsveltosv1beta1.Resource,
	helmResources []libsveltosv1beta1.HelmResources, driftExclusions []configv1beta1.DriftExclusion,
	logger logr.Logger) error {

	logger = logger.WithValues("clustersummary", applicant)
	logger.V(logs.LogDebug).Info("deploy resourcesummary")

	clusterClient, err := getResourceSummaryClient(ctx, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	resourceSummaryNameInfo := getResourceSummaryNameInfo(clusterNamespace, applicant)

	lbls := map[string]string{
		sveltos_upgrade.ClusterNameLabel: clusterName,
		sveltos_upgrade.ClusterTypeLabel: strings.ToLower(string(clusterType)),
	}

	annotations := map[string]string{
		libsveltosv1beta1.ClusterSummaryNameAnnotation:      applicant,
		libsveltosv1beta1.ClusterSummaryNamespaceAnnotation: clusterNamespace,
	}

	// Deploy ResourceSummary instance
	err = deployResourceSummaryInstance(ctx, clusterClient, resources, kustomizeResources, helmResources,
		resourceSummaryNameInfo.Namespace, resourceSummaryNameInfo.Name, lbls, annotations, driftExclusions, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("successuflly deployed resourceSummary CRD and instance")
	return nil
}

func deployDriftDetectionCRDs(ctx context.Context, clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, startInMgmtCluster bool, logger logr.Logger) error {

	// CRDs must be deployed alongside the agent. Since the management cluster already contains these CRDs,
	// this operation is a no-op if the agent is deployed there.
	if startInMgmtCluster {
		return nil
	}

	var err error
	cacheMgr := clustercache.GetManager()
	remoteConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, getManagementClusterClient(), clusterNamespace,
		clusterName, "", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get cluster rest config")
		return err
	}

	logger.V(logs.LogDebug).Info("deploy debuggingConfiguration CRD")
	// Deploy DebuggingConfiguration CRD
	err = deployDebuggingConfigurationCRD(ctx, remoteConfig, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("deploy resourceSummary CRD")
	// Deploy ResourceSummary CRD
	err = deployResourceSummaryCRD(ctx, remoteConfig, logger)
	if err != nil {
		return err
	}

	return nil
}

// deployDebuggingConfigurationCRD deploys DebuggingConfiguration CRD in remote cluster
func deployDebuggingConfigurationCRD(ctx context.Context, remoteRestConfig *rest.Config,
	logger logr.Logger) error {

	u, err := k8s_utils.GetUnstructured(crd.GetDebuggingConfigurationCRDYAML())
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get DebuggingConfiguration CRD unstructured: %v", err))
		return err
	}

	dr, err := k8s_utils.GetDynamicResourceInterface(remoteRestConfig, u.GroupVersionKind(), "")
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
		return err
	}

	options := metav1.ApplyOptions{
		FieldManager: "application/apply-patch",
	}
	_, err = dr.Apply(ctx, u.GetName(), u, options)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to apply DebuggingConfiguration CRD: %v", err))
		return err
	}

	return nil
}

// deployResourceSummaryCRD deploys ResourceSummary CRD in remote cluster
func deployResourceSummaryCRD(ctx context.Context, remoteRestConfig *rest.Config,
	logger logr.Logger) error {

	rsCRD, err := k8s_utils.GetUnstructured(crd.GetResourceSummaryCRDYAML())
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get ResourceSummary CRD unstructured: %v", err))
		return err
	}

	dr, err := k8s_utils.GetDynamicResourceInterface(remoteRestConfig, rsCRD.GroupVersionKind(), "")
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
		return err
	}

	options := metav1.ApplyOptions{
		FieldManager: "application/apply-patch",
	}
	_, err = dr.Apply(ctx, rsCRD.GetName(), rsCRD, options)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to apply ResourceSummary CRD: %v", err))
		return err
	}

	return nil
}

func prepareDriftDetectionManagerYAML(driftDetectionManagerYAML, clusterNamespace, clusterName, mode string,
	clusterType libsveltosv1beta1.ClusterType) string {

	if mode != "do-not-send-updates" {
		driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "do-not-send-updates", "send-updates")
	}

	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-namespace=", fmt.Sprintf("cluster-namespace=%s", clusterNamespace))
	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-name=", fmt.Sprintf("cluster-name=%s", clusterName))
	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-type=", fmt.Sprintf("cluster-type=%s", clusterType))
	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "v=5", "v=0")

	registry := getDriftDetectionRegistry()
	if registry != "" {
		driftDetectionManagerYAML = replaceRegistry(driftDetectionManagerYAML, registry)
	}

	return driftDetectionManagerYAML
}

func replaceRegistry(agentYAML, registry string) string {
	oldRegistry := "docker.io"
	return strings.Replace(agentYAML, oldRegistry, registry, 1)
}

// deployDriftDetectionManagerInManagedCluster deploys the drift-detection-manager component within the managed cluster.
func deployDriftDetectionManagerInManagedCluster(ctx context.Context, clusterNamespace, clusterName, mode string,
	clusterType libsveltosv1beta1.ClusterType, patches []libsveltosv1beta1.Patch, logger logr.Logger) error {

	// Sveltos resources are deployed using cluster-admin role.
	cacheMgr := clustercache.GetManager()
	remoteRestConfig, err := cacheMgr.GetKubernetesRestConfig(ctx, getManagementClusterClient(),
		clusterNamespace, clusterName, "", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get cluster rest config")
		return err
	}

	logger.V(logs.LogDebug).Info("deploy drift-detection-manager in managed cluster")
	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerYAML())

	driftDetectionManagerYAML = prepareDriftDetectionManagerYAML(driftDetectionManagerYAML, clusterNamespace,
		clusterName, mode, clusterType)

	return deployDriftDetectionManagerResources(ctx, remoteRestConfig, driftDetectionManagerYAML, nil, patches, logger)
}

// deployDriftDetectionManagerInManagementCluster deploys drift-detection-manager in the management cluster
// When deploying drift-detection-manager in the management cluster, there is one Deployment instance
// of drift-detection-manager per cluster.
// Those instances are all running in the "projectsveltos" namespace.
func deployDriftDetectionManagerInManagementCluster(ctx context.Context, restConfig *rest.Config,
	clusterNamespace, clusterName, mode string, clusterType libsveltosv1beta1.ClusterType,
	patches []libsveltosv1beta1.Patch, logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("deploy drift-detection-manager in management cluster")
	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerInMgmtClusterYAML())

	driftDetectionManagerYAML = prepareDriftDetectionManagerYAML(driftDetectionManagerYAML, clusterNamespace,
		clusterName, mode, clusterType)

	// Following labels are added on the objects representing the drift-detection-manager
	// for this cluster.
	lbls := getDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)

	name, err := getDriftDetectionManagerDeploymentName(ctx, restConfig, lbls)
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get name for drift-detection-manager deployment: %v", err))
		return err
	}

	driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "$NAME", name)
	return deployDriftDetectionManagerResources(ctx, restConfig, driftDetectionManagerYAML, lbls, patches, logger)
}

// deployDriftDetectionManagerResources handles drift-detection-component deployment.
// The restConfig parameter indicates whether to deploy to the management or managed cluster.
func deployDriftDetectionManagerResources(ctx context.Context, restConfig *rest.Config,
	driftDetectionManagerYAML string, lbls map[string]string, patches []libsveltosv1beta1.Patch,
	logger logr.Logger) error {

	elements, err := customSplit(driftDetectionManagerYAML)
	if err != nil {
		return err
	}
	for i := range elements {
		policy, err := k8s_utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse drift detection manager yaml: %v", err))
			return err
		}

		if lbls != nil {
			// Add extra labels
			currentLabels := policy.GetLabels()
			if currentLabels == nil {
				currentLabels = make(map[string]string)
			}
			for k := range lbls {
				currentLabels[k] = lbls[k]
			}
			policy.SetLabels(currentLabels)

			if policy.GetKind() == deploymentKind {
				policy, err = addTemplateSpecLabels(policy, lbls)
				if err != nil {
					logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to set deployment spec.template.labels: %v", err))
					return err
				}
			}
		}

		var referencedUnstructured []*unstructured.Unstructured
		if len(patches) > 0 {
			p := &patcher.CustomPatchPostRenderer{Patches: patches}
			referencedUnstructured, err = p.RunUnstructured(
				[]*unstructured.Unstructured{policy},
			)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to patch drift-detection-manager: %v", err))
				return err
			}
		} else {
			referencedUnstructured = append(referencedUnstructured, policy)
		}

		err = deployDriftDetectionManagerPatchedResources(ctx, restConfig, referencedUnstructured, logger)
		if err != nil {
			return err
		}
	}

	return nil
}

func deployDriftDetectionManagerPatchedResources(ctx context.Context, restConfig *rest.Config,
	referencedUnstructured []*unstructured.Unstructured, logger logr.Logger) error {

	for i := range referencedUnstructured {
		policy := referencedUnstructured[i]
		dr, err := k8s_utils.GetDynamicResourceInterface(restConfig, policy.GroupVersionKind(), policy.GetNamespace())
		if err != nil {
			logger.V(logsettings.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
			return err
		}

		options := metav1.ApplyOptions{
			FieldManager: "application/apply-patch",
		}

		_, err = dr.Apply(ctx, policy.GetName(), policy, options)
		if err != nil {
			logger.V(logsettings.LogInfo).Info(fmt.Sprintf("failed to apply policy Kind: %s Name: %s: %v",
				policy.GetKind(), policy.GetName(), err))
			return err
		}
	}

	return nil
}

func deployResourceSummaryInstance(ctx context.Context, clusterClient client.Client,
	resources []libsveltosv1beta1.Resource, kustomizeResources []libsveltosv1beta1.Resource,
	helmResources []libsveltosv1beta1.HelmResources, namespace, name string,
	lbls, annotations map[string]string, driftExclusions []configv1beta1.DriftExclusion, logger logr.Logger,
) error {

	logger.V(logs.LogDebug).Info("deploy resourceSummary instance")

	patches := transformDriftExclusionsToPatches(driftExclusions)

	currentResourceSummary := &libsveltosv1beta1.ResourceSummary{}
	err := clusterClient.Get(ctx,
		types.NamespacedName{Namespace: namespace, Name: name},
		currentResourceSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logsettings.LogDebug).Info("resourceSummary instance not present. creating it.")
			toDeployResourceSummary := &libsveltosv1beta1.ResourceSummary{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Namespace:   namespace,
					Labels:      lbls,
					Annotations: annotations,
				},
			}
			if resources != nil {
				toDeployResourceSummary.Spec.Resources = resources
			}
			if kustomizeResources != nil {
				toDeployResourceSummary.Spec.KustomizeResources = kustomizeResources
			}
			if helmResources != nil {
				toDeployResourceSummary.Spec.ChartResources = helmResources
			}
			toDeployResourceSummary.Spec.Patches = patches

			return clusterClient.Create(ctx, toDeployResourceSummary)
		}
		return err
	}

	if resources != nil {
		currentResourceSummary.Spec.Resources = resources
	}
	if kustomizeResources != nil {
		currentResourceSummary.Spec.KustomizeResources = kustomizeResources
	}
	if helmResources != nil {
		currentResourceSummary.Spec.ChartResources = helmResources
	}
	if currentResourceSummary.Labels == nil {
		currentResourceSummary.Labels = map[string]string{}
	}
	currentResourceSummary.Spec.Patches = patches
	currentResourceSummary.Labels = lbls
	currentResourceSummary.Annotations = annotations

	logger.V(logsettings.LogDebug).Info("resourceSummary instance already present. updating it.")
	return clusterClient.Update(ctx, currentResourceSummary)
}

// transformDriftExclusionPathsToPatches transforms a DriftExclusion instance to a Patch instance.
// Operation is always set to remove (the goal of a DriftExclusion is to not consider, so to remove, a path
// during configuration drift evaluation).
func transformDriftExclusionPathsToPatches(driftExclusion *configv1beta1.DriftExclusion) []libsveltosv1beta1.Patch {
	if len(driftExclusion.Paths) == 0 {
		return nil
	}

	patches := make([]libsveltosv1beta1.Patch, len(driftExclusion.Paths))
	for i := range driftExclusion.Paths {
		path := driftExclusion.Paths[i]
		// This patch is exclusively used for removing fields. The drift-detection-manager applies it upon detecting
		// changes to Sveltos-deployed resources. By removing the specified field, it prevents the field from being
		// considered during configuration drift evaluation.
		patches[i] = libsveltosv1beta1.Patch{
			Target: driftExclusion.Target,
			Patch: fmt.Sprintf(`- op: remove
  path: %s`, path),
		}
	}

	return patches
}

// transformDriftExclusionsToPatches transforms a slice of driftExclusion to a slice of Patch
// Operation on each Patch is always set to remove (the goal of a DriftExclusion is to not consider, so to remove,
// a path during configuration drift evaluation).
func transformDriftExclusionsToPatches(driftExclusions []configv1beta1.DriftExclusion) []libsveltosv1beta1.Patch {
	patches := []libsveltosv1beta1.Patch{}

	for i := range driftExclusions {
		item := &driftExclusions[i]
		tmpPatches := transformDriftExclusionPathsToPatches(item)
		patches = append(patches, tmpPatches...)
	}

	return patches
}

func unDeployResourceSummaryInstance(ctx context.Context, clusterNamespace, clusterName, applicant string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) error {

	// ResourceSummaries reside in the same cluster as the drift detection component.
	// This function dynamically selects the appropriate Kubernetes client:
	// - Management cluster's client if drift detection is deployed there.
	// - A managed cluster's client (obtained via clusterproxy) if drift detection is in a managed cluster.

	clusterClient, err := getResourceSummaryClient(ctx, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return err
	}

	resourceSummaryCRD := &apiextensionsv1.CustomResourceDefinition{}
	err = clusterClient.Get(ctx,
		types.NamespacedName{Name: "resourcesummaries.lib.projectsveltos.io"}, resourceSummaryCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logsettings.LogVerbose).Info("resourceSummary CRD not present.")
			return nil
		}
		return err
	}

	logger.V(logs.LogDebug).Info("unDeploy resourceSummary instance")

	resourceSummaryNameInfo := getResourceSummaryNameInfo(clusterNamespace, applicant)
	currentResourceSummary := &libsveltosv1beta1.ResourceSummary{}
	err = clusterClient.Get(ctx, resourceSummaryNameInfo, currentResourceSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logsettings.LogDebug).Info("resourceSummary instance not present.")
			return nil
		}

		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get resourceSummary. Err %s", err.Error()))
		return err
	}

	return clusterClient.Delete(ctx, currentResourceSummary)
}

// Generate unique name for drift-detection-manager

// getDriftDetectionManagerDeploymentName returns the name for a given drift-detection-manager deployment
// started in the management cluster for a given cluster.
func getDriftDetectionManagerDeploymentName(ctx context.Context, restConfig *rest.Config, lbls map[string]string,
) (name string, err error) {

	labelSelector := metav1.LabelSelector{
		MatchLabels: lbls,
	}

	// Create a new ListOptions object.
	listOptions := metav1.ListOptions{
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
	}

	// Create a new ClientSet using the RESTConfig.
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", err
	}

	// using client and a List would require permission at cluster level. So using clientset instead
	deployments, err := clientset.AppsV1().Deployments(getDriftDetectionNamespaceInMgmtCluster()).List(ctx, listOptions)
	if err != nil {
		return "", err
	}

	if len(deployments.Items) > 1 {
		// while rare this can happen. Multiple ClusterProfiles matching same cluster may concurrently query for
		// the same drift-detection-manager deployment, leading to redundant creation attempts if the deployment
		// doesn't exist.
		for i := range deployments.Items {
			// Ignore eventual error, since we are returning an error anyway
			_ = clientset.AppsV1().Deployments(getDriftDetectionNamespaceInMgmtCluster()).Delete(ctx,
				deployments.Items[i].Name, metav1.DeleteOptions{})
		}
		err = fmt.Errorf("more than one drift-detection deployment found")
		return name, err
	}

	objects := make([]client.Object, len(deployments.Items))
	for i := range deployments.Items {
		objects[i] = &deployments.Items[i]
	}

	return getInstantiatedObjectName(objects)
}

func getInstantiatedObjectName(objects []client.Object) (name string, err error) {
	switch len(objects) {
	case 0:
		// no cluster exist yet. Return random name.
		// If one clusterProfile with this name already exists,
		// a conflict will happen. On retry different name will
		// be picked
		prefix := "drift-detection-"
		const nameLength = 20
		name = prefix + util.RandomString(nameLength)
		err = nil
	case 1:
		name = objects[0].GetName()
		err = nil
	default:
		err = fmt.Errorf("more than one drift-detection deployment found")
	}
	return name, err
}

func getDriftDetectionManagerLabels(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) map[string]string {

	// Following labels are added on the objects representing the drift-detection-manager
	// for this cluster.
	lbls := make(map[string]string)
	lbls[driftDetectionClusterNamespaceLabel] = clusterNamespace
	lbls[driftDetectionClusterNameLabel] = clusterName
	lbls[driftDetectionClusterTypeLabel] = strings.ToLower(string(clusterType))
	lbls[driftDetectionFeatureLabelKey] = driftDetectionFeatureLabelValue
	return lbls
}

// removeDriftDetectionManagerFromManagementCluster removes the drift-detection-manager resources
// installed in the management cluster for the cluster: clusterType:clusterNamespace/clusterName
func removeDriftDetectionManagerFromManagementCluster(ctx context.Context,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) error {

	// Get YAML containing drift-detection-manager resources
	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerInMgmtClusterYAML())
	driftDetectionManagerYAML = prepareDriftDetectionManagerYAML(driftDetectionManagerYAML, clusterNamespace,
		clusterName, "", clusterType)

	// Addon-controller deploys drift-detection-manager resources for each cluster matching at least
	// one ClusterProfile with SyncMode set to ContinuousWithDriftDetection.
	lbls := getDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)
	name, err := getDriftDetectionManagerDeploymentName(ctx, getManagementClusterConfig(), lbls)
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get name for drift-detection-manager deployment: %v", err))
		return err
	}

	driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "$NAME", name)

	restConfig := getManagementClusterConfig()

	elements, err := customSplit(driftDetectionManagerYAML)
	if err != nil {
		return err
	}
	for i := range elements {
		policy, err := k8s_utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse drift detection manager yaml: %v", err))
			return err
		}

		dr, err := k8s_utils.GetDynamicResourceInterface(restConfig, policy.GroupVersionKind(), policy.GetNamespace())
		if err != nil {
			logger.V(logsettings.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
			return err
		}

		err = dr.Delete(ctx, policy.GetName(), metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to delete resource %s:%s/%s: %v",
				policy.GetKind(), policy.GetNamespace(), policy.GetName(), err))
		}
	}

	return nil
}

func getDriftDetectionManagerPatches(ctx context.Context, c client.Client,
	logger logr.Logger) ([]libsveltosv1beta1.Patch, error) {

	patches := make([]libsveltosv1beta1.Patch, 0)
	configMapName := getDriftDetectionConfigMap()
	configMap := &corev1.ConfigMap{}
	if configMapName != "" {
		err := c.Get(ctx,
			types.NamespacedName{Namespace: projectsveltos, Name: configMapName},
			configMap)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ConfigMap %s: %v",
				configMapName, err))
			return nil, err
		}
	}

	for k := range configMap.Data {
		// Only Deployment can be patched
		patch := libsveltosv1beta1.Patch{
			Patch: configMap.Data[k],
			Target: &libsveltosv1beta1.PatchSelector{
				Kind:  deploymentKind,
				Group: "apps",
			},
		}
		patches = append(patches, patch)
	}

	return patches, nil
}

func addTemplateSpecLabels(u *unstructured.Unstructured, lbls map[string]string) (*unstructured.Unstructured, error) {
	var deployment appsv1.Deployment
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), &deployment)
	if err != nil {
		return nil, err
	}

	if deployment.Spec.Template.Labels == nil {
		deployment.Spec.Template.Labels = map[string]string{}
	}
	for k := range lbls {
		deployment.Spec.Template.Labels[k] = lbls[k]
	}

	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deployment)
	if err != nil {
		return nil, err
	}

	var uDeployment unstructured.Unstructured
	uDeployment.SetUnstructuredContent(content)
	return &uDeployment, nil
}

// ResourceSummaries reside in the same cluster as the drift detection component.
// This function dynamically selects the appropriate Kubernetes client:
// - Management cluster's client if drift detection is deployed there.
// - A managed cluster's client (obtained via clusterproxy) if drift detection is in a managed cluster.
func getResourceSummaryClient(ctx context.Context, clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
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

// Determines the NamespacedName for the ResourceSummary based on the drift detection component's deployment location.
// If deployed in the management cluster, it uses the cluster's namespace and a management-specific naming scheme.
// If deployed in a managed cluster, it uses the projectsveltos namespace and a managed-cluster-specific naming scheme.
func getResourceSummaryNameInfo(clusterNamespace, clusterSummaryName string) types.NamespacedName {
	var resourceSummaryNamespace, resourceSummaryName string

	if getAgentInMgmtCluster() {
		resourceSummaryNamespace = clusterNamespace
		resourceSummaryName = getResourceSummaryNameInManagemntCluster(clusterSummaryName)
	} else {
		resourceSummaryNamespace = getResourceSummaryNamespaceInManagedCluster()
		resourceSummaryName = getResourceSummaryNameInManagedCluster(clusterNamespace, clusterSummaryName)
	}

	return types.NamespacedName{Namespace: resourceSummaryNamespace, Name: resourceSummaryName}
}
