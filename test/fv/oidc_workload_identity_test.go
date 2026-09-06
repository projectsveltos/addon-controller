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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// The SveltosCluster and Secrets this test targets are created by `make create-cluster-oidc`,
// against the same physical workload cluster `make create-cluster` provisions: Dex is deployed
// as the IdP, the workload cluster's kube-apiserver is configured to trust it, and the cluster
// is registered a second time as a plain SveltosCluster using OIDC workload identity instead of
// a kubeconfig Secret, mirroring how an on-prem cluster fronted by an enterprise IdP (Dex,
// Keycloak, Okta) would be onboarded.
const (
	oidcWorkloadClusterNamespace = "oidc-test"
	oidcWorkloadClusterName      = "oidc-workload"
)

var _ = Describe("OIDC workload identity", Serial, func() {
	const namePrefix = "oidc-"

	It("deploys a ClusterProfile to a cluster registered via OIDC workload identity", Label("OIDC"), func() {
		Byf("Verifying SveltosCluster %s/%s is ready", oidcWorkloadClusterNamespace, oidcWorkloadClusterName)
		Eventually(func() bool {
			sveltosCluster := &libsveltosv1beta1.SveltosCluster{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: oidcWorkloadClusterNamespace, Name: oidcWorkloadClusterName},
				sveltosCluster)
			return err == nil && sveltosCluster.Status.Ready
		}, timeout, pollingInterval).Should(BeTrue())

		devNamespaceName := randomString()
		Byf("Create a ConfigMap with a Namespace to deploy")
		configMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
			fmt.Sprintf(devNamespace, devNamespaceName))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterProfile targeting the OIDC SveltosCluster directly")
		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterRefs: []corev1.ObjectReference{
					{
						APIVersion: libsveltosv1beta1.GroupVersion.String(),
						Kind:       libsveltosv1beta1.SveltosClusterKind,
						Namespace:  oidcWorkloadClusterNamespace,
						Name:       oidcWorkloadClusterName,
					},
				},
				SyncMode: configv1beta1.SyncModeContinuous,
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: configMap.Namespace,
						Name:      configMap.Name,
					},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name,
			&clusterProfile.Spec, oidcWorkloadClusterNamespace, oidcWorkloadClusterName,
			string(libsveltosv1beta1.ClusterTypeSveltos))

		Byf("Verifying ClusterSummary %s status is set to Provisioned for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(oidcWorkloadClusterNamespace, clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying Namespace %s was created in the workload cluster via the OIDC access token",
			devNamespaceName)
		Eventually(func() error {
			currentNamespace := &corev1.Namespace{}
			return workloadClient.Get(context.TODO(), types.NamespacedName{Name: devNamespaceName}, currentNamespace)
		}, timeout, pollingInterval).Should(BeNil())

		deleteClusterProfile(clusterProfile)
	})
})
