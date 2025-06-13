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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
)

const (
	minioReleaseName = "minio"
	minioNamespace   = "minio-operator"
	minioDeployment  = "minio-operator"
)

var _ = Describe("Helm", func() {
	const (
		namePrefix = "leave-helm-charts-"
	)

	It("Deploy helm charts. When Cluster stops matching, policies are left on Cluster",
		Label("FV", "PULLMODE", "EXTENDED"), func() {
			Byf("Create a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			clusterProfile.Spec.StopMatchingBehavior = configv1beta1.LeavePolicies
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyClusterProfileMatches(clusterProfile)

			verifyClusterSummary(clusterops.ClusterProfileLabelName,
				clusterProfile.Name, &clusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
			currentClusterProfile := &configv1beta1.ClusterProfile{}

			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
					{
						RepositoryURL:    "https://operator.min.io",
						RepositoryName:   "minio-operator",
						ChartName:        "minio-operator/operator",
						ChartVersion:     "7.1.1",
						ReleaseName:      minioReleaseName,
						ReleaseNamespace: minioNamespace,
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
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

			Byf("Verifying Minio deployment is created in the workload cluster")
			Eventually(func() error {
				depl := &appsv1.Deployment{}
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: minioNamespace, Name: minioDeployment}, depl)
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

			if !isPullMode() {
				charts := []configv1beta1.Chart{
					{ReleaseName: minioReleaseName, ChartVersion: "7.1.1", Namespace: minioNamespace},
				}

				verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
					clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
					nil, charts)
			}

			Byf("Changing clusterprofile ClusterSelector so Cluster is not a match anymore")
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

			currentClusterProfile.Spec.ClusterSelector = libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						key: value + randomString(),
					},
				},
			}
			Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

			Byf("Verifying ClusterSummary is gone")
			Eventually(func() bool {
				_, err = getClusterSummary(context.TODO(), clusterops.ClusterProfileLabelName,
					clusterProfile.Name, kindWorkloadCluster.GetNamespace(),
					kindWorkloadCluster.GetName(), getClusterType())
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying Minio deployment is still present in the workload cluster")
			depl := &appsv1.Deployment{}
			Expect(workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: minioNamespace, Name: minioDeployment}, depl)).To(Succeed())

			Byf("Verifying Minio deployment annotations and labels are updated")
			Expect(workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: minioNamespace, Name: minioDeployment}, depl)).To(Succeed())
			lbls := depl.Labels
			if lbls != nil {
				_, ok := lbls[deployer.ReferenceKindLabel]
				Expect(ok).To(BeFalse())
				_, ok = lbls[deployer.ReferenceNameLabel]
				Expect(ok).To(BeFalse())
				_, ok = lbls[deployer.ReferenceNamespaceLabel]
				Expect(ok).To(BeFalse())
			}
			annotations := depl.Annotations
			if annotations != nil {
				_, ok := annotations[deployer.OwnerKind]
				Expect(ok).To(BeFalse())
				_, ok = annotations[deployer.OwnerName]
				Expect(ok).To(BeFalse())
				_, ok = annotations[deployer.OwnerTier]
				Expect(ok).To(BeFalse())
			}

			deleteClusterProfile(clusterProfile)
		})
})
