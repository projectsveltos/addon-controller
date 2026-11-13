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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// excludedAnnotation is an annotation an operator stamps on an object Sveltos deployed
	// (like mariadb-operator does with k8s.mariadb.com/last-gtid). It is excluded from drift detection.
	excludedAnnotation = "example.com/last-run"
	// excludedAnnotationPath is excludedAnnotation as a JSON pointer: "/" is escaped as "~1".
	excludedAnnotationPath = "/metadata/annotations/example.com~1last-run"

	// metadataExclusionObjectYAML deploys a Secret. A Secret is used because no controller adds
	// annotations to it (a Deployment gets deployment.kubernetes.io/revision), so only this test
	// changes its metadata.
	metadataExclusionObjectYAML = `apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
stringData:
  key: %s
`
)

var _ = Describe("Drift detection with driftExclusions on metadata", func() {
	const (
		namePrefix = "drift-metadata-"
		secretKey  = "key"
		// noDriftWindow is how long the test watches for a drift that must not be reported
		noDriftWindow = 60 * time.Second
	)

	// Regression test for https://github.com/projectsveltos/addon-controller/issues/2090:
	// drift-detection-manager hashed labels and annotations before applying the driftExclusions
	// patches, so an exclusion on /metadata/annotations/... was ignored. Any change to the
	// excluded annotation was reported as drift and the profile was redeployed.
	It("ignores a change to an annotation excluded by driftExclusions and still detects any other drift",
		Label("NEW-FV", "EXTENDED"), func() {
			Byf("Getting client to access the workload cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			secretNamespace := randomString()
			Byf("Create namespace %s", secretNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: secretNamespace}}
			Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

			secretName := namePrefix + randomString()
			expectedValue := randomString()
			Byf("Create ConfigMap deploying Secret %s/%s", secretNamespace, secretName)
			policyConfigMap := createConfigMapWithPolicy(secretNamespace, namePrefix+randomString(),
				fmt.Sprintf(metadataExclusionObjectYAML, secretName, secretNamespace, expectedValue))
			Expect(k8sClient.Create(context.TODO(), policyConfigMap)).To(Succeed())

			Byf("Create a ClusterProfile matching Cluster %s/%s, excluding annotation %s of Secret %s/%s",
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(),
				excludedAnnotation, secretNamespace, secretName)
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
			clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: policyConfigMap.Namespace,
					Name:      policyConfigMap.Name,
				},
			}
			clusterProfile.Spec.DriftExclusions = []libsveltosv1beta1.DriftExclusion{
				{
					Paths: []string{excludedAnnotationPath},
					Target: &libsveltosv1beta1.PatchSelector{
						Kind:      kindSecret,
						Version:   apiVersionV1,
						Namespace: secretNamespace,
						Name:      secretName,
					},
				},
			}
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyClusterProfileMatches(clusterProfile)

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name,
				&clusterProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying Secret %s/%s is created in the workload cluster", secretNamespace, secretName)
			Eventually(func() error {
				return workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: secretNamespace, Name: secretName}, &corev1.Secret{})
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
				libsveltosv1beta1.FeatureResources)

			verifyDriftDetectionManagerDeployment(workloadClient)

			resourceSummaryClient := workloadClient
			if isAgentLessMode() {
				resourceSummaryClient = k8sClient
			}
			// The hash drift is evaluated against is taken when the resource is first tracked.
			// Annotate only after that, or the annotation would end up in the baseline.
			verifyResourceSummaryExcludesAnnotationAndTracksResource(resourceSummaryClient, clusterSummary)

			annotateSecret := func(annotationValue string) {
				err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					currentSecret := &corev1.Secret{}
					Expect(workloadClient.Get(context.TODO(),
						types.NamespacedName{Namespace: secretNamespace, Name: secretName}, currentSecret)).To(Succeed())
					if currentSecret.Annotations == nil {
						currentSecret.Annotations = map[string]string{}
					}
					currentSecret.Annotations[excludedAnnotation] = annotationValue
					return workloadClient.Update(context.TODO(), currentSecret)
				})
				Expect(err).To(BeNil())
			}

			stampStart := metav1.NewTime(time.Now().Truncate(time.Second))
			Byf("Add annotation %s to Secret %s/%s, as an operator would", excludedAnnotation, secretNamespace, secretName)
			annotateSecret("1")
			Byf("Change annotation %s of Secret %s/%s", excludedAnnotation, secretNamespace, secretName)
			annotateSecret("2")

			Byf("Verifying no drift is reported for ClusterSummary %s: the annotation is excluded", clusterSummary.Name)
			Consistently(func() bool {
				return resourcesDriftDetectedSince(clusterSummary, stampStart)
			}, noDriftWindow, pollingInterval).Should(BeFalse())

			// Positive control: proves drift detection is running, so the check above was not vacuous.
			driftStart := metav1.NewTime(time.Now().Truncate(time.Second))
			Byf("Change data of Secret %s/%s: not excluded, so Sveltos is expected to revert it", secretNamespace, secretName)
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				currentSecret := &corev1.Secret{}
				Expect(workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: secretNamespace, Name: secretName}, currentSecret)).To(Succeed())
				currentSecret.Data[secretKey] = []byte(randomString())
				return workloadClient.Update(context.TODO(), currentSecret)
			})
			Expect(err).To(BeNil())

			Byf("Verifying Sveltos reverts the data of Secret %s/%s", secretNamespace, secretName)
			Eventually(func() bool {
				currentSecret := &corev1.Secret{}
				err := workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: secretNamespace, Name: secretName}, currentSecret)
				if err != nil {
					return false
				}
				return string(currentSecret.Data[secretKey]) == expectedValue
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying ClusterSummary %s records Secret %s/%s as the drifted resource",
				clusterSummary.Name, secretNamespace, secretName)
			Eventually(func() bool {
				return resourcesDriftHistoryHasSecret(clusterSummary, driftStart, secretNamespace, secretName)
			}, timeout, pollingInterval).Should(BeTrue())

			deleteClusterProfile(clusterProfile)

			Byf("Verifying Secret %s/%s is removed from the workload cluster", secretNamespace, secretName)
			Eventually(func() bool {
				err := workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: secretNamespace, Name: secretName}, &corev1.Secret{})
				return err != nil
			}, timeout, pollingInterval).Should(BeTrue())

			currentNs := &corev1.Namespace{}
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: secretNamespace}, currentNs)).To(Succeed())
			Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
		})
})

// verifyResourceSummaryExcludesAnnotationAndTracksResource waits for the ResourceSummary of clusterSummary to
// carry the patch removing excludedAnnotationPath and to have a hash for the resource it tracks: the hash is
// the baseline drift is evaluated against.
func verifyResourceSummaryExcludesAnnotationAndTracksResource(c client.Client,
	clusterSummary *configv1beta1.ClusterSummary) {

	Byf("Verify ResourceSummary excludes %s and tracks the deployed Secret", excludedAnnotationPath)
	Eventually(func() bool {
		resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
		if err := c.List(context.TODO(), resourceSummaries); err != nil {
			return false
		}
		for i := range resourceSummaries.Items {
			rs := &resourceSummaries.Items[i]
			if !matchesClusterSummary(rs, clusterSummary) {
				continue
			}
			return resourceSummaryHasPatchForPath(rs, excludedAnnotationPath) && len(rs.Status.ResourceHashes) > 0
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

func resourceSummaryHasPatchForPath(rs *libsveltosv1beta1.ResourceSummary, path string) bool {
	for i := range rs.Spec.Patches {
		if rs.Spec.Patches[i].Target != nil && rs.Spec.Patches[i].Target.Kind == kindSecret &&
			strings.Contains(rs.Spec.Patches[i].Patch, path) {

			return true
		}
	}
	return false
}

// getResourcesDriftHistory returns the drift history of the Resources feature of clusterSummary, or nil
// when no drift has been recorded.
func getResourcesDriftHistory(clusterSummary *configv1beta1.ClusterSummary) *configv1beta1.DriftHistory {
	current := &configv1beta1.ClusterSummary{}
	err := k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, current)
	if err != nil {
		return nil
	}
	for i := range current.Status.FeatureSummaries {
		featureSummary := &current.Status.FeatureSummaries[i]
		if featureSummary.FeatureID == libsveltosv1beta1.FeatureResources {
			return featureSummary.DriftHistory
		}
	}

	return nil
}

// resourcesDriftDetectedSince returns true when the Resources feature recorded a drift at or after notBefore.
func resourcesDriftDetectedSince(clusterSummary *configv1beta1.ClusterSummary, notBefore metav1.Time) bool {
	history := getResourcesDriftHistory(clusterSummary)
	return history != nil && !history.LastDetectedTime.Before(&notBefore)
}

// resourcesDriftHistoryHasSecret returns true when the Resources feature recorded, at or after notBefore,
// a drift listing the Secret.
func resourcesDriftHistoryHasSecret(clusterSummary *configv1beta1.ClusterSummary, notBefore metav1.Time,
	namespace, name string) bool {

	history := getResourcesDriftHistory(clusterSummary)
	if history == nil || history.LastDetectedTime.Before(&notBefore) {
		return false
	}
	for i := range history.Resources {
		resource := &history.Resources[i]
		if resource.Group == "" && resource.Kind == kindSecret &&
			resource.Namespace == namespace && resource.Name == name {

			return true
		}
	}

	return false
}
