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
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

var _ = Describe("Workload", func() {
	const (
		namePrefix = "workload"
	)

	It("Deploy and updates WorkloadRoles correctly", Label("FV"), func() {
		Byf("Create a ClusterFeature matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterFeature := getClusterfeature(namePrefix, map[string]string{key: value})
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterFeature)).To(Succeed())

		verifyClusterFeatureMatches(clusterFeature)

		verifyClusterSummary(clusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Create a workload role")
		roleNs := randomString()
		workloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type:      configv1alpha1.RoleTypeNamespaced,
				Namespace: &roleNs,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), workloadRole)).To(Succeed())

		Byf("Update ClusterFeature %s to reference WorkloadRole %s", clusterFeature.Name, workloadRole.Name)
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Kind: workloadRole.Kind, Name: workloadRole.Name},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		clusterSummary := verifyClusterSummary(currentClusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying ns is created in the workload cluster")
		Eventually(func() bool {
			ns := &corev1.Namespace{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: *workloadRole.Spec.Namespace}, ns)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper role is created in the workload cluster")
		Eventually(func() bool {
			role := &rbacv1.Role{}
			roleName := workloadRole.Name
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: roleName, Namespace: *workloadRole.Spec.Namespace}, role)
			return err == nil &&
				reflect.DeepEqual(role.Rules, workloadRole.Spec.Rules)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for roles", clusterSummary.Name)
		verifyFeatureStatus(clusterSummary.Name, configv1alpha1.FeatureRole, configv1alpha1.FeatureStatusProvisioned)

		By("Updating WorkloadRole")
		currentWorkloadRole := &configv1alpha1.WorkloadRole{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: workloadRole.Name}, currentWorkloadRole)).To(Succeed())
		currentWorkloadRole.Spec.Rules = []rbacv1.PolicyRule{
			{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
			{Verbs: []string{"get", "list"}, APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}},
		}
		Expect(k8sClient.Update(context.TODO(), currentWorkloadRole)).To(Succeed())

		Byf("Verifying proper role is updated in the workload cluster")
		Eventually(func() bool {
			role := &rbacv1.Role{}
			roleName := workloadRole.Name
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: roleName, Namespace: *workloadRole.Spec.Namespace}, role)
			return err == nil &&
				reflect.DeepEqual(role.Rules, currentWorkloadRole.Spec.Rules)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("changing clusterfeature to not reference workloadrole anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.WorkloadRoleRefs = []corev1.ObjectReference{}
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		clusterSummary = verifyClusterSummary(currentClusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying proper role is removed in the workload cluster")
		Eventually(func() bool {
			role := &rbacv1.Role{}
			roleName := clusterSummary.Name + "-" + workloadRole.Name
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: roleName, Namespace: *workloadRole.Spec.Namespace}, role)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterFeature(clusterFeature)
	})
})
