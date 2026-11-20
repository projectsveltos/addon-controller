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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// The kind is not the one used by drift_new_crd_test.go: specs run in parallel on the same
	// workload cluster and each one installs, and removes, its own CRD.
	gizmoGroup = "helm-repro.example.com"
	gizmoKind  = "Gizmo"

	// bedag/raw renders, as they are, the manifests passed in the "resources" value.
	// Used to deploy, with a Helm chart, a custom resource which has no namespace.
	rawChartURL     = "https://bedag.github.io/helm-charts/"
	rawChartRepo    = "bedag"
	rawChartName    = "bedag/raw"
	rawChartVersion = "2.0.2"

	// gizmoValuesTemplate asks the raw chart to render one cluster scoped Gizmo (no namespace).
	gizmoValuesTemplate = `resources:
- apiVersion: helm-repro.example.com/v1
  kind: Gizmo
  metadata:
    name: %s
  spec:
    size: 1
`
)

// getGizmoCRD returns the CustomResourceDefinition of a cluster scoped kind.
func getGizmoCRD() *apiextensionsv1.CustomResourceDefinition {
	return newClusterScopedCRD(gizmoGroup, gizmoKind, "gizmos", "gizmo")
}

func getGizmoObject(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(fmt.Sprintf("%s/v1", gizmoGroup))
	u.SetKind(gizmoKind)
	u.SetName(name)
	return u
}

// verifyCRDIsEstablished waits for the CRD to be served by the cluster c points to.
func verifyCRDIsEstablished(c client.Client, crdName string) {
	Eventually(func() bool {
		currentCRD := &apiextensionsv1.CustomResourceDefinition{}
		if err := c.Get(context.TODO(), types.NamespacedName{Name: crdName}, currentCRD); err != nil {
			return false
		}
		for i := range currentCRD.Status.Conditions {
			condition := &currentCRD.Status.Conditions[i]
			if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

// getHelmResourceHashes returns the resources tracked, for the Helm charts, by the ResourceSummary
// of clusterSummary. Returns nil if there is no such ResourceSummary or nothing is tracked yet.
func getHelmResourceHashes(c client.Client, clusterSummary *configv1beta1.ClusterSummary,
) []libsveltosv1beta1.ResourceHash {

	resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
	if err := c.List(context.TODO(), resourceSummaries); err != nil {
		return nil
	}
	for i := range resourceSummaries.Items {
		rs := &resourceSummaries.Items[i]
		if matchesClusterSummary(rs, clusterSummary) {
			return rs.Status.HelmResourceHashes
		}
	}
	return nil
}

func isResourceTracked(hashes []libsveltosv1beta1.ResourceHash, group, kind, name string) bool {
	for i := range hashes {
		if hashes[i].Group == group && hashes[i].Kind == kind && hashes[i].Name == name {
			return true
		}
	}
	return false
}

var _ = Describe("Drift detection for a custom resource deployed by a Helm chart", func() {
	const namePrefix = "helm-custom-resource-"

	// Regression test for https://github.com/projectsveltos/addon-controller/issues/2112.
	// When drift-detection-manager runs in the management cluster (agentless mode), it must resolve
	// the kinds deployed by Helm charts against the managed cluster. If it uses the management
	// cluster instead, a kind whose CRD is installed only in the managed cluster cannot be resolved:
	// the ResourceSummary is never processed (helmResourceHashes stays empty) and no resource
	// deployed by the chart is protected by ContinuousWithDriftDetection.
	//
	// Three things are needed to reproduce it:
	//  1. the resource is deployed by a Helm chart (ResourceSummary.spec.chartResources);
	//  2. the resource has no namespace in the manifest (only then is the scope looked up);
	//  3. the CRD is in the managed cluster and not in the management cluster.
	It("tracks a custom resource, with no namespace, whose CRD is only in the managed cluster",
		Label("FV", "EXTENDED"), func() {
			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			gizmoCRD := getGizmoCRD()

			Byf("Verifying the %s CRD is not in the management cluster", gizmoCRD.Name)
			err = k8sClient.Get(context.TODO(), types.NamespacedName{Name: gizmoCRD.Name},
				&apiextensionsv1.CustomResourceDefinition{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			Byf("Installing the %s CRD in the workload cluster only", gizmoCRD.Name)
			Expect(workloadClient.Create(context.TODO(), gizmoCRD)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(workloadClient.Delete(context.TODO(), gizmoCRD))).To(Succeed())
			})
			verifyCRDIsEstablished(workloadClient, gizmoCRD.Name)

			Byf("Create a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			gizmoName := namePrefix + randomString()
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
			clusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    rawChartURL,
					RepositoryName:   rawChartRepo,
					ChartName:        rawChartName,
					ChartVersion:     rawChartVersion,
					ReleaseName:      namePrefix + randomString(),
					ReleaseNamespace: randomString(),
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values:           fmt.Sprintf(gizmoValuesTemplate, gizmoName),
				},
			}
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())
			// Cleanups run last in first out: this one runs before the CRD is deleted. Uninstalling
			// the release needs the CRD, so wait for the ClusterProfile to be gone (and with it the
			// Helm release) before the CRD is deleted. Needed if the spec fails before deleting
			// the ClusterProfile.
			DeferCleanup(func() {
				currentClusterProfile := &configv1beta1.ClusterProfile{}
				err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
				if apierrors.IsNotFound(err) {
					return
				}
				Expect(err).To(BeNil())
				deleteClusterProfile(clusterProfile)
			})

			verifyClusterProfileMatches(clusterProfile)

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name,
				&clusterProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
				libsveltosv1beta1.FeatureHelm)

			Byf("Verifying %s %s is created in the workload cluster", gizmoKind, gizmoName)
			Eventually(func() error {
				return workloadClient.Get(context.TODO(), types.NamespacedName{Name: gizmoName}, getGizmoObject(gizmoName))
			}, timeout, pollingInterval).Should(BeNil())

			// In agentless mode ResourceSummary instances are in the management cluster
			resourceSummaryClient := workloadClient
			if isAgentLessMode() {
				resourceSummaryClient = k8sClient
			}

			Byf("Verifying the ResourceSummary tracks %s %s deployed by the Helm chart", gizmoKind, gizmoName)
			Eventually(func() bool {
				hashes := getHelmResourceHashes(resourceSummaryClient, clusterSummary)
				return isResourceTracked(hashes, gizmoGroup, gizmoKind, gizmoName)
			}, timeout, pollingInterval).Should(BeTrue())

			deleteClusterProfile(clusterProfile)

			Byf("Verifying %s %s is removed from the workload cluster", gizmoKind, gizmoName)
			Eventually(func() bool {
				err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: gizmoName}, getGizmoObject(gizmoName))
				return err != nil
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
