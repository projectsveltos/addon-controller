/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// SveltosClusterReconciler reconciles a SveltosCluster object
type SveltosClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;list;watch

func (r *SveltosClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	sveltosClusterManagerInstance := GetSveltosClusterManager()

	sveltosCluster := &libsveltosv1beta1.SveltosCluster{}
	if err := r.Get(ctx, req.NamespacedName, sveltosCluster); err != nil {
		if apierrors.IsNotFound(err) {
			sveltosClusterManagerInstance.RemoveCluster(req.Namespace, req.Name)
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch SveltosCluster")
		return reconcile.Result{}, fmt.Errorf("failed to fetch SveltosCluster %s: %w", req.NamespacedName, err)
	}

	if !sveltosCluster.DeletionTimestamp.IsZero() || !sveltosCluster.Spec.PullMode {
		sveltosClusterManagerInstance.RemoveCluster(req.Namespace, req.Name)
	}

	sveltosClusterManagerInstance.AddCluster(sveltosCluster)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SveltosClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&libsveltosv1beta1.SveltosCluster{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		Named("sveltoscluster").
		Complete(r)
}

// SveltosClusterManager keeps track of managed SveltosCluster in pull-mode.
// The order might change if addon controller is restarted. But if more than X clusters
// are in pull mode without a license, it is an invalid configuration anyway.
type SveltosClusterManager struct {
	mu       sync.RWMutex // Read-write mutex to protect access to the clusters slice
	clusters []types.NamespacedName
}

var (
	sveltosClusterManagerInstance *SveltosClusterManager
)

// NewSveltosClusterManager creates and returns a new initialized SveltosClusterManager.
func NewSveltosClusterManager() *SveltosClusterManager {
	sveltosClusterManagerInstance = &SveltosClusterManager{
		clusters: make([]types.NamespacedName, 0), // Initialize an empty slice
	}

	return sveltosClusterManagerInstance
}

func GetSveltosClusterManager() *SveltosClusterManager {
	return sveltosClusterManagerInstance
}

// AddCluster adds a SveltosCluster to the manager's collection.
// It ensures that the cluster is not added if it already exists (based on Name and Namespace).
// This method is thread-safe.
func (m *SveltosClusterManager) AddCluster(cluster *libsveltosv1beta1.SveltosCluster) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if the cluster already exists to avoid duplicates
	for i := range m.clusters {
		if m.clusters[i].Name == cluster.Name && m.clusters[i].Namespace == cluster.Namespace {
			// Cluster already tracked
			return
		}
	}

	m.clusters = append(m.clusters, types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name})
}

// RemoveCluster removes a SveltosCluster from the manager's collection
// identified by its name and namespace.
// This method is thread-safe.
func (m *SveltosClusterManager) RemoveCluster(namespace, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.clusters {
		if m.clusters[i].Name == name && m.clusters[i].Namespace == namespace {
			if i == len(m.clusters)-1 {
				m.clusters = m.clusters[:i]
			} else {
				m.clusters = append(m.clusters[:i], m.clusters[i+1:]...)
			}
			break
		}
	}
}

// IsInTopX checks if a SveltosCluster, identified by its name and namespace,
// is among the first 'x' registered clusters in the manager's collection.
// This method is thread-safe.
func (m *SveltosClusterManager) IsInTopX(namespace, name string, x int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	limit := x
	if limit > len(m.clusters) {
		return true
	}

	// Iterate through the first 'limit' clusters
	for i := 0; i < limit; i++ {
		if m.clusters[i].Name == name && m.clusters[i].Namespace == namespace {
			return true
		}
	}

	return false
}
