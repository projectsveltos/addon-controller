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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"

	driftdetection "github.com/projectsveltos/addon-controller/pkg/drift-detection"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/crd"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func getResourceSummaryNamespace() string {
	return "projectsveltos"
}

func getResourceSummaryName(clusterSummaryNamespace, clusterSummaryName string) string {
	return fmt.Sprintf("%s--%s", clusterSummaryNamespace, clusterSummaryName)
}

func deployDriftDetectionManagerInCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant string, clusterType libsveltosv1alpha1.ClusterType,
	logger logr.Logger) error {

	logger = logger.WithValues("clustersummary", applicant)
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

	logger.V(logs.LogDebug).Info("Deploying classifier agent")
	// Deploy ClassifierAgent
	err = deployDriftDetectionManager(ctx, remoteRestConfig, clusterNamespace,
		clusterName, "do-not-send-reports", clusterType, logger)
	if err != nil {
		return err
	}

	return nil
}

func deployResourceSummaryInCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant string, clusterType libsveltosv1alpha1.ClusterType,
	resources []libsveltosv1alpha1.Resource, kustomizeResources []libsveltosv1alpha1.Resource,
	helmResources []libsveltosv1alpha1.HelmResources, logger logr.Logger) error {

	logger = logger.WithValues("clustersummary", applicant)
	logger.V(logs.LogDebug).Info("deploy drift detection manager: do not send updates mode")

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

	dbCRD, err := utils.GetUnstructured(crd.GetDebuggingConfigurationCRDYAML())
	if err != nil {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("failed to get DebuggingConfiguration CRD unstructured: %v", err))
		return err
	}

	dr, err := utils.GetDynamicResourceInterface(remoteRestConfig, dbCRD.GroupVersionKind(), "")
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get dynamic client: %v", err))
		return err
	}

	options := metav1.ApplyOptions{
		FieldManager: "application/apply-patch",
	}
	_, err = dr.Apply(ctx, dbCRD.GetName(), dbCRD, options)
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

func deployDriftDetectionManager(ctx context.Context, remoteRestConfig *rest.Config,
	clusterNamespace, clusterName, mode string, clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) error {

	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerYAML())

	if mode != "do-not-send-updates" {
		driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "do-not-send-updates", "send-updates")
	}

	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-namespace=", fmt.Sprintf("cluster-namespace=%s", clusterNamespace))
	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-name=", fmt.Sprintf("cluster-name=%s", clusterName))
	driftDetectionManagerYAML =
		strings.ReplaceAll(driftDetectionManagerYAML, "cluster-type=", fmt.Sprintf("cluster-type=%s", clusterType))

	const separator = "---"
	elements := strings.Split(driftDetectionManagerYAML, separator)
	for i := range elements {
		policy, err := utils.GetUnstructured([]byte(elements[i]))
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse drift detection manager yaml: %v", err))
			return err
		}

		dr, err := utils.GetDynamicResourceInterface(remoteRestConfig, policy.GroupVersionKind(), policy.GetNamespace())
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
	currentResourceSummary := &libsveltosv1alpha1.ResourceSummary{}
	err := remoteClient.Get(ctx,
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
