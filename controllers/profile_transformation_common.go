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
