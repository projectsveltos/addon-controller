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

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Profile with Deployment Local", func() {
	const (
		namePrefix = "profile-local"

		sa = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
  labels:
    name: fv`
	)

	It("deploymentType Local only allows resources to be created in the same namespace", Label("FV", "EXTENDED"), func() {
		By("Grant addon-controller permission to create/delete ServiceAccount in the management cluster")
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			clusterRole := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: "addon-controller-role-extra"},
				clusterRole)).To(Succeed())
			clusterRole.Rules = append(clusterRole.Rules,
				rbacv1.PolicyRule{
					Verbs:     []string{"*"},
					APIGroups: []string{""},
					Resources: []string{"serviceaccounts"},
				})
			return k8sClient.Update(context.TODO(), clusterRole)
		})
		Expect(err).To(BeNil())

		Byf("Create a Profile matching Cluster %s/%s",
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		profile := getProfile(defaultNamespace, namePrefix, map[string]string{key: value})
		profile.Spec.SyncMode = configv1beta1.SyncModeContinuous

		Expect(k8sClient.Create(context.TODO(), profile)).To(Succeed())

		verifyProfileMatches(profile)

		verifyClusterSummary(controllers.ProfileLabelName,
			profile.Name, &profile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		cmName := randomString()
		Byf("Create a configMap with a sa %s/%s", defaultNamespace, cmName)
		// This ConfigMap contains an invalid ServiceAccount. It is invalid
		// because Profile will reference this ConfigMap and use DeploymentType: Local
		// but the ServiceAccount is in a namespace different than profile and that is
		// not allowed
		invalidConfigMap := createConfigMapWithPolicy(defaultNamespace, randomString(),
			fmt.Sprintf(sa, randomString(), randomString()))
		Expect(k8sClient.Create(context.TODO(), invalidConfigMap)).To(Succeed())

		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: invalidConfigMap.Namespace, Name: invalidConfigMap.Name},
			currentConfigMap)).To(Succeed())

		Byf("Update Profile %s to reference ConfigMap %s/%s",
			profile.Name, invalidConfigMap.Namespace, invalidConfigMap.Name)

		currentProfile := &configv1beta1.Profile{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name},
			currentProfile)).To(Succeed())
		currentProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace:      invalidConfigMap.Namespace,
				Name:           invalidConfigMap.Name,
				DeploymentType: configv1beta1.DeploymentTypeLocal,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(controllers.ProfileLabelName,
			currentProfile.Name, &currentProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s/%s reports an error", clusterSummary.Namespace, clusterSummary.Name)
		Eventually(func() bool {
			errorMsg := "profile can only deploy resource in same namespace in the management cluster"
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}

			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == configv1beta1.FeatureResources {
					if currentClusterSummary.Status.FeatureSummaries[i].Status != configv1beta1.FeatureStatusProvisioned {
						if currentClusterSummary.Status.FeatureSummaries[i].FailureMessage != nil &&
							*currentClusterSummary.Status.FeatureSummaries[i].FailureMessage == errorMsg {
							return true
						}
					}
				}
			}

			return false
		}, timeout, pollingInterval).Should(BeTrue())

		cmName = randomString()
		saName := randomString()
		Byf("Create a configMap with a sa %s/%s", defaultNamespace, cmName)
		// This ConfigMap contains a valid ServiceAccount. It is valid
		// because Profile will reference this ConfigMap and use DeploymentType: Local
		// and the ServiceAccount is in the Profile namespace
		validConfigMap := createConfigMapWithPolicy(defaultNamespace, randomString(),
			fmt.Sprintf(sa, saName, defaultNamespace))
		Expect(k8sClient.Create(context.TODO(), validConfigMap)).To(Succeed())

		Byf("Update Profile %s to reference ConfigMap %s/%s",
			profile.Name, validConfigMap.Namespace, validConfigMap.Name)

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name},
			currentProfile)).To(Succeed())
		currentProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace:      validConfigMap.Namespace,
				Name:           validConfigMap.Name,
				DeploymentType: configv1beta1.DeploymentTypeLocal,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentProfile)).To(Succeed())

		clusterSummary = verifyClusterSummary(controllers.ProfileLabelName,
			currentProfile.Name, &currentProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying ClusterSummary %s/%s is Provisioned", clusterSummary.Namespace, clusterSummary.Name)
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}

			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == configv1beta1.FeatureResources {
					if currentClusterSummary.Status.FeatureSummaries[i].Status == configv1beta1.FeatureStatusProvisioned {
						return true
					}
				}
			}

			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ServiceAccount %s/%s is present", defaultNamespace, saName)
		serviceAccount := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: defaultNamespace, Name: saName}, serviceAccount)).To(Succeed())

		deleteProfile(profile)

		Byf("Verifying ServiceAccount is gone from the management cluster")
		Eventually(func() bool {
			currentServiceAccount := &corev1.ServiceAccount{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: defaultNamespace, Name: saName},
				currentServiceAccount)
			if err == nil {
				return !currentServiceAccount.DeletionTimestamp.IsZero()
			}
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Deleting ConfigMap %s/%s", invalidConfigMap.Namespace, invalidConfigMap.Name)
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: invalidConfigMap.Namespace, Name: invalidConfigMap.Name},
			currentConfigMap)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentConfigMap))

		Byf("Deleting ConfigMap %s/%s", validConfigMap.Namespace, validConfigMap.Name)
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: validConfigMap.Namespace, Name: validConfigMap.Name},
			currentConfigMap)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentConfigMap))

	})
})
