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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	validNamespace = `kind: Namespace
apiVersion: v1
metadata:
  name: %s`

	invalidNamespace = `kind: Namespace1
apiVersion: v1
metadata:
  name: nginx`

	validDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-deployment
  namespace: %s
spec:
  replicas: 2 # number of pods to run
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:latest
        ports:
        - containerPort: 80`
)

var _ = Describe("Feature", Serial, func() {
	const (
		namePrefix = "continue-on-error-"
	)

	It("With ContinueOnError set, Sveltos keeps deploying after an error", Label("NEW-FV", "NEW-FV-PULLMODE"), func() {
		configMapNs := randomString()
		Byf("Create configMap's namespace %s", configMapNs)
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNs,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())
		Byf("Create a configMap with valid and invalid resources")
		resourceNamespace := randomString()
		configMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(),
			fmt.Sprintf(validNamespace, resourceNamespace),
			invalidNamespace,
			fmt.Sprintf(validDeployment, resourceNamespace))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		maxConsecutiveFailures := uint(3)
		clusterProfile.Spec.MaxConsecutiveFailures = &maxConsecutiveFailures
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.GetNamespace(),
			kindWorkloadCluster.GetName(), getClusterType())

		Byf("Update ClusterProfile %s to deploy helm charts and referencing ConfigMap %s/%s",
			clusterProfile.Name, configMap.Namespace, configMap.Name)
		Byf("Setting ClusterProfile %s Spec.ContinueOnError true", clusterProfile.Name)

		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name},
				currentClusterProfile)).To(Succeed())

			// Cert-manager installation will fails as CRDs are not present and we are not deploying those
			// ALso sets timeout otherwise helm takes too long before giving up on cert-manager failures (due to CRDs not being installed)
			const two = 2
			helmTimeout := metav1.Duration{Duration: two * time.Minute}
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://charts.konghq.com",
					RepositoryName:   "kong",
					ChartName:        "kong/kong",
					ChartVersion:     "2.51.0",
					ReleaseName:      "kong",
					ReleaseNamespace: "kong",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    "https://charts.jetstack.io",
					RepositoryName:   "jetstack",
					ChartName:        "jetstack/cert-manager",
					ChartVersion:     "v1.16.2",
					ReleaseName:      "cert-manager",
					ReleaseNamespace: "cert-manager",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Options: &configv1beta1.HelmOptions{
						Timeout: &helmTimeout,
					},
				},
				{
					RepositoryURL:    "https://helm.nginx.com/stable/",
					RepositoryName:   "nginx-stable",
					ChartName:        "nginx-stable/nginx-ingress",
					ChartVersion:     "2.2.1",
					ReleaseName:      "nginx-latest",
					ReleaseNamespace: "nginx",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			}
			currentClusterProfile.Spec.ContinueOnError = true

			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		charts := []configv1beta1.Chart{
			{ReleaseName: "kong", ChartVersion: "2.51.0", Namespace: "kong"},
			{ReleaseName: "nginx-latest", ChartVersion: "2.2.1", Namespace: "nginx"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		policies := []policy{
			{kind: "Namespace", name: resourceNamespace, namespace: "", group: ""},
			{kind: "Deployment", name: "nginx-deployment", namespace: resourceNamespace, group: "apps"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureResources,
			policies, nil)

		const six = 6
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				fs := &currentClusterSummary.Status.FeatureSummaries[i]
				if fs.FeatureID == libsveltosv1beta1.FeatureResources {
					return fs.FailureMessage != nil &&
						*fs.FailureMessage == "the maximum number of consecutive errors has been reached"
				}
			}
			return false
		}, six*time.Minute, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})
