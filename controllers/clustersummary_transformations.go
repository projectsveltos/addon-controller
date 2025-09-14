/*
Copyright 2022. projectsveltos.io. All rights reserved.

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

	corev1 "k8s.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"

	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func (r *ClusterSummaryReconciler) requeueClusterSummaryForFluxGitRepository(
	ctx context.Context, o *sourcev1.GitRepository,
) []reconcile.Request {

	return r.requeueClusterSummaryForFluxSource(ctx, o)
}

func (r *ClusterSummaryReconciler) requeueClusterSummaryForFluxOCIRepository(
	ctx context.Context, o *sourcev1b2.OCIRepository,
) []reconcile.Request {

	return r.requeueClusterSummaryForFluxSource(ctx, o)
}

func (r *ClusterSummaryReconciler) requeueClusterSummaryForFluxBucket(
	ctx context.Context, o *sourcev1b2.Bucket,
) []reconcile.Request {

	return r.requeueClusterSummaryForFluxSource(ctx, o)
}

func (r *ClusterSummaryReconciler) requeueClusterSummaryForFluxSource(
	_ context.Context, o client.Object,
) []reconcile.Request {

	logger := r.Logger.WithValues(
		"objectMapper",
		"requeueClusterSummaryForFluxSources",
		"reference",
		o.GetName(),
	)

	logger.V(logs.LogVerbose).Info("reacting to flux source change")

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	// Following is needed as o.GetObjectKind().GroupVersionKind().Kind is not set
	var key corev1.ObjectReference
	switch o.(type) {
	case *sourcev1.GitRepository:
		key = corev1.ObjectReference{
			APIVersion: sourcev1.GroupVersion.String(),
			Kind:       sourcev1.GitRepositoryKind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	case *sourcev1b2.OCIRepository:
		key = corev1.ObjectReference{
			APIVersion: sourcev1b2.GroupVersion.String(),
			Kind:       sourcev1b2.OCIRepositoryKind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	case *sourcev1b2.Bucket:
		key = corev1.ObjectReference{
			APIVersion: sourcev1b2.GroupVersion.String(),
			Kind:       sourcev1b2.BucketKind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	default:
		key = corev1.ObjectReference{
			APIVersion: o.GetObjectKind().GroupVersionKind().GroupVersion().String(),
			Kind:       o.GetObjectKind().GroupVersionKind().Kind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	}

	logger = logger.WithValues("key", fmt.Sprintf("%s", key))
	requests := make([]ctrl.Request, r.getReferenceMapForEntry(&key).Len())

	consumers := r.getReferenceMapForEntry(&key).Items()
	for i := range consumers {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("requeue consumer: %s", consumers[i]))
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      consumers[i].Name,
				Namespace: consumers[i].Namespace,
			},
		}
	}

	return requests
}

func (r *ClusterSummaryReconciler) requeueClusterSummaryForReference(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	logger := r.Logger.WithValues(
		"objectMapper",
		"requeueClusterSummaryForConfigMap",
		"reference",
		o.GetName(),
	)

	logger.V(logs.LogVerbose).Info("reacting to configMap/secret change")

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	// Following is needed as o.GetObjectKind().GroupVersionKind().Kind is not set
	var key corev1.ObjectReference
	switch o.(type) {
	case *corev1.ConfigMap:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	case *corev1.Secret:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       string(libsveltosv1beta1.SecretReferencedResourceKind),
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
		cacheMgr := clustercache.GetManager()
		cacheMgr.RemoveSecret(&key)
	default:
		key = corev1.ObjectReference{
			APIVersion: o.GetObjectKind().GroupVersionKind().GroupVersion().String(),
			Kind:       o.GetObjectKind().GroupVersionKind().Kind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	}

	logger = logger.WithValues("key", fmt.Sprintf("%s", key))

	requests := make([]ctrl.Request, r.getReferenceMapForEntry(&key).Len())

	consumers := r.getReferenceMapForEntry(&key).Items()
	for i := range consumers {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("requeue consumer: %s", consumers[i]))
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      consumers[i].Name,
				Namespace: consumers[i].Namespace,
			},
		}
	}

	return requests
}

// requeueClusterSummaryForCluster is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// for ClusterSummary to update when its own Sveltos Cluster gets updated.
func (r *ClusterSummaryReconciler) requeueClusterSummaryForSveltosCluster(
	ctx context.Context, cluster client.Object,
) []reconcile.Request {

	return r.requeueClusterSummaryForACluster(ctx, cluster)
}

// requeueClusterSummaryForCluster is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// for ClusterSummary to update when its own Cluster gets updated.
func (r *ClusterSummaryReconciler) requeueClusterSummaryForCluster(
	ctx context.Context, cluster *clusterv1.Cluster,
) []reconcile.Request {

	return r.requeueClusterSummaryForACluster(ctx, cluster)
}

func (r *ClusterSummaryReconciler) requeueClusterSummaryForACluster(
	_ context.Context, cluster client.Object,
) []reconcile.Request {

	logger := r.Logger.WithValues(
		"objectMapper",
		"requeueClusterSummaryForCluster",
		"namespace",
		cluster.GetNamespace(),
		"cluster",
		cluster.GetName(),
	)

	logger.V(logs.LogVerbose).Info("reacting to Cluster change")

	clusterInfo := getKeyFromObject(r.Scheme, cluster)
	if !cluster.GetDeletionTimestamp().IsZero() {
		cacheMgr := clustercache.GetManager()
		cacheMgr.RemoveCluster(cluster.GetNamespace(), cluster.GetName(),
			clusterproxy.GetClusterType(clusterInfo))
	}

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	// Get all ClusterSummaries for this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(clusterInfo).Len())
	consumers := r.getClusterMapForEntry(clusterInfo).Items()

	for i := range consumers {
		l := logger.WithValues("clusterSummary", fmt.Sprintf("%s/%s", consumers[i].Namespace, consumers[i].Name))
		l.V(logs.LogDebug).Info("queuing ClusterSummary")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Namespace: consumers[i].Namespace,
				Name:      consumers[i].Name,
			},
		}
	}

	return requests
}
