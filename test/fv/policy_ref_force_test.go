/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	depNoStrategy = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: main
        image: nginx:latest`

	depRecreateStrategy = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: main
        image: nginx:latest`
)

// This test only runs in push mode: sveltos-applier does not yet act on
// ConfigurationBundle.Spec.Force in pull mode.
var _ = Describe("Feature", func() {
	const (
		namePrefix = "policyref-force-"
	)

	It("Force lets Sveltos recreate a Deployment when a strategy change is rejected by the API server",
		Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
			configMapNs := randomString()
			Byf("Create configMap's namespace %s", configMapNs)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: configMapNs,
				},
			}
			Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

			deploymentName := randomString()
			Byf("Create a configMap with a Deployment (no strategy set)")
			configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
				fmt.Sprintf(depNoStrategy, deploymentName, configMapNs, deploymentName, deploymentName))
			Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

			Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyClusterProfileMatches(clusterProfile)

			verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
			currentClusterProfile := &configv1beta1.ClusterProfile{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			}
			Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying ClusterSummary %s status is set to Provisioned for Resources feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureResources)

			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			Byf("Verifying Deployment %s/%s is created in the workload cluster with a defaulted rollingUpdate strategy",
				configMapNs, deploymentName)
			Eventually(func() bool {
				depl := &appsv1.Deployment{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNs, Name: deploymentName}, depl)
				if err != nil {
					return false
				}
				return depl.Spec.Strategy.RollingUpdate != nil
			}, timeout, pollingInterval).Should(BeTrue())

			By("Updating ConfigMap to set the Deployment's strategy to Recreate")
			currentConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
			currentConfigMap = updateConfigMapWithPolicy(currentConfigMap,
				fmt.Sprintf(depRecreateStrategy, deploymentName, configMapNs, deploymentName, deploymentName))
			Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

			Byf("Verifying ClusterSummary %s reports a failure for Resources feature (rollingUpdate forbidden with Recreate)",
				clusterSummary.Name)
			Eventually(func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
					currentClusterSummary)
				if err != nil {
					return false
				}
				for i := range currentClusterSummary.Status.FeatureSummaries {
					fs := &currentClusterSummary.Status.FeatureSummaries[i]
					if fs.FeatureID == libsveltosv1beta1.FeatureResources {
						return fs.FailureMessage != nil
					}
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue())

			By("Setting Force on the PolicyRef")
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name},
					currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.PolicyRefs[0].Force = true
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			Byf("Verifying ClusterSummary %s status is back to Provisioned for Resources feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureResources)

			Byf("Verifying Deployment %s/%s was recreated with strategy Recreate and no rollingUpdate",
				configMapNs, deploymentName)
			Eventually(func() bool {
				depl := &appsv1.Deployment{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNs, Name: deploymentName}, depl)
				if err != nil {
					return false
				}
				return depl.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType &&
					depl.Spec.Strategy.RollingUpdate == nil
			}, timeout, pollingInterval).Should(BeTrue())

			deleteClusterProfile(clusterProfile)

			currentNs := &corev1.Namespace{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
		})
})
