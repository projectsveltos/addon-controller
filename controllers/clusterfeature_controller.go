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
	"reflect"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

// ClusterFeatureReconciler reconciles a ClusterFeature object
type ClusterFeatureReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterfeatures,verbs=get;list;watch;update
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterfeatures/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterfeatures/finalizers,verbs=update
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;update;create;delete
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;watch;list

func (r *ClusterFeatureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := r.Log.WithValues("clusterfeature", req.NamespacedName)
	logger.Info("Reconciling")

	// Fecth the ClusterFeature instance
	clusterFeature := &configv1alpha1.ClusterFeature{}
	if err := r.Get(ctx, req.NamespacedName, clusterFeature); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ClusterFeature")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch ClusterFeature %s",
			req.NamespacedName,
		)
	}

	clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
		Client:         r.Client,
		Logger:         logger,
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

	// Handle deleted clusters
	if !clusterFeature.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterFeatureScope)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, clusterFeatureScope)
}

func (r *ClusterFeatureReconciler) reconcileDelete(
	ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
) (reconcile.Result, error) {

	logger := clusterFeatureScope.Logger
	logger.Info("Reconciling ClusterFeature delete")
	logger.Info("Reconcile delete success")

	return reconcile.Result{}, nil
}

func (r *ClusterFeatureReconciler) reconcileNormal(
	ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
) (reconcile.Result, error) {

	logger := clusterFeatureScope.Logger
	logger.Info("Reconciling ClusterFeature")

	if !controllerutil.ContainsFinalizer(clusterFeatureScope.ClusterFeature, configv1alpha1.ClusterFeatureFinalizer) {
		if err := r.addFinalizer(ctx, clusterFeatureScope); err != nil {
			return reconcile.Result{}, err
		}
	}

	matchingCluster, err := r.getMatchingClusters(ctx, clusterFeatureScope)
	if err != nil {
		return reconcile.Result{}, err
	}

	clusterFeatureScope.SetMatchingClusters(matchingCluster)

	// For each matching CAPI Cluster, create/update corresponding ClusterSummary
	if err := r.updateClusterSummaries(ctx, clusterFeatureScope); err != nil {
		return reconcile.Result{}, err
	}
	// For CAPI Cluster not matching ClusterFeature, deletes corresponding ClusterSummary
	if err := r.cleanClusterSummaries(ctx, clusterFeatureScope); err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("Reconcile success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterFeatureReconciler) SetupWithManager(mgr ctrl.Manager) error {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterFeature{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "error creating controller")
	}

	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterFeatures need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterFeatureForCluster),
		ClusterPredicates(r.Log),
	); err != nil {
		return err
	}

	// When cluster-api machine changes, according to ClusterPredicates,
	// one or more ClusterFeatures need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Machine{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterFeatureForMachine),
		MachinePredicates(r.Log),
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterFeature{}).
		Complete(r)
}

func (r *ClusterFeatureReconciler) addFinalizer(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope) error {
	// If the SveltosCluster doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(clusterFeatureScope.ClusterFeature, configv1alpha1.ClusterFeatureFinalizer)
	// Register the finalizer immediately to avoid orphaning clusterfeature resources on delete
	if err := clusterFeatureScope.PatchObject(ctx); err != nil {
		clusterFeatureScope.Error(err, "Failed to add finalizer")
		return errors.Wrapf(
			err,
			"Failed to add finalizer for %s",
			clusterFeatureScope.Name(),
		)
	}
	return nil
}

// getMatchingClusters returns all CAPI Clusters currently matching ClusterFeature.Spec.ClusterSelector
func (r *ClusterFeatureReconciler) getMatchingClusters(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope) ([]corev1.ObjectReference, error) {
	clusterList := &clusterv1.ClusterList{}
	if err := r.List(ctx, clusterList); err != nil {
		clusterFeatureScope.Logger.Error(err, "failed to list all Cluster")
		return nil, err
	}

	matching := make([]corev1.ObjectReference, 0)

	parsedSelector, _ := labels.Parse(clusterFeatureScope.GetSelector())

	for i := range clusterList.Items {
		cluster := &clusterList.Items[i]
		if parsedSelector.Matches(labels.Set(cluster.Labels)) {
			matching = append(matching, corev1.ObjectReference{
				Kind:      cluster.Kind,
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			})
		}
	}

	return matching, nil
}

// updateClusterSummaries for each CAPI Cluster currently matching ClusterFeature:
// - creates corresponding ClusterSummary if one does not exist already
// - updates (eventually) corresponding ClusterSummary if one already exists
func (r *ClusterFeatureReconciler) updateClusterSummaries(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope) error {
	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusters {
		cluster := clusterFeatureScope.ClusterFeature.Status.MatchingClusters[i]
		ready, err := r.isClusterReadyToBeConfigured(ctx, clusterFeatureScope, cluster)
		if err != nil {
			return err
		}
		if !ready {
			clusterFeatureScope.Logger.V(5).Info(fmt.Sprintf("Cluster %s/%s is not ready yet",
				cluster.Namespace, cluster.Name))
			continue
		}

		clusterSummary := &configv1alpha1.ClusterSummary{}
		clusterSummaryName := getClusterSummaryName(clusterFeatureScope.Name(), cluster.Namespace, cluster.Name)
		if err := r.Get(ctx, types.NamespacedName{Name: clusterSummaryName}, clusterSummary); err != nil {
			if apierrors.IsNotFound(err) {
				if err := r.createClusterSummary(ctx, clusterFeatureScope, cluster); err != nil {
					clusterFeatureScope.Logger.Error(err, fmt.Sprintf("failed to create ClusterSummary for cluster %s/%s",
						cluster.Namespace, cluster.Name))
				}
			} else {
				clusterFeatureScope.Logger.Error(err, "failed to get ClusterSummary for cluster %s/%s",
					cluster.Namespace, cluster.Name)
				return err
			}
		} else {
			if err := r.updateClusterSummary(ctx, clusterFeatureScope, cluster); err != nil {
				clusterFeatureScope.Logger.Error(err, "failed to update ClusterSummary for cluster %s/%s",
					cluster.Namespace, cluster.Name)
				return err
			}
		}
	}

	return nil
}

// cleanClusterSummaries finds all ClusterSummary currently owned by ClusterFeature.
// For each such ClusterSummary, if corresponding CAPI Cluster is not a match anymore, deletes ClusterSummary
func (r *ClusterFeatureReconciler) cleanClusterSummaries(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope) error {
	matching := make(map[string]bool)

	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusters {
		reference := clusterFeatureScope.ClusterFeature.Status.MatchingClusters[i]
		clusterSummaryName := getClusterSummaryName(clusterFeatureScope.Name(), reference.Namespace, reference.Name)
		matching[clusterSummaryName] = true
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	for i := range clusterSummaryList.Items {
		cs := &clusterSummaryList.Items[i]
		if util.IsOwnedByObject(cs, clusterFeatureScope.ClusterFeature) {
			if _, ok := matching[cs.Name]; !ok {
				if err := r.deleteClusterSummary(ctx, cs); err != nil {
					clusterFeatureScope.Logger.Error(err, "failed to update ClusterSummary for cluster %s/%s",
						cs.Namespace, cs.Name)
					return err
				}
			}
		}
	}

	return nil
}

// createClusterSummary creates ClusterSummary given a ClusterFeature and a matching CAPI Cluster
func (r *ClusterFeatureReconciler) createClusterSummary(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope,
	cluster corev1.ObjectReference) error {
	clusterSummaryName := getClusterSummaryName(clusterFeatureScope.Name(), cluster.Namespace, cluster.Name)

	clusterSummary := &configv1alpha1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterSummaryName,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind: clusterFeatureScope.ClusterFeature.Kind,
					Name: clusterFeatureScope.ClusterFeature.Name,
					UID:  clusterFeatureScope.ClusterFeature.UID,
				},
			},
		},
		Spec: configv1alpha1.ClusterSummarySpec{
			ClusterNamespace:   cluster.Namespace,
			ClusterName:        cluster.Name,
			ClusterFeatureSpec: clusterFeatureScope.ClusterFeature.Spec,
		},
	}

	return r.Create(ctx, clusterSummary)
}

// updateClusterSummary updates if necessary ClusterSummary given a ClusterFeature and a matching CAPI Cluster.
// If ClusterFeature.Spec.SyncMode is set to one time, nothing will happen
func (r *ClusterFeatureReconciler) updateClusterSummary(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope,
	cluster corev1.ObjectReference) error {
	if !clusterFeatureScope.IsContinuosSync() {
		return nil
	}

	clusterSummaryName := getClusterSummaryName(clusterFeatureScope.Name(), cluster.Namespace, cluster.Name)
	clusterSummary := &configv1alpha1.ClusterSummary{}

	if err := r.Get(ctx, types.NamespacedName{Name: clusterSummaryName}, clusterSummary); err != nil {
		return err
	}

	if reflect.DeepEqual(clusterFeatureScope.ClusterFeature.Spec, clusterSummary.Spec.ClusterFeatureSpec) {
		// Nothing has changed
		return nil
	}

	clusterSummary.Spec.ClusterFeatureSpec = clusterFeatureScope.ClusterFeature.Spec
	return r.Update(ctx, clusterSummary)
}

// deleteClusterSummary deletes ClusterSummary given a ClusterFeature and a matching CAPI Cluster
func (r *ClusterFeatureReconciler) deleteClusterSummary(ctx context.Context, clusterSummary *configv1alpha1.ClusterSummary) error {
	return r.Delete(ctx, clusterSummary)
}

// isClusterReadyToBeConfigured gets all Machines for a given CAPI Cluster and returns true
// if at least one control plane machine is in running phase
func (r *ClusterFeatureReconciler) isClusterReadyToBeConfigured(
	ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
	cluster corev1.ObjectReference,
) (bool, error) {
	machineList, err := r.getMachinesForCluster(ctx, clusterFeatureScope, cluster)
	if err != nil {
		return false, err
	}

	for i := range machineList.Items {
		if util.IsControlPlaneMachine(&machineList.Items[i]) &&
			machineList.Items[i].Status.GetTypedPhase() == clusterv1.MachinePhaseRunning {
			return true, nil
		}
	}

	return false, nil
}

// getMachinesForCluster find all Machines for a given CAPI Cluster.
func (r *ClusterFeatureReconciler) getMachinesForCluster(
	ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
	cluster corev1.ObjectReference,
) (*clusterv1.MachineList, error) {
	listOptions := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingFields{clusterv1.ClusterLabelName: cluster.Name},
	}
	var machineList clusterv1.MachineList
	if err := r.Client.List(ctx, &machineList, listOptions...); err != nil {
		clusterFeatureScope.Error(err, fmt.Sprintf("unable to list Machines for CAPI Cluster %s/%s",
			cluster.Namespace, cluster.Name))
		return nil, err
	}
	clusterFeatureScope.V(5).Info(fmt.Sprintf("Found %d machine", len(machineList.Items)))

	return &machineList, nil
}
