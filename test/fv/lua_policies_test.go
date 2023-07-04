/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	// to instantiate pass name, namespace and deployment name in this order
	horizontalAutoscaler = `apiVersion: autoscaling/v1
kind: HorizontalPodAutoscaler
metadata:
  name: %s
  namespace: %s
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: %s
  minReplicas: 1
  maxReplicas: 5
  targetCPUUtilizationPercentage: 80`

	// to instantiate pass name, namespace in this order
	helloWorldDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: my-app
spec:
  replicas: 3
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
      - name: my-container
        image: hello-world
`

	deploymentAndAutoscaler = `function evaluate()
    local hs = {}
    hs.valid = true
    hs.message = ""

    local deployments = {}
    local autoscalers = {}

    -- Separate deployments and services from the resources
    for _, resource in ipairs(resources) do
        local kind = resource.kind
        if resource.metadata.namespace == "%s" then
            if kind == "Deployment" then
                table.insert(deployments, resource)
            elseif kind == "HorizontalPodAutoscaler" then
                table.insert(autoscalers, resource)
            end
        end
    end

    -- Check for each deployment if there is a matching HorizontalPodAutoscaler
    for _, deployment in ipairs(deployments) do
        local deploymentName = deployment.metadata.name
        local matchingAutoscaler = false

        for _, autoscaler in ipairs(autoscalers) do
            if autoscaler.spec.scaleTargetRef.name == deployment.metadata.name then
                matchingAutoscaler = true
                break
            end
        end

        if not matchingAutoscaler then
            hs.valid = false
            hs.message = "No matching autoscaler found for deployment: " .. deploymentName
            break
        end
    end

    return hs
end`
)

var _ = Describe("LUA validations", func() {
	const (
		namePrefix = "lua-"
	)

	var (
		namespace string
	)

	It("Deploy and updates resources referenced in ResourceRefs correctly enforcing lua validations", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		namespace = randomString()
		Byf("Create namespace %s. All namespaced resources used in this test will be placed here.", namespace)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create an AddonCompliance matching managed cluster")
		addonCompliance := &libsveltosv1alpha1.AddonCompliance{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonComplianceSpec{
				ClusterSelector: clusterProfile.Spec.ClusterSelector,
			},
		}
		Expect(k8sClient.Create(context.TODO(), addonCompliance)).To(Succeed())

		Byf("Create a configMap with LUA policy")
		configMap := createConfigMapWithPolicy(namespace, namePrefix+randomString(),
			fmt.Sprintf(deploymentAndAutoscaler, namespace))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update AddonCompliance %s to reference ConfigMap %s/%s", addonCompliance.Name, configMap.Namespace, configMap.Name)
		currentAddonCompliance := &libsveltosv1alpha1.AddonCompliance{}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			retryErr := k8sClient.Get(context.TODO(), types.NamespacedName{Name: addonCompliance.Name}, currentAddonCompliance)
			if retryErr != nil {
				return retryErr
			}
			currentAddonCompliance.Spec.LuaValidationRefs = []libsveltosv1alpha1.LuaValidationRef{
				{
					Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			}
			return k8sClient.Update(context.TODO(), currentAddonCompliance)
		})
		Expect(err).To(BeNil())

		Byf("Verify addon-compliance-controller properly process AddonCompliance instance")
		Eventually(func() bool {
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: addonCompliance.Name}, currentAddonCompliance)
			if err != nil {
				return false
			}
			// There is one matching cluster
			// There is one lua policy
			return len(currentAddonCompliance.Status.MatchingClusterRefs) == 1 &&
				len(currentAddonCompliance.Status.LuaValidations) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		deploymentName := randomString()
		Byf("Create a new configMap with a Deployment")
		myDeployment := fmt.Sprintf(helloWorldDeployment, deploymentName, namespace)
		configMap = createConfigMapWithPolicy(namespace, namePrefix+randomString(), myDeployment)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s", clusterProfile.Name, configMap.Namespace, configMap.Name)
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		// We have a LUA compliance policy enforcing any deployment without an associated
		// HorizontalPodAutoscaler should not be deployed
		Byf("Verifying ClusterSummary reports an error")
		Eventually(func() bool {
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				fs := &currentClusterSummary.Status.FeatureSummaries[i]
				if fs.FeatureID == configv1alpha1.FeatureResources {
					if fs.FailureMessage != nil {
						return strings.Contains(*fs.FailureMessage, "Lua validation")
					}
				}
			}

			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Update configMap to also contain an HorizontalPodAutoscaler")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		myAutoscaler := fmt.Sprintf(horizontalAutoscaler, randomString(), namespace, deploymentName)
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap, myDeployment, myAutoscaler)
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		Byf("Verifying ClusterSummary reports no error as now we are trying to deploy a deployment with an associated autoscaler")
		Eventually(func() bool {
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				fs := &currentClusterSummary.Status.FeatureSummaries[i]
				if fs.FeatureID == configv1alpha1.FeatureResources {
					if fs.Status == configv1alpha1.FeatureStatusProvisioned {
						return true
					}
				}
			}

			return false
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)

		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: addonCompliance.Name},
			currentAddonCompliance)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentAddonCompliance)).To(Succeed())

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})
