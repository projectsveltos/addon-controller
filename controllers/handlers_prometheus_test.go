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

package controllers_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus/kubeprometheus"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/prometheus/kubestatemetrics"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

var _ = Describe("HanldersPrometheus", func() {
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, cluster.Namespace, cluster.Name)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterFeature.Name, cluster.Namespace, cluster.Name)
	})

	AfterEach(func() {
		deleteResources(namespace, clusterFeature, clusterSummary)
	})

	It("isPrometheusOperatorReady returns true when Prometheus Operator deployment is ready", func() {
		verifyDeployment(clusterSummary, prometheus.Namespace, prometheus.Deployment,
			controllers.DeployPrometheusOperatorInWorklaodCluster, controllers.IsPrometheusOperatorReady)
	})

	It("deployPrometheusOperatorInWorklaodCluster installs prometheus CRDs in a cluster", func() {
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
			InstallationMode: configv1alpha1.PrometheusInstallationModeCustom,
		}
		Expect(controllers.DeployPrometheusOperatorInWorklaodCluster(context.TODO(), testEnv,
			clusterSummary, klogr.New())).To(Succeed())

		customResourceDefinitions := &apiextensionsv1.CustomResourceDefinitionList{}
		Expect(testEnv.List(context.TODO(), customResourceDefinitions)).To(Succeed())
		prometheusFound := false
		alertManagerFound := false
		for i := range customResourceDefinitions.Items {
			if customResourceDefinitions.Items[i].Spec.Group == "monitoring.coreos.com" {
				if customResourceDefinitions.Items[i].Spec.Names.Plural == "prometheuses" {
					prometheusFound = true
				}
				if customResourceDefinitions.Items[i].Spec.Names.Plural == "alertmanagerconfigs" {
					alertManagerFound = true
				}
			}
		}
		Expect(alertManagerFound).To(BeTrue())
		Expect(prometheusFound).To(BeTrue())
	})

	It("deployPrometheusOperator deploys prometheus operator deployment in the right namespace", func() {
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
			InstallationMode: configv1alpha1.PrometheusInstallationModeCustom,
		}
		Expect(controllers.DeployPrometheusOperatorInWorklaodCluster(context.TODO(), testEnv,
			clusterSummary, klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: prometheus.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("isKubeStateMetricsReady returns true when KubeStateMetrics deployment is ready", func() {
		verifyDeployment(clusterSummary, kubestatemetrics.Namespace, kubestatemetrics.Deployment,
			controllers.DeployKubeStateMetricsInWorklaodCluster, controllers.IsKubeStateMetricsReady)
	})

	It("deployKubeStateMetrics deploys KubeStateMetrics deployment", func() {
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
			InstallationMode: configv1alpha1.PrometheusInstallationModeKubeStateMetrics,
		}
		Expect(controllers.DeployKubeStateMetricsInWorklaodCluster(context.TODO(), testEnv,
			clusterSummary, klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: kubestatemetrics.Namespace, Name: kubestatemetrics.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deployKubePrometheusInWorklaodCluster deploys prometheus operator deployment", func() {
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
			InstallationMode: configv1alpha1.PrometheusInstallationModeKubePrometheus,
		}
		Expect(controllers.DeployKubePrometheusInWorklaodCluster(context.TODO(), testEnv,
			clusterSummary, klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: prometheus.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deployPrometheus returns an error when CAPI Cluster does not exist", func() {
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		err := controllers.DeployPrometheus(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).ToNot(BeNil())
	})

	It("deployPrometheus deploys prometheus operator with InstallationModeCustom", func() {
		prepareForPrometheusDeployment(clusterSummary, configv1alpha1.PrometheusInstallationModeCustom, cluster)

		Expect(controllers.DeployPrometheus(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: prometheus.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deployPrometheus deploys kubeStateMetrics and Prometheus operator with InstallationModeKubeStateMetrics", func() {
		prepareForPrometheusDeployment(clusterSummary, configv1alpha1.PrometheusInstallationModeKubeStateMetrics, cluster)

		Expect(controllers.DeployPrometheus(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: prometheus.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: kubestatemetrics.Namespace, Name: kubestatemetrics.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deployPrometheus deploys kubeStateMetrics and Prometheus operator with InstallationModeKubePrometheus", func() {
		prepareForPrometheusDeployment(clusterSummary, configv1alpha1.PrometheusInstallationModeKubePrometheus, cluster)

		Expect(controllers.DeployPrometheus(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: prometheus.Namespace, Name: prometheus.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: kubestatemetrics.Namespace, Name: kubestatemetrics.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("unDeployPrometheus does nothing when CAPI Cluster is not found", func() {
		initObjects := []client.Object{
			clusterSummary,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
		err := controllers.UnDeployPrometheus(context.TODO(), c,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).To(BeNil())
	})

	It("unDeployPrometheus removes all Kyverno policies created by a ClusterSummary", func() {
		serviceMonitor0, err := controllers.GetUnstructured([]byte(fmt.Sprintf(serviceMonitorFrontend, randomString())))
		Expect(err).To(BeNil())
		serviceMonitor0.SetNamespace(namespace)

		serviceMonitor1, err := controllers.GetUnstructured([]byte(fmt.Sprintf(serviceMonitorKubeMtrics, randomString())))
		Expect(err).To(BeNil())
		serviceMonitor1.SetNamespace(namespace)
		serviceMonitor1.SetLabels(map[string]string{
			controllers.ConfigLabelName:      randomString(),
			controllers.ConfigLabelNamespace: randomString(),
		})

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + "-kubeconfig",
			},
			Data: map[string][]byte{
				"data": testEnv.Kubeconfig,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = nil
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeaturePrometheus,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"ServiceMonitor.v1.monitoring.coreos.com",
				},
			},
		}
		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), currentClusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), serviceMonitor1)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, serviceMonitor1)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, serviceMonitor1, currentClusterSummary)

		Expect(testEnv.Client.Create(context.TODO(), serviceMonitor0)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, serviceMonitor0)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(controllers.UnDeployPrometheus(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(configv1alpha1.FeaturePrometheus), klogr.New())).To(Succeed())

		// UnDeployPrometheus finds all policies deployed because of a clusterSummary and deletes those.
		// Expect all kyverno policies but ServiceMonitor0 (ConfigLabelNamespace/ConfigLabelName is not set on it
		// and ClusterSummary is the only OwnerReference) to be deleted.

		serviceMonitor := &monitoringv1.ServiceMonitor{}
		Eventually(func() bool {
			err = testEnv.Client.Get(context.TODO(),
				types.NamespacedName{Namespace: serviceMonitor1.GetNamespace(), Name: serviceMonitor1.GetName()},
				serviceMonitor)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// Since serviceMonitor) was not created by clusterSummary, expect it to be still present
		Expect(testEnv.Client.Get(context.TODO(),
			types.NamespacedName{Namespace: serviceMonitor0.GetNamespace(),
				Name: serviceMonitor0.GetName()}, serviceMonitor)).To(Succeed())
	})

	It("getPrometheusInstance returns prometheus instance", func() {
		prometheus := &monitoringv1.Prometheus{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: prometheus.Namespace,
				Name:      kubeprometheus.PrometheusName,
			},
		}

		initObjects := []client.Object{prometheus}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		currentInstance, err := controllers.GetPrometheusInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		Expect(currentInstance).ToNot(BeNil())
		Expect(currentInstance.Name).To(Equal(kubeprometheus.PrometheusName))
	})

	It("addStorageConfig add storage section to prometheus instance", func() {
		prometheus := &monitoringv1.Prometheus{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: prometheus.Namespace,
				Name:      kubeprometheus.PrometheusName,
			},
		}
		value := int64(40000000)
		resourceQuantity := resource.NewQuantity(value, resource.BinarySI)
		scname := "sc" + randomString()
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
			StorageClassName: &scname,
			StorageQuantity:  resourceQuantity,
		}

		initObjects := []client.Object{prometheus}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.AddStorageConfig(context.TODO(), c, clusterSummary, klogr.New())).To(Succeed())

		currentInstance, err := controllers.GetPrometheusInstance(context.TODO(), c)
		Expect(err).To(BeNil())
		Expect(currentInstance).ToNot(BeNil())
		Expect(currentInstance.Name).To(Equal(kubeprometheus.PrometheusName))
		Expect(currentInstance.Spec.Storage).ToNot(BeNil())
		Expect(currentInstance.Spec.Storage.VolumeClaimTemplate.Spec.StorageClassName).ToNot(BeNil())
		Expect(*currentInstance.Spec.Storage.VolumeClaimTemplate.Spec.StorageClassName).To(Equal(scname))
		storageConfig := currentInstance.Spec.Storage.VolumeClaimTemplate.Spec.Resources.Requests.Storage()
		Expect(storageConfig).ToNot(BeNil())
		v, ok := storageConfig.AsInt64()
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(value))
	})
})

var _ = Describe("Hash methods", func() {
	It("prometheusHash returns hash considering all referenced configmap contents", func() {
		configMapNs := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(),
			fmt.Sprintf(serviceMonitorFrontend, randomString()))
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(),
			fmt.Sprintf(serviceMonitorKubeMtrics, randomString()))

		namespace := "reconcile" + randomString()
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					PrometheusConfiguration: &configv1alpha1.PrometheusConfiguration{
						InstallationMode: configv1alpha1.PrometheusInstallationModeCustom,
						PolicyRefs: []corev1.ObjectReference{
							{Namespace: configMapNs, Name: configMap1.Name},
							{Namespace: configMapNs, Name: configMap2.Name},
							{Namespace: configMapNs, Name: randomString()},
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			configMap1,
			configMap2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		config := render.AsCode(configMap1.Data)
		config += render.AsCode(configMap2.Data)
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.PrometheusHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})

func verifyDeployment(clusterSummary *configv1alpha1.ClusterSummary,
	deploymentNamespace, deploymentName string,
	deployFunc func(ctx context.Context, c client.Client,
		clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) error,
	isDeplReadyFunc func(ctx context.Context, c client.Client,
		clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) (present bool, ready bool, err error),
) {

	clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
		InstallationMode: configv1alpha1.PrometheusInstallationModeKubeStateMetrics,
	}
	Expect(deployFunc(context.TODO(), testEnv.Client,
		clusterSummary, klogr.New())).To(Succeed())

	currentDepl := &appsv1.Deployment{}
	Expect(testEnv.Get(context.TODO(),
		types.NamespacedName{Namespace: deploymentNamespace, Name: deploymentName}, currentDepl)).To(Succeed())

	// testEnv does not have a deployment controller, so status is not updated
	present, ready, err := isDeplReadyFunc(context.TODO(), testEnv.Client, clusterSummary, klogr.New())
	Expect(err).To(BeNil())
	Expect(present).To(BeTrue())
	Expect(ready).To(BeFalse())

	currentDepl.Status.AvailableReplicas = 1
	currentDepl.Status.Replicas = 1
	currentDepl.Status.ReadyReplicas = 1
	Expect(testEnv.Status().Update(context.TODO(), currentDepl)).To(Succeed())

	// Eventual loop so testEnv Cache is synced
	Eventually(func() bool {
		present, ready, err = isDeplReadyFunc(context.TODO(),
			testEnv.Client, clusterSummary, klogr.New())
		return err == nil && present && ready
	}, timeout, pollingInterval).Should(BeTrue())
}

func prepareForPrometheusDeployment(clusterSummary *configv1alpha1.ClusterSummary,
	installationMode configv1alpha1.PrometheusInstallationMode,
	cluster *clusterv1.Cluster) {

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterSummary.Spec.ClusterNamespace,
			Name:      clusterSummary.Spec.ClusterName + "-kubeconfig",
		},
		Data: map[string][]byte{
			"data": testEnv.Kubeconfig,
		},
	}
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterSummary.Spec.ClusterNamespace,
		},
	}

	clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
		InstallationMode: installationMode,
	}
	Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
	Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
	Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
	Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())
}
