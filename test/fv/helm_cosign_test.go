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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("HelmCosignVerification", func() {
	const (
		namePrefix              = "cosign-"
		cosignTestOCIURL        = "oci://registry-1.docker.io/gianlucam76"
		cosignTestChart         = "cosign-test"
		cosignTestVersion       = "0.1.0"
		cosignTestNS            = "cosign-test"
		githubActionsOIDCIssuer = "^https://token.actions.githubusercontent.com$"
	)

	It("Deploys OCI helm chart only when Cosign signature verification passes", Label("FV", "PULLMODE"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.GetNamespace(),
			kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to deploy OCI chart with a wrong Cosign identity — must fail", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    cosignTestOCIURL,
					RepositoryName:   cosignTestChart,
					ChartName:        cosignTestChart,
					ChartVersion:     cosignTestVersion,
					ReleaseName:      cosignTestChart,
					ReleaseNamespace: cosignTestNS,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					SignatureVerification: &configv1beta1.CosignVerification{
						Provider: configv1beta1.CosignProviderKeyless,
						MatchOIDCIdentity: []configv1beta1.OIDCIdentityMatcher{
							{
								Issuer:  githubActionsOIDCIssuer,
								Subject: "^https://github.com/wrong-org/wrong-repo/.*$",
							},
						},
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

		Byf("Verifying ClusterSummary reports a Cosign verification failure")
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			if err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary); err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == libsveltosv1beta1.FeatureHelm {
					return currentClusterSummary.Status.FeatureSummaries[i].FailureMessage != nil
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Update ClusterProfile %s to use the correct Cosign identity — must succeed", clusterProfile.Name)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts[0].SignatureVerification = &configv1beta1.CosignVerification{
				Provider: configv1beta1.CosignProviderKeyless,
				MatchOIDCIdentity: []configv1beta1.OIDCIdentityMatcher{
					{
						Issuer:  "^https://token.actions.githubusercontent.com$",
						Subject: "^https://github.com/gianlucam76/cosign-test/.*$",
					},
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Provisioned for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying cosign-test ConfigMap is created in the workload cluster")
		Eventually(func() error {
			cm := &corev1.ConfigMap{}
			return workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: cosignTestNS, Name: cosignTestChart}, cm)
		}, timeout, pollingInterval).Should(BeNil())

		charts := []configv1beta1.Chart{
			{ReleaseName: cosignTestChart, ChartVersion: cosignTestVersion, Namespace: cosignTestNS},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		deleteClusterProfile(clusterProfile)

		Byf("Verifying cosign-test ConfigMap is removed from workload cluster")
		Eventually(func() bool {
			cm := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: cosignTestNS, Name: cosignTestChart}, cm)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
