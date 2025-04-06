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

package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/sveltos_upgrade"
)

// Periodically collects ResourceSummaries from each CAPI/Sveltos cluster.
func collectAndProcessResourceSummaries(ctx context.Context, c client.Client, shardkey, version string,
	logger logr.Logger) {

	const interval = 10 * time.Second

	for {
		logger.V(logs.LogVerbose).Info("collecting ResourceSummaries")
		clusterList, err := clusterproxy.GetListOfClustersForShardKey(ctx, c, "", getCAPIOnboardAnnotation(),
			shardkey, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
		}

		for i := range clusterList {
			cluster := &clusterList[i]
			err = collectResourceSummariesFromCluster(ctx, c, cluster, version, logger)
			if err != nil {
				if !strings.Contains(err.Error(), "unable to retrieve the complete list of server APIs") {
					logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect ResourceSummaries from cluster: %s/%s %v",
						cluster.Namespace, cluster.Name, err))
				}
			}
		}

		time.Sleep(interval)
	}
}

func collectResourceSummariesFromCluster(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	version string, logger logr.Logger) error {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))
	clusterRef := &corev1.ObjectReference{
		Namespace:  cluster.Namespace,
		Name:       cluster.Name,
		APIVersion: cluster.APIVersion,
		Kind:       cluster.Kind,
	}
	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, clusterRef, logger)
	if err != nil {
		logger.V(logs.LogDebug).Info("cluster is not ready yet")
		return err
	}

	if !ready {
		return nil
	}

	//  ResourceSummary location depends on drift-detection-manager: management cluster if it's running there,
	// otherwise managed cluster.
	clusterClient, err := getResourceSummaryClient(ctx, cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(clusterRef), logger)
	if err != nil {
		return err
	}

	var installed bool
	installed, err = isResourceSummaryInstalled(ctx, clusterClient)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to verify if ResourceSummary is installed %v", err))
		return err
	}

	if !installed {
		return nil
	}

	if !sveltos_upgrade.IsDriftDetectionVersionCompatible(ctx, getManagementClusterClient(), version, cluster.Namespace,
		cluster.Name, clusterproxy.GetClusterType(clusterRef), getAgentInMgmtCluster(), logger) {

		msg := "compatibility checks failed"
		logger.V(logs.LogDebug).Info(msg)
		return errors.New(msg)
	}

	logger.V(logs.LogVerbose).Info("collecting ResourceSummaries from cluster")
	rsList := libsveltosv1beta1.ResourceSummaryList{}

	err = clusterClient.List(ctx, &rsList)
	if err != nil {
		return err
	}

	l := logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name))

	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !rs.DeletionTimestamp.IsZero() {
			// ignore deleted ClassifierReport
			continue
		}
		if rs.Status.ResourcesChanged || rs.Status.HelmResourcesChanged || rs.Status.KustomizeResourcesChanged {
			// process resourceSummary
			err = processResourceSummary(ctx, clusterClient, rs, l)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// isResourceSummaryInstalled returns true if ResourceSummary CRD is installed, false otherwise
func isResourceSummaryInstalled(ctx context.Context, c client.Client) (bool, error) {
	clusterCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "resourcesummaries.lib.projectsveltos.io"}, clusterCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// clusterClient points to the cluster where ResourceSummary is. That can be
// the management cluster if running in agentless mode or the managed cluster
// otherwise
func processResourceSummary(ctx context.Context, clusterClient client.Client,
	rs *libsveltosv1beta1.ResourceSummary, logger logr.Logger) error {

	if rs.Labels == nil {
		logger.V(logs.LogInfo).Info("labels not set. Cannot process it")
		return nil
	}

	// Get ClusterSummary
	clusterSummaryName, ok := rs.Labels[libsveltosv1beta1.ClusterSummaryNameLabel]
	if !ok {
		logger.V(logs.LogInfo).Info("clusterSummary name label not set. Cannot process it")
		return nil
	}

	var clusterSummaryNamespace string
	clusterSummaryNamespace, ok = rs.Labels[libsveltosv1beta1.ClusterSummaryNamespaceLabel]
	if !ok {
		logger.V(logs.LogInfo).Info("clusterSummary namespace label not set. Cannot process it")
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		clusterSummary := &configv1beta1.ClusterSummary{}
		err := getManagementClusterClient().Get(ctx,
			types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName}, clusterSummary)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(logs.LogInfo).Info("clusterSummary not found. Nothing to do.")
				return nil
			}
			return err
		}

		if !clusterSummary.DeletionTimestamp.IsZero() {
			logger.V(logs.LogInfo).Info("clusterSummary is marked for deletion. Nothing to do.")
			return nil
		}

		l := logger.WithValues("clusterSummary", clusterSummary.Name)
		for i := range clusterSummary.Status.FeatureSummaries {
			if clusterSummary.Status.FeatureSummaries[i].FeatureID == configv1beta1.FeatureHelm {
				if rs.Status.HelmResourcesChanged {
					l.V(logs.LogDebug).Info("redeploy helm")
					clusterSummary.Status.FeatureSummaries[i].Hash = nil
					clusterSummary.Status.FeatureSummaries[i].Status = configv1beta1.FeatureStatusProvisioning
					trackDrifts(clusterSummaryNamespace, clusterSummary.Spec.ClusterName, string(clusterSummary.Status.FeatureSummaries[i].FeatureID),
						string(clusterSummary.Spec.ClusterType), logger)
				}
			} else if clusterSummary.Status.FeatureSummaries[i].FeatureID == configv1beta1.FeatureResources {
				if rs.Status.ResourcesChanged {
					l.V(logs.LogDebug).Info("redeploy resources")
					clusterSummary.Status.FeatureSummaries[i].Hash = nil
					clusterSummary.Status.FeatureSummaries[i].Status = configv1beta1.FeatureStatusProvisioning
					trackDrifts(clusterSummaryNamespace, clusterSummary.Spec.ClusterName, string(clusterSummary.Status.FeatureSummaries[i].FeatureID),
						string(clusterSummary.Spec.ClusterType), logger)
				}
			} else if clusterSummary.Status.FeatureSummaries[i].FeatureID == configv1beta1.FeatureKustomize {
				if rs.Status.KustomizeResourcesChanged {
					l.V(logs.LogDebug).Info("redeploy kustomization resources")
					clusterSummary.Status.FeatureSummaries[i].Hash = nil
					clusterSummary.Status.FeatureSummaries[i].Status = configv1beta1.FeatureStatusProvisioning
					trackDrifts(clusterSummaryNamespace, clusterSummary.Spec.ClusterName, string(clusterSummary.Status.FeatureSummaries[i].FeatureID),
						string(clusterSummary.Spec.ClusterType), logger)
				}
			}
		}

		err = getManagementClusterClient().Status().Update(ctx, clusterSummary)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update ClusterSummary status: %v", err))
			return err
		}
		return nil
	})

	if err != nil {
		return err
	}

	return resetResourceSummaryStatus(ctx, clusterClient, rs, logger)
}

func resetResourceSummaryStatus(ctx context.Context, remoteClient client.Client,
	rs *libsveltosv1beta1.ResourceSummary, logger logr.Logger) error {

	resourceSummary := &libsveltosv1beta1.ResourceSummary{}

	err := remoteClient.Get(ctx, types.NamespacedName{Namespace: rs.Namespace, Name: rs.Name}, resourceSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.V(logs.LogDebug).Info("reset resourceSummary status")
	resourceSummary.Status.ResourcesChanged = false
	resourceSummary.Status.HelmResourcesChanged = false
	return remoteClient.Status().Update(ctx, resourceSummary)
}
