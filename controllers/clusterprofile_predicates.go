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
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// ClusterPredicates predicates for v1Cluster. ClusterProfileReconciler watches v1Cluster events
// and react to those by reconciling itself based on following predicates
func ClusterPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newCluster := e.ObjectNew.(*clusterv1.Cluster)
			oldCluster := e.ObjectOld.(*clusterv1.Cluster)
			log := logger.WithValues("predicate", "updateEvent",
				"namespace", newCluster.Namespace,
				"cluster", newCluster.Name,
			)

			if oldCluster == nil {
				log.V(logs.LogVerbose).Info("Old Cluster is nil. Reconcile (Cluster)Profiles/(Cluster)Set.")
				return true
			}

			// a label change migth change which clusters match which clusterprofile
			if !reflect.DeepEqual(oldCluster.Labels, newCluster.Labels) {
				log.V(logs.LogVerbose).Info(
					"Cluster labels changed. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.",
				)
				return true
			}

			// if sharding is used, cluster sharding annotation must be copied over ClusterSummary
			if !reflect.DeepEqual(oldCluster.Annotations, newCluster.Annotations) {
				log.V(logs.LogVerbose).Info(
					"Cluster annotations changed. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.",
				)
				return true
			}

			// return true if Cluster.Spec.Paused has changed from true to false
			if oldCluster.Spec.Paused && !newCluster.Spec.Paused {
				log.V(logs.LogVerbose).Info(
					"Cluster was unpaused. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.")
				return true
			}

			// return true if Cluster.Status.ControlPlaneReady has changed
			if oldCluster.Status.ControlPlaneReady != newCluster.Status.ControlPlaneReady {
				log.V(logs.LogVerbose).Info(
					"Cluster ControlPlaneReady changed. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.")
				return true
			}

			if oldCluster.Status.Phase != string(clusterv1.ClusterPhaseDeleting) &&
				newCluster.Status.Phase == string(clusterv1.ClusterPhaseDeleting) {

				log.V(logs.LogVerbose).Info(
					"Cluster is beng deleted. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.")
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				`Cluster did not match expected conditions. \
				Will not attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.`)
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			cluster := e.Object.(*clusterv1.Cluster)
			log := logger.WithValues("predicate", "createEvent",
				"namespace", cluster.Namespace,
				"cluster", cluster.Name,
			)

			// Only need to trigger a reconcile if the Cluster.Spec.Paused is false
			if !cluster.Spec.Paused {
				log.V(logs.LogVerbose).Info(
					"Cluster is not paused.  Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.",
				)
				return true
			}
			log.V(logs.LogVerbose).Info(
				`Cluster did not match expected conditions. \
				Will not attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.`)
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Cluster deleted.  Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				`Cluster did not match expected conditions. \
				Will not attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.`)
			return false
		},
	}
}

// SveltosClusterPredicates predicates for sveltos Cluster. ClusterProfileReconciler watches sveltos Cluster events
// and react to those by reconciling itself based on following predicates
func SveltosClusterPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newCluster := e.ObjectNew.(*libsveltosv1alpha1.SveltosCluster)
			oldCluster := e.ObjectOld.(*libsveltosv1alpha1.SveltosCluster)
			log := logger.WithValues("predicate", "updateEvent",
				"namespace", newCluster.Namespace,
				"cluster", newCluster.Name,
			)

			if oldCluster == nil {
				log.V(logs.LogVerbose).Info("Old Cluster is nil. Reconcile (Cluster)Profiles/(Cluster)Set.")
				return true
			}

			// return true if Cluster.Spec.Paused has changed from true to false
			if oldCluster.Spec.Paused && !newCluster.Spec.Paused {
				log.V(logs.LogVerbose).Info(
					"Cluster was unpaused. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.")
				return true
			}

			if oldCluster.Status.Ready != newCluster.Status.Ready {
				log.V(logs.LogVerbose).Info(
					"Cluster Status.Ready changed. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.")
				return true
			}

			// a label change migth change which clusters match which clusterprofile
			if !reflect.DeepEqual(oldCluster.Labels, newCluster.Labels) {
				log.V(logs.LogVerbose).Info(
					"Cluster labels changed. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.",
				)
				return true
			}

			// if sharding is used, cluster sharding annotation must be copied over ClusterSummary
			if !reflect.DeepEqual(oldCluster.Annotations, newCluster.Annotations) {
				log.V(logs.LogVerbose).Info(
					"Cluster annotations changed. Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.",
				)
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				`Cluster did not match expected conditions.  \
				Will not attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.`)
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			cluster := e.Object.(*libsveltosv1alpha1.SveltosCluster)
			log := logger.WithValues("predicate", "createEvent",
				"namespace", cluster.Namespace,
				"cluster", cluster.Name,
			)

			// Only need to trigger a reconcile if the Cluster.Spec.Paused is false
			if !cluster.Spec.Paused {
				log.V(logs.LogVerbose).Info(
					"Cluster is not paused.  Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.",
				)
				return true
			}
			log.V(logs.LogVerbose).Info(
				`Cluster did not match expected conditions. \
				Will not attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.`)
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Cluster deleted.  Will attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				`Cluster did not match expected conditions. \
				Will not attempt to reconcile associated (Cluster)Profiles/(Cluster)Set.`)
			return false
		},
	}
}

// MachinePredicates predicates for v1Machine. ClusterProfileReconciler watches v1Machine events
// and react to those by reconciling itself based on following predicates
func MachinePredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newMachine := e.ObjectNew.(*clusterv1.Machine)
			oldMachine := e.ObjectOld.(*clusterv1.Machine)
			log := logger.WithValues("predicate", "updateEvent",
				"namespace", newMachine.Namespace,
				"machine", newMachine.Name,
			)

			if newMachine.Status.GetTypedPhase() != clusterv1.MachinePhaseRunning {
				return false
			}

			if oldMachine == nil {
				log.V(logs.LogVerbose).Info("Old Machine is nil. Reconcile ClusterProfile")
				return true
			}

			// return true if Machine.Status.Phase has changed from not running to running
			if oldMachine.Status.GetTypedPhase() != newMachine.Status.GetTypedPhase() {
				log.V(logs.LogVerbose).Info(
					"Machine was not in Running Phase. Will attempt to reconcile associated ClusterProfiles.")
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"Machine did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			machine := e.Object.(*clusterv1.Machine)
			log := logger.WithValues("predicate", "createEvent",
				"namespace", machine.Namespace,
				"machine", machine.Name,
			)

			// Only need to trigger a reconcile if the Machine.Status.Phase is Running
			if machine.Status.GetTypedPhase() == clusterv1.MachinePhaseRunning {
				return true
			}

			log.V(logs.LogVerbose).Info(
				"Machine did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"machine", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Machine did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"machine", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Machine did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
	}
}

// SetPredicates predicates for ClusterSet/Set. ClusterProfileReconciler watches ClusterSet/Set events
// and react to those by reconciling itself based on following predicates
func SetPredicates(logger logr.Logger) predicate.Funcs {
	getStatus := func(o client.Object) *libsveltosv1alpha1.Status {
		addTypeInformationToObject(getManager().Scheme(), o)
		switch o.GetObjectKind().GroupVersionKind().Kind {
		case libsveltosv1alpha1.ClusterSetKind:
			clusterSet := o.(*libsveltosv1alpha1.ClusterSet)
			return &clusterSet.Status
		case libsveltosv1alpha1.SetKind:
			set := o.(*libsveltosv1alpha1.Set)
			return &set.Status
		}

		return nil
	}

	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newStatus := getStatus(e.ObjectNew)
			oldStatus := getStatus(e.ObjectOld)

			log := logger.WithValues("predicate", "updateEvent",
				"namespace", e.ObjectNew.GetNamespace(),
				"set", e.ObjectNew.GetName(),
			)

			if oldStatus == nil {
				log.V(logs.LogVerbose).Info("Old (Cluster)Set Status is nil. Reconcile ClusterProfiles/Profiles")
				return true
			}

			// if (cluster)set selected clusters have changed, reconcile
			if !reflect.DeepEqual(oldStatus.SelectedClusterRefs, newStatus.SelectedClusterRefs) {
				log.V(logs.LogVerbose).Info(
					"(Cluster)Set selected clusters has changed. Will attempt to reconcile associated ClusterProfiles/Profiles.")
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"(Cluster)Set did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles/Profiles.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"set", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"(Cluster)Set did match expected conditions.  Will attempt to reconcile associated ClusterProfiles/Profiles.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"set", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"(Cluster)Set did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles/Profiles.")
			return false
		},
	}
}
