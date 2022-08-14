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

package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus/kubeprometheus"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus/kubestatemetrics"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

func deployPrometheus(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, clusterClient, err := getClusterSummaryAndCAPIClusterClient(ctx, applicant, c, logger)
	if err != nil {
		return err
	}

	if shouldInstallPrometheusOperator(clusterSummary) {
		err = deployPrometheusOperator(ctx, clusterClient, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	if shouldInstallKubeStateMetrics(clusterSummary) {
		err = deployKubeStateMetrics(ctx, clusterClient, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	if shouldInstallKubePrometheusStack(clusterSummary) {
		err = deployKubePrometheusStack(ctx, clusterClient, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	err = addStorageConfig(ctx, clusterClient, clusterSummary, logger)
	if err != nil {
		return err
	}

	clusterRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	currentPolicies := make(map[string]bool, 0)
	if clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration != nil {
		var confgiMaps []corev1.ConfigMap
		confgiMaps, err = collectConfigMaps(ctx, c, clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs, logger)
		if err != nil {
			return err
		}

		var deployed []string
		deployed, err = deployConfigMaps(ctx, confgiMaps, clusterSummary, clusterClient, clusterRestConfig, logger)
		if err != nil {
			return err
		}

		for _, k := range deployed {
			currentPolicies[k] = true
		}
	}

	err = undeployStaleResources(ctx, clusterRestConfig, clusterClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeaturePrometheus), currentPolicies)
	if err != nil {
		return err
	}

	return nil
}

func unDeployPrometheus(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := c.Get(ctx, types.NamespacedName{Name: applicant}, clusterSummary); err != nil {
		return err
	}

	// Get CAPI Cluster
	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info(fmt.Sprintf("Cluster %s/%s not found. Nothing to cleanup", clusterNamespace, clusterName))
			return nil
		}
		return err
	}

	clusterClient, err := getKubernetesClient(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	clusterRestConfig, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	err = undeployStaleResources(ctx, clusterRestConfig, clusterClient, clusterSummary,
		getDeployedGroupVersionKinds(clusterSummary, configv1alpha1.FeaturePrometheus), map[string]bool{})
	if err != nil {
		return err
	}

	return nil
}

// prometheusHash returns the hash of all the Prometheus referenced configmaps.
func prometheusHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration == nil {
		return h.Sum(nil), nil
	}
	for i := range clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs[i]
		configmap := &corev1.ConfigMap{}
		err := c.Get(ctx, types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configmap)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("configMap %s/%s does not exist yet",
					reference.Namespace, reference.Name))
				continue
			}
			logger.Error(err, fmt.Sprintf("failed to get configMap %s/%s",
				reference.Namespace, reference.Name))
			return nil, err
		}

		config += render.AsCode(configmap.Data)
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getPrometheusRefs(clusterSummaryScope *scope.ClusterSummaryScope) []corev1.ObjectReference {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration != nil {
		return clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs
	}
	return nil
}

// shouldInstallPrometheusOperator returns true if prometheus operator needs to be installed
func shouldInstallPrometheusOperator(clusterSummary *configv1alpha1.ClusterSummary) bool {
	// Unless kube-prometheus stack is deployed, prometheus operator needs to be installed
	return clusterSummary != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.InstallationMode !=
			configv1alpha1.InstallationModeKubePrometheus
}

// shouldInstallKubeStateMetrics returns true if kube state metrics needs to be installed
func shouldInstallKubeStateMetrics(clusterSummary *configv1alpha1.ClusterSummary) bool {
	return clusterSummary != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.InstallationMode ==
			configv1alpha1.InstallationModeKubeStateMetrics
}

// shouldInstallKubePrometheusStack returns true if kube prometheus stack needs to be installed
func shouldInstallKubePrometheusStack(clusterSummary *configv1alpha1.ClusterSummary) bool {
	return clusterSummary != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration != nil &&
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.InstallationMode ==
			configv1alpha1.InstallationModeKubePrometheus
}

// isPrometheusOperatorReady checks whether prometheus operator deployment is present and ready
func isPrometheusOperatorReady(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) (present, ready bool, err error) {

	namespace := prometheus.Namespace
	logger = logger.WithValues("prometheusnamespace", namespace, "prometheusdeployment", prometheus.Deployment)
	present = false
	ready = false
	depl := &appsv1.Deployment{}
	err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: prometheus.Deployment}, depl)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("Prometheus operator deployment not found")
			err = nil
			return
		}
		return
	}

	present = true

	if depl.Status.ReadyReplicas != *depl.Spec.Replicas {
		logger.V(logs.LogDebug).Info("Not all replicas are ready for prometheus operator deployment")
		return
	}

	ready = true
	return
}

// isKubeStateMetricsReady checks whether KubeStateMetrics deployment is present and ready
func isKubeStateMetricsReady(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) (present, ready bool, err error) {

	logger = logger.WithValues("ksmnamespace", kubestatemetrics.Namespace,
		"ksmdeployment", kubestatemetrics.Deployment)
	present = false
	ready = false
	depl := &appsv1.Deployment{}
	err = c.Get(ctx, types.NamespacedName{Namespace: kubestatemetrics.Namespace, Name: kubestatemetrics.Deployment}, depl)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogDebug).Info("KubeStateMetrics deployment not found")
			err = nil
			return
		}
		return
	}

	present = true

	if depl.Status.ReadyReplicas != *depl.Spec.Replicas {
		logger.V(logs.LogDebug).Info("Not all replicas are ready for KubeStateMetrics deployment")
		return
	}

	ready = true
	return
}

func deployPrometheusOperator(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	// First verify if prometheus operator is installed, if not install it
	present, ready, err := isPrometheusOperatorReady(ctx, c, clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "Failed to verify presence of prometheus operator deployment")
		return err
	}

	if !present {
		err = deployPrometheusOperatorInWorklaodCluster(ctx, c, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("prometheus operator deployment is not ready yet")
	}

	return nil
}

func deployKubeStateMetrics(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	// Deploy ClusterRole/ClusterRoleBinding/ServiceAccount for prometheus
	if err := deployPrometheusPermissions(ctx, c, logger); err != nil {
		return nil
	}

	logger.V(logs.LogDebug).Info("deploying prometheus instance")
	// Deploy Prometheus instance
	if err := deployDoc(ctx, c, kubeprometheus.Prometheus, logger); err != nil {
		return nil
	}

	logger.V(logs.LogDebug).Info("deploying kubestatemetrics servicemonitor")
	// Deploy ServiceMonitor to scrape KubeStateMetrics
	if err := deployDoc(ctx, c, kubeprometheus.KSMServiceMonitor, logger); err != nil {
		return nil
	}

	// First verify if KubeStateMetrics is installed, if not install it
	present, ready, err := isKubeStateMetricsReady(ctx, c, clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "Failed to verify presence of prometheus operator deployment")
		return err
	}

	if !present {
		err = deployKubeStateMetricsInWorklaodCluster(ctx, c, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("prometheus operator deployment is not ready yet")
	}

	return nil
}

// deployPrometheusPermissions deploys ClusterRole/ClusterRoleBinding and ServiceAccount for
// Prometheus
func deployPrometheusPermissions(ctx context.Context, c client.Client, logger logr.Logger) error {
	logger.V(logs.LogDebug).Info("deploying prometheus ClusterRole")
	err := deployDoc(ctx, c, prometheus.PrometheusClusterRole, logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("deploying prometheus ClusterRoleBinding")
	clusterRoleBinding := fmt.Sprintf(string(prometheus.PrometheusClusterRoleBindingTemplate),
		kubeprometheus.PrometheusServiceAccountName)
	err = deployDoc(ctx, c, []byte(clusterRoleBinding), logger)
	if err != nil {
		return err
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeprometheus.PrometheusServiceAccountName,
			Namespace: prometheus.Namespace,
		},
	}

	logger.V(logs.LogDebug).Info("deploying prometheus serviceaccount")
	err = c.Create(ctx, sa)
	if err != nil && apierrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}

func deployKubePrometheusStack(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	// First verify if prometheus operator is installed, if not install it
	present, ready, err := isPrometheusOperatorReady(ctx, c, clusterSummary, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "Failed to verify presence of prometheus operator deployment")
		return err
	}

	if !present {
		err = deployKubePrometheusInWorklaodCluster(ctx, c, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("prometheus operator deployment is not ready yet")
	}

	return nil
}

func deployPrometheusOperatorInWorklaodCluster(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	if err := createNamespace(ctx, c, prometheus.Namespace); err != nil {
		return err
	}

	return deployDoc(ctx, c, prometheus.PrometheusYAML, logger)
}

func deployKubeStateMetricsInWorklaodCluster(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	logger.V(logs.LogDebug).Info("deploying kubestatemetrics")
	if err := createNamespace(ctx, c, kubestatemetrics.Namespace); err != nil {
		return err
	}

	return deployDoc(ctx, c, kubestatemetrics.KubeStateMetricsYAML, logger)
}

func deployKubePrometheusInWorklaodCluster(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	if err := createNamespace(ctx, c, prometheus.Namespace); err != nil {
		return err
	}

	return deployDoc(ctx, c, kubeprometheus.KubePrometheusYAML, logger)
}

// addStorageConfig adds storage configuration if defined if defined/requested by user.
func addStorageConfig(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	if clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration == nil ||
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.StorageClassName == nil ||
		*clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.StorageClassName == "" {

		logger.V(logs.LogVerbose).Info("no storage configuration")
		return nil
	}

	storageClassName := *clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.StorageClassName

	prometheusInstance, err := getPrometheusInstance(ctx, c)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get prometheus instance. Err: %v", err))
		return err
	}

	if prometheusInstance.Spec.Storage == nil {
		const defaultQuantity = 40000000
		quantity := resource.NewQuantity(defaultQuantity, resource.BinarySI)
		if clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.StorageQuantity != nil {
			quantity = clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.StorageQuantity
		}

		prometheusInstance.Spec.Storage = &monitoringv1.StorageSpec{
			VolumeClaimTemplate: monitoringv1.EmbeddedPersistentVolumeClaim{
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &storageClassName,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							"storage": *quantity,
						},
					},
				},
			},
		}
	}

	return c.Update(ctx, prometheusInstance)
}

func getPrometheusInstance(ctx context.Context, c client.Client) (*monitoringv1.Prometheus, error) {
	prometheusInstance := &monitoringv1.Prometheus{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: prometheus.Namespace, Name: kubeprometheus.PrometheusName},
		prometheusInstance)
	if err != nil {
		return nil, err
	}
	return prometheusInstance, nil
}
