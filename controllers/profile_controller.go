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
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// ProfileReconciler reconciles a Profile object
type ProfileReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	Logger               logr.Logger

	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex

	// key: Set; value: set of Profiles currently referencing the Set
	SetMap map[corev1.ObjectReference]*libsveltosset.Set

	// key: Sveltos/Cluster; value: set of all Profiles matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: Profile; value: set of Sveltos/CAPI Clusters matched
	ProfileMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: Profile; value Profile Selector
	Profiles map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - Profile is created
	// - Cluster is created with labels matching Profile
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which Profile to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	// Reason for the two maps:
	// Profile, via ClusterSelector, matches Sveltos/CAPI Clusters based on Cluster labels.
	// When a Sveltos/Cluster labels change, one or more Profile needs to be reconciled.
	// In order to achieve so, Profile reconciler watches for Sveltos/CAPI Clusters. When a Sveltos/Cluster
	// label changes, find all the Profiles currently referencing it and reconcile those.
	// Problem is no I/O should be present inside a MapFunc (given a Sveltos/Cluster, return all the Profiles matching it).
	// In the MapFunc, if the list Profiles operation failed, we would be unable to retry or re-enqueue the rigth set of
	// Profiles.
	// Instead the approach taken is following:
	// - when a Profile is reconciled, update the Profiles and the ClusterMap;
	// - in the MapFunc, given the Sveltos/Cluster that changed:
	//		* use Profiles to find all Profiles matching the Cluster and reconcile those;
	// - in order to reconcile Profiles previously matching the Cluster and not anymore, use ClusterMap.
	//
	// The ProfileMap is used to update ClusterMap. Consider following scenarios to understand the need:
	// 1. Profile A references Clusters 1 and 2. When reconciled, ClusterMap will have 1 => A and 2 => A;
	// and ProfileMap A => 1,2
	// 2. Cluster 2 label changes and now Profile matches Cluster 1 only. We ned to remove the entry 2 => A in ClusterMap. But
	// when we reconcile Profile we have its current version we don't have its previous version. So we know Profile A
	// now matches Sveltos/Cluster 1, but we don't know it used to match Sveltos/Cluster 2.
	// So we use ProfileMap (at this point value stored here corresponds to reconciliation #1. We know currently
	// Profile matches Sveltos/Cluster 1 only and looking at ProfileMap we know it used to reference
	// Svetos/CAPI Cluster 1 and 2.
	// So we can remove 2 => A from ClusterMap. Only after this update, we update ProfileMap (so new value will be A => 1)

	ctrl controller.Controller
}

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=profiles,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=profiles/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=profiles/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;update;create;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterreports,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterconfigurations,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters/status,verbs=get;watch;list

func (r *ProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

	// Fecth the Profile instance
	profile := &configv1alpha1.Profile{}
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch Profile")
		return reconcile.Result{}, errors.Wrapf(err, "Failed to fetch Profile %s",
			req.NamespacedName)
	}

	// limit all references to be in the namespace
	r.limitReferencesToNamespace(profile)

	profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
		Client:         r.Client,
		Logger:         logger,
		Profile:        profile,
		ControllerName: "profile",
	})
	if err != nil {
		logger.Error(err, "Failed to create profileScope")
		return reconcile.Result{}, errors.Wrapf(err,
			"unable to create profile scope for %s", req.NamespacedName)
	}

	// Always close the scope when exiting this function so we can persist any Profile
	// changes.
	defer func() {
		if err := profileScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted profile
	if !profile.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, profileScope), nil
	}

	// Handle non-deleted profile
	return r.reconcileNormal(ctx, profileScope), nil
}

func (r *ProfileReconciler) reconcileDelete(
	ctx context.Context,
	profileScope *scope.ProfileScope) reconcile.Result {

	logger := profileScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling Profile delete")

	if err := reconcileDeleteCommon(ctx, r.Client, profileScope,
		configv1alpha1.ProfileFinalizer, logger); err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}
	}

	r.cleanMaps(profileScope)

	logger.V(logs.LogInfo).Info("Reconcile delete success")
	return reconcile.Result{}
}

func (r *ProfileReconciler) reconcileNormal(
	ctx context.Context,
	profileScope *scope.ProfileScope) reconcile.Result {

	logger := profileScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling Profile")

	if !controllerutil.ContainsFinalizer(profileScope.Profile, configv1alpha1.ProfileFinalizer) {
		if err := addFinalizer(ctx, profileScope, configv1alpha1.ProfileFinalizer); err != nil {
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
		}
	}

	// Limit the search of matching cluster to the Profile namespace
	matchingCluster, err := getMatchingClusters(ctx, r.Client, profileScope.Profile.GetNamespace(),
		profileScope.GetSelector(), profileScope.GetSpec().ClusterRefs, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	// Get all clusters from referenced Sets
	clusterSetClusters, err := r.getClustersFromSets(ctx, profileScope.GetSpec().SetRefs, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}
	matchingCluster = append(matchingCluster, clusterSetClusters...)

	profileScope.SetMatchingClusterRefs(removeDuplicates(matchingCluster))

	r.updateMaps(profileScope)

	if err := reconcileNormalCommon(ctx, r.Client, profileScope, logger); err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	logger.V(logs.LogInfo).Info("Reconcile success")
	return reconcile.Result{}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.Profile{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "error creating controller")
	}

	// At this point we don't know yet whether CAPI is present in the cluster.
	// Later on, in main, we detect that and if CAPI is present WatchForCAPI will be invoked.
	// When projectsveltos cluster changes, according to SveltosClusterPredicates,
	// one or more Profiles need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.SveltosCluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueProfileForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)
	if err != nil {
		return err
	}

	// When Set changes, according to SetPredicates,
	// one or more ClusterProfiles need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.Set{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueProfileForSet),
		SetPredicates(mgr.GetLogger().WithValues("predicate", "setpredicate")),
	)
	if err != nil {
		return err
	}

	r.ctrl = c

	return nil
}

func (r *ProfileReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more Profiles need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Cluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueProfileForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}
	// When cluster-api machine changes, according to ClusterPredicates,
	// one or more Profiles need to be reconciled.
	if err := c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Machine{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueProfileForMachine),
		MachinePredicates(mgr.GetLogger().WithValues("predicate", "machinepredicate")),
	); err != nil {
		return err
	}
	return nil
}

func (r *ProfileReconciler) limitReferencesToNamespace(profile *configv1alpha1.Profile) {
	for i := range profile.Spec.ClusterRefs {
		profile.Spec.ClusterRefs[i].Namespace = profile.Namespace
	}

	for i := range profile.Spec.PolicyRefs {
		profile.Spec.PolicyRefs[i].Namespace = profile.Namespace
	}

	for i := range profile.Spec.KustomizationRefs {
		profile.Spec.KustomizationRefs[i].Namespace = profile.Namespace
	}

	for i := range profile.Spec.SetRefs {
		profile.Spec.SetRefs[i].Namespace = profile.Namespace
	}
}

func (r *ProfileReconciler) cleanMaps(profileScope *scope.ProfileScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	profileInfo := getKeyFromObject(r.Scheme, profileScope.Profile)

	delete(r.ProfileMap, *profileInfo)
	delete(r.Profiles, *profileInfo)

	// ClusterMap contains for each cluster, set of Profiles matching
	// that cluster. Remove Profile from this map
	for i := range r.ClusterMap {
		profileSet := r.ClusterMap[i]
		profileSet.Erase(profileInfo)
	}

	// SetMap contains for each set, list of Profiles referencing
	// such Set. Remove ClusterProfile from this map
	for i := range r.SetMap {
		profileSet := r.SetMap[i]
		profileSet.Erase(profileInfo)
	}
}

func (r *ProfileReconciler) updateMaps(profileScope *scope.ProfileScope) {
	currentClusters := getCurrentClusterSet(profileScope.GetStatus().MatchingClusterRefs)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	profileInfo := getKeyFromObject(r.Scheme, profileScope.Profile)

	// Get list of Clusters not matched anymore by Profile
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.ProfileMap[*profileInfo]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add Profile as consumer
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		getConsumersForEntry(r.ClusterMap, clusterInfo).Insert(profileInfo)
	}

	// For each Cluster not matched anymore, remove Profile as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		getConsumersForEntry(r.ClusterMap, &clusterName).Erase(profileInfo)
	}

	// For each referenced Set, add Profile as consumer
	for i := range profileScope.GetSpec().SetRefs {
		set := profileScope.GetSpec().SetRefs[i]
		setInfo := &corev1.ObjectReference{Namespace: set.Namespace, Name: set.Name,
			Kind: libsveltosv1alpha1.SetKind, APIVersion: libsveltosv1alpha1.GroupVersion.String()}
		getConsumersForEntry(r.SetMap, setInfo).Insert(profileInfo)
	}

	r.ProfileMap[*profileInfo] = currentClusters
	r.Profiles[*profileInfo] = profileScope.GetSpec().ClusterSelector
}

func (r *ProfileReconciler) GetController() controller.Controller {
	return r.ctrl
}

func (r *ProfileReconciler) getClustersFromSets(ctx context.Context, setRefs []corev1.ObjectReference,
	logger logr.Logger) ([]corev1.ObjectReference, error) {

	clusters := make([]corev1.ObjectReference, 0)
	for i := range setRefs {
		set := &libsveltosv1alpha1.Set{}
		if err := r.Client.Get(ctx,
			types.NamespacedName{Namespace: setRefs[i].Namespace, Name: setRefs[i].Name},
			set); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get set %s/%s",
				setRefs[i].Namespace, setRefs[i].Name))
			return nil, err
		}

		if set.Status.SelectedClusterRefs != nil {
			clusters = append(clusters, set.Status.SelectedClusterRefs...)
		}
	}

	return clusters, nil
}
