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

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	widgetGroup = "repro.example.com"
	widgetKind  = "Widget"

	// widgetInstanceYAML deploys one Widget instance. Cluster-scoped, so no namespace.
	widgetInstanceYAML = `apiVersion: repro.example.com/v1
kind: Widget
metadata:
  name: %s
`

	// baselineConfigMapYAML deploys an ordinary ConfigMap: a kind drift-detection-manager
	// already knows about at startup, with no CRD involved.
	baselineConfigMapYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  control: "true"
`
)

// getWidgetCRD returns the CustomResourceDefinition for the Widget kind used to reproduce
// https://github.com/projectsveltos/addon-controller/issues/1999. Matches the CRD from that
// issue's repro steps.
func getWidgetCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "widgets." + widgetGroup,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: widgetGroup,
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     widgetKind,
				Plural:   "widgets",
				Singular: "widget",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type:                   "object",
							XPreserveUnknownFields: boolPtr(true),
						},
					},
				},
			},
		},
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func getWidgetObject(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(fmt.Sprintf("%s/v1", widgetGroup))
	u.SetKind(widgetKind)
	u.SetName(name)
	return u
}

var _ = Describe("Drift detection for a kind whose CRD arrives after the manager started", func() {
	const namePrefix = "new-crd-drift-"

	// Regression test for https://github.com/projectsveltos/addon-controller/issues/1999:
	// drift-detection-manager's remote RESTMapper is built once at startup. Before the fix, a
	// CRD installed afterward was never picked up, so instances of that kind were silently
	// never protected by ContinuousWithDriftDetection -- deleting one was never reverted.
	//
	// The trick to reproduce it (and to prove the fix): drift-detection-manager must already be
	// running, with its RESTMapper already built, before the Widget CRD is installed. This test
	// forces that ordering explicitly with a baseline ConfigMap deployment first, rather than
	// relying on some other spec having started drift-detection-manager for this cluster first.
	It("protects a resource whose CRD was installed after drift-detection-manager started",
		Label("FV", "PULLMODE", "EXTENDED"), func() {
			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			Byf("Create a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyClusterProfileMatches(clusterProfile)
			verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			configMapNs := randomString()
			Byf("Create configMap's namespace %s", configMapNs)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: configMapNs}}
			Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

			controlConfigMapName := namePrefix + randomString()
			Byf("Deploying a baseline ConfigMap %s/%s: an ordinary kind, no CRD involved, so "+
				"drift-detection-manager starts for this cluster before the Widget CRD exists",
				configMapNs, controlConfigMapName)
			policyConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
				fmt.Sprintf(baselineConfigMapYAML, controlConfigMapName, configMapNs))
			Expect(k8sClient.Create(context.TODO(), policyConfigMap)).To(Succeed())

			currentClusterProfile := &configv1beta1.ClusterProfile{}
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: policyConfigMap.Namespace,
						Name:      policyConfigMap.Name,
					},
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying baseline ConfigMap %s/%s is created in the workload cluster",
				configMapNs, controlConfigMapName)
			Eventually(func() error {
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNs, Name: controlConfigMapName}, &corev1.ConfigMap{})
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
				libsveltosv1beta1.FeatureResources)

			Byf("Verifying drift-detection-manager is running for this cluster, before the Widget CRD exists")
			verifyDriftDetectionManagerDeployment(workloadClient)

			Byf("Installing the Widget CRD in the workload cluster, after drift-detection-manager already started")
			widgetCRD := getWidgetCRD()
			Expect(workloadClient.Create(context.TODO(), widgetCRD)).To(Succeed())

			Byf("Verifying the Widget CRD is Established")
			Eventually(func() bool {
				currentCRD := &apiextensionsv1.CustomResourceDefinition{}
				if err := workloadClient.Get(context.TODO(),
					types.NamespacedName{Name: widgetCRD.Name}, currentCRD); err != nil {
					return false
				}
				for i := range currentCRD.Status.Conditions {
					c := &currentCRD.Status.Conditions[i]
					if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Update ConfigMap %s/%s to also deploy a Widget instance",
				policyConfigMap.Namespace, policyConfigMap.Name)
			widgetName := namePrefix + randomString()
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				currentPolicyConfigMap := &corev1.ConfigMap{}
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: policyConfigMap.Namespace, Name: policyConfigMap.Name},
					currentPolicyConfigMap)).To(Succeed())
				currentPolicyConfigMap.Data["policy1.yaml"] = fmt.Sprintf(widgetInstanceYAML, widgetName)
				return k8sClient.Update(context.TODO(), currentPolicyConfigMap)
			})
			Expect(err).To(BeNil())

			Byf("Verifying Widget %s is created in the workload cluster", widgetName)
			Eventually(func() error {
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Name: widgetName}, getWidgetObject(widgetName))
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
				libsveltosv1beta1.FeatureResources)

			Byf("Deleting Widget %s from the workload cluster", widgetName)
			Expect(workloadClient.Delete(context.TODO(), getWidgetObject(widgetName))).To(Succeed())

			Byf("Verifying drift-detection-manager redeploys Widget %s after it is deleted "+
				"-- this is the regression check for #1999: before the fix, this never happens "+
				"because the CRD did not exist when drift-detection-manager started", widgetName)
			Eventually(func() error {
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Name: widgetName}, getWidgetObject(widgetName))
			}, timeout, pollingInterval).Should(BeNil())

			deleteClusterProfile(clusterProfile)

			Byf("Verifying Widget %s is removed from the workload cluster", widgetName)
			Eventually(func() bool {
				err := workloadClient.Get(context.TODO(),
					types.NamespacedName{Name: widgetName}, getWidgetObject(widgetName))
				return err != nil
			}, timeout, pollingInterval).Should(BeTrue())

			Expect(workloadClient.Delete(context.TODO(), widgetCRD)).To(Succeed())

			currentNs := &corev1.Namespace{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNs}, currentNs)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
		})
})
