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
	"k8s.io/client-go/util/retry"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
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
	cleanupImage                = "reg.kyverno.io/kyverno/cleanup-controller:v1.15.1"
	admissionImage              = "reg.kyverno.io/kyverno/kyverno:v1.15.1"
)

var _ = Describe("Helm", Serial, func() {
	const (
		namePrefix                = "drift-"
		kyvernoAdmissionImageName = "kyverno"
		kyvernoCleanupImageName   = "controller"
	)

	It("React to configuration drift and verifies Values/ValuesFrom", Label("FV", "PULLMODE", "EXTENDED"), func() {
		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

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
		By("use driftExclusion to ignore cleanup controller spec/replicas changes")
		By("Use patches to add projectsveltos.io/driftDetectionIgnore annotation")

		currentClusterProfile := &configv1beta1.ClusterProfile{}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://kyverno.github.io/kyverno/",
					RepositoryName:   "kyverno",
					ChartName:        "kyverno/kyverno",
					ChartVersion:     "v3.5.2",
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
			currentClusterProfile.Spec.DriftExclusions = []libsveltosv1beta1.DriftExclusion{
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

			return k8sClient.Update(context.TODO(), currentClusterProfile)
		})
		Expect(err).To(BeNil())

		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			currentClusterProfile.Name, &currentClusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

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
			if !isDeplLabelCorrect(depl.Labels, clusterKey, kindWorkloadCluster.GetName()) {
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
							"cluster-name":      kindWorkloadCluster.GetName(),
							"cluster-namespace": kindWorkloadCluster.GetNamespace(),
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
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		charts := []configv1beta1.Chart{
			{ReleaseName: "kyverno-latest", ChartVersion: "3.5.2", Namespace: "kyverno"},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
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
				By("Kyverno image is set to v1.15.1")
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
					return depl.Spec.Template.Spec.Containers[i].Image == "reg.kyverno.io/kyverno/cleanup-controller:v1.15.2"
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
		By("Kyverno image is reset to v1.15.2")

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

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

		Eventually(func() bool {
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kyverno", Name: "kyverno-admission-controller"}, depl)
			if err != nil {
				return false
			}
			for i := range depl.Spec.Template.Spec.Containers {
				if depl.Spec.Template.Spec.Containers[i].Name == kyvernoAdmissionImageName {
					By("Kyverno image is set to v1.14.1")
					return depl.Spec.Template.Spec.Containers[i].Image == admissionImage
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

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
		By("Kyverno image is NOT reset to v1.14.2")

		By("Change values section")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    "https://kyverno.github.io/kyverno/",
				RepositoryName:   "kyverno",
				ChartName:        "kyverno/kyverno",
				ChartVersion:     "v3.5.1",
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
		if strings.Contains(addonControllerDepl.Spec.Template.Spec.Containers[0].Args[i], "agent-in-mgmt-cluster=true") {
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

func matchesClusterSummary(rs *libsveltosv1beta1.ResourceSummary, clusterSummary *configv1beta1.ClusterSummary,
) bool {

	if rs.Annotations == nil {
		return false
	}
	v, ok := rs.Annotations[libsveltosv1beta1.ClusterSummaryNameAnnotation]
	if !ok {
		return false
	}
	if v != clusterSummary.Name {
		return false
	}
	v, ok = rs.Annotations[libsveltosv1beta1.ClusterSummaryNamespaceAnnotation]
	if !ok {
		return false
	}
	if v != clusterSummary.Namespace {
		return false
	}
	return true
}

func verifyResourceSummary(c client.Client, clusterSummary *configv1beta1.ClusterSummary) {
	Byf("Verify ResourceSummary is present")

	Eventually(func() bool {
		resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
		err := c.List(context.TODO(), resourceSummaries)
		if err != nil {
			return false
		}
		for i := range resourceSummaries.Items {
			rs := &resourceSummaries.Items[i]
			if matchesClusterSummary(rs, clusterSummary) {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())

	resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
	Expect(c.List(context.TODO(), resourceSummaries)).To(Succeed())

	var currentResourceSummary *libsveltosv1beta1.ResourceSummary
	for i := range resourceSummaries.Items {
		rs := &resourceSummaries.Items[i]
		if matchesClusterSummary(rs, clusterSummary) {
			currentResourceSummary = rs
		}
	}

	Expect(currentResourceSummary).ToNot(BeNil())

	deploymentKind := "Deployment"
	verifyAdmissionControllerDeployment(c, currentResourceSummary, deploymentKind)

	verifySpecReplicas(currentResourceSummary, deploymentKind)
}

func verifyAdmissionControllerDeployment(c client.Client,
	currentResourceSummary *libsveltosv1beta1.ResourceSummary, deploymentKind string) {

	// Patches has been configured to ignore admission controller for configuration
	// drift (by adding annotation projectsveltos.io/driftDetectionIgnore)
	Byf("Verify deployment %s/%s is marked to be ignored for configuration drift",
		kyvernoNamespace, admissionControllerDeplName)
	Eventually(func() bool {
		resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
		err := c.List(context.TODO(), resourceSummaries)
		if err != nil {
			return false
		}
		if len(resourceSummaries.Items) != 1 {
			return false
		}

		currentResourceSummary := &resourceSummaries.Items[0]

		found := false
		ignore := false
		for i := range currentResourceSummary.Spec.ChartResources {
			for j := range currentResourceSummary.Spec.ChartResources[i].Resources {
				r := &currentResourceSummary.Spec.ChartResources[i].Resources[j]
				if r.Kind == deploymentKind && r.Namespace == kyvernoNamespace &&
					r.Name == admissionControllerDeplName {

					ignore = r.IgnoreForConfigurationDrift
					found = true
					break
				}
			}
		}
		return found && ignore
	}, timeout, pollingInterval).Should(BeTrue())

	// DriftExclusion has been configured to NOT ignore for anything else
	for i := range currentResourceSummary.Spec.ChartResources {
		for j := range currentResourceSummary.Spec.ChartResources[i].Resources {
			r := &currentResourceSummary.Spec.ChartResources[i].Resources[j]
			if r.Kind == deploymentKind && r.Namespace == kyvernoNamespace &&
				r.Name == admissionControllerDeplName {

				continue
			} else {
				Expect(r.IgnoreForConfigurationDrift).To(BeFalse())
			}
		}
	}
}

func verifySpecReplicas(currentResourceSummary *libsveltosv1beta1.ResourceSummary,
	deploymentKind string) {

	found := false
	// DriftExclusion has been configured to ignore cleanup controller spec/replicas
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
