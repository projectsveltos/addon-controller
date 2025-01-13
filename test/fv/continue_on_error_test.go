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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
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

	It("With ContinueOnError set, Sveltos keeps deploying after an error", Label("NEW-FV"), func() {
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

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Update ClusterProfile %s to deploy helm charts and referencing ConfigMap %s/%s",
			clusterProfile.Name, configMap.Namespace, configMap.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())

		// Cert-manager installation will fails as CRDs are not present and we are not deploying those
		// ALso sets timeout otherwise helm takes too long before giving up on cert-manager failures (due to CRDs not being installed)
		const two = 2
		timeout := metav1.Duration{Duration: two * time.Minute}
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://charts.konghq.com",
				RepositoryName:   "kong",
				ChartName:        "kong/kong",
				ChartVersion:     "2.46.0",
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
					Timeout: &timeout,
				},
			},
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.3.4",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "kyverno",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
			},
			{
				RepositoryURL:    "https://helm.nginx.com/stable/",
				RepositoryName:   "nginx-stable",
				ChartName:        "nginx-stable/nginx-ingress",
				ChartVersion:     "1.3.1",
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
		Byf("Setting ClusterProfile %s Spec.ContinueOnError true", clusterProfile.Name)
		currentClusterProfile.Spec.ContinueOnError = true

		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		charts := []configv1beta1.Chart{
			{ReleaseName: "kyverno-latest", ChartVersion: "3.3.4", Namespace: "kyverno"},
			{ReleaseName: "kong", ChartVersion: "2.46.0", Namespace: "kong"},
			{ReleaseName: "nginx-latest", ChartVersion: "1.3.1", Namespace: "nginx"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureHelm,
			nil, charts)

		policies := []policy{
			{kind: "Namespace", name: resourceNamespace, namespace: "", group: ""},
			{kind: "Deployment", name: "nginx-deployment", namespace: resourceNamespace, group: "apps"},
		}
		By("MGIANLUC")
		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureResources,
			policies, nil)

		deleteClusterProfile(clusterProfile)
	})
})
