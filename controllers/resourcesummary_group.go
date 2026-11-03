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

package controllers

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	driftdetection "github.com/projectsveltos/addon-controller/pkg/drift-detection"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/agentgroup"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// driftDetectionComponent is the component name used for the drift-detection-manager groups.
	driftDetectionComponent = "drift-detection"

	// doNotSendUpdatesMode is the mode drift-detection-manager is started with when it does not
	// send updates (the default one: addon-controller collects the ResourceSummaries)
	doNotSendUpdatesMode = "do-not-send-updates"
)

var (
	// driftDetectionAssigner remembers the groups, and serializes the decisions taken about them
	// (several ClusterSummaries are reconciled at the same time, also for the same cluster).
	driftDetectionAssigner      *agentgroup.Assigner
	driftDetectionAssignerMutex sync.Mutex
)

// getDriftDetectionAssigner returns the Assigner deciding the drift-detection-manager group of
// each cluster. The groups are remembered by one instance for the life of the process, which
// requires a reader not served from a cache (see SetManagementClusterAPIReader). Without one a
// new instance, that remembers nothing, is returned each time.
func getDriftDetectionAssigner(c client.Client) *agentgroup.Assigner {
	driftDetectionAssignerMutex.Lock()
	defer driftDetectionAssignerMutex.Unlock()

	if managementClusterAPIReader == nil {
		return agentgroup.NewAssigner(c, c, getSveltosNamespace())
	}

	if driftDetectionAssigner == nil {
		driftDetectionAssigner = agentgroup.NewAssigner(managementClusterAPIReader, c, getSveltosNamespace())
	}

	return driftDetectionAssigner
}

func resetDriftDetectionAssigner() {
	driftDetectionAssignerMutex.Lock()
	defer driftDetectionAssignerMutex.Unlock()

	driftDetectionAssigner = nil
}

// isDriftDetectionGroupingEnabled returns true when one drift-detection-manager must serve
// several clusters (agentless mode only).
func isDriftDetectionGroupingEnabled() bool {
	return getAgentInMgmtCluster() && getAgentClustersPerInstance() > 1
}

// getDriftDetectionPool returns the pool of groups a cluster is placed in: clusters of one pool
// share the namespace and the shard, as a drift-detection-manager serves one namespace of one
// shard only.
func getDriftDetectionPool(clusterNamespace, shardKey string) agentgroup.Pool {
	return agentgroup.Pool{
		Component:        driftDetectionComponent,
		ClusterNamespace: clusterNamespace,
		Shard:            shardKey,
	}
}

// getDriftDetectionGroupLabels returns the labels set on the objects of a group Deployment: the
// ones identifying the pool, plus the feature label (which makes it show up with the per cluster
// instances when listing all drift-detection-manager Deployments).
func getDriftDetectionGroupLabels(pool agentgroup.Pool) map[string]string {
	lbls := pool.Labels()
	lbls[driftDetectionFeatureLabelKey] = driftDetectionFeatureLabelValue
	return lbls
}

// deployDriftDetectionManagerInGroup adds the cluster to a group of the pool, then deploys (or
// updates) the drift-detection-manager Deployment serving the group.
//
// Only the global patches apply to a group Deployment: a Deployment serves several clusters, so
// a patch asked by one of them cannot be applied to it.
func deployDriftDetectionManagerInGroup(ctx context.Context, c client.Client, restConfig *rest.Config,
	clusterNamespace, clusterName, mode string, clusterType libsveltosv1beta1.ClusterType,
	shardKey string, watchNamespaces []string, patches []libsveltosv1beta1.Patch, logger logr.Logger) error {

	pool := getDriftDetectionPool(clusterNamespace, shardKey)
	member := agentgroup.Member{Name: clusterName, ClusterType: clusterType, WatchNamespaces: watchNamespaces}

	result, err := getDriftDetectionAssigner(c).AssignCluster(ctx, pool, member, getAgentClustersPerInstance())
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to assign cluster to a drift-detection-manager group: %v", err))
		return err
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("cluster is served by drift-detection-manager group %s", result.GroupName))

	// The cluster might have had its own drift-detection-manager before grouping was enabled
	if err := removeDriftDetectionManagerDeploymentOfCluster(ctx, restConfig, clusterNamespace, clusterName,
		clusterType, logger); err != nil {
		return err
	}

	return deployDriftDetectionManagerGroupDeployment(ctx, restConfig, result.GroupName, clusterNamespace,
		getDriftDetectionGroupLabels(pool), mode, patches, logger)
}

// deployDriftDetectionManagerOfCluster deploys the drift-detection-manager serving this cluster
// only (agentless mode, one cluster per instance).
//
// The cluster may still be listed in a group, because grouping was turned off or the number of
// clusters per instance lowered to one. The ConfigMaps of the groups tell where the cluster is
// served, not the setting: the cluster is taken out of its group first, so that no two
// drift-detection-managers serve it. Does nothing when the cluster is in no group.
func deployDriftDetectionManagerOfCluster(ctx context.Context, c client.Client, restConfig *rest.Config,
	clusterNamespace, clusterName, mode string, clusterType libsveltosv1beta1.ClusterType,
	patches []libsveltosv1beta1.Patch, watchNamespaces []string, logger logr.Logger) error {

	err := removeClusterFromDriftDetectionManagerGroups(ctx, c, restConfig, clusterNamespace, clusterName,
		clusterType, logger)
	if err != nil {
		return err
	}

	return deployDriftDetectionManagerInManagementCluster(ctx, restConfig, clusterNamespace, clusterName,
		mode, clusterType, patches, watchNamespaces, logger)
}

// deployDriftDetectionManagerGroupDeployment applies the drift-detection-manager Deployment (and
// what comes with it) of a group. It is the Deployment, not the cluster, that is named after
// the group, and that is told which ConfigMap lists the clusters it serves.
func deployDriftDetectionManagerGroupDeployment(ctx context.Context, restConfig *rest.Config,
	groupName, clusterNamespace string, lbls map[string]string, mode string, patches []libsveltosv1beta1.Patch,
	logger logr.Logger) error {

	driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerInMgmtClusterYAML())
	driftDetectionManagerYAML = prepareDriftDetectionManagerGroupYAML(driftDetectionManagerYAML, groupName,
		clusterNamespace, mode)
	driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "$NAME", groupName)

	return deployDriftDetectionManagerResources(ctx, restConfig, clusterNamespace, groupName, "", "",
		driftDetectionManagerYAML, lbls, patches, logger)
}

// prepareDriftDetectionManagerGroupYAML sets the arguments of the drift-detection-manager serving
// a group. The cluster (name, type, watch namespaces) is not an argument: each cluster served is
// listed in the group ConfigMap.
func prepareDriftDetectionManagerGroupYAML(driftDetectionManagerYAML, groupName, clusterNamespace, mode string,
) string {

	if mode != doNotSendUpdatesMode {
		driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, doNotSendUpdatesMode, "send-updates")
	}
	driftDetectionManagerYAML = strings.ReplaceAll(driftDetectionManagerYAML, "v=5", "v=0")

	perClusterArgs := []string{"--cluster-name=", "--cluster-type=", "--watch-namespaces="}

	lines := strings.Split(driftDetectionManagerYAML, "\n")
	result := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isOneOf(trimmed, perClusterArgs) {
			continue
		}

		if strings.HasPrefix(trimmed, "- --cluster-namespace=") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
			result = append(result,
				fmt.Sprintf("%s- --cluster-namespace=%s", indent, clusterNamespace),
				fmt.Sprintf("%s- --group-config-map=%s", indent, groupName))
			continue
		}
		result = append(result, line)
	}

	driftDetectionManagerYAML = strings.Join(result, "\n")

	registry := getDriftDetectionRegistry()
	if registry != "" {
		driftDetectionManagerYAML = replaceRegistry(driftDetectionManagerYAML, registry)
	}

	return driftDetectionManagerYAML
}

// isOneOf returns true if the line is the argument "- <prefix>" for one of the prefixes (with
// an empty value, as in the template).
func isOneOf(trimmedLine string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if trimmedLine == "- "+prefix {
			return true
		}
	}
	return false
}

// removeDriftDetectionManagerDeploymentOfCluster deletes the Deployment of the per cluster
// drift-detection-manager, if there is one. Unlike removeDriftDetectionManagerFromManagementCluster
// it leaves everything else (the version ConfigMap, which the group instance keeps up to date) alone.
func removeDriftDetectionManagerDeploymentOfCluster(ctx context.Context, restConfig *rest.Config,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) error {

	lbls := getDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	namespace := getDriftDetectionNamespaceInMgmtCluster(getSveltosNamespace())
	err = clientset.AppsV1().Deployments(namespace).DeleteCollection(ctx, metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: labels.Set(lbls).String()})
	if err != nil && !apierrors.IsNotFound(err) {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove per cluster drift-detection-manager: %v", err))
		return err
	}

	return nil
}

// removeClusterFromDriftDetectionManagerGroups takes the cluster out of its drift-detection-manager
// group, if it is in one. The group Deployment is deleted only when the cluster was its last member:
// as long as the group has a cluster, its drift-detection-manager stays up.
func removeClusterFromDriftDetectionManagerGroups(ctx context.Context, c client.Client, restConfig *rest.Config,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) error {

	emptied, err := getDriftDetectionAssigner(c).RemoveCluster(ctx, driftDetectionComponent,
		clusterNamespace, clusterName, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to remove cluster from drift-detection-manager group: %v", err))
		return err
	}

	if len(emptied) == 0 {
		return nil
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	namespace := getDriftDetectionNamespaceInMgmtCluster(getSveltosNamespace())
	for _, groupName := range emptied {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("last cluster left drift-detection-manager group %s: removing it", groupName))
		err = clientset.AppsV1().Deployments(namespace).Delete(ctx, groupName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

// isDriftDetectionGroupDeployment returns true if the Deployment serves a group of clusters.
func isDriftDetectionGroupDeployment(deployment *appsv1.Deployment) bool {
	return deployment.GetLabels()[agentgroup.ComponentLabel] == driftDetectionComponent
}

// upgradeDriftDetectionGroupDeployment applies again the Deployment of a group, unless the group
// does not exist anymore (the Deployment is then an orphan, to be deleted).
func upgradeDriftDetectionGroupDeployment(ctx context.Context, restConfig *rest.Config, c client.Client,
	deployment *appsv1.Deployment, patches []libsveltosv1beta1.Patch, logger logr.Logger) error {

	lbls := deployment.GetLabels()
	pool := agentgroup.Pool{
		Component:        driftDetectionComponent,
		ClusterNamespace: lbls[agentgroup.ClusterNamespaceLabel],
	}

	groups, err := agentgroup.ListGroups(ctx, c, getSveltosNamespace(), driftDetectionComponent, pool.ClusterNamespace)
	if err != nil {
		return err
	}

	for i := range groups {
		if groups[i].Name != deployment.Name {
			continue
		}
		// The shard is not recoverable from its hash label, and does not matter here: the
		// Deployment is already labeled, so keep its labels.
		return deployDriftDetectionManagerGroupDeployment(ctx, restConfig, deployment.Name, pool.ClusterNamespace,
			lbls, "do-not-send-updates", patches, logger)
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("group %s has no ConfigMap anymore: removing orphan Deployment", deployment.Name))
	return c.Delete(ctx, deployment)
}
