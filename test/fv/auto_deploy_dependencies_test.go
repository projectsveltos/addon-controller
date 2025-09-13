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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	disallowLatestTag = `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: disallow-latest-tag
  annotations:
    policies.kyverno.io/title: Disallow Latest Tag
    policies.kyverno.io/category: Best Practices
    policies.kyverno.io/minversion: 1.6.0
    policies.kyverno.io/severity: medium
    policies.kyverno.io/subject: Pod
    policies.kyverno.io/description: >-
      The ':latest' tag is mutable and can lead to unexpected errors if the
      image changes. A best practice is to use an immutable tag that maps to
      a specific version of an application Pod. This policy validates that the image
      specifies a tag and that it is not called latest.
spec:
  validationFailureAction: Audit
  background: true
  rules:
  - name: require-image-tag
    match:
      any:
      - resources:
          kinds:
          - Pod
    validate:
      message: "An image tag is required."
      foreach:
        - list: "request.object.spec.containers"
          pattern:
            image: "*:*"
        - list: "request.object.spec.initContainers"
          pattern:
            image: "*:*"
        - list: "request.object.spec.ephemeralContainers"
          pattern:
            image: "*:*"
  - name: validate-image-tag
    match:
      any:
      - resources:
          kinds:
          - Pod
    validate:
      message: "Using a mutable image tag e.g. 'latest' is not allowed."
      foreach:
        - list: "request.object.spec.containers"
          pattern:
            image: "!*:latest"
        - list: "request.object.spec.initContainers"
          pattern:
            image: "!*:latest"
        - list: "request.object.spec.ephemeralContainers"
          pattern:
            image: "!*:latest"`
)

var _ = Describe("Feature", func() {
	const (
		namePrefix = "auto-deploy-dependencies-"
	)

	It("With AutoDeployDependencies set Sveltos resolves all prerequesities", Label("NEW-FV", "NEW-FV-PULLMODE"), func() {
		Byf("Create a ClusterProfile matching NO Cluster") // clusterSelector is not set
		helmClusterProfile := getClusterProfile(namePrefix, map[string]string{})
		Byf("Create ClusterProfile %s to deploy Kyverno helm chart", helmClusterProfile.Name)
		helmClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.4.2",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "kyverno",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
		}
		Expect(k8sClient.Create(context.TODO(), helmClusterProfile)).To(Succeed())

		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		Byf("Create a configMap with disallow-latest-tag Kyverno policy")
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), disallowLatestTag)
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		policyClusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		policyClusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Byf("Make ClusterProfile %s depend on ClusterProfile %s", policyClusterProfile.Name, helmClusterProfile.Name)
		policyClusterProfile.Spec.DependsOn = []string{helmClusterProfile.Name}
		Byf("Make ClusterProfile %s reference ConfigMap %s/%s",
			policyClusterProfile.Name, configMap.Namespace, configMap.Name)
		policyClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		Expect(k8sClient.Create(context.TODO(), policyClusterProfile)).To(Succeed())

		verifyClusterProfileMatches(policyClusterProfile)

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: policyClusterProfile.Name},
			currentClusterProfile)).To(Succeed())

		Byf("Verify ClusterSummary for dependent ClusterProfile")
		policyClusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		// Because of dependencies, expect also the ClusterProfile deploying Kyverno is a match
		verifyClusterProfileMatches(helmClusterProfile)

		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: helmClusterProfile.Name},
			currentClusterProfile)).To(Succeed())
		Byf("Verify CLusterSummary for prerequisite ClusterProfile")
		helmClusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		charts := []configv1beta1.Chart{
			{ReleaseName: "kyverno-latest", ChartVersion: "3.4.2", Namespace: "kyverno"},
		}
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, helmClusterProfile.Name,
			helmClusterSummary.Spec.ClusterNamespace, helmClusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		policies := []policy{
			{kind: "ClusterPolicy", name: "disallow-latest-tag", namespace: "", group: "kyverno.io"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, policyClusterProfile.Name,
			policyClusterSummary.Spec.ClusterNamespace, policyClusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureResources,
			policies, nil)

		deleteClusterProfile(policyClusterProfile)

		verifyClusterProfileIsNotAMatch(helmClusterProfile)

		deleteClusterProfile(helmClusterProfile)
	})
})

func verifyClusterProfileIsNotAMatch(clusterProfile *configv1beta1.ClusterProfile) {
	Byf("Verifying Cluster %s/%s is NOT a match for ClusterProfile %s",
		kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), clusterProfile.Name)
	Eventually(func() bool {
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
		if err != nil {
			return false
		}
		for i := range currentClusterProfile.Status.MatchingClusterRefs {
			if currentClusterProfile.Status.MatchingClusterRefs[i].Namespace == kindWorkloadCluster.GetNamespace() &&
				currentClusterProfile.Status.MatchingClusterRefs[i].Name == kindWorkloadCluster.GetName() &&
				currentClusterProfile.Status.MatchingClusterRefs[i].APIVersion == clusterv1.GroupVersion.String() {

				return false
			}
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())
}
