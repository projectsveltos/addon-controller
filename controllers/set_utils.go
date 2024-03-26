/*
Copyright 2024. projectsveltos.io. All rights reserved.

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

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"

	"github.com/projectsveltos/addon-controller/pkg/scope"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func selectClusters(setScope *scope.SetScope) {
	status := setScope.GetStatus()
	spec := setScope.GetSpec()

	// Verify all currently selected are still ready
	// Status.MatchingClusterRef only contains ready cluster
	currentMatchingCluster := make(map[corev1.ObjectReference]bool)
	for i := range status.MatchingClusterRefs {
		currentMatchingCluster[status.MatchingClusterRefs[i]] = true
	}

	currentSelectedClusters := make([]corev1.ObjectReference, 0)
	for i := range status.SelectedClusterRefs {
		cluster := &status.SelectedClusterRefs[i]
		if _, ok := currentMatchingCluster[*cluster]; ok {
			currentSelectedClusters = append(currentSelectedClusters, *cluster)
		}
	}

	// This removes from SelectedClusterRefs any cluster previously selected
	// which is either not a match anymore or does not exist anymore
	status.SelectedClusterRefs = currentSelectedClusters

	if len(currentSelectedClusters) == spec.MaxReplicas {
		// Number of selected cluster matches the MaxReplicas, so there
		// is nothing else to do
		return
	}

	if spec.MaxReplicas == 0 {
		status.SelectedClusterRefs = nil
	} else if len(currentSelectedClusters) > spec.MaxReplicas {
		// drop clusters
		status.SelectedClusterRefs = currentSelectedClusters[:spec.MaxReplicas-1]
	} else if len(currentSelectedClusters) < spec.MaxReplicas {
		// select more clusters
		selectMoreClusters(setScope)
	}
}

func selectMoreClusters(setScope *scope.SetScope) {
	status := setScope.GetStatus()
	spec := setScope.GetSpec()

	if status.SelectedClusterRefs != nil &&
		len(status.SelectedClusterRefs) >= spec.MaxReplicas {

		return
	}

	currentSelectedCluster := make(map[corev1.ObjectReference]bool)
	for i := range status.SelectedClusterRefs {
		currentSelectedCluster[status.SelectedClusterRefs[i]] = true
	}

	for i := range status.MatchingClusterRefs {
		cluster := &status.MatchingClusterRefs[i]
		if _, ok := currentSelectedCluster[*cluster]; !ok {
			status.SelectedClusterRefs = append(status.SelectedClusterRefs, *cluster)
			if len(status.SelectedClusterRefs) == spec.MaxReplicas {
				return
			}
		}
	}
}

func requeueForSet(set client.Object,
	setMap map[corev1.ObjectReference]*libsveltosset.Set,
	kindType string, logger logr.Logger) []reconcile.Request {

	logger = logger.WithValues("set", fmt.Sprintf("%s/%s", set.GetNamespace(), set.GetName()))
	logger.V(logs.LogDebug).Info("reacting to (Cluster)Set change")

	apiVersion, kind := set.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
	setInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
		Namespace: set.GetNamespace(), Name: set.GetName()}

	// Get list of (Cluster)Profiles currently referencing the (Cluster)Set
	currentConsumers := getConsumersForEntry(setMap, &setInfo)

	// Get all (Cluster)Profiles previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, currentConsumers.Len())
	consumers := currentConsumers.Items()

	for i := range consumers {
		l := logger.WithValues(kindType, consumers[i].Name)
		l.V(logs.LogDebug).Info(fmt.Sprintf("queuing %s", kindType))
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Namespace: consumers[i].Namespace,
				Name:      consumers[i].Name,
			},
		}
	}

	return requests
}
