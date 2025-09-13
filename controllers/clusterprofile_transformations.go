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
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

func (r *ClusterProfileReconciler) requeueClusterProfileForClusterSet(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	clusterSet := o

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, clusterSet)

	return requeueForSet(clusterSet, r.ClusterSetMap, configv1beta1.ClusterProfileKind, r.Logger)
}

func (r *ClusterProfileReconciler) requeueClusterProfileForSveltosCluster(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, o)

	return requeueForCluster(o, r.ClusterProfiles, r.ClusterLabels, r.ClusterMap, configv1beta1.ClusterProfileKind, r.Logger)
}

func (r *ClusterProfileReconciler) requeueClusterProfileForCluster(
	ctx context.Context, cluster *clusterv1.Cluster,
) []reconcile.Request {

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, cluster)

	return requeueForCluster(cluster, r.ClusterProfiles, r.ClusterLabels, r.ClusterMap, configv1beta1.ClusterProfileKind, r.Logger)
}

func (r *ClusterProfileReconciler) requeueClusterProfileForMachine(
	ctx context.Context, machine *clusterv1.Machine,
) []reconcile.Request {

	addTypeInformationToObject(r.Scheme, machine)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	return requeueForMachine(machine, r.ClusterProfiles, r.ClusterLabels, r.ClusterMap, configv1beta1.ClusterProfileKind, r.Logger)
}
