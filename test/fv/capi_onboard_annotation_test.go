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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// This test runs in Serial because it requires the addon controller to only onboard
// capi cluster with onboard annotations
var _ = Describe("Helm", Serial, func() {
	const (
		namePrefix        = "onboard-"
		onboardAnnotation = "onboard-capi"

		addonDeplNamespace = "projectsveltos"
		addonDeplName      = "addon-controller"
	)

	BeforeEach(func() {
		Byf("Set capi-onboard-annotation for deployment %s/%s", addonDeplNamespace, addonDeplName)
		updateOnboardAnnotationArg(addonDeplNamespace, addonDeplName, onboardAnnotation)

		removeAnnotationFromCluster(onboardAnnotation)
	})

	AfterEach(func() {
		Byf("Reset capi-onboard-annotation for deployment %s/%s", addonDeplNamespace, addonDeplName)
		updateOnboardAnnotationArg(addonDeplNamespace, addonDeplName, "")

		removeAnnotationFromCluster(onboardAnnotation)
	})

	It("Onboards CAPI Cluster with onboard annotation", Label("NEW-FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://prometheus-community.github.io/helm-charts",
					RepositoryName:   "prometheus-community",
					ChartName:        "prometheus-community/prometheus",
					ChartVersion:     "25.24.0",
					ReleaseName:      "prometheus",
					ReleaseNamespace: "prometheus",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    "https://grafana.github.io/helm-charts",
					RepositoryName:   "grafana",
					ChartName:        "grafana/grafana",
					ChartVersion:     "8.3.4",
					ReleaseName:      "grafana",
					ReleaseNamespace: "grafana",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		// Addon-controller is set to only onboard CAPI cluster with onboard-capi annotation
		// CAPI cluster does not have onboard annotation
		Byf("Verifying Cluster %s/%s is NOT a match for ClusterProfile %s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), clusterProfile.Name)
		Consistently(func() bool {
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
			if err != nil {
				// Ignore error getting clusterProfile. We know it exists. Any error
				// would cause this test to fail.
				Byf("error getting clusterProfile %v (ignoring it)", err)
				return true
			}
			for i := range currentClusterProfile.Status.MatchingClusterRefs {
				if currentClusterProfile.Status.MatchingClusterRefs[i].Namespace == kindWorkloadCluster.GetNamespace() &&
					currentClusterProfile.Status.MatchingClusterRefs[i].Name == kindWorkloadCluster.GetName() &&
					currentClusterProfile.Status.MatchingClusterRefs[i].APIVersion == clusterv1.GroupVersion.String() {

					return false
				}
			}
			return true
		}, time.Minute, pollingInterval).Should(BeTrue())

		time.Sleep(pollingInterval)
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		Expect(len(currentClusterProfile.Status.MatchingClusterRefs)).To(BeZero())

		Byf("Add onboard annotation on CAPI cluster")
		currentCluster := &clusterv1.Cluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: kindWorkloadCluster.GetName()},
			currentCluster)).To(Succeed())
		annotations := currentCluster.Annotations
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[onboardAnnotation] = randomString()
		currentCluster.Annotations = annotations
		Expect(k8sClient.Update(context.TODO(), currentCluster)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
			libsveltosv1beta1.FeatureHelm)

		charts := []configv1beta1.Chart{
			{ReleaseName: "grafana", ChartVersion: "8.3.4", Namespace: "grafana"},
			{ReleaseName: "prometheus", ChartVersion: "25.24.0", Namespace: "prometheus"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		deleteClusterProfile(clusterProfile)
	})
})

func updateOnboardAnnotationArg(addonDeplNamespace, addonDeplName, value string) {
	depl := &appsv1.Deployment{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: addonDeplNamespace, Name: addonDeplName},
		depl)).To(Succeed())

	found := false
	for i := range depl.Spec.Template.Spec.Containers {
		container := depl.Spec.Template.Spec.Containers[i]
		for j := range container.Args {
			if strings.Contains(container.Args[j], "--capi-onboard-annotation=") {
				found = true
				container.Args[j] = fmt.Sprintf("--capi-onboard-annotation=%s", value)
			}
		}
	}
	Expect(found).To(BeTrue())

	Expect(k8sClient.Update(context.TODO(), depl)).To(Succeed())

	time.Sleep(time.Minute)

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: addonDeplNamespace, Name: addonDeplName},
			depl)
		if err != nil {
			return false
		}
		return depl.Status.AvailableReplicas == *depl.Spec.Replicas
	}, timeout, pollingInterval).Should(BeTrue())
}

func removeAnnotationFromCluster(onboardAnnotation string) {
	Byf("Remove %s annotation from capi cluster", onboardAnnotation)
	currentCluster := &clusterv1.Cluster{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: kindWorkloadCluster.GetName()},
		currentCluster)).To(Succeed())
	annotations := currentCluster.Annotations
	if annotations != nil {
		delete(annotations, onboardAnnotation)
	}
	currentCluster.Annotations = annotations
	Expect(k8sClient.Update(context.TODO(), currentCluster)).To(Succeed())
}
