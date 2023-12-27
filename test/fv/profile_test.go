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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var (
	jobTemplate = `apiVersion: batch/v1
kind: Job
metadata:
  namespace: %s
  name: %s
spec:
  template:
    spec:
      containers:
      - name: pi
        image: perl:5.34.0
        command: ["perl",  "-Mbignum=bpi", "-wle", "print bpi(2000)"]
      restartPolicy: Never
  backoffLimit: 4`
)

var _ = Describe("Profile", func() {
	const (
		namePrefix = "profile-"
	)

	It("Deploy Profile", Label("FV", "EXTENDED"), func() {
		Byf("Create a Profile matching Cluster %s/%s",
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		profile := getProfile(defaultNamespace, namePrefix, map[string]string{key: value})
		profile.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		Expect(k8sClient.Create(context.TODO(), profile)).To(Succeed())

		verifyProfileMatches(profile)

		verifyClusterSummary(controllers.ProfileLabelName,
			profile.Name, &profile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		ns := randomString()
		jobName := randomString()
		Byf("Create a configMap with a job")
		configMap := createConfigMapWithPolicy(defaultNamespace, randomString(),
			fmt.Sprintf(jobTemplate, ns, jobName))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name},
			currentConfigMap)).To(Succeed())

		Byf("Update Profile %s to reference ConfigMap %s/%s",
			profile.Name, configMap.Namespace, configMap.Name)

		currentProfile := &configv1alpha1.Profile{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name},
			currentProfile)).To(Succeed())
		currentProfile.Spec.PolicyRefs = []configv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(controllers.ProfileLabelName,
			currentProfile.Name, &currentProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying proper Job is present in the workload cluster")
		Eventually(func() bool {
			currentJob := &batchv1.Job{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: ns, Name: jobName},
				currentJob)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		policies := []policy{
			{kind: "Job", name: jobName, namespace: ns, group: "batch"},
		}
		verifyClusterConfiguration(configv1alpha1.ProfileKind, profile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1alpha1.FeatureResources,
			policies, nil)

		deleteProfile(profile)

		Byf("Verifying proper Job is gone from the workload cluster")
		Eventually(func() bool {
			currentJob := &batchv1.Job{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: ns, Name: jobName},
				currentJob)
			if err == nil {
				return !currentJob.DeletionTimestamp.IsZero()
			}
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Deleting ConfigMap %s/%s", configMap.Namespace, configMap.Name)
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name},
			currentConfigMap)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentConfigMap))
	})
})
