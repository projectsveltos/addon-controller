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

	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// retriggerAnnotation is the annotation used to manually or automatically
	// force a reconciliation of a ClusterSummary.
	retriggerAnnotation = "projectsveltos.io/retrigger"
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

			if !reflect.DeepEqual(oldConfigMap.Annotations, newConfigMap.Annotations) {
				log.V(logs.LogVerbose).Info(
					"ConfigMap Annotation changed. Will attempt to reconcile associated ClusterSummaries.",
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

			if !reflect.DeepEqual(oldSecret.Annotations, newSecret.Annotations) {
				log.V(logs.LogVerbose).Info(
					"Secret Annotation changed. Will attempt to reconcile associated ClusterSummaries.",
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

type FluxGitRepositoryPredicate struct {
	Logger logr.Logger
}

func (p FluxGitRepositoryPredicate) Create(obj event.TypedCreateEvent[*sourcev1.GitRepository]) bool {
	return fluxCreatePredicate(obj.Object, p.Logger)
}

func (p FluxGitRepositoryPredicate) Update(obj event.TypedUpdateEvent[*sourcev1.GitRepository]) bool {
	return fluxUpdatePredicate(obj.ObjectNew, obj.ObjectOld, p.Logger)
}

func (p FluxGitRepositoryPredicate) Delete(obj event.TypedDeleteEvent[*sourcev1.GitRepository]) bool {
	return fluxDeletePredicate(obj.Object, p.Logger)
}

func (p FluxGitRepositoryPredicate) Generic(obj event.TypedGenericEvent[*sourcev1.GitRepository]) bool {
	return fluxGenericPredicate(obj.Object, p.Logger)
}

type FluxOCIRepositoryPredicate struct {
	Logger logr.Logger
}

func (p FluxOCIRepositoryPredicate) Create(obj event.TypedCreateEvent[*sourcev1b2.OCIRepository]) bool {
	return fluxCreatePredicate(obj.Object, p.Logger)
}

func (p FluxOCIRepositoryPredicate) Update(obj event.TypedUpdateEvent[*sourcev1b2.OCIRepository]) bool {
	return fluxUpdatePredicate(obj.ObjectNew, obj.ObjectOld, p.Logger)
}

func (p FluxOCIRepositoryPredicate) Delete(obj event.TypedDeleteEvent[*sourcev1b2.OCIRepository]) bool {
	return fluxDeletePredicate(obj.Object, p.Logger)
}

func (p FluxOCIRepositoryPredicate) Generic(obj event.TypedGenericEvent[*sourcev1b2.OCIRepository]) bool {
	return fluxGenericPredicate(obj.Object, p.Logger)
}

type FluxBucketPredicate struct {
	Logger logr.Logger
}

func (p FluxBucketPredicate) Create(obj event.TypedCreateEvent[*sourcev1b2.Bucket]) bool {
	return fluxCreatePredicate(obj.Object, p.Logger)
}

func (p FluxBucketPredicate) Update(obj event.TypedUpdateEvent[*sourcev1b2.Bucket]) bool {
	return fluxUpdatePredicate(obj.ObjectNew, obj.ObjectOld, p.Logger)
}

func (p FluxBucketPredicate) Delete(obj event.TypedDeleteEvent[*sourcev1b2.Bucket]) bool {
	return fluxDeletePredicate(obj.Object, p.Logger)
}

func (p FluxBucketPredicate) Generic(obj event.TypedGenericEvent[*sourcev1b2.Bucket]) bool {
	return fluxGenericPredicate(obj.Object, p.Logger)
}

func fluxCreatePredicate(obj client.Object, logger logr.Logger) bool {
	log := logger.WithValues("predicate", "createEvent",
		"namespace", obj.GetNamespace(),
		"source", obj.GetName(),
	)

	log.V(logs.LogVerbose).Info(
		"Source did match expected conditions.  Will attempt to reconcile associated ClusterProfiles.")
	return true
}

func fluxUpdatePredicate(objNew, objOld client.Object, logger logr.Logger) bool {
	log := logger.WithValues("predicate", "updateEvent",
		"namespace", objNew.GetNamespace(),
		"source", objNew.GetName(),
	)

	if hasArtifactChanged(objNew, objOld) {
		log.V(logs.LogVerbose).Info(
			"Source artifact has changed.  Will attempt to reconcile associated ClusterProfiles.")
		return true
	}

	// otherwise, return false
	log.V(logs.LogVerbose).Info(
		"GitRepository did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

func fluxDeletePredicate(obj client.Object, logger logr.Logger) bool {
	log := logger.WithValues("predicate", "deleteEvent",
		"namespace", obj.GetNamespace(),
		"source", obj.GetName(),
	)
	log.V(logs.LogVerbose).Info(
		"Source deleted.  Will attempt to reconcile associated ClusterProfiles.")
	return true
}

func fluxGenericPredicate(obj client.Object, logger logr.Logger) bool {
	log := logger.WithValues("predicate", "genericEvent",
		"namespace", obj.GetNamespace(),
		"source", obj.GetName(),
	)
	log.V(logs.LogVerbose).Info(
		"Source did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

func hasArtifactChanged(objNew, objOld client.Object) bool {
	addTypeInformationToObject(getManagementClusterClient().Scheme(), objNew)
	switch objNew.GetObjectKind().GroupVersionKind().Kind {
	case sourcev1.GitRepositoryKind:
		newGitRepo := objNew.(*sourcev1.GitRepository)
		oldGitRepo := objOld.(*sourcev1.GitRepository)
		if oldGitRepo == nil ||
			!isArtifactSame(oldGitRepo.Status.Artifact, newGitRepo.Status.Artifact) {

			return true
		}
	case sourcev1b2.BucketKind:
		newBucket := objNew.(*sourcev1b2.Bucket)
		oldBucket := objOld.(*sourcev1b2.Bucket)
		if oldBucket == nil ||
			!isArtifactSame(oldBucket.Status.Artifact, newBucket.Status.Artifact) {

			return true
		}
	case sourcev1b2.OCIRepositoryKind:
		newOCIRepo := objNew.(*sourcev1b2.OCIRepository)
		oldOCIRepo := objOld.(*sourcev1b2.OCIRepository)
		if oldOCIRepo == nil ||
			!isArtifactSame(oldOCIRepo.Status.Artifact, newOCIRepo.Status.Artifact) {

			return true
		}
	}

	return false
}

func isArtifactSame(oldArtifact, newArtifact *meta.Artifact) bool {
	if oldArtifact == nil && newArtifact == nil {
		return true
	}

	if oldArtifact == nil && newArtifact != nil {
		return false
	}
	if oldArtifact != nil && newArtifact == nil {
		return false
	}

	return reflect.DeepEqual(oldArtifact.Digest, newArtifact.Digest)
}

// ClusterSummaryStatusPredicate is a custom predicate that filters ClusterSummary s events
type ClusterSummaryPredicate struct {
	Logger logr.Logger
}

func (p ClusterSummaryPredicate) Create(e event.CreateEvent) bool {
	// Always reconcile on creation
	return true
}

func (p ClusterSummaryPredicate) Update(e event.UpdateEvent) bool {
	newClusterSummary := e.ObjectNew.(*configv1beta1.ClusterSummary)
	oldClusterSummary := e.ObjectOld.(*configv1beta1.ClusterSummary)
	log := p.Logger.WithValues("predicate", "updateCLusterSummary",
		"clusterSummary", newClusterSummary.Name,
	)

	if oldClusterSummary == nil {
		log.V(logs.LogVerbose).Info("Old ClusterSummary is nil. Reconcile ClusterSummary.")
		return true
	}

	if !reflect.DeepEqual(oldClusterSummary.DeletionTimestamp, newClusterSummary.DeletionTimestamp) {
		return true
	}

	newAnnot := newClusterSummary.GetAnnotations()
	oldAnnot := oldClusterSummary.GetAnnotations()

	var oldVal, newVal string
	if oldAnnot != nil {
		oldVal = oldAnnot[retriggerAnnotation]
	}
	if newAnnot != nil {
		newVal = newAnnot[retriggerAnnotation]
	}

	if oldVal != newVal {
		log.V(logs.LogVerbose).Info("Retrigger annotation changed. Reconciling ClusterSummary.")
		return true
	}

	if !reflect.DeepEqual(oldClusterSummary.Spec, newClusterSummary.Spec) {
		log.V(logs.LogVerbose).Info(
			"ClusterSummary Spec changed. Will attempt to reconcile ClusterSummary.",
		)
		return true
	}

	oldFS := getFeatureSummaryForFeatureID(oldClusterSummary, libsveltosv1beta1.FeatureHelm)
	newFS := getFeatureSummaryForFeatureID(newClusterSummary, libsveltosv1beta1.FeatureHelm)
	if !isFeatureSummarySame(oldFS, newFS) {
		return true
	}
	oldFS = getFeatureSummaryForFeatureID(oldClusterSummary, libsveltosv1beta1.FeatureKustomize)
	newFS = getFeatureSummaryForFeatureID(newClusterSummary, libsveltosv1beta1.FeatureKustomize)
	if !isFeatureSummarySame(oldFS, newFS) {
		return true
	}
	oldFS = getFeatureSummaryForFeatureID(oldClusterSummary, libsveltosv1beta1.FeatureResources)
	newFS = getFeatureSummaryForFeatureID(newClusterSummary, libsveltosv1beta1.FeatureResources)
	return !isFeatureSummarySame(oldFS, newFS)
}

func (p ClusterSummaryPredicate) Delete(e event.DeleteEvent) bool {
	// Always reconcile on deletion
	return true
}

func (p ClusterSummaryPredicate) Generic(e event.GenericEvent) bool {
	// Ignore generic
	return false
}

func isFeatureSummarySame(oldFS, newFS *configv1beta1.FeatureSummary) bool {
	if oldFS == nil && newFS == nil {
		// Both are nil
		return true
	} else if oldFS != nil && newFS != nil {
		// Both are non-nil, compare hash
		return reflect.DeepEqual(oldFS.Hash, newFS.Hash)
	} else {
		// One is nil and the other is not (out of sync)
		return false
	}
}
