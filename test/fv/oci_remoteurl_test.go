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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("OCI RemoteURL", func() {
	const (
		namePrefix   = "oci-remoteurl-"
		ociTestURL   = "oci://ghcr.io/gianlucam76/sveltos-oci-test:head"
		ociConfigMap = "oci-test"
		ociNamespace = "default"
	)

	It("Deploys resources from a public OCI artifact", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to deploy OCI artifact %s", clusterProfile.Name, ociTestURL)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					RemoteURL: &configv1beta1.RemoteURL{
						URL:      ociTestURL,
						Interval: &metav1.Duration{Duration: 5 * time.Minute},
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

		Byf("Verifying ConfigMap %s/%s is created in the workload cluster", ociNamespace, ociConfigMap)
		Eventually(func() error {
			cm := &corev1.ConfigMap{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: ociNamespace, Name: ociConfigMap}, cm)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		policies := []policy{
			{kind: kindConfigMap, name: ociConfigMap, namespace: ociNamespace, group: ""},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureResources,
			policies, nil)

		Byf("Removing OCI artifact reference from ClusterProfile %s", clusterProfile.Name)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Verifying ConfigMap %s/%s is removed from the workload cluster", ociNamespace, ociConfigMap)
		Eventually(func() bool {
			cm := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: ociNamespace, Name: ociConfigMap}, cm)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
