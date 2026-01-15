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

var _ = Describe("ClusterProfile with PatchFrom", func() {
	const (
		namePrefix      = "patchesfrom-"
		labelsKey       = "deployment-labels"
		annotationsKey  = "deployment-annotations"
		tolerationKey   = "deployment-tolerations"
		serviceLabelKey = "service-labels"
	)

	var (
		resourcesTemplate = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: nginx-service-account
  namespace: %s
---
apiVersion: v1
kind: Service
metadata:
  name: nginx-service
  namespace: %s
  labels:
    app: nginx
spec:
  selector:
    app: nginx
  ports:
    - protocol: TCP
      port: 80         # Port exposed by the service
      targetPort: 80   # Port the container is listening on
  type: ClusterIP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-deployment
  namespace: %s
  labels:
    app: nginx
spec:
  replicas: 3
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      serviceAccountName: nginx-service-account
      containers:
      - name: nginx
        image: nginx:1.25.3   # Using a specific stable version
        ports:
        - containerPort: 80
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 500m
            memory: 256Mi
`
	)

	It("Deploy resources and patch them using PatchesFrom", Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		key := randomString()
		value := randomString()
		tolerationKey := randomString()

		deploymentLabelPatch := fmt.Sprintf(`patch: |
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: not-important
    labels:
      %s: %s
target:
  kind: Deployment`, key, value)

		serviceLabelPatch := fmt.Sprintf(`patch: |
  apiVersion: v1
  kind: Service
  metadata:
    name: not-important
    labels:
      %s: %s
target:
  kind: Service`, key, value)

		deploymentAnnotationPatch := fmt.Sprintf(`patch: |
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: not-important
    annotations:
      %s: %s
target:
  kind: Deployment`, key, value)

		deploymentTolerationsPatch := fmt.Sprintf(`patch: |
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: not-important
  spec:
    template:
      spec:
        tolerations:
        - key: %s
          operator: Equal
          value: example-value
          effect: NoSchedule
target:
  kind: Deployment`, tolerationKey)

		jsonPatchKey := randomString()
		jsonPatchValue := randomString()
		deploymentJsonPatch := fmt.Sprintf(`patch: |-
  - op: add
    path: /metadata/annotations/%s
    value: %s
target:
  kind: Deployment`, jsonPatchKey, jsonPatchValue)

		configMapPatchesFrom := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: kindWorkloadCluster.GetNamespace(),
				Name:      fmt.Sprintf("%s-patches", kindWorkloadCluster.GetName()),
			},
			Data: map[string]string{
				labelsKey:       deploymentLabelPatch,
				tolerationKey:   deploymentTolerationsPatch,
				serviceLabelKey: serviceLabelPatch,
			},
		}
		Byf("Creating ConfigMap with StrategicMerge %s/%s", configMapPatchesFrom.Namespace, configMapPatchesFrom.Name)
		Expect(k8sClient.Create(context.TODO(), configMapPatchesFrom)).To(Succeed())

		resourceNamespace := randomString()
		resources := fmt.Sprintf(resourcesTemplate, resourceNamespace, resourceNamespace, resourceNamespace)

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		configMapPolicyRef := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), resources)
		Byf("Create a configMap %s/%s with a ServiceAccount/Service/Deployment",
			configMapPolicyRef.Namespace, configMapPolicyRef.Name)
		Expect(k8sClient.Create(context.TODO(), configMapPolicyRef)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMapPolicyRef.Namespace, Name: configMapPolicyRef.Name},
			currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile to reference ConfigMap %s/%s in PolicyRefs",
			configMapPolicyRef.Namespace, configMapPolicyRef.Name)
		Byf("Update ClusterProfile to reference ConfigMap %s/%s in PatchesFrom",
			configMapPatchesFrom.Namespace, configMapPatchesFrom.Name)

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMapPolicyRef.Namespace,
					Name:      configMapPolicyRef.Name,
				},
			}
			currentClusterProfile.Spec.PatchesFrom = []configv1beta1.ValueFrom{
				{
					Namespace: "{{ .Cluster.metadata.namespace}}",
					Name:      "{{ .Cluster.metadata.name}}-patches",
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
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

		Byf("Verifying Deployment %s/nginx-deployment is created in the workload cluster", resourceNamespace)
		Eventually(func() error {
			currentDeployment := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-deployment"},
				currentDeployment)
		}, timeout, pollingInterval).Should(BeNil())

		currentDeployment := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-deployment"},
			currentDeployment)).To(Succeed())

		Byf("Verifying Deployment %s/nginx-deployment has label %s:%s", resourceNamespace, key, value)
		Expect(currentDeployment.Labels).ToNot(BeNil())
		Expect(currentDeployment.Labels[key]).To(Equal(value))

		Byf("Verifying Deployment %s/nginx-deployment does not have annotation %s:%s", resourceNamespace, key, value)
		Expect(currentDeployment.Annotations).ToNot(BeNil())
		Expect(currentDeployment.Annotations[key]).To(Equal(""))

		Byf("Verifying Deployment %s/nginx-deployment has toleration %s", resourceNamespace, tolerationKey)
		Expect(currentDeployment.Spec.Template.Spec.Tolerations).ToNot(BeNil())
		Expect(currentDeployment.Spec.Template.Spec.Tolerations[0].Key).To(Equal(tolerationKey))

		currentService := &corev1.Service{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-service"},
			currentService)).To(Succeed())

		Byf("Verifying Service %s/nginx-service has label %s:%s", resourceNamespace, key, value)
		Expect(currentService.Labels).ToNot(BeNil())
		Expect(currentService.Labels[key]).To(Equal(value))

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		Byf("Modyfing ConfigMap %s/%s with patches", configMapPatchesFrom.Namespace, configMapPatchesFrom.Name)
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMapPatchesFrom.Namespace, Name: configMapPatchesFrom.Name},
			currentConfigMap)).To(Succeed())

		currentConfigMap.Data = map[string]string{
			labelsKey:       deploymentLabelPatch,
			annotationsKey:  deploymentAnnotationPatch,
			tolerationKey:   deploymentTolerationsPatch,
			serviceLabelKey: serviceLabelPatch,
		}
		Byf("Updating ConfigMap with StrategicMerge %s/%s", currentConfigMap.Namespace, currentConfigMap.Name)
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		Byf("Verifying Deployment %s/nginx-deployment is updated in the workload cluster", resourceNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-deployment"},
				currentDeployment)
			if err != nil {
				return false
			}
			if len(currentDeployment.Annotations) == 0 {
				return false
			}
			v := currentDeployment.Annotations[key]
			return v == value
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-deployment"},
			currentDeployment)).To(Succeed())

		Byf("Verifying Deployment %s/nginx-deployment has label %s:%s", resourceNamespace, key, value)
		Expect(currentDeployment.Labels).ToNot(BeNil())
		Expect(currentDeployment.Labels[key]).To(Equal(value))

		Byf("Verifying Deployment %s/nginx-deployment has annotation %s:%s", resourceNamespace, key, value)
		Expect(currentDeployment.Annotations).ToNot(BeNil())
		Expect(currentDeployment.Annotations[key]).To(Equal(value))

		Byf("Verifying Deployment %s/nginx-deployment has toleration %s", resourceNamespace, tolerationKey)
		Expect(currentDeployment.Spec.Template.Spec.Tolerations).ToNot(BeNil())
		Expect(currentDeployment.Spec.Template.Spec.Tolerations[0].Key).To(Equal(tolerationKey))

		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-service"},
			currentService)).To(Succeed())

		Byf("Verifying Service %s/nginx-service has label %s:%s", resourceNamespace, key, value)
		Expect(currentService.Labels).ToNot(BeNil())
		Expect(currentService.Labels[key]).To(Equal(value))

		configMapJsonPatch := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: kindWorkloadCluster.GetNamespace(),
				Name:      fmt.Sprintf("%s-jsonpatch", kindWorkloadCluster.GetName()),
			},
			Data: map[string]string{
				randomString(): deploymentJsonPatch,
			},
		}
		Byf("Creating ConfigMap with JsonPatch %s/%s", configMapJsonPatch.Namespace, configMapJsonPatch.Name)
		Expect(k8sClient.Create(context.TODO(), configMapJsonPatch)).To(Succeed())

		Byf("Update ClusterProfile to also reference ConfigMap %s/%s in PatchesFrom",
			configMapJsonPatch.Namespace, configMapJsonPatch.Name)

		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMapPolicyRef.Namespace,
					Name:      configMapPolicyRef.Name,
				},
			}
			currentClusterProfile.Spec.PatchesFrom = []configv1beta1.ValueFrom{
				{
					Namespace: "{{ .Cluster.metadata.namespace}}",
					Name:      "{{ .Cluster.metadata.name}}-patches",
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				},
				{
					Namespace: "{{ .Cluster.metadata.namespace}}",
					Name:      "{{ .Cluster.metadata.name}}-jsonpatch",
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Verifying Deployment %s/nginx-deployment is updated in the workload cluster", resourceNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-deployment"},
				currentDeployment)
			if err != nil {
				return false
			}
			if len(currentDeployment.Annotations) == 0 {
				return false
			}
			v := currentDeployment.Annotations[jsonPatchKey]
			return v == jsonPatchValue
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceNamespace, Name: "nginx-deployment"},
			currentDeployment)).To(Succeed())

		Byf("Verifying Deployment %s/nginx-deployment has label %s:%s", resourceNamespace, key, value)
		Expect(currentDeployment.Labels).ToNot(BeNil())
		Expect(currentDeployment.Labels[key]).To(Equal(value))

		Byf("Verifying Deployment %s/nginx-deployment has annotation %s:%s", resourceNamespace, key, value)
		Expect(currentDeployment.Annotations).ToNot(BeNil())
		Expect(currentDeployment.Annotations[key]).To(Equal(value))

		Byf("Verifying Deployment %s/nginx-deployment has toleration %s", resourceNamespace, tolerationKey)
		Expect(currentDeployment.Spec.Template.Spec.Tolerations).ToNot(BeNil())
		Expect(currentDeployment.Spec.Template.Spec.Tolerations[0].Key).To(Equal(tolerationKey))

		deleteClusterProfile(clusterProfile)

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMapPatchesFrom.Namespace, Name: configMapPatchesFrom.Name},
			currentConfigMap)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentConfigMap))

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMapJsonPatch.Namespace, Name: configMapJsonPatch.Name},
			currentConfigMap)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentConfigMap))
	})
})
