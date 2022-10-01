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
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
)

func (r *ClusterProfileReconciler) requeueClusterProfileForCluster(
	o client.Object,
) []reconcile.Request {

	cluster := o.(*clusterv1.Cluster)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterProfileForCluster",
		"namespace",
		cluster.Namespace,
		"cluster",
		cluster.Name,
	)

	logger.V(logs.LogDebug).Info("reacting to CAPI Cluster change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterInfo := configv1alpha1.PolicyRef{Kind: "Cluster", Namespace: cluster.Namespace, Name: cluster.Name}

	// Get all ClusterProfile previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(&clusterInfo).len())
	consumers := r.getClusterMapForEntry(&clusterInfo).items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Iterate over all current ClusterProfile and reconcile the ClusterProfile now
	// matching the Cluster
	for k := range r.ClusterProfiles {
		clusterProfileSelector := r.ClusterProfiles[k]
		parsedSelector, _ := labels.Parse(string(clusterProfileSelector))
		if parsedSelector.Matches(labels.Set(cluster.Labels)) {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name: k.Name,
				},
			})
		}
	}

	return requests
}

func (r *ClusterProfileReconciler) requeueClusterProfileForMachine(
	o client.Object,
) []reconcile.Request {

	machine := o.(*clusterv1.Machine)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterProfileForMachine",
		"namespace",
		machine.Namespace,
		"cluster",
		machine.Name,
	)

	clusterLabelName, ok := machine.Labels[clusterv1.ClusterLabelName]
	if !ok {
		logger.V(logs.LogVerbose).Info("Machine has not ClusterLabelName")
		return nil
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterInfo := configv1alpha1.PolicyRef{Kind: "Cluster", Namespace: machine.Namespace, Name: clusterLabelName}

	// Get all ClusterProfile previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, r.getClusterMapForEntry(&clusterInfo).len())
	consumers := r.getClusterMapForEntry(&clusterInfo).items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	return requests
}
