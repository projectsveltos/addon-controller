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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func requeueForCluster(cluster client.Object,
	profileSelectors map[corev1.ObjectReference]libsveltosv1alpha1.Selector,
	clusterLabels map[corev1.ObjectReference]map[string]string,
	clusterMap map[corev1.ObjectReference]*libsveltosset.Set) []reconcile.Request {

	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(logs.LogInfo))).WithValues(
		"cluster", fmt.Sprintf("%s/%s", cluster.GetNamespace(), cluster.GetName()))
	logger.V(logs.LogDebug).Info("reacting to Cluster change")

	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
	clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
		Namespace: cluster.GetNamespace(), Name: cluster.GetName()}

	clusterCurrentlyMatching := getClusterMapForEntry(clusterMap, &clusterInfo)

	clusterLabels[clusterInfo] = cluster.GetLabels()

	// Get all (Cluster)Profiles previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, clusterCurrentlyMatching.Len())
	consumers := clusterCurrentlyMatching.Items()

	for i := range consumers {
		l := logger.WithValues("profile", consumers[i].Name)
		l.V(logs.LogDebug).Info("queuing profile")
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Iterate over all current (Cluster)Profiles and reconcile the (Cluster)Profiles
	// now matching the Cluster
	for k := range profileSelectors {
		profileSelector := profileSelectors[k]
		parsedSelector, _ := labels.Parse(string(profileSelector))
		if parsedSelector.Matches(labels.Set(cluster.GetLabels())) {
			l := logger.WithValues("profile", k.Name)
			l.V(logs.LogDebug).Info("queuing profile")
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
	profileSelectors map[corev1.ObjectReference]libsveltosv1alpha1.Selector,
	clusterLabels map[corev1.ObjectReference]map[string]string,
	clusterMap map[corev1.ObjectReference]*libsveltosset.Set) []reconcile.Request {

	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(logs.LogInfo))).WithValues(
		"machine", fmt.Sprintf("%s/%s", machine.GetNamespace(), machine.GetName()))

	logger.V(logs.LogDebug).Info("reacting to CAPI Machine change")

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
		Kind:       "Cluster",
		Namespace:  machine.GetNamespace(),
		Name:       clusterNameLabel}

	// Get all ClusterProfile previously matching this cluster and reconcile those
	requests := make([]ctrl.Request, getClusterMapForEntry(clusterMap, &clusterInfo).Len())
	consumers := getClusterMapForEntry(clusterMap, &clusterInfo).Items()

	for i := range consumers {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name: consumers[i].Name,
			},
		}
	}

	// Get Cluster labels
	if clusterLabels, ok := clusterLabels[clusterInfo]; ok {
		// Iterate over all current ClusterProfile and reconcile the ClusterProfile now
		// matching the Cluster
		for k := range profileSelectors {
			profileSelector := profileSelectors[k]
			parsedSelector, _ := labels.Parse(string(profileSelector))
			if parsedSelector.Matches(labels.Set(clusterLabels)) {
				l := logger.WithValues("profile", k.Name)
				l.V(logs.LogDebug).Info("queuing profile")
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
