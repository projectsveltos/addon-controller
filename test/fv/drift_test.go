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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	clusterKey = "cluster"
)

var (
	labelsValues = `customLabels:
  %s: "{{ .Cluster.metadata.name }}"`

	cleanupControllerValues = `cleanupController:
  livenessProbe:
    httpGet:
      path: /health/liveness
      port: 9443
      scheme: HTTPS
    initialDelaySeconds: 16
    periodSeconds: %d
    timeoutSeconds: 5
    failureThreshold: 2
    successThreshold: 1

  readinessProbe:
    httpGet:
      path: /health/readiness
      port: 9443
      scheme: HTTPS
    initialDelaySeconds: 6
    periodSeconds: %d
    timeoutSeconds: 5
    failureThreshold: 6
    successThreshold: 1`

	admissionControllerValues = `admissionController:
  livenessProbe:
    httpGet:
      path: /health/liveness
      port: 9443
      scheme: HTTPS
    initialDelaySeconds: 16
    periodSeconds: %d
    timeoutSeconds: 5
    failureThreshold: 2
    successThreshold: 1

  readinessProbe:
    httpGet:
      path: /health/readiness
      port: 9443
      scheme: HTTPS
    initialDelaySeconds: 6
    periodSeconds: %d
    timeoutSeconds: 5
    failureThreshold: 6
    successThreshold: 1`
)

const (
	kyvernoNamespace            = "kyverno"
	admissionControllerDeplName = "kyverno-admission-controller"
	cleanupControllerDeplName   = "kyverno-cleanup-controller"
	cleanupImage                = "ghcr.io/kyverno/cleanup-controller:v1.12.5"
	admissionImage              = "ghcr.io/kyverno/kyverno:v1.12.5"
)

var _ = Describe("Helm", Serial, func() {
	const (
		namePrefix                = "drift-"
		kyvernoAdmissionImageName = "kyverno"
		kyvernoCleanupImageName   = "controller"
	)

	It("React to configuration drift and verifies Values/ValuesFrom", Label("FV", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(controllers.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		livenessPeriodSecond := int32(31)
		readinessPeriodSecond := int32(11)

		configMapNamespace := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapNamespace,
			},
		}
		Expect(k8sClient.Create(context.TODO(), ns))

		Byf("Creating ConfigMap to hold cleanup controller values")
		cleanupControllerConfigMap := createConfigMapWithPolicy(configMapNamespace, randomString(),
			fmt.Sprintf(cleanupControllerValues, livenessPeriodSecond, readinessPeriodSecond))
		Expect(k8sClient.Create(context.TODO(), cleanupControllerConfigMap)).To(Succeed())

		Byf("Creating ConfigMap to hold admission controller values")
		admissionControllerConfigMap := createConfigMapWithPolicy(configMapNamespace, randomString(),
			fmt.Sprintf(admissionControllerValues, livenessPeriodSecond, readinessPeriodSecond))
		Expect(k8sClient.Create(context.TODO(), admissionControllerConfigMap)).To(Succeed())

		Byf("Creating ConfigMap to hold labels controller values (with template annotation)")
		labelsConfigMap := createConfigMapWithPolicy(configMapNamespace, randomString(),
			fmt.Sprintf(labelsValues, clusterKey))
		labelsConfigMap.Annotations = map[string]string{
			libsveltosv1beta1.PolicyTemplateAnnotation: "ok",
		}
		Expect(k8sClient.Create(context.TODO(), labelsConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.2.6",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "kyverno",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
				Values: `admissionController:
  replicas: 1
backgroundController:
  replicas: 1
cleanupController:
  replicas: 1
reportsController:
  replicas: 1`,
				ValuesFrom: []configv1beta1.ValueFrom{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: labelsConfigMap.Namespace,
						Name:      labelsConfigMap.Name,
					},
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: cleanupControllerConfigMap.Namespace,
						Name:      cleanupControllerConfigMap.Name,
					},
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: admissionControllerConfigMap.Namespace,
						Name:      admissionControllerConfigMap.Name,
					},
				},
			},
		}

		By("use driftExclusion to ignore cleanup controller spec/replicas changes")
		currentClusterProfile.Spec.DriftExclusions = []configv1beta1.DriftExclusion{
			{
				Paths: []string{"/spec/replicas"},
				Target: &libsveltosv1beta1.PatchSelector{
					Kind:      "Deployment",
					Group:     "apps",
					Version:   "v1",
					Namespace: kyvernoNamespace,
					Name:      cleanupControllerDeplName,
				},
			},
		}

		By("Use patches to add projectsveltos.io/driftDetectionIgnore annotation")
		currentClusterProfile.Spec.Patches = []libsveltosv1beta1.Patch{
			{
				Patch: `- op: add
  path: /metadata/annotations/projectsveltos.io~1driftDetectionIgnore
  value: ok`,
				Target: &libsveltosv1beta1.PatchSelector{
					Group:     "apps",
					Version:   "v1",
					Kind:      "Deployment",
					Namespace: kyvernoNamespace,
					Name:      admissionControllerDeplName,
				},
			},
		}

		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(controllers.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying Kyverno deployment is created in the workload cluster")
		Eventually(func() bool {
			expectedReplicas := int32(1)
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kyvernoNamespace, Name: admissionControllerDeplName}, depl)
			if err != nil {
				return false
			}
			if !isDeplLabelCorrect(depl.Labels, clusterKey, kindWorkloadCluster.Name) {
				return false
			}
			return depl.Spec.Replicas != nil && *depl.Spec.Replicas == expectedReplicas
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying helm values")
		verifyHelmValues(workloadClient, kyvernoNamespace, admissionControllerDeplName,
			livenessPeriodSecond, readinessPeriodSecond)
		verifyHelmValues(workloadClient, kyvernoNamespace, cleanupControllerDeplName,
			livenessPeriodSecond, readinessPeriodSecond)

		if isAgentLessMode() {
			Byf("Verifying drift detection manager deployment is created in the management cluster")
			Eventually(func() bool {
				listOptions := []client.ListOption{
					client.MatchingLabels(
						map[string]string{
							"cluster-name":      kindWorkloadCluster.Name,
							"cluster-namespace": kindWorkloadCluster.Namespace,
						},
					),
				}

				depls := &appsv1.DeploymentList{}
				err = k8sClient.List(context.TODO(), depls, listOptions...)
				if err != nil {
					return false
				}
				if len(depls.Items) != 1 {
					return false
				}
				return *depls.Items[0].Spec.Replicas == depls.Items[0].Status.ReadyReplicas
			}, timeout, pollingInterval).Should(BeTrue())
		} else {
			Byf("Verifying drift detection manager deployment is created in the workload cluster")
			Eventually(func() bool {
				depl := &appsv1.Deployment{}
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "projectsveltos", Name: "drift-detection-manager"}, depl)
				if err != nil {
					return false
				}
				return *depl.Spec.Replicas == depl.Status.ReadyReplicas
			}, timeout, pollingInterval).Should(BeTrue())
		}

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.Namespace, clusterSummary.Name, configv1beta1.FeatureHelm)

		charts := []configv1beta1.Chart{
			{ReleaseName: "kyverno-latest", ChartVersion: "3.2.6", Namespace: "kyverno"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, configv1beta1.FeatureHelm,
			nil, charts)

		if isAgentLessMode() {
			verifyResourceSummary(k8sClient, clusterSummary)
		} else {
			// Verify ResourceSummary in the managed cluster
			verifyResourceSummary(workloadClient, clusterSummary)
		}

		// Wait to make sure a watcher is started in the managed cluster
		const sleepTime = 30
		time.Sleep(sleepTime * time.Second)

		// Change Kyverno image
		depl := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "kyverno", Name: "kyverno-cleanup-controller"}, depl)).To(Succeed())
		imageChanged := false
		for i := range depl.Spec.Template.Spec.Containers {
			if depl.Spec.Template.Spec.Containers[i].Name == kyvernoCleanupImageName {
				imageChanged = true
				depl.Spec.Template.Spec.Containers[i].Image = cleanupImage
			}
		}
		Expect(imageChanged).To(BeTrue())
		Expect(workloadClient.Update(context.TODO(), depl)).To(Succeed())

		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "kyverno", Name: "kyverno-cleanup-controller"}, depl)).To(Succeed())
		for i := range depl.Spec.Template.Spec.Containers {
			if depl.Spec.Template.Spec.Containers[i].Name == kyvernoCleanupImageName {
				By("Kyverno image is set to v1.11.0")
				Expect(depl.Spec.Template.Spec.Containers[i].Image).To(Equal(cleanupImage))
			}
		}

		Byf("Verifying Sveltos reacts to drift configuration change")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-cleanup-controller"}, depl)
			if err != nil {
				return false
			}
			for i := range depl.Spec.Template.Spec.Containers {
				if depl.Spec.Template.Spec.Containers[i].Name == kyvernoCleanupImageName {
					return depl.Spec.Template.Spec.Containers[i].Image == "ghcr.io/kyverno/cleanup-controller:v1.12.5"
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
		By("Kyverno image is reset to v1.12.5")

		// Change Kyverno image for admission controller
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)).To(Succeed())
		imageChanged = false
		for i := range depl.Spec.Template.Spec.Containers {
			if depl.Spec.Template.Spec.Containers[i].Name == kyvernoAdmissionImageName {
				imageChanged = true
				depl.Spec.Template.Spec.Containers[i].Image = admissionImage
			}
		}
		Expect(imageChanged).To(BeTrue())
		Expect(workloadClient.Update(context.TODO(), depl)).To(Succeed())

		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)).To(Succeed())
		for i := range depl.Spec.Template.Spec.Containers {
			if depl.Spec.Template.Spec.Containers[i].Name == kyvernoAdmissionImageName {
				By("Kyverno image is set to v1.11.0")
				Expect(depl.Spec.Template.Spec.Containers[i].Image).To(Equal(admissionImage))
			}
		}

		Byf("Verifying Sveltos does not reacts to drift configuration change as admission controller has ignore annotation")
		Consistently(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)
			if err != nil {
				return false
			}
			for i := range depl.Spec.Template.Spec.Containers {
				if depl.Spec.Template.Spec.Containers[i].Name == kyvernoAdmissionImageName {
					return depl.Spec.Template.Spec.Containers[i].Image == admissionImage
				}
			}
			return false
		}, timeout/4, pollingInterval).Should(BeTrue())
		By("Kyverno image is NOT reset to v1.12.5")

		By("Change values section")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.2.6",
				ReleaseName:      "kyverno-latest",
				ReleaseNamespace: "kyverno",
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
				Values: `admissionController:
  replicas: 3
backgroundController:
  replicas: 1
cleanupController:
  replicas: 1
reportsController:
  replicas: 1`,
			},
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		Byf("Verifying Kyverno deployment is updated in the workload cluster")
		Eventually(func() bool {
			expectedReplicas := int32(3)
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)
			if err != nil {
				return false
			}
			return depl.Spec.Replicas != nil && *depl.Spec.Replicas == expectedReplicas
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)

		Byf("Verifying Kyverno deployment is removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-latest"}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		currentNs := &corev1.Namespace{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: configMapNamespace}, currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})

func isAgentLessMode() bool {
	By("Getting addon-controller pod")
	addonControllerDepl := &appsv1.Deployment{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: "projectsveltos", Name: "addon-controller"},
		addonControllerDepl)).To(Succeed())

	Expect(len(addonControllerDepl.Spec.Template.Spec.Containers)).To(Equal(1))

	for i := range addonControllerDepl.Spec.Template.Spec.Containers[0].Args {
		if strings.Contains(addonControllerDepl.Spec.Template.Spec.Containers[0].Args[i], "agent-in-mgmt-cluster") {
			By("Addon-controller in agentless mode")
			return true
		}
	}

	return false
}

// verifyHelmValues verifies periodSecond is set on both livenessProbe and ReadinessProbe
func verifyHelmValues(workloadClient client.Client, deploymentNamespace, deploymentName string,
	livenessPeriodSecond, readyPeriodSecond int32) {

	depl := &appsv1.Deployment{}
	Expect(workloadClient.Get(context.TODO(),
		types.NamespacedName{Namespace: deploymentNamespace, Name: deploymentName},
		depl)).To(Succeed())

	Expect(len(depl.Spec.Template.Spec.Containers)).To(Equal(1))

	Byf("Verifying ReadinessProbe.PeriodSeconds on deployment %s/%s is %d", deploymentNamespace, deploymentName, readyPeriodSecond)
	Expect(depl.Spec.Template.Spec.Containers[0].ReadinessProbe).ToNot(BeNil())
	Expect(depl.Spec.Template.Spec.Containers[0].ReadinessProbe.PeriodSeconds).To(Equal(readyPeriodSecond))

	Byf("Verifying LivenessProbe.PeriodSeconds on deployment %s/%s is %d", deploymentNamespace, deploymentName, livenessPeriodSecond)
	Expect(depl.Spec.Template.Spec.Containers[0].LivenessProbe).ToNot(BeNil())
	Expect(depl.Spec.Template.Spec.Containers[0].LivenessProbe.PeriodSeconds).To(Equal(livenessPeriodSecond))
}

func isDeplLabelCorrect(lbls map[string]string, key, value string) bool {
	if lbls == nil {
		return false
	}
	v := lbls[key]

	return v == value
}

func verifyResourceSummary(c client.Client, clusterSummary *configv1beta1.ClusterSummary) {
	Byf("Verify ResourceSummary is present")

	lbls := map[string]string{
		libsveltosv1beta1.ClusterSummaryNameLabel:      clusterSummary.Name,
		libsveltosv1beta1.ClusterSummaryNamespaceLabel: clusterSummary.Namespace,
	}

	listOptions := []client.ListOption{
		client.MatchingLabels(lbls),
	}

	Eventually(func() bool {
		resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
		err := c.List(context.TODO(), resourceSummaries, listOptions...)
		if err != nil {
			return false
		}
		return len(resourceSummaries.Items) == 1
	}, timeout, pollingInterval).Should(BeTrue())

	resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
	Expect(c.List(context.TODO(), resourceSummaries, listOptions...)).To(Succeed())
	Expect(len(resourceSummaries.Items)).To(Equal(1))

	currentResourceSummary := &resourceSummaries.Items[0]

	deploymentKind := "Deployment"

	// Patches has been configured to ignore admission controller for configuration
	// drift (by adding annotation projectsveltos.io/driftDetectionIgnore)
	Byf("Verify deployment %s/%s is marked to be ignored for configuration drift",
		kyvernoNamespace, admissionControllerDeplName)
	found := false
	for i := range currentResourceSummary.Spec.ChartResources {
		for j := range currentResourceSummary.Spec.ChartResources[i].Resources {
			r := &currentResourceSummary.Spec.ChartResources[i].Resources[j]
			if r.Kind == deploymentKind && r.Namespace == kyvernoNamespace &&
				r.Name == admissionControllerDeplName {

				Expect(r.IgnoreForConfigurationDrift).To(BeTrue())
				found = true
			} else {
				Expect(r.IgnoreForConfigurationDrift).To(BeFalse())
			}
		}
	}
	Expect(found).To(BeTrue())

	// DriftExclusion has been configured to ignore cleanup controller spec/replicas
	found = false
	Byf("Verify deployment %s/%s spec/replicas is marked to be ignored for configuration drift",
		kyvernoNamespace, cleanupControllerDeplName)
	for i := range currentResourceSummary.Spec.Patches {
		p := &currentResourceSummary.Spec.Patches[i]
		if p.Target.Kind == deploymentKind && p.Target.Namespace == kyvernoNamespace &&
			p.Target.Name == cleanupControllerDeplName {

			Expect(p.Patch).To(ContainSubstring("/spec/replicas"))
			found = true
		}
	}
	Expect(found).To(BeTrue())
}
