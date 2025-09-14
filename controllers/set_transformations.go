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
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

func (r *SetReconciler) requeueSetForSveltosCluster(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	cluster := o

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, cluster)

	return requeueForCluster(cluster, r.Sets, r.ClusterLabels, r.ClusterMap, libsveltosv1beta1.SetKind, r.Logger)
}

func (r *SetReconciler) requeueSetForCluster(
	ctx context.Context, cluster *clusterv1.Cluster,
) []reconcile.Request {

	r.Mux.Lock()
	defer r.Mux.Unlock()

	addTypeInformationToObject(r.Scheme, cluster)

	return requeueForCluster(cluster, r.Sets, r.ClusterLabels, r.ClusterMap, libsveltosv1beta1.SetKind, r.Logger)
}
