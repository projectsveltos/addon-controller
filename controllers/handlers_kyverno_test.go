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
	"reflect"

	"github.com/gdexlab/go-render/render"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	kyvernoapi "github.com/kyverno/kyverno/api/kyverno/v1"
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
	"github.com/projectsveltos/cluster-api-feature-manager/internal/kyverno"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

var _ = Describe("HandlersKyverno", func() {
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

	It("isKyvernoReady returns true when Kyverno deployment is ready", func() {
		Expect(controllers.DeployKyvernoInWorklaodCluster(context.TODO(), testEnv, 1, klogr.New())).To(Succeed())

		currentDepl := &appsv1.Deployment{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: kyverno.Namespace, Name: kyverno.Deployment}, currentDepl)).To(Succeed())

		// testEnv does not have a deployment controller, so status is not updated
		present, ready, err := controllers.IsKyvernoReady(context.TODO(), testEnv.Client, klogr.New())
		Expect(err).To(BeNil())
		Expect(present).To(BeTrue())
		Expect(ready).To(BeFalse())

		currentDepl.Status.AvailableReplicas = 1
		currentDepl.Status.Replicas = 1
		currentDepl.Status.ReadyReplicas = 1
		Expect(testEnv.Status().Update(context.TODO(), currentDepl)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			present, ready, err = controllers.IsKyvernoReady(context.TODO(), testEnv.Client, klogr.New())
			return err == nil && present && ready
		}, timeout, pollingInterval).Should(BeTrue())

	})

	It("deployKyvernoInWorklaodCluster installs kyverno CRDs in a cluster", func() {
		Expect(controllers.DeployKyvernoInWorklaodCluster(context.TODO(), testEnv, 1, klogr.New())).To(Succeed())

		customResourceDefinitions := &apiextensionsv1.CustomResourceDefinitionList{}
		Expect(testEnv.List(context.TODO(), customResourceDefinitions)).To(Succeed())
		clusterPolicyFound := false
		policyFound := false
		for i := range customResourceDefinitions.Items {
			if customResourceDefinitions.Items[i].Spec.Group == "kyverno.io" {
				if customResourceDefinitions.Items[i].Spec.Names.Plural == "clusterpolicies" {
					clusterPolicyFound = true
				}
				if customResourceDefinitions.Items[i].Spec.Names.Plural == "policies" {
					policyFound = true
				}
			}
		}
		Expect(clusterPolicyFound).To(BeTrue())
		Expect(policyFound).To(BeTrue())
	})

	It("deployKyvernoPolicy creates and updates kyverno policies", func() {
		configMapNs := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), addLabelPolicyStr)
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(), checkSa)

		clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMapNs, Name: configMap1.Name},
				{Namespace: configMapNs, Name: configMap2.Name},
			},
		}

		Expect(controllers.DeployKyvernoInWorklaodCluster(context.TODO(), testEnv.Client, 1, klogr.New())).To(Succeed())

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), configMap1)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), configMap2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, configMap2)).To(Succeed())

		waitForClusterSummaryPolicyPrefix(context.TODO(), testEnv.Client, clusterSummary)

		// Get current clustersummary as we need Status.PolicyPrefix to be set
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(ctx, types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())

		currentKyvernos := make(map[string]bool)
		Expect(controllers.DeployKyvernoPolicy(context.TODO(), testEnv.Config, testEnv.Client, configMap1,
			currentClusterSummary, currentKyvernos, logger)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterPolicyList := &kyvernoapi.ClusterPolicyList{}
			err := testEnv.List(context.TODO(), clusterPolicyList)
			if err != nil {
				return false
			}
			return len(clusterPolicyList.Items) > 0
		}, timeout, pollingInterval).Should(BeTrue())

		kyvernoPolicyName := controllers.GetPolicyName("add-labels", currentClusterSummary)

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterPolicy := &kyvernoapi.ClusterPolicy{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: kyvernoPolicyName}, clusterPolicy)
			if err != nil {
				return false
			}
			v, ok := clusterPolicy.Labels[controllers.ClusterSummaryLabelName]
			return ok &&
				v == clusterSummary.Name
		}, timeout, pollingInterval).Should(BeTrue())

		version := configMap1.GetResourceVersion()
		configMap1 = createConfigMapWithPolicy(configMapNs, configMap1.Name, addLabelPolicyStr, allowLabelChangeStr)
		configMap1.SetResourceVersion(version)

		Expect(testEnv.Update(context.TODO(), configMap1)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			err := controllers.DeployKyvernoPolicy(context.TODO(), testEnv.Config, testEnv.Client, configMap1,
				currentClusterSummary, currentKyvernos, logger)
			if err != nil {
				return false
			}
			kyvernoPolicyName := controllers.GetPolicyName("allowed-label-changes", currentClusterSummary)
			clusterPolicy := &kyvernoapi.ClusterPolicy{}
			err = testEnv.Get(context.TODO(), types.NamespacedName{Name: kyvernoPolicyName}, clusterPolicy)
			if err != nil {
				return false
			}
			v, ok := clusterPolicy.Labels[controllers.ClusterSummaryLabelName]
			return ok &&
				v == clusterSummary.Name
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("unDeployKyverno does nothing when CAPI Cluster is not found", func() {
		initObjects := []client.Object{
			clusterSummary,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
		err := controllers.UnDeployKyverno(context.TODO(), c,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).To(BeNil())
	})

	It("unDeployKyverno removes all Kyverno policies created by a ClusterSummary", func() {
		kyverno0 := &kyvernoapi.ClusterPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		kyverno1 := &kyvernoapi.ClusterPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:   randomString(),
				Labels: map[string]string{controllers.ClusterSummaryLabelName: clusterSummary.Name},
			},
		}

		kyverno2 := &kyvernoapi.Policy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
				Labels:    map[string]string{controllers.ClusterSummaryLabelName: clusterSummary.Name},
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
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), kyverno0)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), kyverno1)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), kyverno2)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())
		waitForClusterSummaryPolicyPrefix(context.TODO(), testEnv.Client, clusterSummary)

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureKyverno,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"ClusterPolicy.v1.kyverno.io",
					"Policy.v1.kyverno.io",
				},
			},
		}
		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(controllers.UnDeployKyverno(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(configv1alpha1.FeatureKyverno), logger)).To(Succeed())

		// UnDeployKyverno finds all kyverno policies deployed because of a clusterSummary and deletes those.
		// Expect all kyverno policies but Kyverno0 (ClusterSummaryLabelName is not set on it) to be deleted.

		clusterPolicy := &kyvernoapi.ClusterPolicy{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: kyverno0.Name}, clusterPolicy)
			if err != nil {
				return false
			}
			err = testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: kyverno1.Name}, clusterPolicy)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		policy := &kyvernoapi.Policy{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: kyverno2.Name}, policy)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("deployKyverno returns an error when CAPI Cluster does not exist", func() {
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		err := controllers.DeployKyverno(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).ToNot(BeNil())
	})

	It("deployKyverno deploys kyverno deployment", func() {
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
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

		waitForClusterSummaryPolicyPrefix(context.TODO(), testEnv.Client, clusterSummary)

		Expect(controllers.DeployKyverno(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: kyverno.Namespace, Name: kyverno.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deployKyverno deploys kyverno policies", func() {
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
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

		waitForClusterSummaryPolicyPrefix(context.TODO(), testEnv.Client, clusterSummary)

		Expect(controllers.DeployKyverno(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			depl := &appsv1.Deployment{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: kyverno.Namespace, Name: kyverno.Deployment}, depl)
		}, timeout, pollingInterval).Should(BeNil())

		By("Creating ConfigMap with Kyverno ClusterPolicy")
		configMap := createConfigMapWithPolicy(namespace, randomString(), addLabelPolicyStr)
		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())

		By("Updating ClusterSummary to reference ConfigMap")
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: namespace, Name: configMap.Name},
			},
		}
		Expect(testEnv.Client.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, configMap)).To(Succeed())

		By("Verifying kyverno ClusterPolicy is present")
		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			if err := controllers.DeployKyverno(context.TODO(), testEnv.Client,
				cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New()); err != nil {
				return err
			}

			// add-labels is the name of the policy addLabelPolicyStr
			kyvernoName := controllers.GetPolicyName("add-labels", currentClusterSummary)
			clusterPolicy := &kyvernoapi.ClusterPolicy{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: kyvernoName}, clusterPolicy)
		}, timeout, pollingInterval).Should(BeNil())
	})
})

var _ = Describe("Hash methods", func() {
	It("kyvernoHash returns hash considering all referenced configmap contents", func() {
		configMapNs := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), addLabelPolicyStr)
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(), allowLabelChangeStr)

		namespace := "reconcile" + randomString()
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					KyvernoConfiguration: &configv1alpha1.KyvernoConfiguration{
						PolicyRefs: []corev1.ObjectReference{
							{Namespace: configMapNs, Name: configMap1.Name},
							{Namespace: configMapNs, Name: configMap2.Name},
							{Namespace: configMapNs, Name: randomString()},
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			configMap1,
			configMap2,
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
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.KyvernoHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
