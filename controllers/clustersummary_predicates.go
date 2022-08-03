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
	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

// WorkloadRolePredicates predicates for WorkloadRole. ClusterSummaryReconciler watches WorkloadRole events
// and react to those by reconciling itself based on following predicates
func WorkloadRolePredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newWorkloadRole := e.ObjectNew.(*configv1alpha1.WorkloadRole)
			oldWorkloadRole := e.ObjectOld.(*configv1alpha1.WorkloadRole)
			log := logger.WithValues("predicate", "updateEvent",
				"workloadrole", newWorkloadRole.Name,
			)

			if oldWorkloadRole == nil {
				log.V(10).Info("Old WorkloadRole is nil. Reconcile ClusterSummaries.")
				return true
			}

			if !reflect.DeepEqual(oldWorkloadRole.Spec, newWorkloadRole.Spec) {
				log.V(10).Info(
					"WorkloadRole Spec changed. Will attempt to reconcile associated ClusterSummaries.",
				)
				return true
			}

			// otherwise, return false
			log.V(10).Info(
				"WorkloadRole did not match expected conditions.  Will not attempt to reconcile associated ClusterSummaries.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			workloadRole := e.Object.(*configv1alpha1.WorkloadRole)
			log := logger.WithValues("predicate", "createEvent",
				"workloadrole", workloadRole.Name,
			)

			log.V(10).Info(
				"WorkloadRole did match expected conditions.  Will attempt to reconcile associated ClusterSummaries.")
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"workloadrole", e.Object.GetName(),
			)
			log.V(10).Info(
				"WorkloadRole did match expected conditions.  Will attempt to reconcile associated ClusterSummaries.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"workloadrole", e.Object.GetName(),
			)
			log.V(10).Info(
				"WorkloadRole did not match expected conditions.  Will not attempt to reconcile associated ClusterSummaries.")
			return false
		},
	}
}

// ConfigMapPredicates predicates for ConfigMaps. ClusterSummaryReconciler watches ConfigMap events
// and react to those by reconciling itself based on following predicates
func ConfigMapPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newConfigMap := e.ObjectNew.(*corev1.ConfigMap)
			oldConfigMap := e.ObjectOld.(*corev1.ConfigMap)
			log := logger.WithValues("predicate", "updateEvent",
				"configmap", newConfigMap.Name,
			)

			if oldConfigMap == nil {
				log.V(10).Info("Old ConfigMap is nil. Reconcile ClusterSummaries.")
				return true
			}

			if !reflect.DeepEqual(oldConfigMap.Data, newConfigMap.Data) {
				log.V(10).Info(
					"ConfigMap Data changed. Will attempt to reconcile associated ClusterSummaries.",
				)
				return true
			}

			// otherwise, return false
			log.V(10).Info(
				"ConfigMap did not match expected conditions.  Will not attempt to reconcile associated ClusterSummaries.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			configMap := e.Object.(*corev1.ConfigMap)
			log := logger.WithValues("predicate", "createEvent",
				"configmap", configMap.Name,
			)

			log.V(10).Info(
				"ConfigMap did match expected conditions.  Will attempt to reconcile associated ClusterSummaries.")
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"configmap", e.Object.GetName(),
			)
			log.V(10).Info(
				"ConfigMap did match expected conditions.  Will attempt to reconcile associated ClusterSummaries.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"configmap", e.Object.GetName(),
			)
			log.V(10).Info(
				"ConfigMap did not match expected conditions.  Will not attempt to reconcile associated ClusterSummaries.")
			return false
		},
	}
}
