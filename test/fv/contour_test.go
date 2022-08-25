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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1beta1"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/contour"
)

const (
	gatewayClass = `kind: GatewayClass
apiVersion: gateway.networking.k8s.io/v1beta1
metadata:
  name: contour
spec:
  controllerName: projectcontour.io/gateway-controller`

	gateway = `kind: Gateway
apiVersion: gateway.networking.k8s.io/v1beta1
metadata:
  name: contour
  namespace: projectcontour
spec:
  gatewayClassName: contour
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      allowedRoutes:
        namespaces:
          from: All`
)

var _ = Describe("Contour", func() {
	const (
		namePrefix           = "contour"
		deploymentInfoLength = 2
	)

	It("Deploy and updates Contour correctly", Label("FV"), func() {
		Byf("Add configMap containing contour policy")
		configMap := createConfigMapWithPolicy("default", namePrefix+randomString(), gatewayClass)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterFeature matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterFeature := getClusterfeature(namePrefix, map[string]string{key: value})
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterFeature.Spec.ContourConfiguration = &configv1alpha1.ContourConfiguration{
			InstallationMode: configv1alpha1.ContourInstallationModeGateway,
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMap.Namespace, Name: configMap.Name},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterFeature)).To(Succeed())

		verifyClusterFeatureMatches(clusterFeature)

		clusterSummary := verifyClusterSummary(clusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		for i := range contour.ContourGatewayDeployments {
			deplInfo := strings.Split(contour.ContourGatewayDeployments[i], "/")
			Expect(len(deplInfo)).To(Equal(deploymentInfoLength))
			Byf("Verifying countor deployment %s/%s is present", deplInfo[0], deplInfo[1])
			Eventually(func() bool {
				depl := &appsv1.Deployment{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: deplInfo[0], Name: deplInfo[1]}, depl)
				return err == nil
			}, timeout, pollingInterval).Should(BeTrue())
		}

		Byf("Verifying Gateway contour is present")
		Eventually(func() error {
			policy := &gatewayapi.GatewayClass{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: "contour"}, policy)
			return err
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for contour", clusterSummary.Name)
		verifyFeatureStatus(clusterSummary.Name, configv1alpha1.FeatureContour, configv1alpha1.FeatureStatusProvisioned)

		Byf("Modifying configMap")
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		currentConfigMap = updateConfigMapWithPolicy(currentConfigMap, gatewayClass, gateway)
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		Byf("Verifying Gateway contour instance is present")
		Eventually(func() error {
			policy := &gatewayapi.Gateway{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "projectcontour", Name: "contour"}, policy)
			return err
		}, timeout, pollingInterval).Should(BeNil())

		Byf("changing clusterfeature to not require any contour configuration anymore")
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.ContourConfiguration = nil
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		verifyClusterSummary(currentClusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying proper GatewayClass is removed from the workload cluster")
		Eventually(func() bool {
			policy := &gatewayapi.GatewayClass{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Name: "contour"}, policy)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying proper Gateway is removed from the workload cluster")
		Eventually(func() bool {
			policy := &gatewayapi.Gateway{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "projectcontour", Name: "contour"}, policy)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterFeature(clusterFeature)
	})
})
