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

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
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

	// TODO: remove I/O inside a MapFunc.
	// If the list operation failed I would be unable to retry or re-enqueue the change.
	clusterFeatureList := &configv1alpha1.ClusterFeatureList{}
	if err := r.List(context.TODO(), clusterFeatureList); err != nil {
		logger.Error(err, "failed to list all ClusterFeatures")
		return nil
	}

	requests := make([]ctrl.Request, 0)

	for i := range clusterFeatureList.Items {
		clusterFeature := &clusterFeatureList.Items[i]
		parsedSelector, _ := labels.Parse(string(clusterFeature.Spec.ClusterSelector))
		if parsedSelector.Matches(labels.Set(cluster.Labels)) {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: clusterFeature.Namespace,
					Name:      clusterFeature.Name,
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

	clusterName, ok := machine.Labels[clusterv1.ClusterLabelName]
	if !ok {
		logger.V(10).Info("Machine has not ClusterLabelName")
		return nil
	}

	cluster := &clusterv1.Cluster{}
	if err := r.Client.Get(context.TODO(),
		types.NamespacedName{Namespace: machine.Namespace, Name: clusterName}, cluster); err != nil {
		logger.Error(err, fmt.Sprintf("failed to get CAPI Cluster %s/%s", machine.Namespace, clusterName))
		return nil
	}

	clusterFeatureList := &configv1alpha1.ClusterFeatureList{}
	if err := r.List(context.TODO(), clusterFeatureList); err != nil {
		logger.Error(err, "failed to list all ClusterFeatures")
		return nil
	}
	requests := make([]ctrl.Request, 0)

	for i := range clusterFeatureList.Items {
		clusterFeature := &clusterFeatureList.Items[i]
		parsedSelector, _ := labels.Parse(string(clusterFeature.Spec.ClusterSelector))
		if parsedSelector.Matches(labels.Set(cluster.Labels)) {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Namespace: clusterFeature.Namespace,
					Name:      clusterFeature.Name,
				},
			})
		}
	}

	return requests
}
