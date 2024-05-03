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
	"sync"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// ClusterSetReconciler reconciles a ClusterSet object
type ClusterSetReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	Logger               logr.Logger

	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex
	// key: Sveltos/Cluster; value: set of all ClusterSets matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: ClusterSet; value: set of Sveltos/CAPI Clusters matched
	ClusterSetMap map[corev1.ObjectReference]*libsveltosset.Set

	// key: ClusterSets; value ClusterSet Selector
	ClusterSets map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - ClusterSet is created
	// - Cluster is created with labels matching ClusterSet
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which ClusterSet to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	ctrl controller.Controller
}

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=clustersets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=clustersets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=clustersets/finalizers,verbs=update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters/status,verbs=get;watch;list

func (r *ClusterSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")
	// Fecth the ClusterSet instance
	clusterSet := &libsveltosv1alpha1.ClusterSet{}
	if err := r.Get(ctx, req.NamespacedName, clusterSet); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ClusterSet")
		return reconcile.Result{}, errors.Wrapf(err,
			"Failed to fetch ClusterSet %s", req.NamespacedName)
	}

	setScope, err := scope.NewSetScope(scope.SetScopeParams{
		Client:         r.Client,
		Logger:         logger,
		Set:            clusterSet,
		ControllerName: "clusterset",
	})
	if err != nil {
		logger.Error(err, "Failed to create setScope")
		return reconcile.Result{}, errors.Wrapf(err,
			"unable to create setScope for %s", req.NamespacedName)
	}

	// Always close the scope when exiting this function so we can persist any ClusterSet
	// changes.
	defer func() {
		if err := setScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted clusterSet
	if !clusterSet.DeletionTimestamp.IsZero() {
		r.reconcileDelete(setScope)
		return reconcile.Result{}, nil
	}
	// Handle non-deleted clusterSet
	return r.reconcileNormal(ctx, setScope), nil
}

func (r *ClusterSetReconciler) reconcileDelete(setScope *scope.SetScope) {
	logger := setScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling Set delete")

	r.cleanMaps(setScope)

	logger.V(logs.LogInfo).Info("Reconcile delete success")
}

func (r *ClusterSetReconciler) reconcileNormal(
	ctx context.Context,
	setScope *scope.SetScope) reconcile.Result {

	logger := setScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling Set")

	matchingCluster, err := getMatchingClusters(ctx, r.Client, "", setScope.GetSelector(),
		setScope.GetSpec().ClusterRefs, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	setScope.SetMatchingClusterRefs(matchingCluster)

	selectClusters(setScope)

	r.updateMaps(setScope)

	logger.V(logs.LogInfo).Info("Reconcile success")
	return reconcile.Result{}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&libsveltosv1alpha1.ClusterSet{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Watches(&libsveltosv1alpha1.SveltosCluster{},
			handler.EnqueueRequestsFromMapFunc(r.requeueClusterSetForSveltosCluster),
			builder.WithPredicates(
				SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
			),
		).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "error creating controller")
	}

	// At this point we don't know yet whether CAPI is present in the cluster.
	// Later on, in main, we detect that and if CAPI is present WatchForCAPI will be invoked.

	r.ctrl = c

	return err
}

func (r *ClusterSetReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	sourceCluster := source.Kind[*clusterv1.Cluster](
		mgr.GetCache(),
		&clusterv1.Cluster{},
		handler.TypedEnqueueRequestsFromMapFunc(r.requeueClusterSetForCluster),
		ClusterPredicate{Logger: mgr.GetLogger().WithValues("predicate", "clusterpredicate")},
	)

	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(sourceCluster); err != nil {
		return err
	}

	return nil
}

func (r *ClusterSetReconciler) cleanMaps(setScope *scope.SetScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterSetInfo := getKeyFromObject(r.Scheme, setScope.Set)

	delete(r.ClusterSetMap, *clusterSetInfo)
	delete(r.ClusterSets, *clusterSetInfo)

	for i := range r.ClusterMap {
		set := r.ClusterMap[i]
		set.Erase(clusterSetInfo)
	}
}

func (r *ClusterSetReconciler) updateMaps(setScope *scope.SetScope) {
	currentClusters := getCurrentClusterSet(setScope.GetStatus().MatchingClusterRefs)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterSetInfo := getKeyFromObject(r.Scheme, setScope.Set)

	// Get list of Clusters not matched anymore by ClusterSet
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.ClusterSetMap[*clusterSetInfo]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add ClusterSet as consumer
	for i := range setScope.GetStatus().MatchingClusterRefs {
		cluster := setScope.GetStatus().MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		getConsumersForEntry(r.ClusterMap, clusterInfo).Insert(clusterSetInfo)
	}

	// For each Cluster not matched anymore, remove ClusterSet as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		getConsumersForEntry(r.ClusterMap, &clusterName).Erase(clusterSetInfo)
	}

	r.ClusterSetMap[*clusterSetInfo] = currentClusters
	r.ClusterSets[*clusterSetInfo] = setScope.GetSpec().ClusterSelector
}

func (r *ClusterSetReconciler) GetController() controller.Controller {
	return r.ctrl
}
