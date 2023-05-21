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
	"reflect"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

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
				log.V(logs.LogVerbose).Info("Old ConfigMap is nil. Reconcile ClusterSummaries.")
				return true
			}

			if !reflect.DeepEqual(oldConfigMap.Data, newConfigMap.Data) {
				log.V(logs.LogVerbose).Info(
					"ConfigMap Data changed. Will attempt to reconcile associated ClusterSummaries.",
				)
				return true
			}

			if !reflect.DeepEqual(oldConfigMap.BinaryData, newConfigMap.BinaryData) {
				log.V(logs.LogVerbose).Info(
					"ConfigMap BinaryData changed. Will attempt to reconcile associated ClusterSummaries.",
				)
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"ConfigMap did not match expected conditions.  Will not attempt to reconcile associated ClusterSummaries.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return CreateFuncTrue(e, logger)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return DeleteFuncTrue(e, logger)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return GenericFuncFalse(e, logger)
		},
	}
}

// SecretPredicates predicates for Secrets. ClusterSummaryReconciler watches Secret events
// and react to those by reconciling itself based on following predicates
func SecretPredicates(logger logr.Logger) predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			newSecret := e.ObjectNew.(*corev1.Secret)
			oldSecret := e.ObjectOld.(*corev1.Secret)
			log := logger.WithValues("predicate", "updateEvent",
				"secret", newSecret.Name,
			)

			if oldSecret == nil {
				log.V(logs.LogVerbose).Info("Old Secret is nil. Reconcile ClusterSummaries.")
				return true
			}

			if !reflect.DeepEqual(oldSecret.Data, newSecret.Data) {
				log.V(logs.LogVerbose).Info(
					"Secret Data changed. Will attempt to reconcile associated ClusterSummaries.",
				)
				return true
			}

			// otherwise, return false
			log.V(logs.LogVerbose).Info(
				"Secret did not match expected conditions.  Will not attempt to reconcile associated ClusterSummaries.")
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return CreateFuncTrue(e, logger)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return DeleteFuncTrue(e, logger)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return GenericFuncFalse(e, logger)
		},
	}
}

var (
	CreateFuncTrue = func(e event.CreateEvent, logger logr.Logger) bool {
		log := logger.WithValues("predicate", "createEvent",
			e.Object.GetObjectKind(), e.Object.GetName(),
		)

		log.V(logs.LogVerbose).Info(fmt.Sprintf(
			"%s did match expected conditions.  Will attempt to reconcile associated ClusterSummaries.",
			e.Object.GetObjectKind()))
		return true
	}

	DeleteFuncTrue = func(e event.DeleteEvent, logger logr.Logger) bool {
		log := logger.WithValues("predicate", "deleteEvent",
			e.Object.GetObjectKind(), e.Object.GetName(),
		)
		log.V(logs.LogVerbose).Info(fmt.Sprintf(
			"%s did match expected conditions.  Will attempt to reconcile associated ClusterSummaries.",
			e.Object.GetObjectKind()))
		return true
	}

	GenericFuncFalse = func(e event.GenericEvent, logger logr.Logger) bool {
		log := logger.WithValues("predicate", "genericEvent",
			e.Object.GetObjectKind(), e.Object.GetName(),
		)
		log.V(logs.LogVerbose).Info(fmt.Sprintf(
			"%s did not match expected conditions.  Will not attempt to reconcile associated ClusterSummaries.",
			e.Object.GetObjectKind()))
		return false
	}
)

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
