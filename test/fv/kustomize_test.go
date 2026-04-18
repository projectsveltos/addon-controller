/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Kustomize with GitRepository", func() {
	const (
		namePrefix     = "kustomize-"
		mgmt           = "mgmt"
		deploymentName = "the-deployment"
	)

	It("Deploy Kustomize resources with Flux", Serial, Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching mgmt Cluster")
		gitRepositoryNamespace := "flux2"
		mgmtClusterProfile := &configv1beta1.ClusterProfile{
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

		mgmtClusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), mgmtClusterProfile)).To(Succeed())
		Byf("Created ClusterProfile %s", mgmtClusterProfile.Name)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			mgmtClusterProfile.Name, &mgmtClusterProfile.Spec,
			mgmt, mgmt, string(libsveltosv1beta1.ClusterTypeSveltos))

		By("Deploying Flux on the management cluster")
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: mgmtClusterProfile.Name},
				currentClusterProfile)
			if err != nil {
				return err
			}
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://fluxcd-community.github.io/helm-charts",
					RepositoryName:   "flux2",
					ChartName:        "flux2/flux2",
					ChartVersion:     "2.18.2",
					ReleaseName:      "flux2",
					ReleaseNamespace: gitRepositoryNamespace,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: mgmtClusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			mgmt, mgmt, string(libsveltosv1beta1.ClusterTypeSveltos))

		listOpts := []client.ListOption{
			client.InNamespace(gitRepositoryNamespace),
		}

		time.Sleep(time.Minute)

		// When Flux is deployed, Sveltos restarts (so watchers on Flux resources can be started)
		Byf("Waiting for Sveltos addon-controller to be healthy")
		Eventually(func() bool {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "projectsveltos", Name: "addon-controller"},
				deployment)
			return err == nil && deployment.Status.AvailableReplicas == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Flux deployments are present")
		Eventually(func() bool {
			deployments := &appsv1.DeploymentList{}
			err := k8sClient.List(context.TODO(), deployments, listOpts...)
			return err == nil && len(deployments.Items) > 0
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(mgmt, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Deleting NetworkPolicy in the %s namespace", gitRepositoryNamespace)
		netPolList := &networkingv1.NetworkPolicyList{}

		Expect(k8sClient.List(context.Background(), netPolList, listOpts...)).To(Succeed())
		for i := range netPolList.Items {
			Expect(k8sClient.Delete(context.TODO(), &netPolList.Items[i])).To(Succeed())
		}

		gitRepositoryName := gitRepositoryNamespace

		Byf("Create GitRepository %s/%s", gitRepositoryNamespace, gitRepositoryName)
		gitRepository := &sourcev1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: gitRepositoryNamespace,
				Name:      gitRepositoryName,
			},
			Spec: sourcev1.GitRepositorySpec{
				URL:      "https://github.com/gianlucam76/kustomize",
				Interval: metav1.Duration{Duration: time.Minute},
				Reference: &sourcev1.GitRepositoryRef{
					Branch: "main",
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), gitRepository)).To(Succeed())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: gitRepositoryNamespace, Name: gitRepositoryName},
			gitRepository)).To(Succeed())

		Byf("Verifying GitRepository %s/%s artifact is set", gitRepositoryNamespace, gitRepositoryName)
		Eventually(func() bool {
			gitRepository := &sourcev1.GitRepository{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: gitRepositoryNamespace, Name: gitRepositoryName},
				gitRepository)
			return err == nil &&
				gitRepository.Status.Artifact != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		managedClusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		managedClusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		Expect(k8sClient.Create(context.TODO(), managedClusterProfile)).To(Succeed())

		verifyClusterProfileMatches(managedClusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			managedClusterProfile.Name, &managedClusterProfile.Spec, kindWorkloadCluster.GetNamespace(),
			kindWorkloadCluster.GetName(), getClusterType())

		targetNamespace := randomString()

		Byf("Update ClusterProfile %s to reference GitRepository %s/%s",
			managedClusterProfile.Name, gitRepositoryNamespace, gitRepositoryName)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: managedClusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.KustomizationRefs = []configv1beta1.KustomizationRef{
				{
					Kind:            sourcev1.GitRepositoryKind,
					Namespace:       gitRepositoryNamespace,
					Name:            gitRepositoryName,
					Path:            "./helloWorld",
					TargetNamespace: targetNamespace,
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: managedClusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary = verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Service is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-service"}, currentService)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper Deployment is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: deploymentName}, currentDeployment)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper ConfigMap is created in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-map"}, currentConfigMap)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for kustomize", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureKustomize)

		currentConfigMap := &corev1.ConfigMap{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "the-map"}, currentConfigMap)).To(Succeed())

		currentService := &corev1.Service{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: "the-service"}, currentService)).To(Succeed())

		currentDeployment := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: deploymentName}, currentDeployment)).To(Succeed())

		policies := []policy{
			{kind: "Service", name: currentService.Name, namespace: targetNamespace, group: ""},
			{kind: "ConfigMap", name: currentConfigMap.Name, namespace: targetNamespace, group: ""},
			{kind: "Deployment", name: currentDeployment.Name, namespace: targetNamespace, group: "apps"},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, managedClusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureKustomize,
			policies, nil)

		Byf("Deleting deployment")
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: targetNamespace, Name: deploymentName}, currentDeployment)).To(Succeed())
		Expect(workloadClient.Delete(context.TODO(), currentDeployment)).To(Succeed())

		Byf("Verifying proper Deployment is recreated in the workload cluster in namespace %s", targetNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: deploymentName}, currentDeployment)
			return err == nil &&
				currentDeployment.DeletionTimestamp.IsZero()
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Changing clusterprofile to not reference GitRepository anymore")
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: managedClusterProfile.Name},
			currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.KustomizationRefs = []configv1beta1.KustomizationRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying Service is removed from the workload cluster")
		Eventually(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-service"}, currentService)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Deployment is removed from the workload cluster")
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: deploymentName}, currentDeployment)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ConfigMap is removed from the workload cluster")
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: "the-map"}, currentConfigMap)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(managedClusterProfile)
		deleteClusterProfile(mgmtClusterProfile)
	})
})
