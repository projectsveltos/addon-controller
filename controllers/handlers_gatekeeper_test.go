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

const (
	gatekeeperGroup = "externaldata.gatekeeper.sh"

	httpsOnlyConstraint = `apiVersion: templates.gatekeeper.sh/v1
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

	blockWildcarContraint = `apiVersion: templates.gatekeeper.sh/v1
 kind: ConstraintTemplate
 metadata:
   name: k8sblockwildcardingress
   annotations:
	 description: >-
	   Users should not be able to create Ingresses with a blank or wildcard (*) hostname 
	   since that would enable them to intercept traffic for other services in the cluster,
	   even if they don't have access to those services.
 spec:
   crd:
	 spec:
	   names:
		 kind: K8sBlockWildcardIngress
   targets:
	 - target: admission.k8s.gatekeeper.sh
	   rego: |
		 package K8sBlockWildcardIngress
		 contains_wildcard(hostname) = true {
		   hostname == ""
		 }
		 contains_wildcard(hostname) = true {
		   contains(hostname, "*")
		 }
		 violation[{"msg": msg}] {
		   input.review.kind.kind == "Ingress"
		   # object.get is required to detect omitted host fields
		   hostname := object.get(input.review.object.spec.rules[_], "host", "")
		   contains_wildcard(hostname)
		   msg := sprintf("Hostname '%v' is not allowed since it counts as a wildcard, which can be used to intercept traffic from other applications.", [hostname])
		 }`

	podDisruptionBudgetConstraint = `apiVersion: templates.gatekeeper.sh/v1
kind: ConstraintTemplate
metadata:
  name: k8spoddisruptionbudget
  annotations:
    description: >-
      Disallow the following scenarios when deploying PodDisruptionBudgets or resources that implement the 
	  replica subresource (e.g. Deployment, ReplicationController, ReplicaSet, StatefulSet):
      1. Deployment of PodDisruptionBudgets with .spec.maxUnavailable == 0
      2. Deployment of PodDisruptionBudgets with .spec.minAvailable == .spec.replicas of the resource with replica subresource
      This will prevent PodDisruptionBudgets from blocking voluntary disruptions such as node draining.
      https://kubernetes.io/docs/concepts/workloads/pods/disruptions/
spec:
  crd:
    spec:
      names:
        kind: K8sPodDisruptionBudget
  targets:
    - target: admission.k8s.gatekeeper.sh
      rego: |
        package k8spoddisruptionbudget
        violation[{"msg": msg}] {
          input.review.kind.kind == "PodDisruptionBudget"
          pdb := input.review.object
          not valid_pdb_max_unavailable(pdb)
          msg := sprintf(
            "PodDisruptionBudget <%v> has maxUnavailable of 0, only positive integers are allowed for maxUnavailable",
            [pdb.metadata.name],
          )
        }
        violation[{"msg": msg}] {
          obj := input.review.object
          pdb := data.inventory.namespace[obj.metadata.namespace]["policy/v1"].PodDisruptionBudget[_]
          obj.spec.selector.matchLabels == pdb.spec.selector.matchLabels
          not valid_pdb_max_unavailable(pdb)
          msg := sprintf(
            "%v <%v> has been selected by PodDisruptionBudget <%v> but has maxUnavailable of 0, only positive integers are allowed for maxUnavailable",
            [obj.kind, obj.metadata.name, pdb.metadata.name],
          )
        }
        violation[{"msg": msg}] {
          obj := input.review.object
          pdb := data.inventory.namespace[obj.metadata.namespace]["policy/v1"].PodDisruptionBudget[_]
          obj.spec.selector.matchLabels == pdb.spec.selector.matchLabels
          not valid_pdb_min_available(obj, pdb)
          msg := sprintf(
            "%v <%v> has %v replica(s) but PodDisruptionBudget <%v> has minAvailable of %v, 
			PodDisruptionBudget count should always be lower than replica(s), and not used when replica(s) is set to 1",
            [obj.kind, obj.metadata.name, obj.spec.replicas, pdb.metadata.name, pdb.spec.minAvailable, obj.spec.replicas],
          )
        }
        valid_pdb_min_available(obj, pdb) {
          # default to -1 if minAvailable is not set so valid_pdb_min_available is always true
          # for objects with >= 0 replicas. If minAvailable defaults to >= 0, objects with
          # replicas field might violate this constraint if they are equal to the default set here
          min_available := object.get(pdb.spec, "minAvailable", -1)
          obj.spec.replicas > min_available
        }
        valid_pdb_max_unavailable(pdb) {
          # default to 1 if maxUnavailable is not set so valid_pdb_max_unavailable always returns true.
          # If maxUnavailable defaults to 0, it violates this constraint because all pods needs to be
          # available and no pods can be evicted voluntarily
          max_unavailable := object.get(pdb.spec, "maxUnavailable", 1)
          max_unavailable > 0
        }`
)

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

		prepareForDeployment(clusterFeature, clusterSummary, cluster)
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
			if customResourceDefinitions.Items[i].Spec.Group == gatekeeperGroup {
				if customResourceDefinitions.Items[i].Spec.Names.Plural == "providers" {
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
		err := controllers.GenericUndeploy(context.TODO(), c,
			cluster.Namespace, cluster.Name, clusterSummary.Name, string(configv1alpha1.FeatureGatekeeper), klogr.New())
		Expect(err).To(BeNil())
	})

	It("unDeployGatekeeper removes all Gatekeeper policies created by a ClusterSummary", func() {
		// Install ContraintTemplate CRD
		elements := strings.Split(string(gatekeeper.GatekeeperYAML), "---\n")
		for i := range elements {
			policy, err := controllers.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			if policy.GetKind() == customResourceDefinitionCRD {
				Expect(testEnv.Client.Create(context.TODO(), policy)).To(Succeed())
			}
		}

		// Wait for cache to be updated
		Eventually(func() bool {
			customResourceDefinitionList := &apiextensionsv1.CustomResourceDefinitionList{}
			err := testEnv.List(context.TODO(), customResourceDefinitionList)
			if err != nil {
				return false
			}
			for i := range customResourceDefinitionList.Items {
				if customResourceDefinitionList.Items[i].Spec.Group == gatekeeperGroup {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

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

		// Wait for cache to be updated
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)
			return err == nil &&
				currentClusterSummary.Status.FeatureSummaries != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.GenericUndeploy(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
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

	It("applyAuditOptions adds options to audit deployment", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration = &configv1alpha1.GatekeeperConfiguration{
			AuditInterval:  60,
			AuditFromCache: true,
			AuditChunkSize: 300,
		}

		// Deploy gatekeper so audit deployment is created.
		Expect(controllers.DeployGatekeeperInWorklaodCluster(context.TODO(), c, klogr.New())).To(Succeed())

		Expect(controllers.ApplyAuditOptions(context.TODO(), c, clusterSummary, klogr.New())).To(Succeed())

		auditDepl := &appsv1.Deployment{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: gatekeeper.Namespace, Name: gatekeeper.AuditDeployment},
			auditDepl)).To(Succeed())

		Expect(len(auditDepl.Spec.Template.Spec.Containers)).To(Equal(1))
		auditChunkSize := fmt.Sprintf("--audit-chunk-size=%d", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditChunkSize)
		Expect(auditDepl.Spec.Template.Spec.Containers[0].Args).To(ContainElement(auditChunkSize))

		auditFromCache := fmt.Sprintf("--audit-from-cache=%t", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditFromCache)
		Expect(auditDepl.Spec.Template.Spec.Containers[0].Args).To(ContainElement(auditFromCache))

		auditInterval := fmt.Sprintf("--audit-interval=%d", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditInterval)
		Expect(auditDepl.Spec.Template.Spec.Containers[0].Args).To(ContainElement(auditInterval))
	})
})

var _ = Describe("Hash methods", func() {
	It("gatekeeperHash returns hash considering all referenced configmap contents", func() {
		configMapNs := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), httpsOnlyConstraint)
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(), blockWildcarContraint)
		configMap3 := createConfigMapWithPolicy(configMapNs, randomString(), podDisruptionBudgetConstraint)

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
							{Namespace: configMapNs, Name: configMap2.Name},
							{Namespace: configMapNs, Name: configMap3.Name},
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			configMap1,
			configMap2,
			configMap3,
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
		config += render.AsCode(configMap3.Data)
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.GatekeeperHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
