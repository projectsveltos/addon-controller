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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// Periodically collects ResourceSummaries from each CAPI/Sveltos cluster.
func collectAndProcessResourceSummaries(ctx context.Context, c client.Client, logger logr.Logger) {
	const interval = 10 * time.Second

	for {
		logger.V(logs.LogDebug).Info("collecting ResourceSummaries")
		clusterList, err := getListOfClusters(ctx, c, logger)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
		}

		for i := range clusterList {
			cluster := &clusterList[i]
			err = collectResourceSummariesFromCluster(ctx, c, cluster, logger)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect ResourceSummaries from cluster: %s/%s %v",
					cluster.Namespace, cluster.Name, err))
			}
		}

		time.Sleep(interval)
	}
}

func collectResourceSummariesFromCluster(ctx context.Context, c client.Client,
	cluster *corev1.ObjectReference, logger logr.Logger) error {

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

	// Use cluster-admin role to collect Sveltos resources from managed clusters
	var remoteClient client.Client
	remoteClient, err = clusterproxy.GetKubernetesClient(ctx, c, cluster.Namespace, cluster.Name, "",
		clusterproxy.GetClusterType(clusterRef), logger)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("collecting ResourceSummaries from cluster")
	rsList := libsveltosv1alpha1.ResourceSummaryList{}
	err = remoteClient.List(ctx, &rsList)
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
		if rs.Status.ResourcesChanged || rs.Status.HelmResourcesChanged {
			// process resourceSummary
			err = processResourceSummary(ctx, c, remoteClient, rs, l)
			if err != nil {
				return nil
			}
		}
	}

	return nil
}

func processResourceSummary(ctx context.Context, c, remoteClient client.Client,
	rs *libsveltosv1alpha1.ResourceSummary, logger logr.Logger) error {

	if rs.Labels == nil {
		logger.V(logs.LogInfo).Info("labels not set. Cannot process it")
		return nil
	}

	// Get ClusterSummary
	clusterSummaryName, ok := rs.Labels[libsveltosv1alpha1.ClusterSummaryLabelName]
	if !ok {
		logger.V(logs.LogInfo).Info("clusterSummary name label not set. Cannot process it")
		return nil
	}

	var clusterSummaryNamespace string
	clusterSummaryNamespace, ok = rs.Labels[libsveltosv1alpha1.ClusterSummaryLabelNamespace]
	if !ok {
		logger.V(logs.LogInfo).Info("clusterSummaryspace name label not set. Cannot process it")
		return nil
	}

	clusterSummary := &configv1alpha1.ClusterSummary{}
	err := c.Get(ctx, types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName}, clusterSummary)
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
		if clusterSummary.Status.FeatureSummaries[i].FeatureID == configv1alpha1.FeatureHelm {
			if rs.Status.HelmResourcesChanged {
				l.V(logs.LogDebug).Info("redeploy helm")
				clusterSummary.Status.FeatureSummaries[i].Hash = nil
				clusterSummary.Status.FeatureSummaries[i].Status = configv1alpha1.FeatureStatusProvisioning
			}
		} else if clusterSummary.Status.FeatureSummaries[i].FeatureID == configv1alpha1.FeatureResources {
			if rs.Status.ResourcesChanged {
				l.V(logs.LogDebug).Info("redeploy resources")
				clusterSummary.Status.FeatureSummaries[i].Hash = nil
				clusterSummary.Status.FeatureSummaries[i].Status = configv1alpha1.FeatureStatusProvisioning
			}
		}
	}

	err = c.Status().Update(ctx, clusterSummary)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update ClusterSummary status: %v", err))
		return err
	}

	return resetResourceSummaryStatus(ctx, remoteClient, rs, logger)
}

func resetResourceSummaryStatus(ctx context.Context, remoteClient client.Client,
	rs *libsveltosv1alpha1.ResourceSummary, logger logr.Logger) error {

	resourceSummary := &libsveltosv1alpha1.ResourceSummary{}

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
