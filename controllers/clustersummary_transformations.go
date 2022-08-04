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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

func (r *ClusterSummaryReconciler) requeueClusterSummaryForWorkloadRole(
	o client.Object,
) []reconcile.Request {

	workloadRole := o.(*configv1alpha1.WorkloadRole)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterSummaryForWorkloadRole",
		"workloadRole",
		workloadRole.Name,
	)

	logger.V(5).Info("reacting to WorkloadRole change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	key := getEntryKey(WorkloadRole, "", workloadRole.Name)
	requests := make([]ctrl.Request, r.getReferenceMapForEntry(key).len())

	consumers := r.getReferenceMapForEntry(key).items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i],
			},
		}
	}

	return requests
}

func (r *ClusterSummaryReconciler) requeueClusterSummaryForConfigMap(
	o client.Object,
) []reconcile.Request {

	configMap := o.(*corev1.ConfigMap)
	logger := klogr.New().WithValues(
		"objectMapper",
		"requeueClusterSummaryForConfigMap",
		"configMap",
		configMap.Name,
	)

	logger.V(5).Info("reacting to configMap change")

	r.Mux.Lock()
	defer r.Mux.Unlock()

	key := getEntryKey(ConfigMap, configMap.Namespace, configMap.Name)
	requests := make([]ctrl.Request, r.getReferenceMapForEntry(key).len())

	consumers := r.getReferenceMapForEntry(key).items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i],
			},
		}
	}

	return requests
}
