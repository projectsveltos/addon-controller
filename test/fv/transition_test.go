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
	"github.com/projectsveltos/libsveltos/lib/deployer"
)

const (
	transitionSharedClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list"]`

	// transitionSharedClusterRoleUpdated is the successor's version of the same ClusterRole
	// (same name, via %s) with different rules, so the transition proves the successor can
	// actually update the resource it took over, not just leave the predecessor's content in
	// place untouched.
	transitionSharedClusterRoleUpdated = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "watch"]`

	transitionExclusiveClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list"]`

	transitionLabelValue = "fv-transition"
)

// Transition is Serial because it changes cluster labels, which would affect other tests.
var _ = Describe("Transition", Serial, func() {
	const (
		namePrefix = "transition-"
	)

	var (
		predecessor *configv1beta1.ClusterProfile
		successor   *configv1beta1.ClusterProfile
	)

	AfterEach(func() {
		Byf("Restoring cluster label %s=%s", key, value)
		setLabelOnCluster(value)

		if successor != nil {
			deleteClusterProfile(successor)
			successor = nil
		}
		predecessor = nil
	})

	It("A shared resource is taken over and updated in place, and the predecessor's exclusive resource is still cleaned up",
		Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
			Byf("Create predecessor ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			predecessor = getClusterProfile(namePrefix+"predecessor-", map[string]string{key: value})
			predecessor.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Create(context.TODO(), predecessor)).To(Succeed())
			verifyClusterProfileMatches(predecessor)
			verifyClusterSummary(clusterops.ClusterProfileLabelName, predecessor.Name, &predecessor.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			configMapNs := randomString()
			Byf("Create configMap's namespace %s", configMapNs)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: configMapNs,
				},
			}
			Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

			Byf("Create the predecessor's ConfigMap for the shared ClusterRole")
			sharedClusterRoleName := namePrefix + randomString()
			sharedConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
				fmt.Sprintf(transitionSharedClusterRole, sharedClusterRoleName))
			Expect(k8sClient.Create(context.TODO(), sharedConfigMap)).To(Succeed())

			Byf("Create the predecessor-exclusive resource's ConfigMap, referenced only by the predecessor")
			exclusiveClusterRoleName := namePrefix + randomString()
			exclusiveConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
				fmt.Sprintf(transitionExclusiveClusterRole, exclusiveClusterRoleName))
			Expect(k8sClient.Create(context.TODO(), exclusiveConfigMap)).To(Succeed())

			Byf("Update predecessor ClusterProfile %s to reference both ConfigMaps", predecessor.Name)
			currentPredecessor := &configv1beta1.ClusterProfile{}
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: predecessor.Name}, currentPredecessor)).To(Succeed())
				currentPredecessor.Spec.PolicyRefs = []configv1beta1.PolicyRef{
					{Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind), Namespace: sharedConfigMap.Namespace, Name: sharedConfigMap.Name},
					{Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind), Namespace: exclusiveConfigMap.Namespace, Name: exclusiveConfigMap.Name},
				}
				return k8sClient.Update(context.TODO(), currentPredecessor)
			})).To(Succeed())

			predecessorSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, predecessor.Name, &currentPredecessor.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), predecessorSummary.Name, libsveltosv1beta1.FeatureResources)

			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			Byf("Verifying both ClusterRoles are created in the workload cluster")
			Eventually(func() error {
				cr := &rbacv1.ClusterRole{}
				return workloadClient.Get(context.TODO(), types.NamespacedName{Name: sharedClusterRoleName}, cr)
			}, timeout, pollingInterval).Should(BeNil())
			Eventually(func() error {
				cr := &rbacv1.ClusterRole{}
				return workloadClient.Get(context.TODO(), types.NamespacedName{Name: exclusiveClusterRoleName}, cr)
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Storing the shared ClusterRole's UID and creation timestamp")
			originalSharedRole := &rbacv1.ClusterRole{}
			Expect(workloadClient.Get(context.TODO(), types.NamespacedName{Name: sharedClusterRoleName}, originalSharedRole)).To(Succeed())
			originalUID := originalSharedRole.UID
			originalCreationTimestamp := originalSharedRole.CreationTimestamp

			Byf("Create the successor's ConfigMap for the shared ClusterRole %s, with different rules than the predecessor's",
				sharedClusterRoleName)
			successorSharedConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
				fmt.Sprintf(transitionSharedClusterRoleUpdated, sharedClusterRoleName))
			Expect(k8sClient.Create(context.TODO(), successorSharedConfigMap)).To(Succeed())

			Byf("Create successor ClusterProfile, TransitionFrom predecessor %s, referencing its own version of the shared ConfigMap",
				predecessor.Name)
			successor = getClusterProfile(namePrefix+"successor-", map[string]string{key: transitionLabelValue})
			successor.Spec.SyncMode = configv1beta1.SyncModeContinuous
			successor.Spec.TransitionFrom = []string{predecessor.Name}
			successor.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind), Namespace: successorSharedConfigMap.Namespace, Name: successorSharedConfigMap.Name},
			}
			Expect(k8sClient.Create(context.TODO(), successor)).To(Succeed())

			Byf("Changing cluster label from %s=%s to %s=%s to trigger the transition",
				key, value, key, transitionLabelValue)
			setLabelOnCluster(transitionLabelValue)

			Byf("Verifying the shared ClusterRole is never absent and never recreated during the transition")
			Consistently(func() error {
				cr := &rbacv1.ClusterRole{}
				if err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: sharedClusterRoleName}, cr); err != nil {
					return err
				}
				if cr.UID != originalUID {
					return fmt.Errorf("ClusterRole was recreated: original UID %s, current %s", originalUID, cr.UID)
				}
				if !cr.CreationTimestamp.Equal(&originalCreationTimestamp) {
					return fmt.Errorf("ClusterRole was recreated: original creation timestamp %v, current %v",
						originalCreationTimestamp, cr.CreationTimestamp)
				}
				return nil
			}, timeout/2, pollingInterval).Should(BeNil())

			Byf("Verifying successor ClusterProfile eventually matches and is Provisioned")
			verifyClusterProfileMatches(successor)
			successorSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, successor.Name, &successor.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), successorSummary.Name, libsveltosv1beta1.FeatureResources)

			Byf("Verifying predecessor's ClusterSummary %s is eventually gone, now that the successor took over",
				predecessorSummary.Name)
			Eventually(func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: predecessorSummary.Namespace, Name: predecessorSummary.Name},
					currentClusterSummary)
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying the predecessor-exclusive ClusterRole %s is removed once the predecessor's teardown completes",
				exclusiveClusterRoleName)
			Eventually(func() bool {
				cr := &rbacv1.ClusterRole{}
				err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: exclusiveClusterRoleName}, cr)
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying the shared ClusterRole %s is still present, now owned by the successor %s",
				sharedClusterRoleName, successor.Name)
			Eventually(func() bool {
				cr := &rbacv1.ClusterRole{}
				if err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: sharedClusterRoleName}, cr); err != nil {
					return false
				}
				return cr.UID == originalUID && cr.Annotations[deployer.OwnerName] == successor.Name
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying the successor actually updated the shared ClusterRole %s to its own content, not just left it alone",
				sharedClusterRoleName)
			Eventually(func() bool {
				cr := &rbacv1.ClusterRole{}
				if err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: sharedClusterRoleName}, cr); err != nil {
					return false
				}
				for i := range cr.Rules {
					for j := range cr.Rules[i].Verbs {
						if cr.Rules[i].Verbs[j] == "watch" {
							return true
						}
					}
				}
				return false
			}, timeout, pollingInterval).Should(BeTrue())
		})
})
