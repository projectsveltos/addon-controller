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

package fv_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus/kubeprometheus"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus/kubestatemetrics"
)

const (
	smTemplate = `apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: %s
  namespace: monitoring
  labels:
    team: frontend
spec:
  selector:
    matchLabels:
      app: example-app
  endpoints:
  - port: web`
)

var _ = Describe("Prometheus", func() {
	const (
		namePrefix = "prometheus"
	)

	It("Deploy and updates Prometheus correctly", Label("FV"), func() {
		Byf("Add configMap containing servicemonitor policy")
		smName := "sm" + randomString()
		serviceMonitor := fmt.Sprintf(smTemplate, smName)
		configMap := createConfigMapWithPolicy("default", namePrefix+randomString(), serviceMonitor)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterFeature matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterFeature := getClusterfeature(namePrefix, map[string]string{key: value})
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterFeature.Spec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
			InstallationMode: configv1alpha1.InstallationModeKubeStateMetrics,
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMap.Namespace, Name: configMap.Name},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterFeature)).To(Succeed())

		verifyClusterFeatureMatches(clusterFeature)

		clusterSummary := verifyClusterSummary(clusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying Prometheus operator deployment %s/%s is present", prometheus.Namespace, prometheus.Deployment)
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: prometheus.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying Prometheus instance  %s/%s is present", prometheus.Namespace, kubeprometheus.PrometheusName)
		Eventually(func() error {
			prometheusInstance := &monitoringv1.Prometheus{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: kubeprometheus.PrometheusName}, prometheusInstance)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying KubeStateMetrics deployment %s/%s is present", kubestatemetrics.Namespace, kubestatemetrics.Deployment)
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kubestatemetrics.Namespace, Name: kubestatemetrics.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		policyName := smName
		Byf("Verifying ServiceMonitor monitoring/%s is present", policyName)
		Eventually(func() error {
			currentServiceMonitor := &monitoringv1.ServiceMonitor{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: policyName}, currentServiceMonitor)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for prometheus", clusterSummary.Name)
		verifyFeatureStatus(clusterSummary.Name, configv1alpha1.FeaturePrometheus, configv1alpha1.FeatureStatusProvisioned)

		Byf("Modifying configMap %s/%s", configMap.Namespace, configMap.Name)
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		newSMName := "sm" + randomString()
		serviceMonitor = fmt.Sprintf(smTemplate, newSMName)
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap, serviceMonitor)
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		policyName = newSMName
		Byf("Verifying ServiceMonitor monitoring/%s is present", policyName)
		Eventually(func() bool {
			currentServiceMonitor := &monitoringv1.ServiceMonitor{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: policyName}, currentServiceMonitor)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("changing clusterfeature to not require any prometheus configuration anymore")
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.PrometheusConfiguration = nil
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		verifyClusterSummary(currentClusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ServiceMonitor is removed in the workload cluster")
		Eventually(func() bool {
			currentServiceMonitor := &monitoringv1.ServiceMonitor{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: newSMName}, currentServiceMonitor)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterFeature(clusterFeature)
	})
})
