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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// Must match sveltosAgentDisabledReason in controllers/health_monitoring.go
	sveltosAgentDisabledReason = "SveltosAgentDisabled"

	debuggingConfigurationCRDName = "debuggingconfigurations.lib.projectsveltos.io"
	resourceSummaryCRDName        = "resourcesummaries.lib.projectsveltos.io"
	reloaderCRDName               = "reloaders.lib.projectsveltos.io"
)

// These tests run only against addon-controller started with --disable-sveltos-agent=true, see the
// fv-sveltos-agent-disabled Makefile target. That target leaves in the managed cluster only the CRDs classifier
// deploys when it is started with --disable-sveltos-agent=true: DebuggingConfiguration and ResourceSummary.
var _ = Describe("Sveltos agent disabled", Serial, func() {
	const namePrefix = "agent-disabled-"

	It("Deploys a profile with Reloader and HealthMonitoring, ignoring both", Label("SVELTOS-AGENT-DISABLED"), func() {
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())

		Byf("Verifying the Reloader CRD is not present in the managed cluster")
		verifyCRDPresence(workloadClient, reloaderCRDName, false)

		Byf("Create a ClusterProfile with Reloader and HealthMonitoring set matching Cluster %s/%s",
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		clusterProfile.Spec.Reloader = true
		clusterProfile.Spec.HealthMonitoring = &configv1beta1.HealthMonitoring{Enabled: true}

		ns := randomString()
		deploymentName := randomString()
		Byf("Create a configMap with a deployment and configmap")
		policyConfigMap := createConfigMapWithPolicy(defaultNamespace, randomString(),
			fmt.Sprintf(configMap, ns),
			fmt.Sprintf(deploymentWithVolume, deploymentName, ns))
		Expect(k8sClient.Create(context.TODO(), policyConfigMap)).To(Succeed())

		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: policyConfigMap.Namespace,
				Name:      policyConfigMap.Name,
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name,
			&clusterProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Resources feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
			libsveltosv1beta1.FeatureResources)

		Byf("Verifying the CRDs needed by drift detection are present in the managed cluster")
		verifyCRDPresence(workloadClient, debuggingConfigurationCRDName, true)
		verifyCRDPresence(workloadClient, resourceSummaryCRDName, true)

		Byf("Verifying Reloader is ignored: its CRD is still not present in the managed cluster")
		verifyCRDPresence(workloadClient, reloaderCRDName, false)

		Byf("Verifying HealthMonitoring is ignored: no HealthCheck and no ClusterHealthCheck is created")
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		listOptions := []client.ListOption{
			client.MatchingLabels{
				configv1beta1.HealthMonitoringClusterSummaryUIDLabel: string(currentClusterSummary.UID),
			},
		}
		healthCheckList := &libsveltosv1beta1.HealthCheckList{}
		Expect(k8sClient.List(context.TODO(), healthCheckList, listOptions...)).To(Succeed())
		Expect(healthCheckList.Items).To(BeEmpty())
		clusterHealthCheckList := &libsveltosv1beta1.ClusterHealthCheckList{}
		err = k8sClient.List(context.TODO(), clusterHealthCheckList, listOptions...)
		// The ClusterHealthCheck CRD is not installed in every FV management cluster: no CRD, no object
		if err != nil {
			Expect(meta.IsNoMatchError(err)).To(BeTrue())
		}
		Expect(clusterHealthCheckList.Items).To(BeEmpty())

		Byf("Verifying a %s Warning event is emitted for ClusterSummary %s", sveltosAgentDisabledReason, clusterSummary.Name)
		Eventually(func() bool {
			return hasEventWithReason(currentClusterSummary, sveltosAgentDisabledReason)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterProfile(clusterProfile)
	})
})

// verifyCRDPresence verifies, in the cluster the client points to, that the CRD is present or absent
func verifyCRDPresence(c client.Client, crdName string, present bool) {
	Eventually(func() bool {
		currentCRD := &apiextensionsv1.CustomResourceDefinition{}
		err := c.Get(context.TODO(), types.NamespacedName{Name: crdName}, currentCRD)
		if present {
			return err == nil
		}
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())
}

// hasEventWithReason returns true if an event with the given reason has been recorded for the ClusterSummary
func hasEventWithReason(clusterSummary *configv1beta1.ClusterSummary, reason string) bool {
	eventList := &corev1.EventList{}
	err := k8sClient.List(context.TODO(), eventList, client.InNamespace(clusterSummary.Namespace))
	if err != nil {
		return false
	}

	for i := range eventList.Items {
		event := &eventList.Items[i]
		if event.InvolvedObject.Kind == configv1beta1.ClusterSummaryKind &&
			event.InvolvedObject.Name == clusterSummary.Name &&
			event.Reason == reason {

			return true
		}
	}
	return false
}
