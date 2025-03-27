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
	"fmt"
	"strings"

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

const (
	configMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: example-configmap
  namespace: %s
data:
  key1: value1
  key2: value2
`

	deploymentWithVolume = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 2
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
      - name: my-app-container
        image: nginx:latest
        ports:
        - containerPort: 80
        volumeMounts:
        - name: config-volume
          mountPath: /etc/config
      volumes:
      - name: config-volume
        configMap:
          name: example-configmap

`
)

var _ = Describe("Reloader", func() {
	const (
		namePrefix = "reloader-"
	)

	It("Deploy ClusterProfile with Reloader knob set", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile with Reloader knob set matching Cluster %s/%s",
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.Reloader = true
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		ns := randomString()
		deploymentName := randomString()
		Byf("Create a configMap with a deployment and configmap")
		configMap := createConfigMapWithPolicy(defaultNamespace, randomString(),
			fmt.Sprintf(configMap, ns),
			fmt.Sprintf(deploymentWithVolume, deploymentName, ns))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name},
			currentConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap %s/%s",
			clusterProfile.Name, configMap.Namespace, configMap.Name)

		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name,
			libsveltosv1beta1.FeatureResources)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		if isAgentLessMode() {
			Byf("Verifying Reloader is present in the management cluster")
			currentReloader := &libsveltosv1beta1.Reloader{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: getReloaderName(clusterProfile.Name)},
				currentReloader)).To(Succeed())
			Byf("Verifying Reloader list Deployment")
			Expect(len(currentReloader.Spec.ReloaderInfo)).To(Equal(1))
			Expect(currentReloader.Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1beta1.ReloaderInfo{
					Kind:      "Deployment",
					Namespace: ns,
					Name:      deploymentName,
				},
			))
		} else {
			Byf("Verifying Reloader is present in the managed cluster")
			currentReloader := &libsveltosv1beta1.Reloader{}
			Expect(workloadClient.Get(context.TODO(),
				types.NamespacedName{Name: getReloaderName(clusterProfile.Name)},
				currentReloader)).To(Succeed())
			Byf("Verifying Reloader list Deployment")
			Expect(len(currentReloader.Spec.ReloaderInfo)).To(Equal(1))
			Expect(currentReloader.Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1beta1.ReloaderInfo{
					Kind:      "Deployment",
					Namespace: ns,
					Name:      deploymentName,
				},
			))
		}

		deleteClusterProfile(clusterProfile)

		if isAgentLessMode() {
			Byf("Verifying Reloader is removed from the management cluster")
			Eventually(func() bool {
				currentReloader := &libsveltosv1beta1.Reloader{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: getReloaderName(clusterProfile.Name)},
					currentReloader)
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		} else {
			Byf("Verifying Reloader is removed from the workload cluster")
			Eventually(func() bool {
				currentReloader := &libsveltosv1beta1.Reloader{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Name: getReloaderName(clusterProfile.Name)},
					currentReloader)
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		}
	})
})

// getReloaderName returns the Reloader's name
func getReloaderName(clusterProfileName string) string {
	feature := libsveltosv1beta1.FeatureResources
	return fmt.Sprintf("%s--%s", clusterProfileName, strings.ToLower(string(feature)))
}
