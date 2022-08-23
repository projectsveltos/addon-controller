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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gdexlab/go-render/render"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

var _ = Describe("HandlersResource", func() {
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

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

	It("DeployResources creates referenced ClusterRole", func() {
		clusterRoleName := randomString()
		configMap := createConfigMapWithPolicy("default", randomString(), fmt.Sprintf(viewClusterRole, clusterRoleName))

		clusterSummary.Spec.ClusterFeatureSpec.ResourceRefs = []corev1.ObjectReference{
			{Namespace: configMap.Namespace, Name: configMap.Name},
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
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			return controllers.DeployResources(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
				string(configv1alpha1.FeatureResources), klogr.New())
		}, timeout, pollingInterval).Should(BeNil())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			currentClusterRole := &rbacv1.ClusterRole{}
			return testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: clusterRoleName}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		currentClusterRole := &rbacv1.ClusterRole{}
		Expect(testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: clusterRoleName}, currentClusterRole)).To(Succeed())
		Expect(currentClusterRole.OwnerReferences).ToNot(BeNil())
		Expect(len(currentClusterRole.OwnerReferences)).To(Equal(1))
		Expect(util.IsOwnedByObject(currentClusterRole, clusterSummary)).To(BeTrue())
	})

	It("unDeployResources removes all ClusterRole and Role created by a ClusterSummary", func() {
		role0 := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				Labels: map[string]string{
					controllers.ConfigLabelName:      randomString(),
					controllers.ConfigLabelNamespace: randomString(),
				},
			},
		}

		role1 := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}

		clusterRole0 := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
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
		Expect(testEnv.Client.Create(context.TODO(), role0)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), role1)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole0)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterRole0)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, role0, clusterSummary)
		addOwnerReference(ctx, testEnv.Client, clusterRole0, clusterSummary)

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureResources,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"ClusterRole.v1.rbac.authorization.k8s.io",
					"Role.v1.rbac.authorization.k8s.io",
				},
			},
		}
		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(controllers.UndeployResources(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(configv1alpha1.FeatureKyverno), klogr.New())).To(Succeed())

		// UnDeployResources finds all policies deployed because of a clusterSummary and deletes those.
		// Expect role0 and cluster0 to be deleted. role1 should remain as ConfigLabelName is not set on it

		currentRole := &rbacv1.Role{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(),
				types.NamespacedName{Namespace: role1.Namespace, Name: role1.Name}, currentRole)
			if err != nil {
				return false
			}
			err = testEnv.Client.Get(context.TODO(),
				types.NamespacedName{Namespace: role0.Namespace, Name: role0.Name}, currentRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		currentClusterRole := &rbacv1.ClusterRole{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(),
				types.NamespacedName{Name: clusterRole0.Name}, currentClusterRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) error {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return fmt.Errorf("missing apiVersion or kind and cannot assign it; %w", err)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}

	return nil
}

var _ = Describe("Hash methods", func() {
	It("ResourcesHash returns hash considering all referenced core resources", func() {
		clusterRole1 := rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
				{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
			},
		}
		Expect(addTypeInformationToObject(scheme, &clusterRole1)).To(Succeed())
		configMap1 := createConfigMapWithPolicy(randomString(), randomString(), render.AsCode(clusterRole1))

		clusterRole2 := rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"get", "list"}, APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}},
			},
		}
		Expect(addTypeInformationToObject(scheme, &clusterRole2)).To(Succeed())
		configMap2 := createConfigMapWithPolicy(randomString(), randomString(), render.AsCode(clusterRole2))

		namespace := "reconcile" + randomString()
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					ResourceRefs: []corev1.ObjectReference{
						{Namespace: configMap1.Namespace, Name: configMap1.Name},
						{Namespace: configMap2.Namespace, Name: configMap2.Name},
						{Namespace: randomString(), Name: randomString()},
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

		hash, err := controllers.ResourcesHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
