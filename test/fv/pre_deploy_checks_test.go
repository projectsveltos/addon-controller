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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	preDeployDeployment = `apiVersion: apps/v1
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
      - name: nginx
        image: nginx:1.25.0`
)

var _ = Describe("PreDeployChecks", func() {
	const (
		namePrefix = "pre-deploy-"
	)

	// Runs in both normal and pull mode: in normal mode the addon-controller runs the check directly;
	// in pull mode the agent runs it and reports the result via ConfigurationGroup status.
	It("blocks deployment until the required ServiceAccount exists, then provisions", Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		deployNs := randomString()
		deployName := randomString()
		saName := randomString()
		saLabelKey := "sveltos-test-app"
		saLabelValue := randomString()

		Byf("Create namespace %s in the workload cluster", deployNs)
		workloadNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: deployNs},
		}
		Expect(workloadClient.Create(context.TODO(), workloadNs)).To(Succeed())

		// ConfigMap in the management cluster holding the Deployment resource
		configMapNs := randomString()
		Byf("Create ConfigMap namespace %s in the management cluster", configMapNs)
		mgmtNs := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: configMapNs},
		}
		Expect(k8sClient.Create(context.TODO(), mgmtNs)).To(Succeed())

		deploymentYAML := fmt.Sprintf(preDeployDeployment, deployName, deployNs, deployName, deployName)
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), deploymentYAML)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create ClusterProfile with a preDeployCheck requiring ServiceAccount label %s=%s in namespace %s",
			saLabelKey, saLabelValue, deployNs)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		clusterProfile.Spec.PreDeployChecks = []libsveltosv1beta1.ValidateHealth{
			{
				Name:      "sa-exists",
				FeatureID: libsveltosv1beta1.FeatureResources,
				Group:     "",
				Version:   "v1",
				Kind:      "ServiceAccount",
				Namespace: deployNs,
				LabelFilters: []libsveltosv1beta1.LabelFilter{
					{
						Key:       saLabelKey,
						Operation: libsveltosv1beta1.OperationEqual,
						Value:     saLabelValue,
					},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s reports a preDeployChecks failure for Resources", clusterSummary.Name)
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			if err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary); err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				fs := &currentClusterSummary.Status.FeatureSummaries[i]
				if fs.FeatureID == libsveltosv1beta1.FeatureResources &&
					fs.FailureMessage != nil &&
					strings.Contains(*fs.FailureMessage, "preDeployChecks") {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Deployment %s/%s is not deployed while preDeployChecks is failing", deployNs, deployName)
		Consistently(func() bool {
			depl := &appsv1.Deployment{}
			err := workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deployNs, Name: deployName}, depl)
			return apierrors.IsNotFound(err)
		}, "30s", pollingInterval).Should(BeTrue())

		Byf("Create ServiceAccount %s/%s (label %s=%s) directly in the workload cluster",
			deployNs, saName, saLabelKey, saLabelValue)
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: deployNs,
				Labels:    map[string]string{saLabelKey: saLabelValue},
			},
		}
		Expect(workloadClient.Create(context.TODO(), sa)).To(Succeed())

		Byf("Verifying Deployment %s/%s is now deployed in the workload cluster", deployNs, deployName)
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deployNs, Name: deployName}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s becomes Provisioned for Resources after ServiceAccount is created", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		Byf("Deleting test namespace %s from the workload cluster (removes SA and Deployment)", deployNs)
		Expect(workloadClient.Delete(context.TODO(), workloadNs)).To(Succeed())

		Byf("Deleting ConfigMap namespace %s from the management cluster", configMapNs)
		Expect(k8sClient.Delete(context.TODO(), mgmtNs)).To(Succeed())

		deleteClusterProfile(clusterProfile)
	})
})
