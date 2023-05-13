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

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
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
				log.V(logs.LogVerbose).Info("Old Cluster is nil. Reconcile ClusterProfile")
				return true
			}

			// return true if Cluster.Spec.Paused has changed from true to false
			if oldCluster.Spec.Paused && !newCluster.Spec.Paused {
				log.V(logs.LogVerbose).Info(
					"Cluster was unpaused. Will attempt to reconcile associated ClusterProfiles.")
				return true
			}

			if !reflect.DeepEqual(oldCluster.Labels, newCluster.Labels) {
				log.V(logs.LogVerbose).Info(
					"Cluster labels changed. Will attempt to reconcile associated ClusterProfiles.",
				)
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
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
					"Cluster is not paused.  Will attempt to reconcile associated ClusterProfiles.",
				)
				return true
			}
			log.V(logs.LogVerbose).Info(
				"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Cluster deleted.  Will attempt to reconcile associated ClusterProfiles.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
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
				log.V(logs.LogVerbose).Info("Old Cluster is nil. Reconcile ClusterProfile")
				return true
			}

			// return true if Cluster.Spec.Paused has changed from true to false
			if oldCluster.Spec.Paused && !newCluster.Spec.Paused {
				log.V(logs.LogVerbose).Info(
					"Cluster was unpaused. Will attempt to reconcile associated ClusterProfiles.")
				return true
			}

			if !oldCluster.Status.Ready && newCluster.Status.Ready {
				log.V(logs.LogVerbose).Info(
					"Cluster was not ready. Will attempt to reconcile associated ClusterProfiles.")
				return true
			}

			if !reflect.DeepEqual(oldCluster.Labels, newCluster.Labels) {
				log.V(logs.LogVerbose).Info(
					"Cluster labels changed. Will attempt to reconcile associated ClusterProfiles.",
				)
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
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
					"Cluster is not paused.  Will attempt to reconcile associated ClusterProfiles.",
				)
				return true
			}
			log.V(logs.LogVerbose).Info(
				"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Cluster deleted.  Will attempt to reconcile associated ClusterProfiles.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"cluster", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
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

// FluxSourcePredicates predicates for GitRepository/OCIRepository/Bucket.
// ClusterProfileReconciler watches GitRepository/OCIRepository/Bucket events and
// react to those by reconciling itself based on following predicates
func FluxSourcePredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			log := logger.WithValues("predicate", "updateEvent",
				"namespace", e.ObjectNew.GetNamespace(),
				"source", e.ObjectNew.GetName(),
			)

			if hasArtifactChanged(e) {
				log.V(logs.LogInfo).Info(
					"Source artifact has changed.  Will attempt to reconcile associated ClusterProfiles.")
				return true
			}

			// otherwise, return false
			log.V(logs.LogInfo).Info(
				"GitRepository did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			log := logger.WithValues("predicate", "createEvent",
				"namespace", e.Object.GetNamespace(),
				"source", e.Object.GetName(),
			)

			log.V(logs.LogVerbose).Info(
				"Source did match expected conditions.  Will attempt to reconcile associated ClusterProfiles.")
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log := logger.WithValues("predicate", "deleteEvent",
				"namespace", e.Object.GetNamespace(),
				"source", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Source deleted.  Will attempt to reconcile associated ClusterProfiles.")
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			log := logger.WithValues("predicate", "genericEvent",
				"namespace", e.Object.GetNamespace(),
				"source", e.Object.GetName(),
			)
			log.V(logs.LogVerbose).Info(
				"Source did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
			return false
		},
	}
}

func hasArtifactChanged(e event.UpdateEvent) bool {
	switch e.ObjectNew.GetObjectKind().GroupVersionKind().Kind {
	case sourcev1.GitRepositoryKind:
		newGitRepo := e.ObjectNew.(*sourcev1.GitRepository)
		oldGitRepo := e.ObjectOld.(*sourcev1.GitRepository)
		if oldGitRepo == nil ||
			!isArtifactSame(oldGitRepo.Status.Artifact, newGitRepo.Status.Artifact) {

			return true
		}
	case sourcev1b2.BucketKind:
		newBucket := e.ObjectNew.(*sourcev1b2.Bucket)
		oldBucket := e.ObjectOld.(*sourcev1b2.Bucket)
		if oldBucket == nil ||
			!isArtifactSame(oldBucket.Status.Artifact, newBucket.Status.Artifact) {

			return true
		}
	case sourcev1b2.OCIRepositoryKind:
		newOCIRepo := e.ObjectNew.(*sourcev1b2.OCIRepository)
		oldOCIRepo := e.ObjectOld.(*sourcev1b2.OCIRepository)
		if oldOCIRepo == nil ||
			!isArtifactSame(oldOCIRepo.Status.Artifact, newOCIRepo.Status.Artifact) {

			return true
		}
	}

	return false
}

func isArtifactSame(oldArtifact, newArtifact *sourcev1.Artifact) bool {
	if oldArtifact == nil && newArtifact != nil {
		return false
	}
	if oldArtifact != nil && newArtifact == nil {
		return false
	}
	return reflect.DeepEqual(oldArtifact, newArtifact)
}
