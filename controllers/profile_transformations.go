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
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *ProfileReconciler) requeueProfileForCluster(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	cluster := o

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, cluster)

	return requeueForCluster(cluster, r.Profiles, r.ClusterLabels, r.ClusterMap)
}

func (r *ProfileReconciler) requeueProfileForMachine(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	machine := o.(*clusterv1.Machine)

	addTypeInformationToObject(r.Scheme, machine)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	return requeueClusterProfileForMachine(machine, r.Profiles, r.ClusterLabels, r.ClusterMap)
}
