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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	templatePolicy = `apiVersion: v1
kind: ConfigMap
metadata:
  name: template
  namespace: default 
data:
  info: |
    cidr="{{ index .Cluster.spec.clusterNetwork.pods.cidrBlocks 0 }}"`
)

var _ = Describe("Template", func() {
	const (
		namePrefix = "template-"
	)

	It("Deploy a template correctly", Label("FV", "EXTENDED"), func() {
		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Add configMap containing a template policy. Policy has annotation to indicate it is a template")
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), templatePolicy)
		configMap.Annotations = map[string]string{
			libsveltosv1beta1.PolicyTemplateAnnotation: "ok",
		}
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}

		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		By("Get CAPI cluster")
		currentCluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.Namespace, Name: kindWorkloadCluster.Name},
			currentCluster)).To(Succeed())

		Byf("Verifying policy containined in the referenced configMap is present with correct value")
		Eventually(func() bool {
			cm := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: defaultNamespace, Name: "template"}, cm)
			return err == nil &&
				strings.Contains(cm.Data["info"], currentCluster.Spec.ClusterNetwork.Pods.CIDRBlocks[0])
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for resources", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureResources)

		Byf("Changing clusterprofile to not reference configmap anymore")
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying policy is removed in the workload cluster")
		Eventually(func() bool {
			cm := &corev1.ConfigMap{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: defaultNamespace, Name: "template"}, cm)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})
