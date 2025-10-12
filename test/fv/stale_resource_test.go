/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	serviceTemplate = `  apiVersion: v1
  kind: Service
  metadata:
    namespace: %s
    name: %s
    labels:
      app: my-app
      environment: production
  spec:
    selector:
      app: my-app
    ports:
      - port: 80
        targetPort: 8080
        protocol: TCP
        name: http
      - port: 443
        targetPort: 8443
        protocol: TCP
        name: https
    type: ClusterIP
    sessionAffinity: None`

	// the YAML here is incorrect. Note the indentation is wrong.
	incorrectService = ` apiVersion: v1
  kind: Service
  metadata:
    namespace: %s
    name: %s
    labels:
      app: my-app
      environment: production
  spec:
    ports:
      - port: 80
        targetPort: 8080
        protocol: TCP
        name: http
      - port: 443
        targetPort: 8443
        protocol: TCP
        name: https
    type: ClusterIP
    sessionAffinity: None`
)

var _ = Describe("Stale Resources", func() {
	const (
		namePrefix = "stale-resource"
	)

	It("Stale resources are removed only after successful deployment", Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		service1 := randomString()
		service2 := randomString()
		service3 := randomString()

		Byf("Create a configMap with Service")
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
			fmt.Sprintf(serviceTemplate, configMapNs, service1),
			fmt.Sprintf(serviceTemplate, configMapNs, service2),
			fmt.Sprintf(serviceTemplate, configMapNs, service3),
		)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		for _, serviceName := range []string{service1, service2, service3} {
			Byf("Verifying Service %s is created in the workload cluster", serviceName)
			Eventually(func() error {
				currentService := &corev1.Service{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNs, Name: serviceName}, currentService)
			}, timeout, pollingInterval).Should(BeNil())
		}

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		By("Updating ConfigMap to reference incorrect Service")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		allClusterRoleName := randomString()
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap, fmt.Sprintf(allClusterRole, allClusterRoleName))
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		By("Updating ConfigMap to reference also an incorrect Service")
		incorrectServiceName := randomString()
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap,
			fmt.Sprintf(serviceTemplate, configMapNs, service1),
			fmt.Sprintf(incorrectService, configMapNs, incorrectServiceName),
			fmt.Sprintf(serviceTemplate, configMapNs, service2),
			fmt.Sprintf(serviceTemplate, configMapNs, service3))
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		Byf("Verifying ClusterSummary %s/%s reports failure", clusterSummary.Namespace, clusterSummary.Name)
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureResources {

					return currentClusterSummary.Status.FeatureSummaries[i].FailureMessage != nil &&
						strings.Contains(*currentClusterSummary.Status.FeatureSummaries[i].FailureMessage,
							"error converting YAML to JSON: yaml")
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		for _, serviceName := range []string{service1, service2, service3} {
			Byf("Verifying Service %s is still in the workload cluster", serviceName)
			Consistently(func() error {
				currentService := &corev1.Service{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNs, Name: serviceName}, currentService)
			}, time.Minute, pollingInterval).Should(BeNil())
		}

		Byf("Verifying Service %s is not created the workload cluster", incorrectServiceName)
		Consistently(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: configMapNs, Name: incorrectServiceName}, currentService)
			return err != nil && apierrors.IsNotFound(err)
		}, time.Minute, pollingInterval).ShouldNot(BeNil())

		By("Updating ConfigMap to reference also a fourth Service")
		service4 := randomString()
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap,
			fmt.Sprintf(serviceTemplate, configMapNs, service1),
			fmt.Sprintf(serviceTemplate, configMapNs, service4),
			fmt.Sprintf(serviceTemplate, configMapNs, service2),
			fmt.Sprintf(serviceTemplate, configMapNs, service3))
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		for _, serviceName := range []string{service1, service2, service3, service4} {
			Byf("Verifying Service %s is created in the workload cluster", serviceName)
			Eventually(func() error {
				currentService := &corev1.Service{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNs, Name: serviceName}, currentService)
			}, timeout, pollingInterval).Should(BeNil())
		}

		deleteClusterProfile(clusterProfile)

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})
