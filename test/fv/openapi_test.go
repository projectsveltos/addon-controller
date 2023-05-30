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

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	myAppServiceYAML = `apiVersion: v1
kind: Service
metadata:
  name: my-service
  namespace: %s
spec:
  selector:
    app: my-app
  ports:
    - protocol: TCP
      port: 80
      targetPort: 8080
  type: ClusterIP`

	preventPortPolicyYAML = `openapi: 3.0.0
info:
  title: My API
  version: 1.0.0
paths:
  /api/v1/namespaces/%s/services/{service}:
    put:
      parameters:
        - name: service
          in: path
          required: true
          schema:
            type: string
      requestBody:
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/ServiceRequest"
      responses:
        '201':
          description: Service created successfully
        '400':
          description: Invalid request
      x-kubernetes-body-schema:
        $ref: "#/components/schemas/ServiceRequest"
components:
  schemas:
    ServiceRequest:
      type: object
      properties:
        metadata:
          type: object
          properties:
            name:
              type: string
        spec:
          type: object
          properties:
            ports:
              type: array
              items:
                $ref: "#/components/schemas/Port"
    Port:
      type: object
      properties:
        port:
          type: integer
          not:
            enum: [80]
          example: 8080`
)

var _ = Describe("OpenAPI validations", func() {
	const (
		namePrefix = "openapi-"
	)

	It("Deploy and updates resources referenced in ResourceRefs correctly enforcing openapi validations", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create an AddConstraint")
		addonConstraint := &libsveltosv1alpha1.AddonConstraint{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonConstraintSpec{
				ClusterSelector: clusterProfile.Spec.ClusterSelector,
			},
		}
		Expect(k8sClient.Create(context.TODO(), addonConstraint)).To(Succeed())

		Byf("Create a configMap with openAPI policy")
		preventPortPolicy := fmt.Sprintf(preventPortPolicyYAML, configMapNs)
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), preventPortPolicy)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Update AddonConstraint %s to reference ConfigMap %s/%s", addonConstraint.Name, configMap.Namespace, configMap.Name)
		currentAddonConstraint := &libsveltosv1alpha1.AddonConstraint{}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			retryErr := k8sClient.Get(context.TODO(), types.NamespacedName{Name: addonConstraint.Name}, currentAddonConstraint)
			if retryErr != nil {
				return retryErr
			}
			currentAddonConstraint.Spec.OpenAPIValidationRefs = []libsveltosv1alpha1.OpenAPIValidationRef{
				{
					Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			}
			return k8sClient.Update(context.TODO(), currentAddonConstraint)
		})
		Expect(err).To(BeNil())

		Byf("Verify addon-constraint-controller properly process AddonConstraint instance")
		Eventually(func() bool {
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: addonConstraint.Name}, currentAddonConstraint)
			if err != nil {
				return false
			}
			// There is one matching cluster
			// There is one openapi policy
			return len(currentAddonConstraint.Status.MatchingClusterRefs) == 1 &&
				len(currentAddonConstraint.Status.OpenapiValidations) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Create a configMap with a Service")
		myAppService := fmt.Sprintf(myAppServiceYAML, configMapNs)
		configMap = createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), myAppService)
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
						return strings.Contains(*fs.FailureMessage, "OpenAPI validation")
					}
				}
			}

			return false
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)

		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: addonConstraint.Name}, currentAddonConstraint)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentAddonConstraint)).To(Succeed())

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})
