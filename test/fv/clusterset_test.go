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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("ClusterSet", func() {
	const (
		namePrefix = "clusterset-"
	)

	It("ClusterSet picks matching clusters", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterSet matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterSet := getClusterSet(namePrefix, map[string]string{key: value})
		clusterSet.Spec.MaxReplicas = 1
		Expect(k8sClient.Create(context.TODO(), clusterSet)).To(Succeed())
		verifyClusterSetMatches(clusterSet)

		By("Verify ClusterSet has selected the matching cluster")
		currentClusterSet := &libsveltosv1beta1.ClusterSet{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		Expect(currentClusterSet.Status.SelectedClusterRefs).ToNot(BeNil())
		Expect(len(currentClusterSet.Status.SelectedClusterRefs)).To(Equal(1))
		Expect(currentClusterSet.Status.SelectedClusterRefs).To(ContainElement(
			corev1.ObjectReference{
				Kind:       "Cluster",
				APIVersion: clusterv1.GroupVersion.String(),
				Namespace:  kindWorkloadCluster.Namespace,
				Name:       kindWorkloadCluster.Name,
			}))

		By("Update ClusterSet MaxReplicas to 0")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		currentClusterSet.Spec.MaxReplicas = 0
		Expect(k8sClient.Update(context.TODO(), currentClusterSet)).To(Succeed())

		By("Verify no cluster is selected anymore")
		Eventually(func() bool {
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)
			if err != nil {
				return false
			}
			return len(currentClusterSet.Status.SelectedClusterRefs) == 0
		}, timeout, pollingInterval).Should(BeTrue())

		By("Update ClusterSet MaxReplicas to 2")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		currentClusterSet.Spec.MaxReplicas = 2
		Expect(k8sClient.Update(context.TODO(), currentClusterSet)).To(Succeed())

		By("Verify matching cluster is selected again")
		Eventually(func() bool {
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)
			if err != nil {
				return false
			}
			return len(currentClusterSet.Status.SelectedClusterRefs) != 0
		}, timeout, pollingInterval).Should(BeTrue())

		By("Delete ClusterSet")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentClusterSet)).To(Succeed())
	})
})
