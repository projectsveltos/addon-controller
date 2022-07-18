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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
)

var _ = Describe("ClustersummaryDeployerHandlers", func() {
	var logger logr.Logger
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		logger = klogr.New()

		namespace = "reconcile" + util.RandomString(5)

		logger = klogr.New()
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + util.RandomString(5),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
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
	})

	AfterEach(func() {
		ns := &corev1.Namespace{}
		err := testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: namespace}, ns)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			return
		}
		err = testEnv.Client.Delete(context.TODO(), ns)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		err = testEnv.Client.Delete(context.TODO(), clusterFeature)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		err = testEnv.Client.Delete(context.TODO(), clusterSummary)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}

		workloadRules := &configv1alpha1.WorkloadRoleList{}
		Expect(testEnv.Client.List(context.TODO(), workloadRules)).To(Succeed())
		for i := range workloadRules.Items {
			Expect(testEnv.Client.Delete(context.TODO(), &workloadRules.Items[i])).To(Succeed())
		}
	})

	It("getSecretData returns an error when cluster does not exist", func() {
		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		_, err := controllers.GetSecretData(context.TODO(), logger, c, cluster.Namespace, cluster.Name)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("Cluster %s/%s does not exist", cluster.Namespace, cluster.Name)))
	})

	It("getSecretData returns an error when secret does not exist", func() {
		initObjects := []client.Object{
			clusterFeature,
			cluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		_, err := controllers.GetSecretData(context.TODO(), logger, c, cluster.Namespace, cluster.Name)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("Failed to get secret %s/%s-kubeconfig", cluster.Namespace, cluster.Name)))
	})

	It("getSecretData returns secret data", func() {
		randomData := []byte(util.RandomString(22))
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + "-kubeconfig",
			},
			Data: map[string][]byte{
				"data": randomData,
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			cluster,
			clusterSummary,
			&secret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		data, err := controllers.GetSecretData(context.TODO(), logger, c, cluster.Namespace, cluster.Name)
		Expect(err).To(BeNil())
		Expect(data).To(Equal(randomData))
	})

	It("getKubernetesClient returns client to access CAPI cluster", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterFeature)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + "-kubeconfig",
			},
			Data: map[string][]byte{
				"data": testEnv.Kubeconfig,
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		wcClient, err := controllers.GetKubernetesClient(context.TODO(), logger, testEnv.Client, cluster.Namespace, cluster.Name)
		Expect(err).To(BeNil())
		Expect(wcClient).ToNot(BeNil())
	})

	It("deployNamespacedWorkloadRole creates and updates Role", func() {
		roleNs := util.RandomString(6)
		workloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type:      configv1alpha1.RoleTypeNamespaced,
				Namespace: &roleNs,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
					{Verbs: []string{"get", "list"}, APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}},
				},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles = []corev1.ObjectReference{{Name: workloadRole.Name}}

		initObjects := []client.Object{
			workloadRole,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.DeployNamespacedWorkloadRole(ctx, c, workloadRole, clusterSummary, logger)).To(Succeed())

		listOptions := []client.ListOption{
			client.InNamespace(roleNs),
		}
		roleList := &rbacv1.RoleList{}
		Expect(c.List(context.TODO(), roleList, listOptions...)).To(Succeed())
		Expect(len(roleList.Items)).To(Equal(1))
		Expect(roleList.Items[0].Namespace).To(Equal(roleNs))
		Expect(roleList.Items[0].Rules).To(Equal(workloadRole.Spec.Rules))
		Expect(util.IsOwnedByObject(&roleList.Items[0], clusterSummary)).To(BeTrue())

		workloadRole.Spec.Rules = []rbacv1.PolicyRule{
			{Verbs: []string{"*"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"*"}},
		}
		Expect(c.Update(context.TODO(), workloadRole)).To(Succeed())

		Expect(controllers.DeployNamespacedWorkloadRole(ctx, c, workloadRole, clusterSummary, logger)).To(Succeed())
		Expect(c.List(context.TODO(), roleList, listOptions...)).To(Succeed())
		Expect(len(roleList.Items)).To(Equal(1))
		Expect(roleList.Items[0].Namespace).To(Equal(roleNs))
		Expect(roleList.Items[0].Rules).To(Equal(workloadRole.Spec.Rules))
		Expect(util.IsOwnedByObject(&roleList.Items[0], clusterSummary)).To(BeTrue())
	})

	It("deployClusterWorkloadRole creates and updates ClusterRole", func() {
		workloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get", "update"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"*"}},
				},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles = []corev1.ObjectReference{{Name: workloadRole.Name}}

		initObjects := []client.Object{
			workloadRole,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.DeployClusterWorkloadRole(ctx, c, workloadRole, clusterSummary, logger)).To(Succeed())

		clusterRoleList := &rbacv1.ClusterRoleList{}
		Expect(c.List(context.TODO(), clusterRoleList)).To(Succeed())
		Expect(len(clusterRoleList.Items)).To(Equal(1))
		Expect(clusterRoleList.Items[0].Rules).To(Equal(workloadRole.Spec.Rules))
		Expect(util.IsOwnedByObject(&clusterRoleList.Items[0], clusterSummary)).To(BeTrue())

		workloadRole.Spec.Rules = []rbacv1.PolicyRule{
			{Verbs: []string{"*"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"*"}},
		}
		Expect(c.Update(context.TODO(), workloadRole)).To(Succeed())

		Expect(controllers.DeployClusterWorkloadRole(ctx, c, workloadRole, clusterSummary, logger)).To(Succeed())
		Expect(c.List(context.TODO(), clusterRoleList)).To(Succeed())
		Expect(len(clusterRoleList.Items)).To(Equal(1))
		Expect(clusterRoleList.Items[0].Rules).To(Equal(workloadRole.Spec.Rules))
		Expect(util.IsOwnedByObject(&clusterRoleList.Items[0], clusterSummary)).To(BeTrue())
	})

	It("DeployWorkloadRoles creates ClusterRole and Role", func() {
		roleNs := util.RandomString(6)
		nsWorkloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type:      configv1alpha1.RoleTypeNamespaced,
				Namespace: &roleNs,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"*"}, APIGroups: []string{"storage.k8s.io"}, Resources: []string{"*"}},
				},
			},
		}

		clusterWorkloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
					{Verbs: []string{"get", "list"}, APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}},
					{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
				},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles = []corev1.ObjectReference{{Name: nsWorkloadRole.Name}, {Name: clusterWorkloadRole.Name}}

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
		Expect(testEnv.Client.Create(context.TODO(), nsWorkloadRole)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterWorkloadRole)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		Expect(controllers.DeployWorkloadRoles(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(configv1alpha1.FeatureRole), logger)).To(Succeed())

		name := controllers.GetRoleName(clusterWorkloadRole, clusterSummary.Name)
		currentClusterRole := &rbacv1.ClusterRole{}
		Expect(testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: name}, currentClusterRole)).To(Succeed())
		Expect(currentClusterRole.Rules).To(Equal(clusterWorkloadRole.Spec.Rules))
		Expect(len(currentClusterRole.OwnerReferences)).To(Equal(1))
		Expect(currentClusterRole.OwnerReferences).ToNot(BeNil())
		Expect(util.IsOwnedByObject(currentClusterRole, clusterSummary)).To(BeTrue())

		listOptions := []client.ListOption{
			client.InNamespace(roleNs),
		}
		roleList := &rbacv1.RoleList{}
		Expect(testEnv.Client.List(context.TODO(), roleList, listOptions...)).To(Succeed())
		Expect(len(roleList.Items)).To(Equal(1))
		Expect(roleList.Items[0].Namespace).To(Equal(roleNs))
		Expect(roleList.Items[0].Rules).To(Equal(nsWorkloadRole.Spec.Rules))
		Expect(roleList.Items[0].OwnerReferences).ToNot(BeNil())
		Expect(len(roleList.Items[0].OwnerReferences)).To(Equal(1))
		Expect(util.IsOwnedByObject(&roleList.Items[0], clusterSummary)).To(BeTrue())
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
