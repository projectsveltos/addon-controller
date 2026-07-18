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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// Both specs below deploy the same content: helloWorldWithOverlays/overlays/production from
// https://github.com/gianlucam76/kustomize, which references helloWorldWithOverlays/base via a
// relative path (../../base). That only works if the fetched archive's directory structure is
// preserved rather than flattened, which is what these two specs are here to verify.
var _ = Describe("Kustomize RemoteURL", func() {
	const (
		namePrefix   = "kustomize-remoteurl-"
		ociTestURL   = "oci://ghcr.io/gianlucam76/sveltos-oci-kustomize-test:head"
		httpsTestURL = "https://github.com/gianlucam76/kustomize/archive/refs/heads/main.tar.gz"
	)

	verifyKustomizeRemoteURLDeployment := func(remoteURL *configv1beta1.RemoteKustomizeURL, path string) {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		targetNamespace := randomString()

		Byf("Update ClusterProfile %s to reference RemoteURL %s", clusterProfile.Name, remoteURL.URL)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.KustomizationRefs = []configv1beta1.KustomizationRef{
				{
					RemoteURL:       remoteURL,
					Path:            path,
					TargetNamespace: targetNamespace,
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

		Byf("Verifying Service %s is created in namespace %s", kustomizeProdServiceName, targetNamespace)
		Eventually(func() bool {
			currentService := &corev1.Service{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: kustomizeProdServiceName}, currentService)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying Deployment %s is created in namespace %s", kustomizeProdDeployName, targetNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: kustomizeProdDeployName}, currentDeployment)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ConfigMap %s is created in namespace %s", kustomizeProdMapName, targetNamespace)
		Eventually(func() bool {
			currentConfigMap := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: kustomizeProdMapName}, currentConfigMap)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Provisioned for kustomize", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureKustomize)

		policies := []policy{
			{kind: kindService, name: kustomizeProdServiceName, namespace: targetNamespace, group: ""},
			{kind: kindConfigMap, name: kustomizeProdMapName, namespace: targetNamespace, group: ""},
			{kind: kindDeployment, name: kustomizeProdDeployName, namespace: targetNamespace, group: appsGroupName},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureKustomize,
			policies, nil)

		Byf("Removing RemoteURL reference from ClusterProfile %s", clusterProfile.Name)
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.KustomizationRefs = []configv1beta1.KustomizationRef{}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Byf("Verifying Deployment %s is removed from namespace %s", kustomizeProdDeployName, targetNamespace)
		Eventually(func() bool {
			currentDeployment := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: targetNamespace, Name: kustomizeProdDeployName}, currentDeployment)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	}

	It("Deploys Kustomize resources from an OCI artifact, preserving directory structure",
		Label("FV", "PULLMODE", "EXTENDED"), func() {
			verifyKustomizeRemoteURLDeployment(&configv1beta1.RemoteKustomizeURL{URL: ociTestURL}, "overlays/production")
		})

	It("Deploys Kustomize resources from an HTTPS tarball, preserving directory structure",
		Label("FV", "PULLMODE", "EXTENDED"), func() {
			verifyKustomizeRemoteURLDeployment(&configv1beta1.RemoteKustomizeURL{URL: httpsTestURL},
				"kustomize-main/helloWorldWithOverlays/overlays/production")
		})
})
