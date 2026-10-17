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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Helm CRDs", func() {
	const (
		namePrefix = "helm-crds-"
	)

	// Envoy Gateway v1.7.x ships its CRDs (Gateway API included) in the chart's crds/ directory.
	// Starting with v1.9.x all of them are in the crds subchart. Upgrading from v1.7.4 to v1.9.2
	// with upgradeCRDs set must therefore update CRDs that only exist in a subchart.
	It("Upgrades the CRDs shipped in a subchart", Label("FV", "EXTENDED"), func() {
		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		// Helm never removes CRDs. Start from, and leave behind, a cluster without them.
		deleteEnvoyGatewayCRDs(workloadClient)
		DeferCleanup(func() { deleteEnvoyGatewayCRDs(workloadClient) })

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			newEnvoyGatewayChart(envoyGatewayVersion174),
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name,
			&clusterProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Verifying the Gateway API CRDs are at bundle version %s", gatewayAPIBundleVersion141)
		Eventually(func() string {
			return getCRDBundleVersion(workloadClient, gatewayClassesCRDName)
		}, timeout, pollingInterval).Should(Equal(gatewayAPIBundleVersion141))

		Byf("Verifying the CRD only present in newer Gateway API bundles does not exist yet")
		Expect(crdExists(workloadClient, gatewayListenerSetsCRDName)).To(BeFalse())

		Byf("Changing ClusterProfile %s to require Envoy Gateway %s", clusterProfile.Name, envoyGatewayVersion192)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				newEnvoyGatewayChart(envoyGatewayVersion192),
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary = verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		charts := []configv1beta1.Chart{
			{ReleaseName: envoyGatewayRelease, ChartVersion: envoyGatewayVersion192, Namespace: envoyGatewayNamespace},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		Byf("Verifying the Gateway API CRDs, now only in the crds subchart, are at bundle version %s",
			gatewayAPIBundleVersion161)
		Eventually(func() string {
			return getCRDBundleVersion(workloadClient, gatewayClassesCRDName)
		}, timeout, pollingInterval).Should(Equal(gatewayAPIBundleVersion161))

		Byf("Verifying the CRD only present in newer Gateway API bundles has been created")
		Eventually(func() bool {
			return crdExists(workloadClient, gatewayListenerSetsCRDName)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})

// newEnvoyGatewayChart returns the Envoy Gateway chart at the given version, with CRDs upgraded.
// Hooks are disabled: the certgen job is not needed to verify CRDs and would require the
// Envoy Gateway image to be pulled in the workload cluster.
func newEnvoyGatewayChart(version string) configv1beta1.HelmChart {
	return configv1beta1.HelmChart{
		RepositoryURL:    envoyGatewayURL,
		RepositoryName:   envoyGatewayRepoName,
		ChartName:        envoyGatewayChartName,
		ChartVersion:     version,
		ReleaseName:      envoyGatewayRelease,
		ReleaseNamespace: envoyGatewayNamespace,
		HelmChartAction:  configv1beta1.HelmChartActionInstall,
		Options: &configv1beta1.HelmOptions{
			DisableHooks: true,
			InstallOptions: configv1beta1.HelmInstallOptions{
				CreateNamespace: ptr.To(true),
			},
			UpgradeOptions: configv1beta1.HelmUpgradeOptions{
				UpgradeCRDs: true,
			},
		},
	}
}

// getCRDBundleVersion returns the Gateway API bundle version annotation of the CRD,
// or an empty string when the CRD cannot be read.
func getCRDBundleVersion(workloadClient client.Client, crdName string) string {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: crdName}, crd); err != nil {
		return ""
	}

	return crd.Annotations[gatewayAPIBundleVersionKey]
}

func crdExists(workloadClient client.Client, crdName string) bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: crdName}, crd)
	if apierrors.IsNotFound(err) {
		return false
	}
	Expect(err).To(BeNil())

	return true
}

// isEnvoyGatewayCRD returns true for the CRDs installed by the Envoy Gateway chart, Gateway API ones included.
func isEnvoyGatewayCRD(crd *apiextensionsv1.CustomResourceDefinition) bool {
	switch crd.Spec.Group {
	case gatewayAPIGroup, gatewayAPIExperimentalGroup, envoyGatewayGroup:
		return true
	default:
		return false
	}
}

// deleteEnvoyGatewayCRDs removes the CRDs installed by the Envoy Gateway chart and waits for them to be gone.
// Helm does not remove CRDs when a release is uninstalled.
func deleteEnvoyGatewayCRDs(workloadClient client.Client) {
	crds := &apiextensionsv1.CustomResourceDefinitionList{}
	Expect(workloadClient.List(context.TODO(), crds)).To(Succeed())

	for i := range crds.Items {
		if isEnvoyGatewayCRD(&crds.Items[i]) {
			Expect(client.IgnoreNotFound(workloadClient.Delete(context.TODO(), &crds.Items[i]))).To(Succeed())
		}
	}

	Eventually(func() int {
		current := &apiextensionsv1.CustomResourceDefinitionList{}
		Expect(workloadClient.List(context.TODO(), current)).To(Succeed())
		remaining := 0
		for i := range current.Items {
			if isEnvoyGatewayCRD(&current.Items[i]) {
				remaining++
			}
		}
		return remaining
	}, timeout, pollingInterval).Should(BeZero())
}
