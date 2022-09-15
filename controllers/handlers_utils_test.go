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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
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
  name: nginx
  namespace: %s
spec:
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: nginx
  replicas: 3
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx
        ports:
        - containerPort: 80`
)

var _ = Describe("HandlersUtils", func() {
	var clusterSummary *configv1alpha1.ClusterSummary
	var clusterFeature *configv1alpha1.ClusterFeature
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

		cluster := &clusterv1.Cluster{
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

		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())

		Expect(waitForObject(ctx, testEnv.Client, configMap)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		policies, err := controllers.DeployContentOfConfigMap(context.TODO(), testEnv.Config, testEnv.Client,
			configMap, clusterSummary, klogr.New())
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(3))
	})

	It(`undeployStaleResources removes all policies created by ClusterSummary due to ConfigMaps not referenced anymore`, func() {
		configMapNs := randomString()
		viewClusterRoleName := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(viewClusterRole, viewClusterRoleName))
		editClusterRoleName := randomString()
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(editClusterRole, editClusterRoleName))

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterFeatureSpec.PolicyRefs = []corev1.ObjectReference{
			{Namespace: configMapNs, Name: configMap1.Name},
			{Namespace: configMapNs, Name: configMap2.Name},
		}
		Expect(testEnv.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Wait for cache to be updated
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)
			return err == nil &&
				currentClusterSummary.Spec.ClusterFeatureSpec.PolicyRefs != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(addTypeInformationToObject(testEnv.Scheme(), currentClusterSummary)).To(Succeed())

		clusterRoleName1 := controllers.GetPolicyName(viewClusterRoleName, currentClusterSummary)
		clusterRole1 := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterRoleName1,
				Labels: map[string]string{
					controllers.ConfigLabelNamespace: configMap1.Namespace,
					controllers.ConfigLabelName:      configMap1.Name,
				},
			},
		}

		clusterRoleName2 := controllers.GetPolicyName(editClusterRoleName, currentClusterSummary)
		clusterRole2 := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterRoleName2,
				Namespace: "default",
				Labels: map[string]string{
					controllers.ConfigLabelNamespace: configMap2.Namespace,
					controllers.ConfigLabelName:      configMap2.Name,
				},
			},
		}

		// Add list of GroupVersionKind this ClusterSummary has deployed in the CAPI Cluster
		// because of the PolicyRefs feature. This is used by UndeployStaleResources.
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureResources,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"ClusterRole.v1.rbac.authorization.k8s.io",
				},
			},
		}

		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole1)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole2)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, clusterRole2)).To(Succeed())

		addOwnerReference(context.TODO(), testEnv.Client, clusterRole1, currentClusterSummary)
		addOwnerReference(context.TODO(), testEnv.Client, clusterRole2, currentClusterSummary)

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterRole1)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterRole2)).To(Succeed())

		currentClusterRoles := map[string]configv1alpha1.Resource{}
		clusterRoleResource1 := &configv1alpha1.Resource{
			Name:  clusterRole1.Name,
			Kind:  clusterRole1.GroupVersionKind().Kind,
			Group: clusterRole1.GetObjectKind().GroupVersionKind().Group,
		}
		currentClusterRoles[controllers.GetPolicyInfo(clusterRoleResource1)] = *clusterRoleResource1
		clusterRoleResource2 := &configv1alpha1.Resource{
			Name:  clusterRole2.Name,
			Kind:  clusterRole2.GroupVersionKind().Kind,
			Group: clusterRole2.GetObjectKind().GroupVersionKind().Group,
		}
		currentClusterRoles[controllers.GetPolicyInfo(clusterRoleResource2)] = *clusterRoleResource2

		deployedGKVs := controllers.GetDeployedGroupVersionKinds(currentClusterSummary, configv1alpha1.FeatureResources)
		Expect(deployedGKVs).ToNot(BeEmpty())
		// undeployStaleResources finds all instances of policies deployed because of clusterSummary and
		// removes the stale ones.
		err := controllers.UndeployStaleResources(context.TODO(), testEnv.Config, testEnv.Client, currentClusterSummary,
			deployedGKVs, currentClusterRoles)
		Expect(err).To(BeNil())

		// Consistently loop so testEnv Cache is synced
		Consistently(func() error {
			// Since ClusterSummary is referencing configMap, expect ClusterRole to not be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName1}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		// Consistently loop so testEnv Cache is synced
		Consistently(func() error {
			// Since ClusterSummary is referencing configMap, expect Policy to not be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName2}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		currentClusterSummary.Spec.ClusterFeatureSpec.PolicyRefs = nil
		delete(currentClusterRoles, controllers.GetPolicyInfo(clusterRoleResource1))
		delete(currentClusterRoles, controllers.GetPolicyInfo(clusterRoleResource2))

		err = controllers.UndeployStaleResources(context.TODO(), testEnv.Config, testEnv.Client, currentClusterSummary,
			deployedGKVs, currentClusterRoles)
		Expect(err).To(BeNil())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			// Since ClusterSummary is not referencing configMap with ClusterRole, expect ClusterRole to be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName1}, currentClusterRole)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			// Since ClusterSummary is not referencing configMap with ClusterRole, expect ClusterRole to be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName2}, currentClusterRole)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
