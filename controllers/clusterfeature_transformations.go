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
	"k8s.io/apimachinery/pkg/labels"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *ClusterFeatureReconciler) requeueClusterFeatureForCluster(
	o client.Object,
) []reconcile.Request {

	cluster := o.(*clusterv1.Cluster)
	logger := r.Log.WithValues(
		"objectMapper",
		"requeueClusterFeatureForCluster",
		"namespace",
		cluster.Namespace,
		"cluster",
		cluster.Name,
	)

	logger.V(5).Info("reacting to CAPI Cluster change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterName := cluster.Namespace + "/" + cluster.Name

	// Get all ClusterFeature previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(clusterName).len())
	consumers := r.getClusterMapForEntry(clusterName).items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i],
			},
		}
	}

	// Iterate over all current ClusterFeature and reconcile the ClusterFeature now
	// matching the Cluster
	for k := range r.ClusterFeatures {
		clusterFeatureSelector := r.ClusterFeatures[k]
		parsedSelector, _ := labels.Parse(string(clusterFeatureSelector))
		if parsedSelector.Matches(labels.Set(cluster.Labels)) {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name: k,
				},
			})
		}
	}

	return requests
}

func (r *ClusterFeatureReconciler) requeueClusterFeatureForMachine(
	o client.Object,
) []reconcile.Request {
	machine := o.(*clusterv1.Machine)
	logger := r.Log.WithValues(
		"objectMapper",
		"requeueClusterFeatureForMachine",
		"namespace",
		machine.Namespace,
		"cluster",
		machine.Name,
	)

	clusterLabelName, ok := machine.Labels[clusterv1.ClusterLabelName]
	if !ok {
		logger.V(10).Info("Machine has not ClusterLabelName")
		return nil
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterName := machine.Namespace + "/" + clusterLabelName

	// Get all ClusterFeature previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(clusterName).len())
	consumers := r.getClusterMapForEntry(clusterName).items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i],
			},
		}
	}

	return requests
}
