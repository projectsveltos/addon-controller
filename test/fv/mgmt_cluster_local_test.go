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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	mgmtLocalPolicyTemplate = `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-test-{{ .Cluster.metadata.name }}
  namespace: %s
data:
  name: {{ .Cluster.metadata.name }}
  namespace: {{ .Cluster.metadata.namespace }}`
)

var _ = Describe("ClusterProfile matching the management cluster with deploymentType Local", func() {
	const (
		namePrefix = "mgmt-cluster-local-"
		mgmt       = "mgmt"
	)

	It("Deploys Local resources when the matching cluster is the self-managed management cluster",
		Label("FV", "PULLMODE", "EXTENDED"), func() {

			By("Grant addon-controller permission to create/delete ConfigMaps in the management cluster")
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				clusterRole := &rbacv1.ClusterRole{}
				Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: addonControllerRoleExtra},
					clusterRole)).To(Succeed())
				clusterRole.Rules = append(clusterRole.Rules,
					rbacv1.PolicyRule{
						Verbs:     []string{"*"},
						APIGroups: []string{""},
						Resources: []string{configMapsResource},
					})
				return k8sClient.Update(context.TODO(), clusterRole)
			})
			Expect(err).To(BeNil())

			Byf("Create a ClusterProfile matching the mgmt Cluster")
			clusterProfile := &configv1beta1.ClusterProfile{
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
					SyncMode:             configv1beta1.SyncModeContinuousWithDriftDetection,
					StopMatchingBehavior: configv1beta1.WithdrawPolicies,
				},
			}
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())
			Byf("Created ClusterProfile %s", clusterProfile.Name)

			configMapNamespace := randomString()
			Byf("Create configMap's namespace %s", configMapNamespace)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: configMapNamespace,
				},
			}
			Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

			Byf("Create a ConfigMap containing a templated ConfigMap, to be deployed in namespace %s", configMapNamespace)
			policyConfigMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
				fmt.Sprintf(mgmtLocalPolicyTemplate, configMapNamespace))
			policyConfigMap.Annotations = map[string]string{
				libsveltosv1beta1.PolicyTemplateAnnotation: annotationTrueValue,
			}
			Expect(k8sClient.Create(context.TODO(), policyConfigMap)).To(Succeed())

			Byf("Update ClusterProfile %s to reference ConfigMap %s/%s with deploymentType Local",
				clusterProfile.Name, policyConfigMap.Namespace, policyConfigMap.Name)
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				currentClusterProfile := &configv1beta1.ClusterProfile{}
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
					{
						Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace:      policyConfigMap.Namespace,
						Name:           policyConfigMap.Name,
						DeploymentType: configv1beta1.DeploymentTypeLocal,
					},
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			currentClusterProfile := &configv1beta1.ClusterProfile{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				mgmt, mgmt, string(libsveltosv1beta1.ClusterTypeSveltos))

			Byf("Verifying ClusterSummary %s status is set to Provisioned for resources", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureResources)

			Byf("Verifying ConfigMap my-test-%s is present in namespace %s of the management cluster",
				mgmt, configMapNamespace)
			Consistently(func() error {
				cm := &corev1.ConfigMap{}
				return k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNamespace, Name: "my-test-" + mgmt}, cm)
			}, timeout/2, pollingInterval).Should(BeNil())

			deleteClusterProfile(clusterProfile)

			Byf("Verifying ConfigMap my-test-%s is removed from the management cluster", mgmt)
			Eventually(func() bool {
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: configMapNamespace, Name: "my-test-" + mgmt}, cm)
				if err == nil {
					return !cm.DeletionTimestamp.IsZero()
				}
				return err != nil && apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
