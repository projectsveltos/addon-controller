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

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// SetReconciler reconciles a Set object
type SetReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	Logger               logr.Logger

	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex
	// key: Sveltos/Cluster; value: set of all Sets matching the Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set
	// key: Set; value: set of Sveltos/CAPI Clusters matched
	SetMap map[corev1.ObjectReference]*libsveltosset.Set

	// key: Sets; value Set Selector
	Sets map[corev1.ObjectReference]libsveltosv1alpha1.Selector

	// For each cluster contains current labels
	// This is needed in following scenario:
	// - Set is created
	// - Cluster is created with labels matching Set
	// - When first control plane machine in such cluster becomes available
	// we need Cluster labels to know which Set to reconcile
	ClusterLabels map[corev1.ObjectReference]map[string]string

	ctrl controller.Controller
}

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sets/finalizers,verbs=update
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=sveltosclusters/status,verbs=get;watch;list

func (r *SetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

	// Fecth the Set instance
	set := &libsveltosv1alpha1.Set{}
	if err := r.Get(ctx, req.NamespacedName, set); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch Set")
		return reconcile.Result{}, errors.Wrapf(err, "Failed to fetch Set %s",
			req.NamespacedName)
	}

	// limit all references to be in the namespace
	r.limitReferencesToNamespace(set)

	setScope, err := scope.NewSetScope(scope.SetScopeParams{
		Client:         r.Client,
		Logger:         logger,
		Set:            set,
		ControllerName: "set",
	})
	if err != nil {
		logger.Error(err, "Failed to create setScope")
		return reconcile.Result{}, errors.Wrapf(err,
			"unable to create set scope for %s", req.NamespacedName)
	}

	// Always close the scope when exiting this function so we can persist any Set
	// changes.
	defer func() {
		if err := setScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted set
	if !set.DeletionTimestamp.IsZero() {
		r.reconcileDelete(setScope)
		return reconcile.Result{}, nil
	}

	// Handle non-deleted set
	return r.reconcileNormal(ctx, setScope), nil
}

func (r *SetReconciler) reconcileDelete(
	setScope *scope.SetScope) {

	logger := setScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling Set delete")

	r.cleanMaps(setScope)

	logger.V(logs.LogInfo).Info("Reconcile delete success")
}

func (r *SetReconciler) reconcileNormal(
	ctx context.Context,
	setScope *scope.SetScope) reconcile.Result {

	logger := setScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling Set")

	// Limit the search of matching cluster to the Set namespace
	matchingCluster, err := getMatchingClusters(ctx, r.Client, setScope.Set.GetNamespace(),
		setScope.GetSelector(), setScope.GetSpec().ClusterRefs, logger)
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
func (r *SetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&libsveltosv1alpha1.Set{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Watches(&libsveltosv1alpha1.SveltosCluster{},
			handler.EnqueueRequestsFromMapFunc(r.requeueSetForSveltosCluster),
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

func (r *SetReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	sourceCluster := source.Kind[*clusterv1.Cluster](
		mgr.GetCache(),
		&clusterv1.Cluster{},
		handler.TypedEnqueueRequestsFromMapFunc(r.requeueSetForCluster),
		ClusterPredicate{Logger: mgr.GetLogger().WithValues("predicate", "clusterpredicate")},
	)

	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(sourceCluster); err != nil {
		return err
	}

	return nil
}

func (r *SetReconciler) cleanMaps(setScope *scope.SetScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	setInfo := getKeyFromObject(r.Scheme, setScope.Set)

	delete(r.SetMap, *setInfo)
	delete(r.Sets, *setInfo)

	for i := range r.ClusterMap {
		set := r.ClusterMap[i]
		set.Erase(setInfo)
	}
}

func (r *SetReconciler) updateMaps(setScope *scope.SetScope) {
	currentClusters := getCurrentClusterSet(setScope.GetStatus().MatchingClusterRefs)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	profileInfo := getKeyFromObject(r.Scheme, setScope.Set)

	// Get list of Clusters not matched anymore by Profile
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.SetMap[*profileInfo]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add Profile as consumer
	for i := range setScope.GetStatus().MatchingClusterRefs {
		cluster := setScope.GetStatus().MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name,
			Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		getConsumersForEntry(r.ClusterMap, clusterInfo).Insert(profileInfo)
	}

	// For each Cluster not matched anymore, remove Profile as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		getConsumersForEntry(r.ClusterMap, &clusterName).Erase(profileInfo)
	}

	r.SetMap[*profileInfo] = currentClusters
	r.Sets[*profileInfo] = setScope.GetSpec().ClusterSelector
}

func (r *SetReconciler) limitReferencesToNamespace(set *libsveltosv1alpha1.Set) {
	for i := range set.Spec.ClusterRefs {
		set.Spec.ClusterRefs[i].Namespace = set.Namespace
	}
}

func (r *SetReconciler) GetController() controller.Controller {
	return r.ctrl
}
