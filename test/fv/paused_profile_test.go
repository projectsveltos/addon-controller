/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
)

var _ = Describe("Paused Profile", func() {
	const (
		namePrefix = "paused-"
	)

	It("Paused Profile is not reconciled", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile with paused annotation matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Annotations = map[string]string{
			configv1beta1.ProfilePausedAnnotation: "true",
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		// ClusterProfile with paused annotation, are not reconciled. So no cluster will be a match
		By("Verify no cluster is match")
		Consistently(func() bool {
			currentclusterProfile := &configv1beta1.ClusterProfile{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name},
				currentclusterProfile)
			return err == nil && len(currentclusterProfile.Status.MatchingClusterRefs) == 0
		}, timeout, pollingInterval).Should(BeTrue())

		By("Remove the paused annotation from the ClusterProfile")
		currentclusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name},
			currentclusterProfile)).To(Succeed())
		currentclusterProfile.Annotations = map[string]string{}
		Expect(k8sClient.Update(context.TODO(), currentclusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		deleteClusterProfile(clusterProfile)
	})
})
