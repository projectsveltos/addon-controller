/*
Copyright 2024. projectsveltos.io. All rights reserved.

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

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	dragonflyReleaseName = "dragonfly-operator"
	dragonflyNamespace   = "dragonfly-operator"
	dragonflyDeployment  = "dragonfly-operator"
	dragonflyOCIURL      = "oci://ghcr.io/dragonflydb/dragonfly-operator/helm"
	dragonflyChartName   = "dragonfly-operator"
	dragonflyVersionA    = "v1.5.0"
	dragonflyVersionB    = "v1.4.0"
	handoffLabelValue    = "fv-handoff"
)

// HelmHandoff is Serial because it changes cluster labels, which would affect other tests.
var _ = Describe("HelmHandoff", Serial, func() {
	const (
		namePrefix = "helm-handoff-"
	)

	var (
		clusterProfileA *configv1beta1.ClusterProfile
		clusterProfileB *configv1beta1.ClusterProfile
	)

	AfterEach(func() {
		Byf("Restoring cluster label %s=%s", key, value)
		setLabelOnCluster(key, value)

		if clusterProfileA != nil {
			deleteClusterProfile(clusterProfileA)
			clusterProfileA = nil
		}
	})

	It("Helm chart is upgraded not reinstalled when cluster switches between ClusterProfiles",
		Label("NEW-FV", "EXTENDED"), func() {
			Byf("Create ClusterProfile A matching Cluster %s/%s deploying dragonfly-operator %s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), dragonflyVersionA)
			clusterProfileA = getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfileA.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
			clusterProfileA.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    dragonflyOCIURL,
					RepositoryName:   dragonflyReleaseName,
					ChartName:        dragonflyChartName,
					ChartVersion:     dragonflyVersionA,
					ReleaseName:      dragonflyReleaseName,
					ReleaseNamespace: dragonflyNamespace,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			Expect(k8sClient.Create(context.TODO(), clusterProfileA)).To(Succeed())

			verifyClusterProfileMatches(clusterProfileA)

			clusterSummaryA := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				clusterProfileA.Name, &clusterProfileA.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummaryA.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummaryA.Name, libsveltosv1beta1.FeatureHelm)

			charts := []configv1beta1.Chart{
				{ReleaseName: dragonflyReleaseName, ChartVersion: dragonflyVersionA, Namespace: dragonflyNamespace},
			}
			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfileA.Name,
				clusterSummaryA.Spec.ClusterNamespace, clusterSummaryA.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)

			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			Byf("Verifying dragonfly-operator deployment is created in the workload cluster")
			Eventually(func() error {
				depl := &appsv1.Deployment{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dragonflyNamespace, Name: dragonflyDeployment}, depl)
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Storing dragonfly-operator deployment creation timestamp")
			originalDepl := &appsv1.Deployment{}
			Expect(workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: dragonflyNamespace, Name: dragonflyDeployment}, originalDepl)).To(Succeed())
			originalCreationTimestamp := originalDepl.CreationTimestamp

			Byf("Create ClusterProfile B matching label %s=%s deploying dragonfly-operator %s",
				key, handoffLabelValue, dragonflyVersionB)
			clusterProfileB = getClusterProfile(namePrefix, map[string]string{key: handoffLabelValue})
			clusterProfileB.Spec.SyncMode = configv1beta1.SyncModeContinuous
			clusterProfileB.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    dragonflyOCIURL,
					RepositoryName:   dragonflyReleaseName,
					ChartName:        dragonflyChartName,
					ChartVersion:     dragonflyVersionB,
					ReleaseName:      dragonflyReleaseName,
					ReleaseNamespace: dragonflyNamespace,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			Expect(k8sClient.Create(context.TODO(), clusterProfileB)).To(Succeed())

			Byf("Changing cluster label from %s=%s to %s=%s to trigger handoff",
				key, value, key, handoffLabelValue)
			setLabelOnCluster(key, handoffLabelValue)

			Byf("Verifying dragonfly-operator deployment is never absent and never recreated during the handoff")
			Consistently(func() error {
				depl := &appsv1.Deployment{}
				if err := workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dragonflyNamespace, Name: dragonflyDeployment}, depl); err != nil {
					return err
				}
				if !depl.CreationTimestamp.Equal(&originalCreationTimestamp) {
					return fmt.Errorf("deployment was recreated: original timestamp %v, current %v",
						originalCreationTimestamp, depl.CreationTimestamp)
				}
				return nil
			}, timeout/2, pollingInterval).Should(BeNil())

			Byf("Verifying ClusterProfile B eventually manages dragonfly-operator on the cluster")
			verifyClusterProfileMatches(clusterProfileB)

			clusterSummaryB := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				clusterProfileB.Name, &clusterProfileB.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummaryB.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummaryB.Name, libsveltosv1beta1.FeatureHelm)

			Byf("Verifying ClusterConfiguration shows dragonfly-operator upgraded to version %s", dragonflyVersionB)
			chartsB := []configv1beta1.Chart{
				{ReleaseName: dragonflyReleaseName, ChartVersion: dragonflyVersionB, Namespace: dragonflyNamespace},
			}
			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfileB.Name,
				clusterSummaryB.Spec.ClusterNamespace, clusterSummaryB.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, chartsB)

			deleteClusterProfile(clusterProfileB)

			Byf("Verifying dragonfly-operator deployment is removed from workload cluster")
			Eventually(func() bool {
				depl := &appsv1.Deployment{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dragonflyNamespace, Name: dragonflyDeployment}, depl)
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
