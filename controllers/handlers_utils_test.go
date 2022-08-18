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
	"fmt"

	kyvernoapi "github.com/kyverno/kyverno/api/kyverno/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

const (
	serviceTemplate = `apiVersion: v1
kind: Service
metadata:
  name: service0
  namespace: %s
spec:
  selector:
    app.kubernetes.io/name: service0
  ports:
    - protocol: TCP
      port: 80
      targetPort: 9376
---
apiVersion: v1
kind: Service
metadata:
  name: service1
  namespace: %s
spec:
  selector:
    app.kubernetes.io/name: service1
  ports:
  - name: name-of-service-port
    protocol: TCP
    port: 80
    targetPort: http-web-svc
`

	deplTemplate = `apiVersion: apps/v1
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
      containers:
      - name: nginx
        image: nginx:1.14.2
        ports:
        - containerPort: 80`
)

var _ = Describe("HandlersUtils", func() {
	var clusterSummary *configv1alpha1.ClusterSummary
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()
		clusterName := upstreamClusterNamePrefix + randomString()
		clusterFeatureName := clusterFeatureNamePrefix + randomString()
		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeatureName, namespace, clusterName)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      clusterName,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterFeatureName, namespace, clusterName)
	})

	It("addClusterSummaryLabel adds label with clusterSummary name", func() {
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}

		controllers.AddLabel(role, controllers.ClusterSummaryLabelName, clusterSummary.Name)
		Expect(role.Labels).ToNot(BeNil())
		Expect(len(role.Labels)).To(Equal(1))
		for k := range role.Labels {
			Expect(role.Labels[k]).To(Equal(clusterSummary.Name))
		}

		role.Labels = map[string]string{"reader": "ok"}
		controllers.AddLabel(role, controllers.ClusterSummaryLabelName, clusterSummary.Name)
		Expect(role.Labels).ToNot(BeNil())
		Expect(len(role.Labels)).To(Equal(2))
		found := false
		for k := range role.Labels {
			if role.Labels[k] == clusterSummary.Name {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue())
	})

	It("createNamespace creates namespace", func() {
		initObjects := []client.Object{}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.CreateNamespace(context.TODO(), c, namespace)).To(BeNil())

		currentNs := &corev1.Namespace{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
	})

	It("createNamespace returns no error if namespace already exists", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		initObjects := []client.Object{ns}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.CreateNamespace(context.TODO(), c, namespace)).To(BeNil())

		currentNs := &corev1.Namespace{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
	})

	It("deployContentOfConfigMap deploys all policies contained in a ConfigMap", func() {
		services := fmt.Sprintf(serviceTemplate, namespace, namespace)
		depl := fmt.Sprintf(deplTemplate, namespace)

		configMap := createConfigMapWithPolicy(namespace, randomString(), depl, services)

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())

		Expect(waitForObject(ctx, testEnv.Client, configMap)).To(Succeed())

		// Get current clustersummary as we need Status.PolicyPrefix to be set
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(ctx, types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())

		policies, err := controllers.DeployContentOfConfigMap(context.TODO(), testEnv.Config, testEnv.Client,
			configMap, currentClusterSummary, klogr.New())
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(3))
		Expect(policies).To(ContainElement(fmt.Sprintf("Service.:%s:service0",
			namespace)))
		Expect(policies).To(ContainElement(fmt.Sprintf("Service.:%s:service1",
			namespace)))
		Expect(policies).To(ContainElement(fmt.Sprintf("Deployment.apps:%s:nginx-deployment",
			namespace)))
	})

	It(`undeployStaleResources removes all policies created by ClusterSummary due to ConfigMaps not referenced anymore`, func() {
		configMapNs := randomString()
		addLabelPolicyName := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(addLabelPolicyStr, addLabelPolicyName))
		checkSaName := randomString()
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(checkSa, checkSaName))

		clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMapNs, Name: configMap1.Name},
				{Namespace: configMapNs, Name: configMap2.Name},
			},
		}

		// install kyverno so crds are present. this is needed to create kyverno policies with testenv
		Expect(controllers.DeployKyvernoInWorklaodCluster(context.TODO(), testEnv, 1, klogr.New())).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, clusterSummary)).To(Succeed())

		// Get current clustersummary as we need Status.PolicyPrefix to be set
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(ctx, types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), currentClusterSummary)).To(Succeed())

		kyvernoName1 := controllers.GetPolicyName(addLabelPolicyName, currentClusterSummary)
		kyverno1 := &kyvernoapi.ClusterPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: kyvernoName1,
				Labels: map[string]string{
					controllers.ConfigLabelNamespace: configMap1.Namespace,
					controllers.ConfigLabelName:      configMap1.Name,
				},
			},
		}

		kyvernoName2 := controllers.GetPolicyName(checkSaName, currentClusterSummary)
		kyverno2 := &kyvernoapi.Policy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kyvernoName2,
				Namespace: "default",
				Labels: map[string]string{
					controllers.ConfigLabelNamespace: configMap2.Namespace,
					controllers.ConfigLabelName:      configMap2.Name,
				},
			},
		}

		// Add list of GroupVersionKind this ClusterSummary has deployed in the CAPI Cluster
		// because of the Kyverno feature. This is used by UndeployStaleResources.
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
		Expect(testEnv.Client.Create(context.TODO(), kyverno1)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), kyverno2)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, kyverno2)).To(Succeed())

		addOwnerReference(context.TODO(), testEnv.Client, kyverno1, currentClusterSummary)
		addOwnerReference(context.TODO(), testEnv.Client, kyverno2, currentClusterSummary)

		Expect(addTypeInformationToObject(testEnv.Scheme(), kyverno1)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), kyverno2)).To(Succeed())

		currentKyvernos := map[string]bool{}
		currentKyvernos[controllers.GetPolicyInfo(kyverno1)] = true
		currentKyvernos[controllers.GetPolicyInfo(kyverno2)] = true

		deployedGKVs := controllers.GetDeployedGroupVersionKinds(currentClusterSummary, configv1alpha1.FeatureKyverno)
		Expect(deployedGKVs).ToNot(BeEmpty())
		// undeployStaleResources finds all instances of policies deployed because of clusterSummary and
		// removes the stale ones.
		err := controllers.UndeployStaleResources(context.TODO(), testEnv.Config, testEnv.Client, currentClusterSummary,
			deployedGKVs, currentKyvernos)
		Expect(err).To(BeNil())

		// Consistently loop so testEnv Cache is synced
		Consistently(func() error {
			// Since ClusterSummary is referencing configMap, expect ClusterPolicy to not be deleted
			currentClusterPolicy := &kyvernoapi.ClusterPolicy{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: kyvernoName1}, currentClusterPolicy)
		}, timeout, pollingInterval).Should(BeNil())

		// Consistently loop so testEnv Cache is synced
		Consistently(func() error {
			// Since ClusterSummary is referencing configMap, expect Policy to not be deleted
			currentPolicy := &kyvernoapi.Policy{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: kyverno2.Namespace, Name: kyvernoName2}, currentPolicy)
		}, timeout, pollingInterval).Should(BeNil())

		currentClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs = nil
		delete(currentKyvernos, controllers.GetPolicyInfo(kyverno1))
		delete(currentKyvernos, controllers.GetPolicyInfo(kyverno2))

		err = controllers.UndeployStaleResources(context.TODO(), testEnv.Config, testEnv.Client, currentClusterSummary,
			deployedGKVs, currentKyvernos)
		Expect(err).To(BeNil())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			// Since ClusterSummary is not referencing configMap with ClusterPolicy, expect ClusterPolicy to be deleted
			currentClusterPolicy := &kyvernoapi.ClusterPolicy{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Name: kyvernoName1}, currentClusterPolicy)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			// Since ClusterSummary is not referencing configMap with Policy, expect Policy to be deleted
			currentPolicy := &kyvernoapi.Policy{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: kyverno2.Namespace, Name: kyvernoName2}, currentPolicy)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
