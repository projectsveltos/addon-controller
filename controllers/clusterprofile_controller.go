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
	"sync"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// ClusterProfileReconciler reconciles a ClusterProfile object
type ClusterProfileReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int

	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex
	// key: Sveltos/Cluster; value: set of all ClusterProfiles matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: ClusterProfile; value: set of Sveltos/CAPI Clusters matched
	ClusterProfileMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: ClusterProfile; value ClusterProfile Selector
	ClusterProfiles map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - ClusterProfile is created
	// - Cluster is created with labels matching ClusterProfile
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which ClusterProfile to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	// Reason for the two maps:
	// ClusterProfile, via ClusterSelector, matches Sveltos/CAPI Clusters based on Cluster labels.
	// When a Sveltos/Cluster labels change, one or more ClusterProfile needs to be reconciled.
	// In order to achieve so, ClusterProfile reconciler watches for Sveltos/CAPI Clusters. When a Sveltos/Cluster
	// label changes, find all the ClusterProfiles currently referencing it and reconcile those.
	// Problem is no I/O should be present inside a MapFunc (given a Sveltos/Cluster, return all the ClusterProfiles matching it).
	// In the MapFunc, if the list ClusterProfiles operation failed, we would be unable to retry or re-enqueue the rigth set of
	// ClusterProfiles.
	// Instead the approach taken is following:
	// - when a ClusterProfile is reconciled, update the ClusterProfiles amd the ClusterMap;
	// - in the MapFunc, given the Sveltos/Cluster that changed:
	//		* use ClusterProfiles to find all ClusterProfile matching the Cluster and reconcile those;
	// - in order to reconcile ClusterProfiles previously matching the Cluster and not anymore, use ClusterMap.
	//
	// The ClusterProfileMap is used to update ClusterMap. Consider following scenarios to understand the need:
	// 1. ClusterProfile A references Clusters 1 and 2. When reconciled, ClusterMap will have 1 => A and 2 => A;
	// and ClusterProfileMap A => 1,2
	// 2. Cluster 2 label changes and now ClusterProfile matches Cluster 1 only. We ned to remove the entry 2 => A in ClusterMap. But
	// when we reconcile ClusterProfile we have its current version we don't have its previous version. So we know ClusterProfile A
	// now matches Sveltos/Cluster 1, but we don't know it used to match Sveltos/Cluster 2.
	// So we use ClusterProfileMap (at this point value stored here corresponds to reconciliation #1. We know currently
	// ClusterProfile matches Sveltos/Cluster 1 only and looking at ClusterProfileMap we know it used to reference
	// Svetos/CAPI Cluster 1 and 2.
	// So we can remove 2 => A from ClusterMap. Only after this update, we update ClusterProfileMap (so new value will be A => 1)
}

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterprofiles,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterprofiles/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterprofiles/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;update;create;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterreports,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterconfigurations,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters/status,verbs=get;watch;list

func (r *ClusterProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

	// Fecth the ClusterProfile instance
	clusterProfile := &configv1alpha1.ClusterProfile{}
	if err := r.Get(ctx, req.NamespacedName, clusterProfile); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ClusterProfile")
		return reconcile.Result{}, errors.Wrapf(err,
			"Failed to fetch ClusterProfile %s", req.NamespacedName)
	}

	profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
		Client:         r.Client,
		Logger:         logger,
		Profile:        clusterProfile,
		ControllerName: "clusterprofile",
	})
	if err != nil {
		logger.Error(err, "Failed to create profileScope")
		return reconcile.Result{}, errors.Wrapf(err,
			"unable to create profileScope for %s", req.NamespacedName)
	}

	// Always close the scope when exiting this function so we can persist any ClusterProfile
	// changes.
	defer func() {
		if err := profileScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted clusterProfile
	if !clusterProfile.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, profileScope), nil
	}

	// Handle non-deleted clusterProfile
	return r.reconcileNormal(ctx, profileScope), nil
}

func (r *ClusterProfileReconciler) reconcileDelete(
	ctx context.Context,
	profileScope *scope.ProfileScope) reconcile.Result {

	logger := profileScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling ClusterProfile delete")

	if err := reconcileDeleteCommon(ctx, r.Client, profileScope,
		configv1alpha1.ClusterProfileFinalizer, logger); err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}
	}

	r.cleanMaps(profileScope)

	logger.V(logs.LogInfo).Info("Reconcile delete success")
	return reconcile.Result{}
}

func (r *ClusterProfileReconciler) reconcileNormal(
	ctx context.Context,
	profileScope *scope.ProfileScope) reconcile.Result {

	logger := profileScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling ClusterProfile")

	if !controllerutil.ContainsFinalizer(&configv1alpha1.ClusterSummary{}, configv1alpha1.ClusterProfileFinalizer) {
		if err := addFinalizer(ctx, profileScope, configv1alpha1.ClusterProfileFinalizer); err != nil {
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
		}
	}

	matchingCluster, err := getMatchingClusters(ctx, r.Client, "", profileScope, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	profileScope.SetMatchingClusterRefs(matchingCluster)

	r.updateMaps(profileScope)

	if err := reconcileNormalCommon(ctx, r.Client, profileScope, logger); err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	logger.V(logs.LogInfo).Info("Reconcile success")
	return reconcile.Result{}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterProfileReconciler) SetupWithManager(mgr ctrl.Manager) (controller.Controller, error) {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterProfile{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// At this point we don't know yet whether CAPI is present in the cluster.
	// Later on, in main, we detect that and if CAPI is present WatchForCAPI will be invoked.

	// When projectsveltos cluster changes, according to SveltosClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.SveltosCluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)

	return c, err
}

func (r *ClusterProfileReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Cluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}
	// When cluster-api machine changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Machine{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForMachine),
		MachinePredicates(mgr.GetLogger().WithValues("predicate", "machinepredicate")),
	); err != nil {
		return err
	}

	return nil
}

func (r *ClusterProfileReconciler) cleanMaps(profileScope *scope.ProfileScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterProfileInfo := getKeyFromObject(r.Scheme, profileScope.Profile)

	delete(r.ClusterProfileMap, *clusterProfileInfo)
	delete(r.ClusterProfiles, *clusterProfileInfo)

	for i := range r.ClusterMap {
		clusterProfileSet := r.ClusterMap[i]
		clusterProfileSet.Erase(clusterProfileInfo)
	}
}

func (r *ClusterProfileReconciler) updateMaps(profileScope *scope.ProfileScope) {
	currentClusters := getCurrentClusterSet(profileScope)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterProfileInfo := getKeyFromObject(r.Scheme, profileScope.Profile)

	// Get list of Clusters not matched anymore by ClusterProfile
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.ClusterProfileMap[*clusterProfileInfo]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add ClusterProfile as consumer
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		getClusterMapForEntry(r.ClusterMap, clusterInfo).Insert(clusterProfileInfo)
	}

	// For each Cluster not matched anymore, remove ClusterProfile as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		getClusterMapForEntry(r.ClusterMap, &clusterName).Erase(clusterProfileInfo)
	}

	r.ClusterProfileMap[*clusterProfileInfo] = currentClusters
	r.ClusterProfiles[*clusterProfileInfo] = profileScope.GetSpec().ClusterSelector
}
