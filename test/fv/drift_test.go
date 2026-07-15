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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	clusterKey = "cluster"
)

var (
	// podAnnotationsValuesTemplate is applied (via ValuesFrom) only to podinfoIgnoredRelease.
	// It exercises the same "Values templated from Cluster annotation" mechanism the drift
	// test relied on, retargeted from a chart-wide customLabels key (kyverno-specific) to
	// podinfo's podAnnotations key, which lands on the pod template's annotations.
	podAnnotationsValuesTemplate = `podAnnotations:
  %s: "{{ .Cluster.metadata.annotations.cluster }}"`

	// probesValuesTemplate is shared (via ValuesFrom) by both podinfo releases.
	probesValuesTemplate = `probes:
  liveness:
    periodSeconds: %d
  readiness:
    periodSeconds: %d`
)

const (
	podinfoDriftNamespace    = "podinfo-drift"
	podinfoDriftRepoURL      = "https://stefanprodan.github.io/podinfo"
	podinfoDriftRepoName     = "podinfo"
	podinfoDriftChartName    = "podinfo/podinfo"
	podinfoDriftChartVersion = "6.14.0"
	podinfoDriftImageRepo    = "docker.io/stefanprodan/podinfo"
	podinfoBaselineTag       = "6.13.0"

	// podinfoIgnoredRelease plays the role the admission-controller Deployment used to:
	// fully ignored for configuration drift via a projectsveltos.io/driftDetectionIgnore patch.
	podinfoIgnoredRelease  = "podinfo-ignored"
	podinfoIgnoredDriftTag = "6.7.1"

	// podinfoExcludedRelease plays the role the cleanup-controller Deployment used to:
	// only /spec/replicas is excluded from drift detection, everything else is not.
	podinfoExcludedRelease  = "podinfo-excluded"
	podinfoExcludedDriftTag = "6.7.0"
)

var _ = Describe("Helm", func() {
	const (
		namePrefix = "drift-"
	)

	It("React to configuration drift and verifies Values/ValuesFrom", Label("FV", "PULLMODE", "EXTENDED"), func() {
		tagValue := randomString()

		// Annotation is used to instantiate ConfigMap with podAnnotationsValuesTemplate used in ValuesFrom
		Byf("Add annotation %s: %s on cluster %s/%s",
			clusterKey, tagValue, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		setAnnotationOnCluster(clusterKey, tagValue)

		Byf("Create a ClusterProfile matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		Byf("Update ClusterProfile %s to reference cluster in templateResourceRefs", clusterProfile.Name)
		clusterProfile.Spec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
			{
				Resource: corev1.ObjectReference{
					Kind:       kindWorkloadCluster.GetKind(),
					APIVersion: kindWorkloadCluster.GetAPIVersion(),
					Name:       clusterNameTemplate,
					Namespace:  clusterNamespaceTemplate,
				},
				IgnoreStatusChanges: true,
			},
		}
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

		Byf("Creating ConfigMap to hold probes values (shared by both podinfo releases)")
		probesConfigMap := createConfigMapWithPolicy(configMapNamespace, randomString(),
			fmt.Sprintf(probesValuesTemplate, livenessPeriodSecond, readinessPeriodSecond))
		Expect(k8sClient.Create(context.TODO(), probesConfigMap)).To(Succeed())

		Byf("Creating ConfigMap to hold podAnnotations values (with template annotation)")
		podAnnotationsConfigMap := createConfigMapWithPolicy(configMapNamespace, randomString(),
			fmt.Sprintf(podAnnotationsValuesTemplate, clusterKey))
		podAnnotationsConfigMap.Annotations = map[string]string{
			libsveltosv1beta1.PolicyTemplateAnnotation: annotationOkValue,
		}
		Expect(k8sClient.Create(context.TODO(), podAnnotationsConfigMap)).To(Succeed())

		Byf("Update ClusterProfile %s to deploy helm charts", clusterProfile.Name)
		By("use driftExclusion to ignore podinfo-excluded's spec/replicas changes")
		By("Use patches to add projectsveltos.io/driftDetectionIgnore annotation to podinfo-ignored")

		currentClusterProfile := &configv1beta1.ClusterProfile{}

		baselineImageValues := fmt.Sprintf(`replicaCount: 1
image:
  repository: %s
  tag: %s`, podinfoDriftImageRepo, podinfoBaselineTag)

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    podinfoDriftRepoURL,
					RepositoryName:   podinfoDriftRepoName,
					ChartName:        podinfoDriftChartName,
					ChartVersion:     podinfoDriftChartVersion,
					ReleaseName:      podinfoIgnoredRelease,
					ReleaseNamespace: podinfoDriftNamespace,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values:           baselineImageValues,
					ValuesFrom: []configv1beta1.ValueFrom{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: podAnnotationsConfigMap.Namespace,
							Name:      podAnnotationsConfigMap.Name,
						},
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: probesConfigMap.Namespace,
							Name:      probesConfigMap.Name,
						},
					},
				},
				{
					RepositoryURL:    podinfoDriftRepoURL,
					RepositoryName:   podinfoDriftRepoName,
					ChartName:        podinfoDriftChartName,
					ChartVersion:     podinfoDriftChartVersion,
					ReleaseName:      podinfoExcludedRelease,
					ReleaseNamespace: podinfoDriftNamespace,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values:           baselineImageValues,
					ValuesFrom: []configv1beta1.ValueFrom{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: probesConfigMap.Namespace,
							Name:      probesConfigMap.Name,
						},
					},
				},
			}

			By("use driftExclusion to ignore podinfo-excluded's spec/replicas changes")
			currentClusterProfile.Spec.DriftExclusions = []libsveltosv1beta1.DriftExclusion{
				{
					Paths: []string{"/spec/replicas"},
					Target: &libsveltosv1beta1.PatchSelector{
						Kind:      kindDeployment,
						Group:     appsGroupName,
						Version:   apiVersionV1,
						Namespace: podinfoDriftNamespace,
						Name:      podinfoExcludedRelease,
					},
				},
			}

			currentClusterProfile.Spec.Patches = []libsveltosv1beta1.Patch{
				{
					Patch: `- op: add
  path: /metadata/annotations/projectsveltos.io~1driftDetectionIgnore
  value: ok`,
					Target: &libsveltosv1beta1.PatchSelector{
						Group:     appsGroupName,
						Version:   apiVersionV1,
						Kind:      kindDeployment,
						Namespace: podinfoDriftNamespace,
						Name:      podinfoIgnoredRelease,
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

		expectedReplicas := int32(1)
		Byf("Verifying podinfo deployment %s/%s is created in the workload cluster (with annotation %s/%s and replicas %d)",
			podinfoDriftNamespace, podinfoIgnoredRelease, clusterKey, tagValue, expectedReplicas)
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoIgnoredRelease}, depl)
			if err != nil {
				return false
			}
			if !hasAnnotation(depl.Spec.Template.Annotations, clusterKey, tagValue) {
				return false
			}
			return depl.Spec.Replicas != nil && *depl.Spec.Replicas == expectedReplicas
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying helm values")
		verifyHelmValues(workloadClient, podinfoDriftNamespace, podinfoIgnoredRelease,
			livenessPeriodSecond, readinessPeriodSecond)
		verifyHelmValues(workloadClient, podinfoDriftNamespace, podinfoExcludedRelease,
			livenessPeriodSecond, readinessPeriodSecond)

		verifyDriftDetectionManagerDeployment(workloadClient)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		charts := []configv1beta1.Chart{
			{ReleaseName: podinfoIgnoredRelease, ChartVersion: podinfoDriftChartVersion, Namespace: podinfoDriftNamespace},
			{ReleaseName: podinfoExcludedRelease, ChartVersion: podinfoDriftChartVersion, Namespace: podinfoDriftNamespace},
		}

		verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
			nil, charts)

		verifyDriftDetectionManagerDeployment(workloadClient)

		if isAgentLessMode() {
			verifyResourceSummary(k8sClient, clusterSummary)
		} else {
			// Verify ResourceSummary in the managed cluster
			verifyResourceSummary(workloadClient, clusterSummary)
		}

		// Wait to make sure a watcher is started in the managed cluster
		const sleepTime = 30
		time.Sleep(sleepTime * time.Second)

		// Change podinfo-excluded's image (this Deployment only has /spec/replicas excluded,
		// so Sveltos is expected to revert this)
		depl := &appsv1.Deployment{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoExcludedRelease}, depl)).To(Succeed())
		Expect(len(depl.Spec.Template.Spec.Containers)).To(Equal(1))
		depl.Spec.Template.Spec.Containers[0].Image = fmt.Sprintf("%s:%s", podinfoDriftImageRepo, podinfoExcludedDriftTag)
		Expect(workloadClient.Update(context.TODO(), depl)).To(Succeed())

		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoExcludedRelease}, depl)).To(Succeed())
		By("podinfo-excluded image is set to the drift tag")
		Expect(depl.Spec.Template.Spec.Containers[0].Image).To(Equal(fmt.Sprintf("%s:%s", podinfoDriftImageRepo, podinfoExcludedDriftTag)))

		Byf("Verifying Sveltos reacts to drift configuration change")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoExcludedRelease}, depl)
			if err != nil {
				return false
			}
			return depl.Spec.Template.Spec.Containers[0].Image == fmt.Sprintf("%s:%s", podinfoDriftImageRepo, podinfoBaselineTag)
		}, timeout, pollingInterval).Should(BeTrue())
		By("podinfo-excluded image is reset to the baseline tag")

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		// Change podinfo-ignored's image (this Deployment has the driftDetectionIgnore
		// annotation, so Sveltos is expected to leave this alone)
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoIgnoredRelease}, depl)).To(Succeed())
		Expect(len(depl.Spec.Template.Spec.Containers)).To(Equal(1))
		depl.Spec.Template.Spec.Containers[0].Image = fmt.Sprintf("%s:%s", podinfoDriftImageRepo, podinfoIgnoredDriftTag)
		Expect(workloadClient.Update(context.TODO(), depl)).To(Succeed())

		Eventually(func() bool {
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoIgnoredRelease}, depl)
			if err != nil {
				return false
			}
			By("podinfo-ignored image is set to the drift tag")
			return depl.Spec.Template.Spec.Containers[0].Image == fmt.Sprintf("%s:%s", podinfoDriftImageRepo, podinfoIgnoredDriftTag)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Verifying Sveltos does not reacts to drift configuration change as podinfo-ignored has ignore annotation")
		Consistently(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoIgnoredRelease}, depl)
			if err != nil {
				return false
			}
			return depl.Spec.Template.Spec.Containers[0].Image == fmt.Sprintf("%s:%s", podinfoDriftImageRepo, podinfoIgnoredDriftTag)
		}, timeout/4, pollingInterval).Should(BeTrue())
		By("podinfo-ignored image is NOT reset to the baseline tag")

		By("Change values section")
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())
		for i := range currentClusterProfile.Spec.HelmCharts {
			if currentClusterProfile.Spec.HelmCharts[i].ReleaseName == podinfoIgnoredRelease {
				currentClusterProfile.Spec.HelmCharts[i].Values = fmt.Sprintf(`replicaCount: 3
image:
  repository: %s
  tag: %s`, podinfoDriftImageRepo, podinfoBaselineTag)
			}
		}
		Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

		Byf("Verifying podinfo-ignored deployment is updated in the workload cluster")
		Eventually(func() bool {
			expectedReplicas := int32(3)
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoIgnoredRelease}, depl)
			if err != nil {
				return false
			}
			return depl.Spec.Replicas != nil && *depl.Spec.Replicas == expectedReplicas
		}, timeout, pollingInterval).Should(BeTrue())

		tagValue = randomString()
		Byf("Update annotation %s: %s on cluster %s/%s",
			clusterKey, tagValue, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		setAnnotationOnCluster(clusterKey, tagValue)

		const sleepFor = 30 * time.Second
		time.Sleep(sleepFor)

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Verifying podinfo-ignored deployment %s/%s is updated in the workload cluster with annotation %s:%s",
			podinfoDriftNamespace, podinfoIgnoredRelease, clusterKey, tagValue)
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoIgnoredRelease}, depl)
			if err != nil {
				return false
			}
			return hasAnnotation(depl.Spec.Template.Annotations, clusterKey, tagValue)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)

		Byf("Verifying podinfo deployments are removed from workload cluster")
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoIgnoredRelease}, depl)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: podinfoDriftNamespace, Name: podinfoExcludedRelease}, depl)
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
		types.NamespacedName{Namespace: sveltosNamespace, Name: addonDeplName},
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

func hasAnnotation(annotations map[string]string, key, value string) bool {
	if annotations == nil {
		return false
	}
	v := annotations[key]

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

	verifyIgnoredResourceSummary(c, currentResourceSummary, kindDeployment)

	verifySpecReplicas(currentResourceSummary, kindDeployment)
}

func verifyIgnoredResourceSummary(c client.Client,
	currentResourceSummary *libsveltosv1beta1.ResourceSummary, deploymentKind string) {

	// Patches has been configured to ignore podinfo-ignored for configuration
	// drift (by adding annotation projectsveltos.io/driftDetectionIgnore)
	Byf("Verify deployment %s/%s is marked to be ignored for configuration drift",
		podinfoDriftNamespace, podinfoIgnoredRelease)
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
				if r.Kind == deploymentKind && r.Namespace == podinfoDriftNamespace &&
					r.Name == podinfoIgnoredRelease {

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
			if r.Kind == deploymentKind && r.Namespace == podinfoDriftNamespace &&
				r.Name == podinfoIgnoredRelease {

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
	// DriftExclusion has been configured to ignore podinfo-excluded's spec/replicas
	Byf("Verify deployment %s/%s spec/replicas is marked to be ignored for configuration drift",
		podinfoDriftNamespace, podinfoExcludedRelease)
	for i := range currentResourceSummary.Spec.Patches {
		p := &currentResourceSummary.Spec.Patches[i]
		if p.Target.Kind == deploymentKind && p.Target.Namespace == podinfoDriftNamespace &&
			p.Target.Name == podinfoExcludedRelease {

			Expect(p.Patch).To(ContainSubstring("/spec/replicas"))
			found = true
		}
	}
	Expect(found).To(BeTrue())
}
