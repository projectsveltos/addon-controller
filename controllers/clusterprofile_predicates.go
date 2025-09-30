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
	"bytes"
	"reflect"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// SetPredicates predicates for ClusterSet/Set. ClusterProfileReconciler watches ClusterSet/Set events
// and react to those by reconciling itself based on following predicates
func SetPredicates(logger logr.Logger) predicate.Funcs {
	getStatus := func(o client.Object) *libsveltosv1beta1.Status {
		addTypeInformationToObject(getManager().Scheme(), o)
		switch o.GetObjectKind().GroupVersionKind().Kind {
		case libsveltosv1beta1.ClusterSetKind:
			clusterSet := o.(*libsveltosv1beta1.ClusterSet)
			return &clusterSet.Status
		case libsveltosv1beta1.SetKind:
			set := o.(*libsveltosv1beta1.Set)
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

// DependenciesHashChangedPredicate implements a default update predicate function
// that triggers reconciliation only when the DependenciesHash in the Status changes.
type DependenciesHashChangedPredicate struct {
	predicate.Funcs
}

// getDependenciesHash safely extracts the hash from either a ClusterProfile or a Profile.
func getDependenciesHash(obj client.Object) []byte {
	switch o := obj.(type) {
	case *configv1beta1.ClusterProfile:
		return o.Status.DependenciesHash
	case *configv1beta1.Profile:
		return o.Status.DependenciesHash
	default:
		return nil
	}
}

// Update implements default UpdateEvent filter for validating DependenciesHash change.
func (DependenciesHashChangedPredicate) Update(e event.UpdateEvent) bool {
	oldHash := getDependenciesHash(e.ObjectOld)
	newHash := getDependenciesHash(e.ObjectNew)

	changed := !bytes.Equal(oldHash, newHash)

	return changed
}
