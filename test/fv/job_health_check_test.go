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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// jobHealthCheckConfigMap is the resource ClusterProfile.PolicyRefs deploys; the ValidateHealth
// JobCheck runs after it, gated on FeatureResources.
const jobHealthCheckConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  foo: bar`

// jobHealthCheckJob is a fast-completing (or fast-failing, via %[3]d exit code) Job manifest
// referenced by ValidateHealth.JobCheck.JobRef. backoffLimit is 0 so a failing Job fails
// immediately instead of retrying.
const jobHealthCheckJob = `apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: probe
        image: busybox:1.36
        command: ["sh", "-c", "exit %d"]`

var _ = Describe("JobCheck", func() {
	const (
		namePrefix = "job-check-"
	)

	It("provisions once the referenced Job completes successfully", Label("Enterprise", "NEW-FV-PULLMODE"), func() {
		resourceNs := randomString()
		resourceName := randomString()
		jobName := randomString()

		Byf("Create ConfigMap in the management cluster holding the deployed ConfigMap resource")
		resourceConfigMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
			fmt.Sprintf(jobHealthCheckConfigMap, resourceName, resourceNs))
		Expect(k8sClient.Create(context.TODO(), resourceConfigMap)).To(Succeed())

		Byf("Create ConfigMap in the management cluster holding a Job manifest that exits 0")
		jobConfigMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
			fmt.Sprintf(jobHealthCheckJob, jobName, defaultNamespace, 0))
		Expect(k8sClient.Create(context.TODO(), jobConfigMap)).To(Succeed())

		Byf("Create ClusterProfile deploying the ConfigMap resource with a JobCheck ValidateHealth")
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: resourceConfigMap.Namespace,
				Name:      resourceConfigMap.Name,
			},
		}
		clusterProfile.Spec.ValidateHealths = []libsveltosv1beta1.ValidateHealth{
			{
				Name:      "job-completes",
				FeatureID: libsveltosv1beta1.FeatureResources,
				JobCheck: &libsveltosv1beta1.JobHealthCheck{
					JobRef: libsveltosv1beta1.PolicyRef{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: jobConfigMap.Namespace,
						Name:      jobConfigMap.Name,
					},
					Timeout: &metav1.Duration{Duration: time.Minute},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s becomes Provisioned for Resources once the Job completes", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

		Byf("Deleting ConfigMap %s/%s (deployed resource)", resourceConfigMap.Namespace, resourceConfigMap.Name)
		Expect(k8sClient.Delete(context.TODO(), resourceConfigMap)).To(Succeed())

		Byf("Deleting ConfigMap %s/%s (Job manifest)", jobConfigMap.Namespace, jobConfigMap.Name)
		Expect(k8sClient.Delete(context.TODO(), jobConfigMap)).To(Succeed())

		deleteClusterProfile(clusterProfile)
	})

	It("reports a failure while the referenced Job keeps failing", Label("Enterprise", "NEW-FV-PULLMODE"), func() {
		resourceNs := randomString()
		resourceName := randomString()
		jobName := randomString()

		Byf("Create ConfigMap in the management cluster holding the deployed ConfigMap resource")
		resourceConfigMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
			fmt.Sprintf(jobHealthCheckConfigMap, resourceName, resourceNs))
		Expect(k8sClient.Create(context.TODO(), resourceConfigMap)).To(Succeed())

		Byf("Create ConfigMap in the management cluster holding a Job manifest that exits 1")
		jobConfigMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
			fmt.Sprintf(jobHealthCheckJob, jobName, defaultNamespace, 1))
		Expect(k8sClient.Create(context.TODO(), jobConfigMap)).To(Succeed())

		Byf("Create ClusterProfile deploying the ConfigMap resource with a JobCheck ValidateHealth")
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: resourceConfigMap.Namespace,
				Name:      resourceConfigMap.Name,
			},
		}
		clusterProfile.Spec.ValidateHealths = []libsveltosv1beta1.ValidateHealth{
			{
				Name:      "job-fails",
				FeatureID: libsveltosv1beta1.FeatureResources,
				JobCheck: &libsveltosv1beta1.JobHealthCheck{
					JobRef: libsveltosv1beta1.PolicyRef{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: jobConfigMap.Namespace,
						Name:      jobConfigMap.Name,
					},
					Timeout: &metav1.Duration{Duration: time.Minute},
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s reports a JobCheck failure for Resources", clusterSummary.Name)
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			if err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary); err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				fs := &currentClusterSummary.Status.FeatureSummaries[i]
				if fs.FeatureID == libsveltosv1beta1.FeatureResources &&
					fs.FailureMessage != nil &&
					strings.Contains(*fs.FailureMessage, "job-fails") {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Deleting ConfigMap %s/%s (deployed resource)", resourceConfigMap.Namespace, resourceConfigMap.Name)
		Expect(k8sClient.Delete(context.TODO(), resourceConfigMap)).To(Succeed())

		Byf("Deleting ConfigMap %s/%s (Job manifest)", jobConfigMap.Namespace, jobConfigMap.Name)
		Expect(k8sClient.Delete(context.TODO(), jobConfigMap)).To(Succeed())

		deleteClusterProfile(clusterProfile)
	})
})
