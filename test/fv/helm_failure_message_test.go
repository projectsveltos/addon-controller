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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Helm with conflicts", func() {
	const (
		namePrefix = "helm-failures-"
		mgmt       = "mgmt"
	)

	It("ClusterProfile deploying helm charts with failures", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching mgmt Cluster")

		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: namePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterRefs: []corev1.ObjectReference{
					{
						APIVersion: libsveltosv1beta1.GroupVersion.String(),
						Kind:       libsveltosv1beta1.SveltosClusterKind,
						Namespace:  mgmt,
						Name:       mgmt,
					},
				},
			},
		}
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())
		Byf("Created ClusterProfile %s", clusterProfile.Name)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			mgmt, mgmt, string(libsveltosv1beta1.ClusterTypeSveltos))

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "oci://ghcr.io/k0rdent/catalog/charts",
					RepositoryName:   "ingress-nginx",
					ChartName:        "ingress-nginx",
					ChartVersion:     "4.13.0",
					ReleaseName:      "nginx",
					ReleaseNamespace: "nginx",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Options: &configv1beta1.HelmOptions{
						InstallOptions: configv1beta1.HelmInstallOptions{
							CreateNamespace: true,
							Replace:         true,
						},
						Timeout: &metav1.Duration{Duration: time.Second},
					},
					RegistryCredentialsConfig: &configv1beta1.RegistryCredentialsConfig{
						PlainHTTP: true,
					},
					Values: `ingress-nginx:
  global:
    image:
      registry: this.does.not.exist.com`,
				},
				{
					RepositoryURL:    "oci://ghcr.io/k0rdent/catalog/charts",
					RepositoryName:   "postgres-operator",
					ChartName:        "postgres-operator",
					ChartVersion:     "1.15.1",
					ReleaseName:      "postgres-operator",
					ReleaseNamespace: "postgres-operator",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					RegistryCredentialsConfig: &configv1beta1.RegistryCredentialsConfig{
						PlainHTTP: true,
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
			mgmt, mgmt, string(libsveltosv1beta1.ClusterTypeSveltos))

		By("Verifying helmReleaseSummaries reports error")
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}

			for i := range currentClusterSummary.Status.HelmReleaseSummaries {
				if currentClusterSummary.Status.HelmReleaseSummaries[i].FailureMessage != nil &&
					*currentClusterSummary.Status.HelmReleaseSummaries[i].FailureMessage == "context deadline exceeded" {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		Expect(k8sClient.Delete(context.TODO(), currentClusterProfile)).To(Succeed())
	})
})
