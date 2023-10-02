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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	driftdetection "github.com/projectsveltos/addon-controller/pkg/drift-detection"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/logsettings"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

const (
	projectsveltos = "projectsveltos"
)

func getResourceSummaryNamespace() string {
	return projectsveltos
}

func getDriftDetectionNamespaceInMgmtCluster() string {
	return projectsveltos
}

func getResourceSummaryName(clusterSummaryNamespace, clusterSummaryName string) string {
	return fmt.Sprintf("%s--%s", clusterSummaryNamespace, clusterSummaryName)
}

func deployDriftDetectionManagerInCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant string, clusterType libsveltosv1alpha1.ClusterType,
	startInMgmtCluster bool, logger logr.Logger) error {

	logger = logger.WithValues("clustersummary", applicant)
	logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterType, clusterNamespace, clusterName))
	logger.V(logs.LogDebug).Info("deploy drift detection manager: do not send updates mode")

	// Sveltos resources are deployed using cluster-admin role.
	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, c, clusterNamespace,
		clusterName, "", "", clusterType, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get cluster rest config")
		return err
	}

	err = deployDriftDetectionCRDs(ctx, remoteRestConfig, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("Deploying drift-detection-manager")
	// Deploy DriftDetectionManager
	if startInMgmtCluster {
		restConfig := getManagementClusterConfig()
		return deployDriftDetectionManagerInManagementCluster(ctx, restConfig, clusterNamespace,
			clusterName, "do-not-send-reports", clusterType, logger)
	}

	return deployDriftDetectionManager(ctx, remoteRestConfig, clusterNamespace,
		clusterName, "do-not-send-reports", clusterType, logger)
}

func deployResourceSummaryInCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant string, clusterType libsveltosv1alpha1.ClusterType,
	resources []libsveltosv1alpha1.Resource, kustomizeResources []libsveltosv1alpha1.Resource,
	helmResources []libsveltosv1alpha1.HelmResources, logger logr.Logger) error {

	logger = logger.WithValues("clustersummary", applicant)
	logger.V(logs.LogDebug).Info("deploy resourcesummary")

	// ResourceSummary is a Sveltos resource created in managed clusters.
	// Sveltos resources are always created using cluster-admin so that admin does not need to be
	// given such permissions.
	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterNamespace, clusterName, "", "",
		clusterType, logger)
	if err != nil {
		return err
	}

	// Deploy ResourceSummary instance
	err = deployResourceSummaryInstance(ctx, remoteClient, resources, kustomizeResources,
		helmResources, clusterNamespace, applicant, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("successuflly deployed resourceSummary CRD and instance")
	return nil
}

func deployDriftDetectionCRDs(ctx context.Context, remoteRestConfig *rest.Config, logger logr.Logger) error {
	logger.V(logs.LogDebug).Info("deploy debuggingConfiguration CRD")
	// Deploy DebuggingConfiguration CRD
	err := deployDebuggingConfigurationCRD(ctx, remoteRestConfig, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("deploy resourceSummary CRD")
	// Deploy ResourceSummary CRD
	err = deployResourceSummaryCRD(ctx, remoteRestConfig, logger)
	if err != nil {
		return err
	}

	return nil
}

// deployDebuggingConfigurationCRD deploys DebuggingConfiguration CRD in remote cluster
func deployDebuggingConfigurationCRD(ctx context.Context, remoteRestConfig *rest.Config,
	logger logr.Logger) error {

	u, err := utils.GetUnstructured(crd.GetDebuggingConfigurationCRDYAML())
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get DebuggingConfiguration CRD unstructured: %v", err))
		return err
	}

	dr, err := utils.GetDynamicResourceInterface(remoteRestConfig, u.GroupVersionKind(), "")
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

	rsCRD, err := utils.GetUnstructured(crd.GetResourceSummaryCRDYAML())
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get ResourceSummary CRD unstructured: %v", err))
		return err
	}

	dr, err := utils.GetDynamicResourceInterface(remoteRestConfig, rsCRD.GroupVersionKind(), "")
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
	clusterType libsveltosv1alpha1.ClusterType) string {

	if mode != "do-not-send-updates" {
		driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "do-not-send-updates", "send-updates")
	}

	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-namespace=", fmt.Sprintf("cluster-namespace=%s", clusterNamespace))
	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-name=", fmt.Sprintf("cluster-name=%s", clusterName))
	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-type=", fmt.Sprintf("cluster-type=%s", clusterType))

	return driftDetectionManagerYAML
}

// deployDriftDetectionManager deploys drift-detection-manager in the managed cluster
func deployDriftDetectionManager(ctx context.Context, remoteRestConfig *rest.Config,
	clusterNamespace, clusterName, mode string, clusterType libsveltosv1alpha1.ClusterType,
	logger logr.Logger) error {

	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerYAML())

	driftDetectionManagerYAML = prepareDriftDetectionManagerYAML(driftDetectionManagerYAML, clusterNamespace,
		clusterName, mode, clusterType)

	return deployDriftDetectionManagerResources(ctx, remoteRestConfig, driftDetectionManagerYAML, nil, logger)
}

// deployDriftDetectionManagerInManagementCluster deploys drift-detection-manager in the management cluster
// When deploying drift-detection-manager in the management cluster, there is one Deployment instance
// of drift-detection-manager per cluster.
// Those instances are all running in the "projectsveltos" namespace.
func deployDriftDetectionManagerInManagementCluster(ctx context.Context, restConfig *rest.Config,
	clusterNamespace, clusterName, mode string, clusterType libsveltosv1alpha1.ClusterType,
	logger logr.Logger) error {

	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerInMgmtClusterYAML())

	driftDetectionManagerYAML = prepareDriftDetectionManagerYAML(driftDetectionManagerYAML, clusterNamespace,
		clusterName, mode, clusterType)

	c := getManagementClusterClient()
	// Following labels are added on the objects representing the drift-detection-manager
	// for this cluster.
	labels := getDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)

	name, create, err := getDriftDetectionManagerDeploymentName(ctx, c, labels)
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get name for drift-detection-manager deployment: %v", err))
		return err
	}

	if create {
		driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "$NAME", name)
		return deployDriftDetectionManagerResources(ctx, restConfig, driftDetectionManagerYAML, labels, logger)
	}

	return nil
}

func deployDriftDetectionManagerResources(ctx context.Context, restConfig *rest.Config,
	driftDetectionManagerYAML string, labels map[string]string, logger logr.Logger) error {

	const separator = "---"
	elements := strings.Split(driftDetectionManagerYAML, separator)
	for i := range elements {
		policy, err := utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse drift detection manager yaml: %v", err))
			return err
		}

		if labels != nil {
			// Add extra labels
			currentLabels := policy.GetLabels()
			if currentLabels == nil {
				currentLabels = make(map[string]string)
			}
			for k := range labels {
				currentLabels[k] = labels[k]
			}
			policy.SetLabels(currentLabels)
		}

		dr, err := utils.GetDynamicResourceInterface(restConfig, policy.GroupVersionKind(), policy.GetNamespace())
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

func deployResourceSummaryInstance(ctx context.Context, remoteClient client.Client,
	resources []libsveltosv1alpha1.Resource, kustomizeResources []libsveltosv1alpha1.Resource,
	helmResources []libsveltosv1alpha1.HelmResources, clusterNamespace, applicant string,
	logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("deploy resourceSummary instance")

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: getResourceSummaryNamespace(),
		},
	}
	err := remoteClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.V(logsettings.LogInfo).Info(fmt.Sprintf("failed to create namespace %s: %v", ns.Name, err))
		return err
	}

	currentResourceSummary := &libsveltosv1alpha1.ResourceSummary{}
	err = remoteClient.Get(ctx,
		types.NamespacedName{
			Namespace: getResourceSummaryNamespace(),
			Name:      getResourceSummaryName(clusterNamespace, applicant)},
		currentResourceSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logsettings.LogDebug).Info("resourceSummary instance not present. creating it.")
			toDeployResourceSummary := &libsveltosv1alpha1.ResourceSummary{
				ObjectMeta: metav1.ObjectMeta{
					Name:      getResourceSummaryName(clusterNamespace, applicant),
					Namespace: getResourceSummaryNamespace(),
					Labels: map[string]string{
						libsveltosv1alpha1.ClusterSummaryLabelName:      applicant,
						libsveltosv1alpha1.ClusterSummaryLabelNamespace: clusterNamespace,
					},
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

			return remoteClient.Create(ctx, toDeployResourceSummary)
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
	currentResourceSummary.Labels[libsveltosv1alpha1.ClusterSummaryLabelName] = applicant
	currentResourceSummary.Labels[libsveltosv1alpha1.ClusterSummaryLabelNamespace] = clusterNamespace

	logger.V(logsettings.LogDebug).Info("resourceSummary instance already present. updating it.")
	return remoteClient.Update(ctx, currentResourceSummary)
}

func unDeployResourceSummaryInstance(ctx context.Context, remoteClient client.Client,
	clusterNamespace, applicant string, logger logr.Logger) error {

	resourceSummaryCRD := &apiextensionsv1.CustomResourceDefinition{}
	err := remoteClient.Get(ctx,
		types.NamespacedName{Name: "resourcesummaries.lib.projectsveltos.io"}, resourceSummaryCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logsettings.LogDebug).Info("resourceSummary CRD not present.")
			return nil
		}
		return err
	}

	logger.V(logs.LogDebug).Info("unDeploy resourceSummary instance")
	currentResourceSummary := &libsveltosv1alpha1.ResourceSummary{}
	err = remoteClient.Get(ctx,
		types.NamespacedName{
			Namespace: getResourceSummaryNamespace(),
			Name:      getResourceSummaryName(clusterNamespace, applicant)},
		currentResourceSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logsettings.LogDebug).Info("resourceSummary instance not present.")
			return nil
		}

		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get resourceSummary. Err %s", err.Error()))
		return err
	}

	return remoteClient.Delete(ctx, currentResourceSummary)
}

// Generate unique name for drift-detection-manager

// getDriftDetectionManagerDeploymentName returns the name for a given drift-detection-manager deployment
// started in the management cluster for a given cluster.
func getDriftDetectionManagerDeploymentName(ctx context.Context, c client.Client, labels map[string]string,
) (name string, create bool, err error) {

	listOptions := []client.ListOption{
		client.MatchingLabels(labels),
		client.InNamespace(getDriftDetectionNamespaceInMgmtCluster()),
	}

	deployments := &appsv1.DeploymentList{}
	err = c.List(ctx, deployments, listOptions...)
	if err != nil {
		return
	}

	objects := make([]client.Object, len(deployments.Items))
	for i := range deployments.Items {
		objects[i] = &deployments.Items[i]
	}

	return getInstantiatedObjectName(objects)
}

func getInstantiatedObjectName(objects []client.Object) (name string, create bool, err error) {
	prefix := "drift-detection-"
	switch len(objects) {
	case 0:
		// no cluster exist yet. Return random name.
		// If one clusterProfile with this name already exists,
		// a conflict will happen. On retry different name will
		// be picked
		const nameLength = 20
		name = prefix + util.RandomString(nameLength)
		create = true
		err = nil
	case 1:
		name = objects[0].GetName()
		create = false
		err = nil
	default:
		err = fmt.Errorf("more than one resource")
	}
	return name, create, err
}

func getDriftDetectionManagerLabels(clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType) map[string]string {

	// Following labels are added on the objects representing the drift-detection-manager
	// for this cluster.
	labels := make(map[string]string)
	labels["cluster-namespace"] = clusterNamespace
	labels["cluster-name"] = clusterName
	labels["cluster-type"] = strings.ToLower(string(clusterType))
	return labels
}

// removeDriftDetectionManagerFromManagementCluster removes the drift-detection-manager resources
// installed in the management cluster for the cluster: clusterType:clusterNamespace/clusterName
func removeDriftDetectionManagerFromManagementCluster(ctx context.Context,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
	logger logr.Logger) error {

	// Get YAML containing drift-detection-manager resources
	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerInMgmtClusterYAML())
	driftDetectionManagerYAML = prepareDriftDetectionManagerYAML(driftDetectionManagerYAML, clusterNamespace,
		clusterName, "", clusterType)

	// Addon-controller deploys drift-detection-manager resources for each cluster matching at least
	// one ClusterProfile with SyncMode set to ContinuousWithDriftDetection.
	c := getManagementClusterClient()
	labels := getDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)
	name, _, err := getDriftDetectionManagerDeploymentName(ctx, c, labels)
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get name for drift-detection-manager deployment: %v", err))
		return err
	}

	driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "$NAME", name)

	restConfig := getManagementClusterConfig()

	const separator = "---"
	elements := strings.Split(driftDetectionManagerYAML, separator)
	for i := range elements {
		policy, err := utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse drift detection manager yaml: %v", err))
			return err
		}

		dr, err := utils.GetDynamicResourceInterface(restConfig, policy.GroupVersionKind(), policy.GetNamespace())
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
