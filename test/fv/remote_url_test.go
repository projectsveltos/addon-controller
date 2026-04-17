/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Remote URL", func() {
	const (
		namePrefix          = "remote-url-"
		clusterRoleName     = "system:aggregated-metrics-reader"
		deploymentNamespace = "kube-system"
		deploymentName      = "metrics-server"
		saNamespace         = "kube-system"
		saName              = "metrics-server"
	)

	// Extra Labels/Annotations are deprecated. Not supported in pull mode
	// Do not run in PullMode. ExtraLabels/ExtraAnnotations are deprecated. So not implemented in pull mode.
	It("Deploy the content of a remote URL", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to reference Remote URL", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					RemoteURL: &configv1beta1.RemoteURL{
						URL:      "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml",
						Interval: &metav1.Duration{Duration: time.Hour},
					},
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying metric-server ClusterRole is created in the workload cluster")
		Eventually(func() error {
			currentClusterRole := &rbacv1.ClusterRole{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName},
				currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying metric-server Deplyment is created in the workload cluster")
		Eventually(func() error {
			currentDeployment := &appsv1.Deployment{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deploymentNamespace, Name: deploymentName},
				currentDeployment)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying metric-server ServiceAccount is created in the workload cluster")
		Eventually(func() error {
			currentServiceAccount := &corev1.ServiceAccount{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: saNamespace, Name: saName},
				currentServiceAccount)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		policies := []policy{
			{kind: "ClusterRole", name: clusterRoleName, namespace: "", group: "rbac.authorization.k8s.io"},
			{kind: "Deployment", name: deploymentName, namespace: deploymentNamespace, group: "apps"},
			{kind: "ServiceAccount", name: saName, namespace: saNamespace, group: ""},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureResources,
			policies, nil)

		Byf("Update ClusterProfile %s to not reference Remote URL", clusterProfile.Name)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Verifying metric-server ClusterRole is removed from the workload cluster")
		Eventually(func() bool {
			currentClusterRole := &rbacv1.ClusterRole{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName},
				currentClusterRole)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying metric-server Deplyment is removed from the workload cluster")
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: deploymentNamespace, Name: deploymentName},
				currentDeployment)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying metric-server ServiceAccount is removed from the workload cluster")
		Eventually(func() bool {
			currentServiceAccount := &corev1.ServiceAccount{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: saNamespace, Name: saName},
				currentServiceAccount)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
