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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

// ClusterSummaryReconciler reconciles a ClusterSummary object
type ClusterSummaryReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	Deployer          deployer.DeployerInterface
	Mux               sync.Mutex      // use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	WorkloadRoleMap   map[string]*Set // key: WorkloadRole name; value: set of all ClusterSummaries referencing the WorkloadRole
	ClusterSummaryMap map[string]*Set // key: ClusterSummary name; value: set of WorkloadRoles referenced

	// Reason for the two maps:
	// ClusterSummary references WorkloadRoles. When a WorkloadRole changes, all the ClusterSummaries referencing it need to be
	// reconciled. In order to achieve so, ClusterSummary reconciler could watch for WorkloadRoles. When a WorkloadRole spec changes,
	// find all the ClusterSummaries currently referencing it and reconcile those. Problem is no I/O should be present inside a MapFunc
	// (given a WorkloadRole, return all the ClusterSummary referencing such WorkloadRole).
	// In the MapFunc, if the list ClusterSummaries operation failed, we would be unable to retry or re-enqueue the ClusterSummaries
	// referencing the WorkloadRole that changed.
	// Instead the approach taken is following:
	// - when a ClusterSummary is reconciled, update the WorkloadRoleMap;
	// - in the MapFunc, given the WorkloadRole that changed, we can immeditaly get all the ClusterSummaries needing a reconciliation (by
	// using the WorkloadRoleMap);
	// - if a ClusterSummary is referencing a WorkloadRole but its reconciliation is still queued, when WorkloadRole changes, WorkloadRoleMap
	// won't have such ClusterSummary. This is not a problem as ClusterSummary reconciliation is already queued and will happen.
	//
	// The ClusterSummaryMap is used to update WorkloadRoleMap. Consider following scenarios to understand the need:
	// 1. ClusterSummary A references WorkloadRoles 1 and 2. When reconciled, WorkloadRoleMap will have 1 => A and 2 => A;
	// and ClusterSummaryMap A => 1,2
	// 2. ClusterSummary A changes and now references WorkloadRole 1 only. We ned to remove the entry 2 => A in WorkloadRoleMap. But
	// when we reconcile ClusterSummary we have its current version we don't have its previous version. So we use ClusterSummaryMap (at this
	// point value stored here corresponds to reconciliation #1. We know currently ClusterSummary references WorkloadRole 1 only and looking
	// at ClusterSummaryMap we know it used to reference WorkloadRole 1 and 2. So we can remove 2 => A from WorkloadRoleMap. Only after this
	// update, we update ClusterSummaryMap (so new value will be A => 1)
}

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=workloadroles,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *ClusterSummaryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Reconciling")

	// Fecth the clusterSummary instance
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := r.Get(ctx, req.NamespacedName, clusterSummary); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch clusterSummary")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch clusterSummary %s",
			req.NamespacedName,
		)
	}

	// Fetch the ClusterFeature.
	clusterFeature, err := getClusterFeatureOwner(ctx, r.Client, clusterSummary)
	if err != nil {
		logger.Error(err, "Failed to get owner clusterFeature")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to get owner clusterFeature for %s",
			req.NamespacedName,
		)
	}

	clusterFeatureScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
		Client:         r.Client,
		Logger:         logger,
		ClusterSummary: clusterSummary,
		ClusterFeature: clusterFeature,
		ControllerName: "clusterfeature",
	})
	if err != nil {
		logger.Error(err, "Failed to create clusterFeatureScope")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"unable to create clusterfeature scope for %s",
			req.NamespacedName,
		)
	}

	// Always close the scope when exiting this function so we can persist any ClusterFeature
	// changes.
	defer func() {
		if err := clusterFeatureScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted clusterSummary
	if !clusterSummary.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterFeatureScope, logger)
	}

	// Handle non-deleted clusterSummary
	return r.reconcileNormal(ctx, clusterFeatureScope, logger)
}

func (r *ClusterSummaryReconciler) reconcileDelete(
	ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger,
) (reconcile.Result, error) {
	logger.Info("Reconciling ClusterSummary delete")

	if err := r.undeploy(ctx, clusterSummaryScope, logger); err != nil {
		return reconcile.Result{}, err
	}

	// Cluster is deleted so remove the finalizer.
	logger.Info("Removing finalizer")
	if controllerutil.ContainsFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer) {
		controllerutil.RemoveFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer)
	}

	logger.Info("Reconcile delete success")

	return reconcile.Result{}, nil
}

func (r *ClusterSummaryReconciler) reconcileNormal(
	ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger,
) (reconcile.Result, error) {
	logger.Info("Reconciling ClusterSummary")

	if !controllerutil.ContainsFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer) {
		if err := r.addFinalizer(ctx, clusterSummaryScope); err != nil {
			return reconcile.Result{}, err
		}
	}

	r.updatesMaps(clusterSummaryScope)

	if err := r.deploy(ctx, clusterSummaryScope, logger); err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("Reconciling ClusterSummary success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSummaryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterSummary{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "error creating controller")
	}

	// When WorkloadRole changes, according to WorkloadRolePredicates,
	// one or more ClusterSummaries need to be reconciled.
	return c.Watch(&source.Kind{Type: &configv1alpha1.WorkloadRole{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForWorkloadRole),
		WorkloadRolePredicates(klogr.New().WithValues("predicate", "workloadrolepredicate")),
	)
}

func (r *ClusterSummaryReconciler) addFinalizer(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope) error {
	// If the SveltosCluster doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer)
	// Register the finalizer immediately to avoid orphaning clusterfeature resources on delete
	if err := clusterSummaryScope.PatchObject(ctx); err != nil {
		clusterSummaryScope.Error(err, "Failed to add finalizer")
		return errors.Wrapf(
			err,
			"Failed to add finalizer for %s",
			clusterSummaryScope.Name(),
		)
	}
	return nil
}

func (r *ClusterSummaryReconciler) deploy(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if err := r.deployRoles(ctx, clusterSummaryScope, logger); err != nil {
		return err
	}

	return nil
}

func (r *ClusterSummaryReconciler) deployRoles(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeatureRole,
		currentHash: workloadRoleHash,
		deploy:      DeployWorkloadRoles,
	}

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeploy(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	clusterSummary := clusterSummaryScope.ClusterSummary

	// If CAPI Cluster is not found, there is nothing to clean up.
	cluster := &clusterv1.Cluster{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: clusterSummary.Spec.ClusterNamespace, Name: clusterSummary.Spec.ClusterName}, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info(fmt.Sprintf("cluster %s/%s not found. Nothing to do.", clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName))
			return nil
		}
		return err
	}

	if err := r.undeployRoles(ctx, clusterSummaryScope, logger); err != nil {
		return err
	}

	return nil
}

func (r *ClusterSummaryReconciler) undeployRoles(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeatureRole,
		currentHash: workloadRoleHash,
		deploy:      UnDeployWorkloadRoles,
	}

	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) updatesMaps(clusterSummaryScope *scope.ClusterSummaryScope) {
	currentWorkloadRoles := Set{}
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles {
		workloadRoleName := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles[i].Name
		currentWorkloadRoles.insert(workloadRoleName)
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Get list of WorkloadRoles not referenced anymore by ClusterSummary
	var toBeRemoved []string
	if v, ok := r.ClusterSummaryMap[clusterSummaryScope.Name()]; ok {
		toBeRemoved = v.difference(currentWorkloadRoles)
	}

	// For each currently referenced WorkloadRole, add ClusterSummary as consumer
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles {
		workloadRoleName := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles[i].Name
		r.getWorkloadRoleMapForEntry(workloadRoleName).insert(clusterSummaryScope.Name())
	}

	// For each WorkloadRole not reference anymore, remove ClusterSummary as consumer
	for i := range toBeRemoved {
		workloadRoleName := toBeRemoved[i]
		r.getWorkloadRoleMapForEntry(workloadRoleName).erase(clusterSummaryScope.Name())
	}

	// Update list of WorklaodRoles currently referenced by ClusterSummary
	r.ClusterSummaryMap[clusterSummaryScope.Name()] = &currentWorkloadRoles
}

func (r *ClusterSummaryReconciler) getWorkloadRoleMapForEntry(entry string) *Set {
	s := r.WorkloadRoleMap[entry]
	if s == nil {
		s = &Set{}
		r.WorkloadRoleMap[entry] = s
	}
	return s
}
