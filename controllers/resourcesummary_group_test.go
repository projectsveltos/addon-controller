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

package controllers_test

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/agentgroup"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

const groupDriftDetectionComponent = "drift-detection"

var _ = Describe("drift-detection-manager serving a group of clusters", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
	})

	AfterEach(func() {
		controllers.SetAgentClustersPerInstance(0)
		controllers.SetAgentInMgmtCluster(false)
	})

	// getGroupDeployment returns the Deployment of the group, if any.
	getGroupDeployment := func(groupName string) (*appsv1.Deployment, error) {
		deployment := &appsv1.Deployment{}
		err := testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: controllers.GetDriftDetectionNamespaceInMgmtCluster(sveltosNamespace), Name: groupName},
			deployment)
		return deployment, err
	}

	deployInGroup := func(clusterNamespace, clusterName string) {
		Expect(controllers.DeployDriftDetectionManagerInGroup(context.TODO(), testEnv.Client, testEnv.Config,
			clusterNamespace, clusterName, "do-not-send-updates", libsveltosv1beta1.ClusterTypeSveltos, "", nil, nil,
			logger)).To(Succeed())
	}

	removeFromGroup := func(clusterNamespace, clusterName string) {
		Expect(controllers.RemoveClusterFromDriftDetectionManagerGroups(context.TODO(), testEnv.Client, testEnv.Config,
			clusterNamespace, clusterName, libsveltosv1beta1.ClusterTypeSveltos, logger)).To(Succeed())
	}

	groupsOf := func(clusterNamespace string) []agentgroup.Group {
		groups, err := agentgroup.ListGroups(context.TODO(), testEnv.Client, sveltosNamespace, groupDriftDetectionComponent,
			clusterNamespace)
		Expect(err).To(BeNil())
		return groups
	}

	It("grouping is enabled only in agentless mode with more than one cluster per instance", func() {
		Expect(controllers.IsDriftDetectionGroupingEnabled()).To(BeFalse())

		controllers.SetAgentClustersPerInstance(5)
		Expect(controllers.IsDriftDetectionGroupingEnabled()).To(BeFalse())

		controllers.SetAgentInMgmtCluster(true)
		Expect(controllers.IsDriftDetectionGroupingEnabled()).To(BeTrue())

		controllers.SetAgentClustersPerInstance(1)
		Expect(controllers.IsDriftDetectionGroupingEnabled()).To(BeFalse())
	})

	It("prepareDriftDetectionManagerGroupYAML replaces the per cluster arguments with the group ones", func() {
		const yaml = `      - args:
        - --diagnostics-address=:8443
        - --v=5
        - --cluster-namespace=
        - --cluster-name=
        - --cluster-type=
        - --watch-namespaces=
        - --current-cluster=management-cluster
        - --run-mode=do-not-send-updates
`
		result := controllers.PrepareDriftDetectionManagerGroupYAML(yaml, "drift-detection-abcd-0", "team-a",
			"do-not-send-updates")

		Expect(result).To(ContainSubstring("        - --cluster-namespace=team-a\n"))
		Expect(result).To(ContainSubstring("        - --group-config-map=drift-detection-abcd-0\n"))
		Expect(result).To(ContainSubstring("--current-cluster=management-cluster"))
		Expect(result).To(ContainSubstring("--run-mode=do-not-send-updates"))
		Expect(result).ToNot(ContainSubstring("--cluster-name="))
		Expect(result).ToNot(ContainSubstring("--cluster-type="))
		Expect(result).ToNot(ContainSubstring("--watch-namespaces="))
	})

	It("clusters of a namespace share a Deployment up to the capacity, then a new one is created", func() {
		const clustersPerInstance = 2
		controllers.SetAgentClustersPerInstance(clustersPerInstance)

		clusterNamespace := randomString()
		cluster1, cluster2, cluster3 := randomString(), randomString(), randomString()

		deployInGroup(clusterNamespace, cluster1)
		deployInGroup(clusterNamespace, cluster2)
		deployInGroup(clusterNamespace, cluster3)

		groups := groupsOf(clusterNamespace)
		Expect(groups).To(HaveLen(2))

		for i := range groups {
			deployment, err := getGroupDeployment(groups[i].Name)
			Expect(err).To(BeNil())

			args := deployment.Spec.Template.Spec.Containers[0].Args
			Expect(args).To(ContainElement(fmt.Sprintf("--group-config-map=%s", groups[i].Name)))
			Expect(args).To(ContainElement(fmt.Sprintf("--cluster-namespace=%s", clusterNamespace)))
			Expect(args).ToNot(ContainElement(HavePrefix("--cluster-name=")))
			Expect(deployment.Labels[agentgroup.ComponentLabel]).To(Equal(groupDriftDetectionComponent))
			Expect(deployment.Labels[agentgroup.ClusterNamespaceLabel]).To(Equal(clusterNamespace))
			Expect(controllers.IsDriftDetectionGroupDeployment(deployment)).To(BeTrue())
		}

		Expect(len(groups[0].Members) + len(groups[1].Members)).To(Equal(3))
	})

	It("deploying a cluster again does not create another Deployment", func() {
		controllers.SetAgentClustersPerInstance(3)

		clusterNamespace := randomString()
		clusterName := randomString()

		deployInGroup(clusterNamespace, clusterName)
		deployInGroup(clusterNamespace, clusterName)

		groups := groupsOf(clusterNamespace)
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].Members).To(HaveLen(1))
	})

	It("keeps the Deployment while the group has a cluster, removes it with the last one", func() {
		controllers.SetAgentClustersPerInstance(2)

		clusterNamespace := randomString()
		cluster1, cluster2 := randomString(), randomString()

		deployInGroup(clusterNamespace, cluster1)
		deployInGroup(clusterNamespace, cluster2)

		groups := groupsOf(clusterNamespace)
		Expect(groups).To(HaveLen(1))
		groupName := groups[0].Name

		removeFromGroup(clusterNamespace, cluster1)

		_, err := getGroupDeployment(groupName)
		Expect(err).To(BeNil())
		Expect(groupsOf(clusterNamespace)).To(HaveLen(1))

		removeFromGroup(clusterNamespace, cluster2)

		Eventually(func() bool {
			_, err := getGroupDeployment(groupName)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
		Expect(groupsOf(clusterNamespace)).To(BeEmpty())
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Namespace: sveltosNamespace, Name: groupName},
			&corev1.ConfigMap{})).ToNot(Succeed())
	})

	It("removing a cluster that is in no group is a no-op", func() {
		removeFromGroup(randomString(), randomString())
	})

	It("removes the Deployment the cluster had of its own before grouping was enabled", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		Expect(controllers.DeployDriftDetectionManagerInManagementCluster(context.TODO(), testEnv.Config,
			clusterNamespace, clusterName, "do-not-send-updates", clusterType, nil, nil, logger)).To(Succeed())

		perClusterLabels := controllers.GetDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)
		listPerCluster := func() int {
			deployments := &appsv1.DeploymentList{}
			Expect(testEnv.List(context.TODO(), deployments, client.InNamespace(sveltosNamespace),
				client.MatchingLabels(perClusterLabels))).To(Succeed())
			return len(deployments.Items)
		}
		Eventually(listPerCluster, timeout, pollingInterval).Should(Equal(1))

		controllers.SetAgentClustersPerInstance(2)
		deployInGroup(clusterNamespace, clusterName)

		Eventually(listPerCluster, timeout, pollingInterval).Should(Equal(0))
		Expect(groupsOf(clusterNamespace)).To(HaveLen(1))

		removeFromGroup(clusterNamespace, clusterName)
	})

	It("a cluster still in a group when grouping is turned off is served by its own Deployment only", func() {
		controllers.SetAgentClustersPerInstance(2)

		clusterNamespace := randomString()
		cluster1, cluster2 := randomString(), randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		deployInGroup(clusterNamespace, cluster1)
		deployInGroup(clusterNamespace, cluster2)
		groups := groupsOf(clusterNamespace)
		Expect(groups).To(HaveLen(1))
		groupName := groups[0].Name

		controllers.SetAgentClustersPerInstance(1)

		deployOfCluster := func(clusterName string) {
			Expect(controllers.DeployDriftDetectionManagerOfCluster(context.TODO(), testEnv.Client, testEnv.Config,
				clusterNamespace, clusterName, "do-not-send-updates", clusterType, nil, nil, logger)).To(Succeed())
		}
		countPerCluster := func(clusterName string) int {
			deployments := &appsv1.DeploymentList{}
			Expect(testEnv.List(context.TODO(), deployments, client.InNamespace(sveltosNamespace),
				client.MatchingLabels(controllers.GetDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)))).To(Succeed())
			return len(deployments.Items)
		}

		deployOfCluster(cluster1)

		By("the cluster has a Deployment of its own and left the group")
		Eventually(func() int { return countPerCluster(cluster1) }, timeout, pollingInterval).Should(Equal(1))
		groups = groupsOf(clusterNamespace)
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].Members).To(HaveLen(1))
		Expect(groups[0].Members[0].Name).To(Equal(cluster2))

		By("the group Deployment stays while it has a cluster")
		_, err := getGroupDeployment(groupName)
		Expect(err).To(BeNil())

		By("a cluster that is in no group is not affected")
		deployOfCluster(cluster1)
		Expect(countPerCluster(cluster1)).To(Equal(1))

		By("the group Deployment is removed when its last cluster leaves")
		deployOfCluster(cluster2)
		Eventually(func() bool {
			_, err := getGroupDeployment(groupName)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
		Expect(groupsOf(clusterNamespace)).To(BeEmpty())
		Eventually(func() int { return countPerCluster(cluster2) }, timeout, pollingInterval).Should(Equal(1))
	})

	It("remembers the groups when an API reader is set: a cluster already in a group needs no read", func() {
		controllers.SetManagementClusterAPIReader(testEnv.Client)
		defer controllers.SetManagementClusterAPIReader(nil)
		controllers.SetAgentClustersPerInstance(2)

		clusterNamespace := randomString()
		cluster1, cluster2 := randomString(), randomString()

		deployInGroup(clusterNamespace, cluster1)
		deployInGroup(clusterNamespace, cluster2)
		deployInGroup(clusterNamespace, cluster1)

		groups := groupsOf(clusterNamespace)
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].Members).To(HaveLen(2))

		removeFromGroup(clusterNamespace, cluster1)
		removeFromGroup(clusterNamespace, cluster2)

		Eventually(func() bool {
			_, err := getGroupDeployment(groups[0].Name)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
		Expect(groupsOf(clusterNamespace)).To(BeEmpty())
	})

	Context("clusters deleted while they are in a group", func() {
		// createClusterNamespace creates the namespace of the clusters and returns its name.
		createClusterNamespace := func() string {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: randomString()}}
			Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())
			return ns.Name
		}

		createSveltosCluster := func(clusterNamespace, clusterName string) {
			cluster := &libsveltosv1beta1.SveltosCluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: clusterName},
			}
			Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())
		}

		createResourceSummary := func(clusterNamespace, clusterName string) *libsveltosv1beta1.ResourceSummary {
			resourceSummary := &libsveltosv1beta1.ResourceSummary{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: clusterNamespace,
					Name:      randomString(),
					Labels: map[string]string{
						sveltos_upgrade.ClusterNameLabel:              clusterName,
						sveltos_upgrade.ClusterTypeLabel:              strings.ToLower(string(libsveltosv1beta1.ClusterTypeSveltos)),
						"projectsveltos.io/cluster-summary-namespace": clusterNamespace,
					},
				},
			}
			Expect(testEnv.Create(context.TODO(), resourceSummary)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, resourceSummary)).To(Succeed())
			return resourceSummary
		}

		removeStaleMembers := func(groupName string) {
			deployment, err := getGroupDeployment(groupName)
			Expect(err).To(BeNil())
			controllers.RemoveStaleGroupMembers(context.TODO(), testEnv.Client, testEnv.Config, deployment, logger)
		}

		memberNames := func(clusterNamespace string) []string {
			names := []string{}
			for _, group := range groupsOf(clusterNamespace) {
				for _, member := range group.Members {
					names = append(names, member.Name)
				}
			}
			return names
		}

		It("a cluster that does not exist anymore leaves its group, the others stay", func() {
			controllers.SetAgentClustersPerInstance(3)

			clusterNamespace := createClusterNamespace()
			existing, deleted := randomString(), randomString()
			createSveltosCluster(clusterNamespace, existing)

			deployInGroup(clusterNamespace, existing)
			deployInGroup(clusterNamespace, deleted)
			groupName := groupsOf(clusterNamespace)[0].Name

			removeStaleMembers(groupName)

			Expect(memberNames(clusterNamespace)).To(ConsistOf(existing))
			_, err := getGroupDeployment(groupName)
			Expect(err).To(BeNil())
		})

		It("the Deployment is removed with the last cluster of the group", func() {
			controllers.SetAgentClustersPerInstance(3)

			clusterNamespace := createClusterNamespace()
			deleted1, deleted2 := randomString(), randomString()

			deployInGroup(clusterNamespace, deleted1)
			deployInGroup(clusterNamespace, deleted2)
			groupName := groupsOf(clusterNamespace)[0].Name

			removeStaleMembers(groupName)

			Expect(groupsOf(clusterNamespace)).To(BeEmpty())
			Eventually(func() bool {
				_, err := getGroupDeployment(groupName)
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		})

		It("the cluster stays in the group until its ResourceSummaries are gone", func() {
			controllers.SetAgentClustersPerInstance(3)

			clusterNamespace := createClusterNamespace()
			deleted := randomString()

			deployInGroup(clusterNamespace, deleted)
			groupName := groupsOf(clusterNamespace)[0].Name
			resourceSummary := createResourceSummary(clusterNamespace, deleted)

			// The ResourceSummary is deleted, and the cluster is not removed yet: the
			// drift-detection-manager serving the group is the one removing its finalizer.
			removeStaleMembers(groupName)
			Expect(memberNames(clusterNamespace)).To(ConsistOf(deleted))

			Eventually(func() bool {
				err := testEnv.Get(context.TODO(),
					types.NamespacedName{Namespace: resourceSummary.Namespace, Name: resourceSummary.Name},
					&libsveltosv1beta1.ResourceSummary{})
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())

			removeStaleMembers(groupName)
			Expect(groupsOf(clusterNamespace)).To(BeEmpty())
		})
	})
})
