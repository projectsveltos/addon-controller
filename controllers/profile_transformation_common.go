/*
Copyright 2023. projectsveltos.io. All rights reserved.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func requeueForCluster(cluster client.Object,
	profileSelectors map[corev1.ObjectReference]libsveltosv1beta1.Selector,
	clusterLabels map[corev1.ObjectReference]map[string]string,
	clusterMap map[corev1.ObjectReference]*libsveltosset.Set,
	kindType string, logger logr.Logger) []reconcile.Request {

	logger = logger.WithValues("cluster", fmt.Sprintf("%s/%s", cluster.GetNamespace(), cluster.GetName()))
	logger.V(logs.LogVerbose).Info("reacting to Cluster change")

	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
	clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
		Namespace: cluster.GetNamespace(), Name: cluster.GetName()}

	profileCurrentlyMatching := getConsumersForEntry(clusterMap, &clusterInfo)

	clusterLabels[clusterInfo] = cluster.GetLabels()

	// Get all (Cluster)Profiles previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, profileCurrentlyMatching.Len())
	consumers := profileCurrentlyMatching.Items()

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

	// Iterate over all current (Cluster)Profiles and reconcile the (Cluster)Profiles
	// now matching the Cluster
	for k := range profileSelectors {
		profileSelector := profileSelectors[k]

		clusterSelector, err := metav1.LabelSelectorAsSelector(&profileSelector.LabelSelector)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to convert selector %v", err))
			continue
		}

		if clusterSelector.Matches(labels.Set(cluster.GetLabels())) {
			l := logger.WithValues(kindType, k.Name)
			l.V(logs.LogDebug).Info(fmt.Sprintf("queuing %s", kindType))
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      k.Name,
					Namespace: k.Namespace,
				},
			})
		}
	}

	return requests
}

func requeueForMachine(machine client.Object,
	profileSelectors map[corev1.ObjectReference]libsveltosv1beta1.Selector,
	clusterLabels map[corev1.ObjectReference]map[string]string,
	clusterMap map[corev1.ObjectReference]*libsveltosset.Set,
	kind string, logger logr.Logger) []reconcile.Request {

	logger = logger.WithValues("machine", fmt.Sprintf("%s/%s", machine.GetNamespace(), machine.GetName()))

	logger.V(logs.LogVerbose).Info("reacting to CAPI Machine change")

	machineLabels := machine.GetLabels()
	if machineLabels == nil {
		return nil
	}

	clusterNameLabel, ok := machineLabels[clusterv1.ClusterNameLabel]
	if !ok {
		logger.V(logs.LogVerbose).Info("Machine has not ClusterNameLabel")
		return nil
	}

	clusterInfo := corev1.ObjectReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       clusterKind,
		Namespace:  machine.GetNamespace(),
		Name:       clusterNameLabel}

	// Get all ClusterProfile previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, getConsumersForEntry(clusterMap, &clusterInfo).Len())
	consumers := getConsumersForEntry(clusterMap, &clusterInfo).Items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Namespace: consumers[i].Namespace,
				Name:      consumers[i].Name,
			},
		}
	}

	// Get Cluster labels
	if clusterLabels, ok := clusterLabels[clusterInfo]; ok {
		// Iterate over all current ClusterProfile and reconcile the ClusterProfile now
		// matching the Cluster
		for k := range profileSelectors {
			profileSelector := profileSelectors[k]

			clusterSelector, err := metav1.LabelSelectorAsSelector(&profileSelector.LabelSelector)
			if err != nil {
				logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to convert selector %v", err))
				continue
			}

			if clusterSelector.Matches(labels.Set(clusterLabels)) {
				l := logger.WithValues(kind, k.Name)
				l.V(logs.LogDebug).Info(fmt.Sprintf("queuing %s", kind))
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKey{
						Name:      k.Name,
						Namespace: k.Namespace,
					},
				})
			}
		}
	}

	return requests
}
