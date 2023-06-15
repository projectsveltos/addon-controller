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
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	//nolint: gosec // just a test
	deplAndSecret = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  selector:
    matchLabels:
      app: nginx
  replicas: 2 # tells deployment to run 2 pods matching the template
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:1.14.2
        ports:
        - containerPort: 80
---
apiVersion: v1
kind: Secret
metadata:
  name: % s
  namespace: default
data:
  .secret-file: dmFsdWUtMg0KDQo=
`
)

var _ = Describe("Feature", func() {
	const (
		namePrefix = "tenant-admin-"
	)

	// In this test, tenant is given permission to create Deployment but not to create Secret.
	// When a ClusterProfile is created by this tenant, deployment will fail because of missing permissions.
	It("Tenant created ClusterProfile: Deploy resources in the management cluster fails", Label("FV", "EXTENDED"), func() {
		Byf("Create a ServiceAccount representing tenant")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		tenantServiceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: ns.Name,
			},
		}
		Expect(k8sClient.Create(context.TODO(), tenantServiceAccount)).To(Succeed())

		Byf("Create a ClusterRole that has permission to create Deployment")
		clusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"apps"},
					Resources: []string{"deployments"},
					Verbs:     []string{"*"},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterRole)).To(Succeed())

		Byf("Create a ClusterRoleBinding that associate ServiceAccount %s/%s to ClusterRole %s",
			tenantServiceAccount.Namespace, tenantServiceAccount.Name, clusterRole.Name)
		clusterRoleBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     clusterRole.Name,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Namespace: tenantServiceAccount.Namespace,
					Name:      tenantServiceAccount.Name,
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterRoleBinding)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		resourceName := randomString()

		configMapNs := defaultNamespace
		configMapName := namePrefix + randomString()

		Byf("Create a configMap with a Deployment and a Secret")
		configMap := createConfigMapWithPolicy(configMapNs, configMapName, fmt.Sprintf(deplAndSecret, resourceName, resourceName))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Update ClusterProfile %s to reference ConfigMap and add Tenant admin labels", clusterProfile.Name)
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Labels = map[string]string{
			libsveltosv1alpha1.ServiceAccountNameLabel:      tenantServiceAccount.Name,
			libsveltosv1alpha1.ServiceAccountNamespaceLabel: tenantServiceAccount.Namespace,
		}
		currentClusterProfile.Spec.PolicyRefs = []configv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: configMapNs,
				Name:      configMapName,
				// Deploy content of this configMap into management cluster
				DeploymentType: configv1alpha1.DeploymentTypeLocal,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		verifyClusterSummary(currentClusterProfile, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying deployment has been created into management cluster")
		Eventually(func() error {
			currentDeployment := &appsv1.Deployment{}
			return k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: defaultNamespace, Name: resourceName},
				currentDeployment)
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary reports an error")
		Eventually(func() bool {
			clusterSummary, err := getClusterSummary(context.TODO(),
				clusterProfile.Name, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
			if err != nil ||
				clusterSummary == nil {
				return false
			}
			if clusterSummary.Status.FeatureSummaries == nil {
				return false
			}
			if len(clusterSummary.Status.FeatureSummaries) != 1 {
				return false
			}
			if clusterSummary.Status.FeatureSummaries[0].FailureMessage == nil {
				return false
			}
			return strings.Contains(*clusterSummary.Status.FeatureSummaries[0].FailureMessage,
				"is forbidden")
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying secret has not been created into management cluster")
		Eventually(func() bool {
			currentSecret := &corev1.Secret{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: defaultNamespace, Name: resourceName},
				currentSecret)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: ns.Name}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})
