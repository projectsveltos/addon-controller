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
			configv1beta1.ProfilePausedAnnotation: annotationTrueValue,
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
		}, timeout/2, pollingInterval).Should(BeTrue())

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

	It("Paused Profile and ClusterProfile report they are paused and can still be deleted",
		Label("FV", "PULLMODE", "EXTENDED"), func() {
			Byf("Create a Profile and a ClusterProfile matching Cluster %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			profile := getProfile(defaultNamespace, namePrefix, map[string]string{key: value})
			profile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Create(context.TODO(), profile)).To(Succeed())

			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyProfileMatches(profile)
			verifyClusterSummary(clusterops.ProfileLabelName, profile.Name, &profile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			verifyClusterProfileMatches(clusterProfile)
			verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Add the paused annotation to Profile %s/%s and ClusterProfile %s",
				profile.Namespace, profile.Name, clusterProfile.Name)
			// The controllers update the status concurrently: retry on conflict
			Eventually(func() error {
				currentProfile := &configv1beta1.Profile{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name}, currentProfile)
				if err != nil {
					return err
				}
				currentProfile.Annotations = map[string]string{
					configv1beta1.ProfilePausedAnnotation: annotationTrueValue,
				}
				return k8sClient.Update(context.TODO(), currentProfile)
			}, timeout, pollingInterval).Should(Succeed())

			Eventually(func() error {
				currentClusterProfile := &configv1beta1.ClusterProfile{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
				if err != nil {
					return err
				}
				currentClusterProfile.Annotations = map[string]string{
					configv1beta1.ProfilePausedAnnotation: annotationTrueValue,
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			}, timeout, pollingInterval).Should(Succeed())

			// A paused Profile must report it, as a paused ClusterProfile does
			Byf("Verify Profile %s/%s and ClusterProfile %s report they are paused",
				profile.Namespace, profile.Name, clusterProfile.Name)
			Eventually(func() bool {
				currentProfile := &configv1beta1.Profile{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name}, currentProfile)
				return err == nil && currentProfile.Status.ReconciliationSuspended &&
					currentProfile.Status.SuspensionReason != nil &&
					*currentProfile.Status.SuspensionReason == "Profile is paused"
			}, timeout, pollingInterval).Should(BeTrue())

			Eventually(func() bool {
				currentClusterProfile := &configv1beta1.ClusterProfile{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
				return err == nil && currentClusterProfile.Status.ReconciliationSuspended &&
					currentClusterProfile.Status.SuspensionReason != nil &&
					*currentClusterProfile.Status.SuspensionReason == "ClusterProfile is paused"
			}, timeout, pollingInterval).Should(BeTrue())

			// Pause must not block deletion. The annotation is still set: both helpers delete the
			// instance and verify its ClusterSummaries and the instance itself are gone.
			deleteProfile(profile)
			deleteClusterProfile(clusterProfile)
		})
})
