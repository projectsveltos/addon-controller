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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/agentgroup"
)

// This test needs the environment created by "make create-cluster-agentless-grouping": addon-controller
// in agentless mode with --agent-clusters-per-instance=2, and two kind clusters registered as
// SveltosClusters in the same namespace. It is skipped when addon-controller is not started
// with --agent-clusters-per-instance greater than one.
const (
	agentClustersPerInstanceArg = "--agent-clusters-per-instance="

	// Both clusters are labeled by the Makefile
	groupingClusterLabelKey   = "agentless-grouping"
	groupingClusterLabelValue = "true"

	// The first cluster is kindWorkloadCluster (clusterapi-workload)
	groupingSecondClusterName       = "clusterapi-workload-2"
	groupingSecondClusterKubeconfig = "workload2_kubeconfig"

	driftDetectionComponent = "drift-detection"

	nginxIngressRepoURL   = "https://helm.nginx.com/stable/"
	nginxIngressRepoName  = "nginx-stable"
	nginxIngressChartName = "nginx-stable/nginx-ingress"

	helmInstanceLabel = "app.kubernetes.io/instance"

	// label of the per cluster drift-detection-manager Deployment with the namespace of its cluster
	clusterNamespaceLabelKey = "cluster-namespace"

	// How long the lastAppliedTime of the cluster not drifting is observed
	otherClusterObservationTime = 30 * time.Second
)

var _ = Describe("Agentless drift detection serving several clusters", Serial, func() {
	const namePrefix = "grouping-"

	It("Detects drift in each cluster of a group without affecting the other", Label("AGENTLESS-GROUPING"), func() {
		if getAgentClustersPerInstance() < 2 {
			Skip(fmt.Sprintf("addon-controller is not started with %s greater than one", agentClustersPerInstanceArg))
		}

		firstCluster := types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: kindWorkloadCluster.GetName()}
		secondCluster := types.NamespacedName{Namespace: firstCluster.Namespace, Name: groupingSecondClusterName}

		Byf("Verifying cluster %s is registered", secondCluster)
		Expect(k8sClient.Get(context.TODO(), secondCluster, &libsveltosv1beta1.SveltosCluster{})).To(Succeed())

		firstClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		secondClient, err := getClientFromKubeconfigFile(groupingSecondClusterKubeconfig)
		Expect(err).To(BeNil())

		Byf("Create a ClusterProfile matching both clusters, deploying nginx with ContinuousWithDriftDetection")
		clusterProfile := getClusterProfile(namePrefix, map[string]string{groupingClusterLabelKey: groupingClusterLabelValue})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		clusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
			{
				RepositoryURL:    nginxIngressRepoURL,
				RepositoryName:   nginxIngressRepoName,
				ChartName:        nginxIngressChartName,
				ChartVersion:     nginxVersion272,
				ReleaseName:      nginxLatestRelease,
				ReleaseNamespace: nginxNamespace,
				HelmChartAction:  configv1beta1.HelmChartActionInstall,
				Options: &configv1beta1.HelmOptions{
					SkipSchemaValidation: true,
				},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		firstSummary := waitForProvisionedClusterSummary(clusterProfile.Name, firstCluster)
		secondSummary := waitForProvisionedClusterSummary(clusterProfile.Name, secondCluster)

		Byf("Verifying nginx is deployed in both clusters")
		firstNginx := waitForNginxDeployment(firstClient)
		secondNginx := waitForNginxDeployment(secondClient)

		verifyOneDriftDetectionManagerServes(firstCluster.Namespace, firstCluster.Name, secondCluster.Name)

		Byf("Waiting for a ResourceSummary for each cluster")
		waitForResourceSummary(firstSummary)
		waitForResourceSummary(secondSummary)

		// Wait to make sure the watchers are started in the managed clusters
		const sleepTime = 30
		time.Sleep(sleepTime * time.Second)

		Byf("Deleting nginx Deployment in cluster %s", firstCluster)
		deleteAndVerifyNginxIsRecreated(firstClient, firstNginx, firstSummary)
		Byf("Verifying cluster %s was not affected", secondCluster)
		verifyClusterNotAffected(secondClient, secondNginx, secondSummary)

		Byf("Deleting nginx Deployment in cluster %s", secondCluster)
		secondNginx = waitForNginxDeployment(secondClient)
		deleteAndVerifyNginxIsRecreated(secondClient, secondNginx, secondSummary)
		Byf("Verifying cluster %s was not affected", firstCluster)
		firstNginx = waitForNginxDeployment(firstClient)
		verifyClusterNotAffected(firstClient, firstNginx, firstSummary)

		deleteClusterProfile(clusterProfile)
	})
})

// getAgentClustersPerInstance returns the value of addon-controller --agent-clusters-per-instance,
// 1 if not set.
func getAgentClustersPerInstance() int {
	addonControllerDepl := &appsv1.Deployment{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: sveltosNamespace, Name: addonDeplName},
		addonControllerDepl)).To(Succeed())
	Expect(len(addonControllerDepl.Spec.Template.Spec.Containers)).To(Equal(1))

	for _, arg := range addonControllerDepl.Spec.Template.Spec.Containers[0].Args {
		if !strings.HasPrefix(arg, agentClustersPerInstanceArg) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(arg, agentClustersPerInstanceArg))
		Expect(err).To(BeNil())
		return n
	}

	return 1
}

// getClientFromKubeconfigFile returns a client for the cluster whose kubeconfig is in the file
// (created in this directory by the Makefile).
func getClientFromKubeconfigFile(kubeconfigPath string) (client.Client, error) {
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	return client.New(restConfig, client.Options{Scheme: scheme})
}

func waitForProvisionedClusterSummary(clusterProfileName string, cluster types.NamespacedName) *configv1beta1.ClusterSummary {
	Byf("Waiting for the ClusterSummary of cluster %s", cluster)
	var clusterSummary *configv1beta1.ClusterSummary
	Eventually(func() error {
		var err error
		clusterSummary, err = getClusterSummary(context.TODO(), clusterops.ClusterProfileLabelName, clusterProfileName,
			cluster.Namespace, cluster.Name, string(libsveltosv1beta1.ClusterTypeSveltos))
		return err
	}, timeout, pollingInterval).Should(Succeed())

	verifyFeatureStatusIsProvisioned(cluster.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
	return clusterSummary
}

// getNginxDeployment returns the Deployment of the nginx release, nil if there is not exactly one.
func getNginxDeployment(c client.Client) *appsv1.Deployment {
	depls := &appsv1.DeploymentList{}
	err := c.List(context.TODO(), depls, client.InNamespace(nginxNamespace),
		client.MatchingLabels{helmInstanceLabel: nginxLatestRelease})
	if err != nil || len(depls.Items) != 1 {
		return nil
	}
	return &depls.Items[0]
}

func waitForNginxDeployment(c client.Client) *appsv1.Deployment {
	var depl *appsv1.Deployment
	Eventually(func() bool {
		depl = getNginxDeployment(c)
		return depl != nil
	}, timeout, pollingInterval).Should(BeTrue())
	return depl
}

// verifyOneDriftDetectionManagerServes verifies the clusters are all served by the same group:
// one drift-detection-manager Deployment, one pod, and no Deployment dedicated to a single cluster.
func verifyOneDriftDetectionManagerServes(clusterNamespace string, clusterNames ...string) {
	Byf("Verifying one drift-detection-manager serves clusters %v", clusterNames)

	Eventually(func() error {
		groups, err := agentgroup.ListGroups(context.TODO(), k8sClient, sveltosNamespace,
			driftDetectionComponent, clusterNamespace)
		if err != nil {
			return err
		}
		if len(groups) != 1 {
			return fmt.Errorf("expected 1 group, got %d", len(groups))
		}
		if err := verifyGroupMembers(&groups[0], clusterNames); err != nil {
			return err
		}

		depl := &appsv1.Deployment{}
		err = k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: sveltosNamespace, Name: groups[0].Name}, depl)
		if err != nil {
			return err
		}
		if depl.Spec.Replicas == nil || *depl.Spec.Replicas != 1 || depl.Status.ReadyReplicas != 1 {
			return fmt.Errorf("group deployment %s not ready: %#v", depl.Name, depl.Status)
		}
		return nil
	}, timeout, pollingInterval).Should(Succeed())

	for _, clusterName := range clusterNames {
		depls := &appsv1.DeploymentList{}
		Expect(k8sClient.List(context.TODO(), depls, client.InNamespace(sveltosNamespace),
			client.MatchingLabels{clusterNameKey: clusterName, clusterNamespaceLabelKey: clusterNamespace})).To(Succeed())
		Expect(depls.Items).To(BeEmpty(), "cluster %s has a drift-detection-manager of its own", clusterName)
	}
}

func verifyGroupMembers(group *agentgroup.Group, clusterNames []string) error {
	if len(group.Members) != len(clusterNames) {
		return fmt.Errorf("group %s has %d members, expected %d", group.Name, len(group.Members), len(clusterNames))
	}
	for _, clusterName := range clusterNames {
		found := false
		for i := range group.Members {
			if group.Members[i].Name == clusterName {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("cluster %s is not a member of group %s", clusterName, group.Name)
		}
	}
	return nil
}

func waitForResourceSummary(clusterSummary *configv1beta1.ClusterSummary) {
	Eventually(func() bool {
		resourceSummaries := &libsveltosv1beta1.ResourceSummaryList{}
		if err := k8sClient.List(context.TODO(), resourceSummaries); err != nil {
			return false
		}
		for i := range resourceSummaries.Items {
			if matchesClusterSummary(&resourceSummaries.Items[i], clusterSummary) {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

// deleteAndVerifyNginxIsRecreated deletes the nginx Deployment, which Sveltos deployed, and verifies
// Sveltos deploys it again and reconciles the ClusterSummary of the cluster.
func deleteAndVerifyNginxIsRecreated(c client.Client, depl *appsv1.Deployment, clusterSummary *configv1beta1.ClusterSummary) {
	lastAppliedTime := getFeatureLastAppliedTime(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
	Expect(lastAppliedTime).ToNot(BeNil())

	Expect(c.Delete(context.TODO(), depl)).To(Succeed())

	Byf("Verifying nginx Deployment is recreated")
	Eventually(func() bool {
		recreated := getNginxDeployment(c)
		return recreated != nil && recreated.UID != depl.UID
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Verifying ClusterSummary %s was reconciled", clusterSummary.Name)
	Eventually(func() bool {
		newLastAppliedTime := getFeatureLastAppliedTime(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
		return newLastAppliedTime != nil && newLastAppliedTime.After(lastAppliedTime.Time)
	}, timeout, pollingInterval).Should(BeTrue())

	verifyFeatureStatusIsProvisioned(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
}

// verifyClusterNotAffected verifies, for some time, that the ClusterSummary was not reconciled
// and that nginx was not touched.
func verifyClusterNotAffected(c client.Client, depl *appsv1.Deployment, clusterSummary *configv1beta1.ClusterSummary) {
	lastAppliedTime := getFeatureLastAppliedTime(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
	Expect(lastAppliedTime).ToNot(BeNil())

	Consistently(func() bool {
		current := getFeatureLastAppliedTime(clusterSummary.Namespace, clusterSummary.Name, libsveltosv1beta1.FeatureHelm)
		if current == nil || !current.Equal(lastAppliedTime) {
			return false
		}
		currentDepl := getNginxDeployment(c)
		return currentDepl != nil && currentDepl.UID == depl.UID
	}, otherClusterObservationTime, pollingInterval).Should(BeTrue())
}
