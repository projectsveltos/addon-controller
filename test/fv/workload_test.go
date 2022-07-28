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
	"k8s.io/client-go/util/retry"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

var _ = Describe("Workload", func() {
	const (
		key        = "env"
		value      = "fv"
		namePrefix = "workload"
	)

	It("Deploy and updates WorkloadRoles correctly", Label("FV"), func() {
		Byf("Add label %s:%s to Cluster %s/%s", key, value, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			currentCluster := &clusterv1.Cluster{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name},
				currentCluster)).To(Succeed())

			currentLabels := currentCluster.Labels
			if currentLabels == nil {
				currentLabels = make(map[string]string)
			}
			currentLabels[key] = value

			return k8sClient.Update(context.TODO(), currentCluster)
		})
		Expect(err).To(BeNil())

		Byf("Create a ClusterFeature matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterFeature := getClusterfeature(namePrefix, map[string]string{key: value})
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuos
		Expect(k8sClient.Create(context.TODO(), clusterFeature)).To(Succeed())

		Byf("Verifying Cluster %s/%s is a match for ClusterFeature %s",
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name, clusterFeature.Name)
		Eventually(func() bool {
			currentClusterFeature := &configv1alpha1.ClusterFeature{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)
			return err == nil &&
				len(currentClusterFeature.Status.MatchingClusters) == 1 &&
				currentClusterFeature.Status.MatchingClusters[0].Namespace == kindWorkloadCluster.Namespace &&
				currentClusterFeature.Status.MatchingClusters[0].Name == kindWorkloadCluster.Name
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary is created")
		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSummaryName}, clusterSummary)).To(Succeed())

		ref, err := getClusterSummaryOwnerReference(clusterSummary)
		Expect(err).To(BeNil())
		Expect(ref).ToNot(BeNil())
		Expect(ref.Name).To(Equal(clusterFeature.Name))

		Byf("Create a workload role")
		roleNs := util.RandomString(7)
		workloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type:      configv1alpha1.RoleTypeNamespaced,
				Namespace: &roleNs,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
					// {Verbs: []string{"get", "list"}, APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), workloadRole)).To(Succeed())

		Byf("Update ClusterFeature %s to reference WorkloadRole %s", clusterFeature.Name, workloadRole.Name)
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.WorkloadRoles = []corev1.ObjectReference{
			{Kind: workloadRole.Kind, Name: workloadRole.Name},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		Byf("Verifying ClusterSummary %s is updated", clusterSummary.Name)
		Eventually(func() bool {
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSummaryName}, currentClusterSummary)
			return err == nil &&
				len(currentClusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles) == 1 &&
				currentClusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles[0].Name == workloadRole.Name
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying ns is created in the workload cluster")
		Eventually(func() bool {
			ns := &corev1.Namespace{}
			err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: *workloadRole.Spec.Namespace}, ns)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper role is created in the workload cluster")
		Eventually(func() bool {
			role := &rbacv1.Role{}
			roleName := clusterSummary.Name + "-" + workloadRole.Name
			err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: roleName, Namespace: *workloadRole.Spec.Namespace}, role)
			return err == nil &&
				reflect.DeepEqual(role.Rules, workloadRole.Spec.Rules)
		}, timeout, pollingInterval).Should(BeTrue())

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
			roleName := clusterSummary.Name + "-" + workloadRole.Name
			err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: roleName, Namespace: *workloadRole.Spec.Namespace}, role)
			return err == nil &&
				reflect.DeepEqual(role.Rules, currentWorkloadRole.Spec.Rules)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("changing clusterfeature to not reference workloadrole anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.WorkloadRoles = []corev1.ObjectReference{}
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		Byf("Verifying ClusterSummary %s is updated", clusterSummary.Name)
		Eventually(func() bool {
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSummaryName}, currentClusterSummary)
			return err == nil &&
				len(currentClusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles) == 0
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper role is removed in the workload cluster")
		Eventually(func() bool {
			role := &rbacv1.Role{}
			roleName := clusterSummary.Name + "-" + workloadRole.Name
			err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: roleName, Namespace: *workloadRole.Spec.Namespace}, role)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
