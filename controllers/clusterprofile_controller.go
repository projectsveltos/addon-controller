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
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
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
	Logger               logr.Logger

	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex

	// key: ClusterSet: value ClusterProfiles currently referencing the ClusterSet
	ClusterSetMap map[corev1.ObjectReference]*libsveltosset.Set

	// key: Sveltos/Cluster; value: set of all ClusterProfiles matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set

	// key: ClusterProfile; value ClusterProfile Selector
	ClusterProfiles map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - ClusterProfile is created
	// - Cluster is created with labels matching ClusterProfile
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which ClusterProfile to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	ctrl controller.Controller
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

	if !controllerutil.ContainsFinalizer(profileScope.Profile, configv1alpha1.ClusterProfileFinalizer) {
		if err := addFinalizer(ctx, profileScope, configv1alpha1.ClusterProfileFinalizer); err != nil {
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
		}
	}

	// Get all clusters matching clusterSelector and ClusterRefs
	matchingCluster, err := getMatchingClusters(ctx, r.Client, "", profileScope.GetSelector(),
		profileScope.GetSpec().ClusterRefs, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	// Get all clusters from referenced ClusterSets
	clusterSetClusters, err := r.getClustersFromClusterSets(ctx, profileScope.GetSpec().SetRefs, logger)
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
func (r *ClusterProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterProfile{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "error creating controller")
	}

	// When ClusterSet changes, according to SetPredicates,
	// one or more ClusterProfiles need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.ClusterSet{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForClusterSet),
		SetPredicates(mgr.GetLogger().WithValues("predicate", "clustersetpredicate")),
	)
	if err != nil {
		return err
	}

	// At this point we don't know yet whether CAPI is present in the cluster.
	// Later on, in main, we detect that and if CAPI is present WatchForCAPI will be invoked.

	// When projectsveltos cluster changes, according to SveltosClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	err = c.Watch(source.Kind(mgr.GetCache(), &libsveltosv1alpha1.SveltosCluster{}),
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)

	r.ctrl = c

	return err
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

	delete(r.ClusterProfiles, *clusterProfileInfo)

	// ClusterMap contains for each cluster, list of ClusterProfiles matching
	// such cluster. Remove ClusterProfile from this map
	for i := range r.ClusterMap {
		clusterProfileSet := r.ClusterMap[i]
		clusterProfileSet.Erase(clusterProfileInfo)
	}

	// ClusterSetMap contains for each clusterSet, list of ClusterProfiles referencing
	// such ClusterSet. Remove ClusterProfile from this map
	for i := range r.ClusterSetMap {
		clusterProfileSet := r.ClusterSetMap[i]
		clusterProfileSet.Erase(clusterProfileInfo)
	}
}

func (r *ClusterProfileReconciler) updateMaps(profileScope *scope.ProfileScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterProfileInfo := getKeyFromObject(r.Scheme, profileScope.Profile)

	for k, l := range r.ClusterMap {
		l.Erase(clusterProfileInfo)
		if l.Len() == 0 {
			delete(r.ClusterMap, k)
		}
	}

	// For each currently matching Cluster, add ClusterProfile as consumer
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		getConsumersForEntry(r.ClusterMap, clusterInfo).Insert(clusterProfileInfo)
	}

	for k, l := range r.ClusterSetMap {
		l.Erase(clusterProfileInfo)
		if l.Len() == 0 {
			delete(r.ClusterSetMap, k)
		}
	}

	// For each referenced ClusterSet, add ClusterProfile as consumer
	for i := range profileScope.GetSpec().SetRefs {
		clusterSet := profileScope.GetSpec().SetRefs[i]
		clusterSetInfo := &corev1.ObjectReference{Name: clusterSet,
			Kind: libsveltosv1alpha1.ClusterSetKind, APIVersion: libsveltosv1alpha1.GroupVersion.String()}
		getConsumersForEntry(r.ClusterSetMap, clusterSetInfo).Insert(clusterProfileInfo)
	}

	r.ClusterProfiles[*clusterProfileInfo] = profileScope.GetSpec().ClusterSelector
}

func (r *ClusterProfileReconciler) GetController() controller.Controller {
	return r.ctrl
}

func (r *ClusterProfileReconciler) getClustersFromClusterSets(ctx context.Context, clusterSetRefs []string,
	logger logr.Logger) ([]corev1.ObjectReference, error) {

	clusters := make([]corev1.ObjectReference, 0)
	for i := range clusterSetRefs {
		clusterSet := &libsveltosv1alpha1.ClusterSet{}
		if err := r.Client.Get(ctx,
			types.NamespacedName{Name: clusterSetRefs[i]},
			clusterSet); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusterSet %s", clusterSetRefs[i]))
			return nil, err
		}

		if clusterSet.Status.SelectedClusterRefs != nil {
			clusters = append(clusters, clusterSet.Status.SelectedClusterRefs...)
		}
	}

	return clusters, nil
}
