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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func (r *ClusterSummaryReconciler) requeueClusterSummaryForFluxSources(
	o client.Object,
) []reconcile.Request {

	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterSummaryForFluxSources",
		"reference",
		o.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to configMap/secret change")

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	// Following is needed as o.GetObjectKind().GroupVersionKind().Kind is not set
	var key corev1.ObjectReference
	switch o.(type) {
	case *sourcev1.GitRepository:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       sourcev1.GitRepositoryKind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	case *sourcev1b2.OCIRepository:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       sourcev1b2.OCIRepositoryKind,
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	case *sourcev1b2.Bucket:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
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

	logger.V(logs.LogDebug).Info(fmt.Sprintf("referenced key: %s", key))

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
	o client.Object,
) []reconcile.Request {

	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterSummaryForConfigMap",
		"reference",
		o.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to configMap/secret change")

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	// Following is needed as o.GetObjectKind().GroupVersionKind().Kind is not set
	var key corev1.ObjectReference
	switch o.(type) {
	case *corev1.ConfigMap:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
			Namespace:  o.GetNamespace(),
			Name:       o.GetName(),
		}
	case *corev1.Secret:
		key = corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       string(libsveltosv1alpha1.SecretReferencedResourceKind),
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

	logger.V(logs.LogDebug).Info(fmt.Sprintf("referenced key: %s", key))

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
// for ClusterSummary to update when its own Sveltos/CAPI Cluster gets updated.
func (r *ClusterSummaryReconciler) requeueClusterSummaryForCluster(
	o client.Object,
) []reconcile.Request {

	cluster := o
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterSummaryForCluster",
		"namespace",
		cluster.GetNamespace(),
		"cluster",
		cluster.GetName(),
	)

	logger.V(logs.LogDebug).Info("reacting to CAPI Cluster change")

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	clusterInfo := getKeyFromObject(r.Scheme, cluster)
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
