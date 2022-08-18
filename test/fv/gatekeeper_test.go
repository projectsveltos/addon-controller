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
	b64 "encoding/base64"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	opav1 "github.com/open-policy-agent/frameworks/constraint/pkg/apis/templates/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/gatekeeper"
)

const (
	replicaLimitisTemplae = `apiVersion: templates.gatekeeper.sh/v1
kind: ConstraintTemplate
metadata:
  name: k8sreplicalimits
  annotations:
    description: >-
      Requires that objects with the field spec.replicas (Deployments,
      ReplicaSets, etc.) specify a number of replicas within defined ranges.
spec:
  crd:
    spec:
      names:
        kind: K8sReplicaLimits
      validation:
        # Schema for the parameters field
        openAPIV3Schema:
          type: object
          properties:
            ranges:
              type: array
              description: Allowed ranges for numbers of replicas.  Values are inclusive.
              items:
                type: object
                description: A range of allowed replicas.  Values are inclusive.
                properties:
                  min_replicas:
                    description: The minimum number of replicas allowed, inclusive.
                    type: integer
                  max_replicas:
                    description: The maximum number of replicas allowed, inclusive.
                    type: integer
  targets:
    - target: admission.k8s.gatekeeper.sh
      rego: |
        package k8sreplicalimits
        deployment_name = input.review.object.metadata.name
        violation[{"msg": msg}] {
            spec := input.review.object.spec
            not input_replica_limit(spec)
            msg := sprintf("The provided number of replicas is not allowed for deployment: %v. Allowed ranges: %v", [deployment_name, input.parameters])
        }
        input_replica_limit(spec) {
            provided := input.review.object.spec.replicas
            count(input.parameters.ranges) > 0
            range := input.parameters.ranges[_]
            value_within_range(range, provided)
        }
        value_within_range(range, value) {
            range.min_replicas <= value
            range.max_replicas >= value
        }`

	replicaLimitis = `apiVersion: constraints.gatekeeper.sh/v1beta1
kind: K8sReplicaLimits
metadata:
  name: replica-limits
spec:
  match:
    kinds:
      - apiGroups: ["apps"]
        kinds: ["Deployment"]
  parameters:
    ranges:
    - min_replicas: 1
      max_replicas: 50`

	disallowLatestTemplate = `apiVersion: templates.gatekeeper.sh/v1
kind: ConstraintTemplate
metadata:
  name: k8sdisallowedtags
  annotations:
    description: >-
      Requires container images to have an image tag different from the ones in
      the specified list.
      https://kubernetes.io/docs/concepts/containers/images/#image-names
spec:
  crd:
    spec:
      names:
        kind: K8sDisallowedTags
      validation:
        # Schema for the parameters field
        openAPIV3Schema:
          type: object
          properties:
            exemptImages:
              description: >-
                Any container that uses an image that matches an entry in this list will be excluded
                from enforcement. Prefix-matching can be signified with "*". For example: "my-image-*"".
                It is recommended that users use the fully-qualified Docker image name (e.g. start with a domain name)
                in order to avoid unexpectedly exempting images from an untrusted repository.
              type: array
              items:
                type: string
            tags:
              type: array
              description: Disallowed container image tags.
              items:
                type: string
  targets:
    - target: admission.k8s.gatekeeper.sh
      rego: |
        package k8sdisallowedtags
        import data.lib.exempt_container.is_exempt
        violation[{"msg": msg}] {
            container := input_containers[_]
            not is_exempt(container)
            tags := [forbid | tag = input.parameters.tags[_] ; forbid = endswith(container.image, concat(":", ["", tag]))]
            any(tags)
            msg := sprintf("container <%v> uses a disallowed tag <%v>; disallowed tags are %v", [container.name, container.image, input.parameters.tags])
        }
        violation[{"msg": msg}] {
            container := input_containers[_]
            not is_exempt(container)
            tag := [contains(container.image, ":")]
            not all(tag)
            msg := sprintf("container <%v> didn't specify an image tag <%v>", [container.name, container.image])
        }
        input_containers[c] {
            c := input.review.object.spec.containers[_]
        }
        input_containers[c] {
            c := input.review.object.spec.initContainers[_]
        }
        input_containers[c] {
            c := input.review.object.spec.ephemeralContainers[_]
        }
      libs:
        - |
          package lib.exempt_container
          is_exempt(container) {
              exempt_images := object.get(object.get(input, "parameters", {}), "exemptImages", [])
              img := container.image
              exemption := exempt_images[_]
              _matches_exemption(img, exemption)
          }
          _matches_exemption(img, exemption) {
              not endswith(exemption, "*")
              exemption == img
          }
          _matches_exemption(img, exemption) {
              endswith(exemption, "*")
              prefix := trim_suffix(exemption, "*")
              startswith(img, prefix)
          }`

	disallowLatest = `apiVersion: constraints.gatekeeper.sh/v1beta1
kind: K8sDisallowedTags
metadata:
  name: container-image-must-not-have-latest-tag
spec:
  match:
    kinds:
      - apiGroups: [""]
        kinds: ["Pod"]
    namespaces:
      - "default"
  parameters:
    tags: ["latest"]
    exemptImages: ["openpolicyagent/opa-exp:latest", "openpolicyagent/opa-exp2:latest"]`
)

var _ = Describe("Gatekeeper", func() {
	const (
		namePrefix = "gatekeeper"
	)

	It("Deploy and updates Gatkeeper correctly", Label("FV"), func() {
		Byf("Add configMap containing gatekeeper policy")
		replicaLimitisTemplateEncoded := b64.StdEncoding.EncodeToString([]byte(replicaLimitisTemplae))
		configMapTemplate := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      namePrefix + randomString(),
			},
			Data: map[string]string{
				"data": replicaLimitisTemplateEncoded,
			},
		}
		Expect(k8sClient.Create(context.TODO(), configMapTemplate)).To(Succeed())

		replicaLimitisEncoded := b64.StdEncoding.EncodeToString([]byte(replicaLimitis))
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      namePrefix + randomString(),
			},
			Data: map[string]string{
				"data": replicaLimitisEncoded,
			},
		}
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterFeature matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterFeature := getClusterfeature(namePrefix, map[string]string{key: value})
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterFeature.Spec.GatekeeperConfiguration = &configv1alpha1.GatekeeperConfiguration{
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMapTemplate.Namespace, Name: configMapTemplate.Name},
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

		for i := range gatekeeper.Deployments {
			Byf("Verifying Gatekeper deployment %s/%s is present", gatekeeper.Namespace, gatekeeper.Deployments[i])
			Eventually(func() bool {
				depl := &appsv1.Deployment{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: gatekeeper.Namespace, Name: gatekeeper.Deployments[i]}, depl)
				return err == nil
			}, timeout, pollingInterval).Should(BeTrue())
		}

		policyName := "k8sreplicalimits"
		Byf("Verifying Gatekeeper ConstraintTemplate CRD %s is present", policyName)
		Eventually(func() error {
			policy := &opav1.ConstraintTemplate{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: policyName}, policy)
			return err
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Gatekeeper", clusterSummary.Name)
		verifyFeatureStatus(clusterSummary.Name, configv1alpha1.FeatureGatekeeper, configv1alpha1.FeatureStatusProvisioned)

		Byf("Modifying configMap")
		currentConfigMapTemplate := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMapTemplate.Namespace, Name: configMapTemplate.Name}, currentConfigMapTemplate)).To(Succeed())
		disallowLatestTemplateEncoded := b64.StdEncoding.EncodeToString([]byte(disallowLatestTemplate))
		currentConfigMapTemplate.Data["data"] = disallowLatestTemplateEncoded
		Expect(k8sClient.Update(context.TODO(), currentConfigMapTemplate)).To(Succeed())

		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		disallowLatestEncoded := b64.StdEncoding.EncodeToString([]byte(disallowLatest))
		currentConfigMap.Data["data"] = disallowLatestEncoded
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		policyName = "k8sdisallowedtags"
		Byf("Verifying Gatekeeper ConstraintTemplate %s is present", policyName)
		Eventually(func() error {
			policy := &opav1.ConstraintTemplate{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: policyName}, policy)
			return err
		}, timeout, pollingInterval).Should(BeNil())

		Byf("changing clusterfeature to not require any gatekeeper configuration anymore")
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.GatekeeperConfiguration = nil
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		Byf("Verifying Gatekeeper ConstraintTemplate %s is gone", policyName)
		Eventually(func() bool {
			policy := &opav1.ConstraintTemplate{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: policyName}, policy)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterFeature(clusterFeature)
	})
})
