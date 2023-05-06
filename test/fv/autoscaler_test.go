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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	autoscalerServiceAccount = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: "{{ .Cluster.metadata.name }}-autoscaler"
  namespace: "{{ .Cluster.metadata.namespace }}"`

	//nolint: gosec // ok for a test
	autoscalerSecret = `apiVersion: v1
kind: Secret
metadata:
  name: autoscaler
  namespace: "{{ .Cluster.metadata.namespace }}"
  annotations:
    kubernetes.io/service-account.name: "{{ .Cluster.metadata.name }}-autoscaler"
type: kubernetes.io/service-account-token`

	// autoscalerInfo is a template that will fetch values from a resource in the management
	// cluster. Such resource is identified as AutoscalerSecret.
	// In this test, ClusterProfile will have identifier AutoscalerSecret point to
	// secret defined in autoscalerSecret
	autoscalerInfo = `apiVersion: v1
kind: Secret
metadata:
  name: autoscaler
  namespace: {{ (index .MgtmResources "AutoscalerSecret").metadata.namespace }}
data:
  token: {{ (index .MgtmResources "AutoscalerSecret").data.token }}
  ca.crt: {{ $data:=(index .MgtmResources "AutoscalerSecret").data }} {{ (index $data "ca.crt") }}`
)

var _ = Describe("Feature", func() {
	const (
		namePrefix = "autoscaler-"
	)

	It("Deploy resources in the management cluster and the managed cluster", Label("FV", "EXTENDED"), func() {
		Byf("Extend sveltos addon-manager permission in the management cluster")
		clusterRole := &rbacv1.ClusterRole{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: "addon-manager-role-extra"}, clusterRole)).To(Succeed())
		clusterRole.Rules = []rbacv1.PolicyRule{
			{Verbs: []string{"*"}, APIGroups: []string{""}, Resources: []string{"serviceaccounts", "secrets"}},
		}
		Expect(k8sClient.Update(context.TODO(), clusterRole)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		configMapNs := defaultNamespace

		configMap1Name := namePrefix + randomString()
		Byf("Create a configMap with a Autoscaler ServiceAccount and Secret")
		configMap := createConfigMapWithPolicy(configMapNs, configMap1Name, autoscalerServiceAccount, autoscalerSecret)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		// Define it as template as content of autoscalerServiceAccount and autoscalerSecret is a template
		currentConfigMap.Annotations = map[string]string{
			"projectsveltos.io/template": "true",
		}
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		configMap2Name := namePrefix + randomString()
		Byf("Create a configMap with a Autoscaler info")
		configMap = createConfigMapWithPolicy(configMapNs, configMap2Name, autoscalerInfo)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		// Defined it as template as content of autoscalerInfo is a template
		currentConfigMap.Annotations = map[string]string{
			"projectsveltos.io/template": "true",
		}
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference both ConfigMaps", clusterProfile.Name)
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.TemplateResourceRefs = []configv1alpha1.TemplateResourceRef{
			{
				Resource: corev1.ObjectReference{
					Kind: "Secret",
					Name: "autoscaler",
				},
				Identifier: "AutoscalerSecret",
			},
		}
		currentClusterProfile.Spec.PolicyRefs = []configv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: configMapNs,
				Name:      configMap1Name,
				// Deploy content of this configMap into management cluster
				DeploymentType: configv1alpha1.DeploymentTypeLocal,
			},
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: configMapNs,
				Name:      configMap2Name,
				// Deploy content of this configMap into managed cluster
				DeploymentType: configv1alpha1.DeploymentTypeRemote,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying autoscaler serviceAccount has been created into management cluster")
		Eventually(func() error {
			currentServiceAccount := &corev1.ServiceAccount{}
			return k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name + "-autoscaler"},
				currentServiceAccount)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying autoscaler secret has been created into management cluster")
		Eventually(func() error {
			currentSecret := &corev1.Secret{}
			return k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: "autoscaler"},
				currentSecret)
		}, timeout, pollingInterval).Should(BeNil())

		mgmtCurrentSecret := &corev1.Secret{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: "autoscaler"},
			mgmtCurrentSecret)).To(Succeed())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Secret is created in the workload cluster")
		Eventually(func() error {
			currentSecret := &corev1.Secret{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: "autoscaler"},
				currentSecret)
		}, timeout, pollingInterval).Should(BeNil())

		workloadCurrentSecret := &corev1.Secret{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: "autoscaler"},
			workloadCurrentSecret)).To(Succeed())

		Expect(mgmtCurrentSecret.Data).ToNot(BeNil())
		Expect(workloadCurrentSecret.Data).ToNot(BeNil())

		Expect(workloadCurrentSecret.Data["ca.crt"]).ToNot(BeEmpty())
		Expect(workloadCurrentSecret.Data["ca.crt"]).To(Equal(mgmtCurrentSecret.Data["ca.crt"]))

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1alpha1.FeatureResources)

		deleteClusterProfile(clusterProfile)

		Byf("Verifying autoscaler serviceAccount is removed from management cluster")
		Eventually(func() bool {
			currentServiceAccount := &corev1.ServiceAccount{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name + "-autoscaler"},
				currentServiceAccount)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying autoscaler secret is removed from management cluster")
		Eventually(func() bool {
			currentSecret := &corev1.Secret{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: "autoscaler"},
				currentSecret)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Secret is created removed from the workload cluster")
		Eventually(func() bool {
			currentSecret := &corev1.Secret{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: "autoscaler"},
				currentSecret)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
