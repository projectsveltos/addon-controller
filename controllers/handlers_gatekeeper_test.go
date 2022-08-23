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
	"strings"

	"github.com/gdexlab/go-render/render"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	opav1 "github.com/open-policy-agent/frameworks/constraint/pkg/apis/templates/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/gatekeeper"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

const gatekeeperConstraint = `apiVersion: templates.gatekeeper.sh/v1
kind: ConstraintTemplate
metadata:
  name: k8shttpsonly
  annotations:
    description: >-
      Requires Ingress resources to be HTTPS only.
      Ingress resources must:
      - include a valid TLS configuration
      - include the kubernetes.io/ingress.allow-http annotation, set to
        false.
      https://kubernetes.io/docs/concepts/services-networking/ingress/#tls
spec:
  crd:
    spec:
      names:
        kind: K8sHttpsOnly
  targets:
    - target: admission.k8s.gatekeeper.sh
      rego: |
        package k8shttpsonly
        violation[{"msg": msg}] {
          input.review.object.kind == "Ingress"
          re_match("^(extensions|networking.k8s.io)/", input.review.object.apiVersion)
          ingress := input.review.object
          not https_complete(ingress)
          msg := sprintf("Ingress should be https. tls configuration and allow-http=false annotation are required for %v", [ingress.metadata.name])
        }
        https_complete(ingress) = true {
          ingress.spec["tls"]
          count(ingress.spec.tls) > 0
          ingress.metadata.annotations["kubernetes.io/ingress.allow-http"] == "false"
        }`

var _ = Describe("HandlersGatekeeper", func() {
	var logger logr.Logger
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

		logger = klogr.New()
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

	It("isGatekeeperReady returns true when Gatekeepers deployments are ready", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		replicas := int32(1)
		for i := range gatekeeper.Deployments {
			currentDepl := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: gatekeeper.Namespace,
					Name:      gatekeeper.Deployments[i],
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
				},
			}
			Expect(c.Create(context.TODO(), currentDepl)).To(Succeed())
		}

		present, ready, err := controllers.IsGatekeeperReady(context.TODO(), c, klogr.New())
		Expect(err).To(BeNil())
		Expect(present).To(BeTrue())
		Expect(ready).To(BeFalse())

		for i := range gatekeeper.Deployments {
			currentDepl := &appsv1.Deployment{}
			Expect(c.Get(context.TODO(),
				types.NamespacedName{Namespace: gatekeeper.Namespace, Name: gatekeeper.Deployments[i]},
				currentDepl)).To(Succeed())
			currentDepl.Status.AvailableReplicas = 1
			currentDepl.Status.Replicas = 1
			currentDepl.Status.ReadyReplicas = 1
			Expect(c.Status().Update(context.TODO(), currentDepl)).To(Succeed())
		}

		present, ready, err = controllers.IsGatekeeperReady(context.TODO(), c, klogr.New())
		Expect(err).To(BeNil())
		Expect(present).To(BeTrue())
		Expect(ready).To(BeTrue())
	})

	It("deployGatekeeperInWorklaodCluster installs Gatekeeper CRDs in a cluster", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration = &configv1alpha1.GatekeeperConfiguration{}

		Expect(controllers.DeployGatekeeperInWorklaodCluster(context.TODO(), c, klogr.New())).To(Succeed())

		customResourceDefinitions := &apiextensionsv1.CustomResourceDefinitionList{}
		Expect(c.List(context.TODO(), customResourceDefinitions)).To(Succeed())
		constrainttemplatesFound := false
		for i := range customResourceDefinitions.Items {
			if customResourceDefinitions.Items[i].Spec.Group == "templates.gatekeeper.sh" {
				if customResourceDefinitions.Items[i].Spec.Names.Plural == "constrainttemplates" {
					constrainttemplatesFound = true
				}
			}
		}
		Expect(constrainttemplatesFound).To(BeTrue())
	})

	It("unDeployGatekeeper does nothing when CAPI Cluster is not found", func() {
		initObjects := []client.Object{
			clusterSummary,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
		err := controllers.UnDeployGatekeeper(context.TODO(), c,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).To(BeNil())
	})

	It("unDeployGatekeeper removes all Gatekeeper policies created by a ClusterSummary", func() {
		// Install ContraintTemplate CRD
		elements := strings.Split(string(gatekeeper.GatekeeperYAML), "---\n")
		for i := range elements {
			policy, err := controllers.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			if policy.GetKind() == "CustomResourceDefinition" {
				Expect(testEnv.Client.Create(context.TODO(), policy)).To(Succeed())
			}
		}

		ct0 := &opav1.ConstraintTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		ct1 := &opav1.ConstraintTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					controllers.ConfigLabelName:      randomString(),
					controllers.ConfigLabelNamespace: randomString(),
				},
			},
		}

		ct2 := &opav1.ConstraintTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					controllers.ConfigLabelName:      randomString(),
					controllers.ConfigLabelNamespace: randomString(),
				},
			},
		}

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

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), ct1)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), ct2)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, ct2)).To(Succeed())

		addOwnerReference(ctx, testEnv.Client, ct1, clusterSummary)
		addOwnerReference(ctx, testEnv.Client, ct2, clusterSummary)

		Expect(testEnv.Client.Create(context.TODO(), ct0)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ct0)).To(Succeed())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureGatekeeper,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"ConstraintTemplate.v1.templates.gatekeeper.sh",
				},
			},
		}
		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(controllers.UnDeployGatekeeper(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(configv1alpha1.FeatureGatekeeper), logger)).To(Succeed())

		// UnDeployGatekeeper finds all gatekeeper policies deployed because of a clusterSummary and deletes those.
		// Expect all constraintTemplate policies but ct0 (ConfigLabelName is not set on it) to be deleted.

		policy := &opav1.ConstraintTemplate{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: ct0.Name}, policy)
			if err != nil {
				return false
			}
			err = testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: ct1.Name}, policy)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("deployGatekeeper returns an error when CAPI Cluster does not exist", func() {
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		err := controllers.DeployGatekeeper(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).ToNot(BeNil())
	})

	It("hasContraintTemplates returns true only if configmap contains a ConsraintTemplate", func() {
		configMap := createConfigMapWithPolicy(randomString(), randomString(), gatekeeperConstraint)
		Expect(controllers.HasContraintTemplates(configMap, klogr.New())).To(BeTrue())

		configMap = createConfigMapWithPolicy(randomString(), randomString(), fmt.Sprintf(addLabelPolicyStr, randomString()))
		Expect(controllers.HasContraintTemplates(configMap, klogr.New())).To(BeFalse())
	})

	It("sortConfigMapByConstraintsFirst sorts a ConfigMap slice by putting ConfigMaps with ConstraintTemplate first", func() {
		configMaps := make([]corev1.ConfigMap, 0)

		configMap1 := createConfigMapWithPolicy(randomString(), randomString(), fmt.Sprintf(addLabelPolicyStr, randomString()))
		configMaps = append(configMaps, *configMap1)

		configMap2 := createConfigMapWithPolicy(randomString(), randomString(), gatekeeperConstraint)
		configMaps = append(configMaps, *configMap2)

		configMap3 := createConfigMapWithPolicy(randomString(), randomString(), fmt.Sprintf(checkSa, randomString()))
		configMaps = append(configMaps, *configMap3)

		sortedSlice, err := controllers.SortConfigMapByConstraintsFirst(configMaps, klogr.New())
		Expect(err).To(BeNil())
		Expect(sortedSlice).ToNot(BeNil())
		Expect(len(sortedSlice)).To(Equal(len(configMaps)))
		Expect(sortedSlice[0].Name).To(Equal(configMap2.Name))
		Expect(sortedSlice).To(ContainElement(*configMap3))
		Expect(sortedSlice).To(ContainElement(*configMap1))
	})
})

var _ = Describe("Hash methods", func() {
	It("gatekeeperHash returns hash considering all referenced configmap contents", func() {
		configMapNs := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), gatekeeperConstraint)

		namespace := "reconcile" + randomString()
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					GatekeeperConfiguration: &configv1alpha1.GatekeeperConfiguration{
						PolicyRefs: []corev1.ObjectReference{
							{Namespace: configMapNs, Name: configMap1.Name},
							{Namespace: configMapNs, Name: randomString()},
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			configMap1,
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
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.GatekeeperHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
