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
	"sync"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/pkg/scope"
)

// ClusterProfileReconciler reconciles a ClusterProfile object
type ClusterProfileReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	// use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	Mux sync.Mutex
	// key: Sveltos/CAPI Cluster; value: set of all ClusterProfiles matching the Cluster
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
	// When a Sveltos/CAPI Cluster labels change, one or more ClusterProfile needs to be reconciled.
	// In order to achieve so, ClusterProfile reconciler watches for Sveltos/CAPI Clusters. When a Sveltos/CAPI Cluster
	// label changes, find all the ClusterProfiles currently referencing it and reconcile those.
	// Problem is no I/O should be present inside a MapFunc (given a Sveltos/CAPI Cluster, return all the ClusterProfiles matching it).
	// In the MapFunc, if the list ClusterProfiles operation failed, we would be unable to retry or re-enqueue the rigth set of
	// ClusterProfiles.
	// Instead the approach taken is following:
	// - when a ClusterProfile is reconciled, update the ClusterProfiles amd the ClusterMap;
	// - in the MapFunc, given the Sveltos/CAPI Cluster that changed:
	//		* use ClusterProfiles to find all ClusterProfile matching the Cluster and reconcile those;
	// - in order to reconcile ClusterProfiles previously matching the Cluster and not anymore, use ClusterMap.
	//
	// The ClusterProfileMap is used to update ClusterMap. Consider following scenarios to understand the need:
	// 1. ClusterProfile A references Clusters 1 and 2. When reconciled, ClusterMap will have 1 => A and 2 => A;
	// and ClusterProfileMap A => 1,2
	// 2. Cluster 2 label changes and now ClusterProfile matches Cluster 1 only. We ned to remove the entry 2 => A in ClusterMap. But
	// when we reconcile ClusterProfile we have its current version we don't have its previous version. So we know ClusterProfile A
	// now matches Sveltos/CAPI Cluster 1, but we don't know it used to match Sveltos/CAPI Cluster 2.
	// So we use ClusterProfileMap (at this point value stored here corresponds to reconciliation #1. We know currently
	// ClusterProfile matches Sveltos/CAPI Cluster 1 only and looking at ClusterProfileMap we know it used to reference
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
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch ClusterProfile %s",
			req.NamespacedName,
		)
	}

	clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
		Client:         r.Client,
		Logger:         logger,
		ClusterProfile: clusterProfile,
		ControllerName: "clusterprofile",
	})
	if err != nil {
		logger.Error(err, "Failed to create clusterProfileScope")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"unable to create clusterprofile scope for %s",
			req.NamespacedName,
		)
	}

	// Always close the scope when exiting this function so we can persist any ClusterProfile
	// changes.
	defer func() {
		if err := clusterProfileScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted clusterProfile
	if !clusterProfile.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterProfileScope)
	}

	// Handle non-deleted clusterProfile
	return r.reconcileNormal(ctx, clusterProfileScope)
}

func (r *ClusterProfileReconciler) reconcileDelete(
	ctx context.Context,
	clusterProfileScope *scope.ClusterProfileScope,
) (reconcile.Result, error) {

	logger := clusterProfileScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling ClusterProfile delete")

	clusterProfileScope.SetMatchingClusterRefs(nil)

	if err := r.cleanClusterSummaries(ctx, clusterProfileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterSummaries")
		return reconcile.Result{}, err
	}

	if !r.allClusterSummariesGone(ctx, clusterProfileScope) {
		logger.V(logs.LogInfo).Info("Not all cluster summaries are gone")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	if err := r.cleanClusterConfigurations(ctx, clusterProfileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterConfigurations")
		return reconcile.Result{}, err
	}

	if err := r.cleanClusterReports(ctx, clusterProfileScope.ClusterProfile); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterReports")
		return reconcile.Result{}, err
	}

	if !r.canRemoveFinalizer(ctx, clusterProfileScope) {
		logger.V(logs.LogInfo).Info("Cannot remove finalizer yet")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	if controllerutil.ContainsFinalizer(clusterProfileScope.ClusterProfile, configv1alpha1.ClusterProfileFinalizer) {
		controllerutil.RemoveFinalizer(clusterProfileScope.ClusterProfile, configv1alpha1.ClusterProfileFinalizer)
	}

	r.cleanMaps(clusterProfileScope)

	logger.V(logs.LogInfo).Info("Reconcile delete success")
	return reconcile.Result{}, nil
}

func (r *ClusterProfileReconciler) reconcileNormal(
	ctx context.Context,
	clusterProfileScope *scope.ClusterProfileScope,
) (reconcile.Result, error) {

	logger := clusterProfileScope.Logger
	logger.V(logs.LogInfo).Info("Reconciling ClusterProfile")

	if !controllerutil.ContainsFinalizer(clusterProfileScope.ClusterProfile, configv1alpha1.ClusterProfileFinalizer) {
		if err := r.addFinalizer(ctx, clusterProfileScope); err != nil {
			return reconcile.Result{}, err
		}
	}

	parsedSelector, _ := labels.Parse(clusterProfileScope.GetSelector())
	matchingCluster, err := clusterproxy.GetMatchingClusters(ctx, r.Client, parsedSelector, logger)
	if err != nil {
		return reconcile.Result{}, err
	}

	clusterProfileScope.SetMatchingClusterRefs(matchingCluster)

	r.updateMaps(clusterProfileScope)

	// For each matching Sveltos/CAPI Cluster, create/update corresponding ClusterConfiguration
	if err := r.updateClusterConfigurations(ctx, clusterProfileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterConfigurations")
		return reconcile.Result{}, err
	}
	// For each matching Sveltos/CAPI Cluster, create or delete corresponding ClusterReport if needed
	if err := r.updateClusterReports(ctx, clusterProfileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterReports")
		return reconcile.Result{}, err
	}
	// For each matching Sveltos/CAPI Cluster, create/update corresponding ClusterSummary
	if err := r.updateClusterSummaries(ctx, clusterProfileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterSummaries")
		return reconcile.Result{}, err
	}

	// For Sveltos/CAPI Cluster not matching ClusterProfile, deletes corresponding ClusterSummary
	if err := r.cleanClusterSummaries(ctx, clusterProfileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterSummaries")
		return reconcile.Result{}, err
	}
	// For Sveltos/CAPI Cluster not matching ClusterProfile, removes ClusterProfile as OwnerReference
	// from corresponding ClusterConfiguration
	if err := r.cleanClusterConfigurations(ctx, clusterProfileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterConfigurations")
		return reconcile.Result{}, err
	}

	logger.V(logs.LogInfo).Info("Reconcile success")
	return reconcile.Result{}, nil
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
	err = c.Watch(&source.Kind{Type: &libsveltosv1alpha1.SveltosCluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
	)

	return c, err
}

func (r *ClusterProfileReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}
	// When cluster-api machine changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Machine{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterProfileForMachine),
		MachinePredicates(mgr.GetLogger().WithValues("predicate", "machinepredicate")),
	); err != nil {
		return err
	}

	return nil
}

func (r *ClusterProfileReconciler) addFinalizer(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope) error {
	// If the SveltosCluster doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(clusterProfileScope.ClusterProfile, configv1alpha1.ClusterProfileFinalizer)
	// Register the finalizer immediately to avoid orphaning clusterprofile resources on delete
	if err := clusterProfileScope.PatchObject(ctx); err != nil {
		clusterProfileScope.Error(err, "Failed to add finalizer")
		return errors.Wrapf(
			err,
			"Failed to add finalizer for %s",
			clusterProfileScope.Name(),
		)
	}
	return nil
}

// updateClusterReports for each Sveltos/CAPI Cluster currently matching ClusterProfile:
// - if syncMode is DryRun, creates corresponding ClusterReport if one does not exist already;
// - if syncMode is DryRun, deletes ClusterReports for any Sveltos/CAPI Cluster not matching anymore;
// - if syncMode is not DryRun, deletes ClusterReports created by this ClusterProfile instance
func (r *ClusterProfileReconciler) updateClusterReports(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope) error {
	if clusterProfileScope.ClusterProfile.Spec.SyncMode == configv1alpha1.SyncModeDryRun {
		err := r.createClusterReports(ctx, clusterProfileScope.ClusterProfile)
		if err != nil {
			clusterProfileScope.Logger.Error(err, "failed to create ClusterReports")
			return err
		}
	} else {
		// delete all ClusterReports created by this ClusterProfile instance
		err := r.cleanClusterReports(ctx, clusterProfileScope.ClusterProfile)
		if err != nil {
			clusterProfileScope.Logger.Error(err, "failed to create ClusterReports")
			return err
		}
	}

	return nil
}

// createClusterReports creates a ClusterReport for each matching Cluster
func (r *ClusterProfileReconciler) createClusterReports(ctx context.Context, clusterProfile *configv1alpha1.ClusterProfile) error {
	for i := range clusterProfile.Status.MatchingClusterRefs {
		cluster := clusterProfile.Status.MatchingClusterRefs[i]

		// Create ClusterConfiguration if not already existing.
		err := r.createClusterReport(ctx, clusterProfile, &cluster)
		if err != nil {
			return err
		}
	}
	return nil
}

// createClusterReport creates ClusterReport given a Sveltos/CAPI Cluster.
// If already existing, return nil
func (r *ClusterProfileReconciler) createClusterReport(ctx context.Context, clusterProfile *configv1alpha1.ClusterProfile,
	cluster *corev1.ObjectReference) error {

	clusterType := clusterproxy.GetClusterType(cluster)

	clusterReport := &configv1alpha1.ClusterReport{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      getClusterReportName(clusterProfile.Name, cluster.Name, clusterType),
			Labels: map[string]string{
				ClusterProfileLabelName:         clusterProfile.Name,
				configv1alpha1.ClusterNameLabel: cluster.Name,
				configv1alpha1.ClusterTypeLabel: string(clusterproxy.GetClusterType(cluster)),
			},
		},
		Spec: configv1alpha1.ClusterReportSpec{
			ClusterNamespace: cluster.Namespace,
			ClusterName:      cluster.Name,
		},
	}

	err := r.Create(ctx, clusterReport)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
	}

	return err
}

// cleanClusterReports deletes ClusterReports created by this ClusterProfile instance.
func (r *ClusterProfileReconciler) cleanClusterReports(ctx context.Context,
	clusterProfile *configv1alpha1.ClusterProfile) error {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			ClusterProfileLabelName: clusterProfile.Name,
		},
	}

	clusterReportList := &configv1alpha1.ClusterReportList{}
	err := r.List(ctx, clusterReportList, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterReportList.Items {
		err = r.Delete(ctx, &clusterReportList.Items[i])
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

// updateClusterSummaries for each Sveltos/CAPI Cluster currently matching ClusterProfile:
// - creates corresponding ClusterSummary if one does not exist already
// - updates (eventually) corresponding ClusterSummary if one already exists
func (r *ClusterProfileReconciler) updateClusterSummaries(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope) error {
	for i := range clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs {
		cluster := clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs[i]
		ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, r.Client, &cluster, clusterProfileScope.Logger)
		if err != nil {
			return err
		}
		if !ready {
			clusterProfileScope.Logger.V(logs.LogDebug).Info(fmt.Sprintf("Cluster %s/%s is not ready yet",
				cluster.Namespace, cluster.Name))
			continue
		}

		// ClusterProfile does not look at whether Cluster is paused or not.
		// If a Cluster exists and it is a match, ClusterSummary is created (and ClusterSummary.Spec kept in sync if mode is
		// continuous).
		// ClusterSummary won't program cluster in paused state.
		_, err = getClusterSummary(ctx, r.Client, clusterProfileScope.Name(), cluster.Namespace, cluster.Name,
			clusterproxy.GetClusterType(&cluster))
		if err != nil {
			if apierrors.IsNotFound(err) {
				err = r.createClusterSummary(ctx, clusterProfileScope, &cluster)
				if err != nil {
					clusterProfileScope.Logger.Error(err, fmt.Sprintf("failed to create ClusterSummary for cluster %s/%s",
						cluster.Namespace, cluster.Name))
				}
			} else {
				clusterProfileScope.Logger.Error(err, "failed to get ClusterSummary for cluster %s/%s",
					cluster.Namespace, cluster.Name)
				return err
			}
		} else {
			err = r.updateClusterSummary(ctx, clusterProfileScope, &cluster)
			if err != nil {
				clusterProfileScope.Logger.Error(err, "failed to update ClusterSummary for cluster %s/%s",
					cluster.Namespace, cluster.Name)
				return err
			}
		}
	}

	return nil
}

// cleanClusterSummaries finds all ClusterSummary currently owned by ClusterProfile.
// For each such ClusterSummary, if corresponding Sveltos/CAPI Cluster is not a match anymore, deletes ClusterSummary
func (r *ClusterProfileReconciler) cleanClusterSummaries(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope) error {
	matching := make(map[string]bool)

	getClusterInfo := func(clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
		return fmt.Sprintf("%s-%s-%s", clusterType, clusterNamespace, clusterName)
	}

	for i := range clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs {
		reference := clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs[i]
		clusterName := getClusterInfo(reference.Namespace, reference.Name, clusterproxy.GetClusterType(&reference))
		matching[clusterName] = true
	}

	listOptions := []client.ListOption{
		client.MatchingLabels{
			ClusterProfileLabelName: clusterProfileScope.Name(),
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := r.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return err
	}

	foundClusterSummaries := false
	for i := range clusterSummaryList.Items {
		cs := &clusterSummaryList.Items[i]
		if util.IsOwnedByObject(cs, clusterProfileScope.ClusterProfile) {
			if _, ok := matching[getClusterInfo(cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cs.Spec.ClusterType)]; !ok {
				err := r.deleteClusterSummary(ctx, cs)
				if err != nil {
					clusterProfileScope.Logger.Error(err, fmt.Sprintf("failed to update ClusterSummary for cluster %s/%s",
						cs.Namespace, cs.Name))
					return err
				}
				foundClusterSummaries = true
			}
			// update SyncMode
			err := r.updateClusterSummarySyncMode(ctx, clusterProfileScope.ClusterProfile, cs)
			if err != nil {
				return err
			}
		}
	}

	if foundClusterSummaries {
		return fmt.Errorf("clusterSummaries still present")
	}

	return nil
}

// cleanClusterConfigurations finds all ClusterConfigurations currently owned by ClusterProfile.
// For each such ClusterConfigurations, if corresponding Cluster is not a match anymore:
// - remove ClusterProfile as OwnerReference
// - if no more OwnerReferences are left, delete ClusterConfigurations
func (r *ClusterProfileReconciler) cleanClusterConfigurations(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope) error {
	clusterConfiguratioList := &configv1alpha1.ClusterConfigurationList{}

	matchingClusterMap := make(map[string]bool)

	info := func(namespace, clusterConfigurationName string) string {
		return fmt.Sprintf("%s--%s", namespace, clusterConfigurationName)
	}

	for i := range clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs {
		ref := &clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs[i]
		matchingClusterMap[info(ref.Namespace, getClusterConfigurationName(ref.Name, clusterproxy.GetClusterType(ref)))] = true
	}

	err := r.List(ctx, clusterConfiguratioList)
	if err != nil {
		return err
	}

	for i := range clusterConfiguratioList.Items {
		cc := &clusterConfiguratioList.Items[i]

		// If Sveltos/CAPI Cluster is still a match, continue (don't remove ClusterProfile as OwnerReference)
		if _, ok := matchingClusterMap[info(cc.Namespace, cc.Name)]; ok {
			continue
		}

		err = r.cleanClusterConfiguration(ctx, clusterProfileScope.ClusterProfile, cc)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *ClusterProfileReconciler) cleanClusterConfiguration(ctx context.Context, clusterProfile *configv1alpha1.ClusterProfile,
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	// remove ClusterProfile as one of the ClusterConfiguration's owners
	err := r.cleanClusterConfigurationOwnerReferences(ctx, clusterProfile, clusterConfiguration)
	if err != nil {
		return err
	}

	// remove the section in ClusterConfiguration.Status.ClusterProfileResource used for this ClusterProfile
	err = r.cleanClusterConfigurationClusterProfileResources(ctx, clusterProfile, clusterConfiguration)
	if err != nil {
		return err
	}

	return nil
}

func (r *ClusterProfileReconciler) cleanClusterConfigurationOwnerReferences(ctx context.Context, clusterProfile *configv1alpha1.ClusterProfile,
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	ownerRef := metav1.OwnerReference{
		Kind:       clusterProfile.Kind,
		UID:        clusterProfile.UID,
		APIVersion: clusterProfile.APIVersion,
		Name:       clusterProfile.Name,
	}

	if !util.IsOwnedByObject(clusterConfiguration, clusterProfile) {
		return nil
	}

	clusterConfiguration.OwnerReferences = util.RemoveOwnerRef(clusterConfiguration.OwnerReferences, ownerRef)
	if len(clusterConfiguration.OwnerReferences) == 0 {
		return r.Delete(ctx, clusterConfiguration)
	} else {
		return r.Update(ctx, clusterConfiguration)
	}
}

func (r *ClusterProfileReconciler) cleanClusterConfigurationClusterProfileResources(ctx context.Context, clusterProfile *configv1alpha1.ClusterProfile,
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, r.Client,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			// If ClusterConfiguration is not found, nothing to do here.
			// ClusterConfiguration is removed if ClusterProfile was the last owner.
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		toBeUpdated := false
		for i := range currentClusterConfiguration.Status.ClusterProfileResources {
			if currentClusterConfiguration.Status.ClusterProfileResources[i].ClusterProfileName != clusterProfile.Name {
				continue
			}
			// Order is not important. So move the element at index i with last one in order to avoid moving all elements.
			length := len(currentClusterConfiguration.Status.ClusterProfileResources)
			currentClusterConfiguration.Status.ClusterProfileResources[i] = currentClusterConfiguration.Status.ClusterProfileResources[length-1]
			currentClusterConfiguration.Status.ClusterProfileResources = currentClusterConfiguration.Status.ClusterProfileResources[:length-1]
			toBeUpdated = true
			break
		}

		if toBeUpdated {
			return r.Status().Update(ctx, currentClusterConfiguration)
		}
		return nil
	})
	return err
}

// createClusterSummary creates ClusterSummary given a ClusterProfile and a matching Sveltos/CAPI Cluster
func (r *ClusterProfileReconciler) createClusterSummary(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope,
	cluster *corev1.ObjectReference) error {

	clusterSummaryName := GetClusterSummaryName(clusterProfileScope.Name(), cluster.Name, cluster.APIVersion == libsveltosv1alpha1.GroupVersion.String())

	clusterSummary := &configv1alpha1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterSummaryName,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: clusterProfileScope.ClusterProfile.APIVersion,
					Kind:       clusterProfileScope.ClusterProfile.Kind,
					Name:       clusterProfileScope.ClusterProfile.Name,
					UID:        clusterProfileScope.ClusterProfile.UID,
				},
			},
			// Copy annotation. Paused annotation might be set on ClusterProfile.
			Annotations: clusterProfileScope.ClusterProfile.Annotations,
		},
		Spec: configv1alpha1.ClusterSummarySpec{
			ClusterNamespace:   cluster.Namespace,
			ClusterName:        cluster.Name,
			ClusterProfileSpec: clusterProfileScope.ClusterProfile.Spec,
		},
	}

	clusterSummary.Spec.ClusterType = clusterproxy.GetClusterType(cluster)
	clusterSummary.Labels = clusterProfileScope.ClusterProfile.Labels
	r.addClusterSummaryLabels(clusterSummary, clusterProfileScope, cluster)

	return r.Create(ctx, clusterSummary)
}

// updateClusterSummary updates if necessary ClusterSummary given a ClusterProfile and a matching Sveltos/CAPI Cluster.
// If ClusterProfile.Spec.SyncMode is set to one time, nothing will happen
func (r *ClusterProfileReconciler) updateClusterSummary(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope,
	cluster *corev1.ObjectReference) error {

	if clusterProfileScope.IsOneTimeSync() {
		return nil
	}

	clusterSummary, err := getClusterSummary(ctx, r.Client, clusterProfileScope.Name(), cluster.Namespace, cluster.Name,
		clusterproxy.GetClusterType(cluster))
	if err != nil {
		return err
	}

	if reflect.DeepEqual(clusterProfileScope.ClusterProfile.Spec, clusterSummary.Spec.ClusterProfileSpec) &&
		reflect.DeepEqual(clusterProfileScope.ClusterProfile.Annotations, clusterSummary.Annotations) {
		// Nothing has changed
		return nil
	}

	clusterSummary.Annotations = clusterProfileScope.ClusterProfile.Annotations
	clusterSummary.Spec.ClusterProfileSpec = clusterProfileScope.ClusterProfile.Spec
	clusterSummary.Spec.ClusterType = clusterproxy.GetClusterType(cluster)
	r.addClusterSummaryLabels(clusterSummary, clusterProfileScope, cluster)

	return r.Update(ctx, clusterSummary)
}

func (r *ClusterProfileReconciler) addClusterSummaryLabels(clusterSummary *configv1alpha1.ClusterSummary, clusterProfileScope *scope.ClusterProfileScope,
	cluster *corev1.ObjectReference) {

	addLabel(clusterSummary, ClusterProfileLabelName, clusterProfileScope.Name())
	addLabel(clusterSummary, configv1alpha1.ClusterNameLabel, cluster.Name)
	if cluster.APIVersion == libsveltosv1alpha1.GroupVersion.String() {
		addLabel(clusterSummary, configv1alpha1.ClusterTypeLabel, string(libsveltosv1alpha1.ClusterTypeSveltos))
	} else {
		addLabel(clusterSummary, configv1alpha1.ClusterTypeLabel, string(libsveltosv1alpha1.ClusterTypeCapi))
	}
}

// deleteClusterSummary deletes ClusterSummary given a ClusterProfile and a matching Sveltos/CAPI Cluster
func (r *ClusterProfileReconciler) deleteClusterSummary(ctx context.Context,
	clusterSummary *configv1alpha1.ClusterSummary) error {

	return r.Delete(ctx, clusterSummary)
}

func (r *ClusterProfileReconciler) updateClusterSummarySyncMode(ctx context.Context,
	clusterProfile *configv1alpha1.ClusterProfile, clusterSummary *configv1alpha1.ClusterSummary) error {

	currentClusterSummary := &configv1alpha1.ClusterSummary{}
	err := r.Get(ctx, types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
		currentClusterSummary)
	if err != nil {
		return err
	}
	currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = clusterProfile.Spec.SyncMode
	return r.Update(ctx, currentClusterSummary)
}

// updateClusterConfigurations for each Sveltos/CAPI Cluster currently matching ClusterProfile:
// - creates corresponding ClusterConfiguration if one does not exist already
// - updates (eventually) corresponding ClusterConfiguration if one already exists
// Both create and update only add ClusterProfile as OwnerReference for ClusterConfiguration
func (r *ClusterProfileReconciler) updateClusterConfigurations(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope) error {
	for i := range clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs {
		cluster := clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs[i]

		// Create ClusterConfiguration if not already existing.
		err := r.createClusterConfiguration(ctx, &cluster)
		if err != nil {
			clusterProfileScope.Logger.Error(err, fmt.Sprintf("failed to create ClusterConfiguration for cluster %s/%s",
				cluster.Namespace, cluster.Name))
			return err
		}

		// Update ClusterConfiguration
		err = r.updateClusterConfiguration(ctx, clusterProfileScope, &cluster)
		if err != nil {
			clusterProfileScope.Logger.Error(err, fmt.Sprintf("failed to update ClusterConfiguration for cluster %s/%s",
				cluster.Namespace, cluster.Name))
			return err
		}
	}

	return nil
}

// createClusterConfiguration creates ClusterConfiguration given a Sveltos/CAPI Cluster.
// If already existing, return nil
func (r *ClusterProfileReconciler) createClusterConfiguration(ctx context.Context, cluster *corev1.ObjectReference) error {
	clusterConfiguration := &configv1alpha1.ClusterConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      getClusterConfigurationName(cluster.Name, clusterproxy.GetClusterType(cluster)),
			Labels: map[string]string{
				configv1alpha1.ClusterNameLabel: cluster.Name,
				configv1alpha1.ClusterTypeLabel: string(clusterproxy.GetClusterType(cluster)),
			},
		},
	}

	err := r.Create(ctx, clusterConfiguration)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
	}

	return err
}

// updateClusterConfiguration updates if necessary ClusterConfiguration given a ClusterProfile and a matching Sveltos/CAPI Cluster.
// Update consists in:
// - adding ClusterProfile as one of OwnerReferences for ClusterConfiguration
// - adding a section in Status.ClusterProfileResources for this ClusterProfile
func (r *ClusterProfileReconciler) updateClusterConfiguration(ctx context.Context, clusterProfileScope *scope.ClusterProfileScope,
	cluster *corev1.ObjectReference) error {

	clusterConfiguration, err := getClusterConfiguration(ctx, r.Client, cluster.Namespace,
		getClusterConfigurationName(cluster.Name, clusterproxy.GetClusterType(cluster)))
	if err != nil {
		return err
	}

	// add ClusterProfile as one of the ClusterConfiguration's owners
	err = r.updateClusterConfigurationOwnerReferences(ctx, clusterProfileScope.ClusterProfile, clusterConfiguration)
	if err != nil {
		return err
	}

	// add a section in ClusterConfiguration.Status.ClusterProfileResource for ClusterProfile
	err = r.updateClusterConfigurationClusterProfileResources(ctx, clusterProfileScope.ClusterProfile, clusterConfiguration)
	if err != nil {
		return err
	}

	return nil
}

// updateClusterConfigurationOwnerReferences adds clusterProfile as owner of ClusterConfiguration
func (r *ClusterProfileReconciler) updateClusterConfigurationOwnerReferences(ctx context.Context,
	clusterProfile *configv1alpha1.ClusterProfile, clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	if util.IsOwnedByObject(clusterConfiguration, clusterProfile) {
		return nil
	}

	ownerRef := metav1.OwnerReference{
		Kind:       clusterProfile.Kind,
		UID:        clusterProfile.UID,
		APIVersion: clusterProfile.APIVersion,
		Name:       clusterProfile.Name,
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, r.Client,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			return err
		}

		currentClusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences, ownerRef)
		return r.Update(ctx, currentClusterConfiguration)
	})
	return err
}

// updateClusterConfigurationClusterProfileResources adds a section for ClusterProfile in clusterConfiguration
// Status.ClusterProfileResources
func (r *ClusterProfileReconciler) updateClusterConfigurationClusterProfileResources(ctx context.Context,
	clusterProfile *configv1alpha1.ClusterProfile, clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, r.Client,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			return err
		}

		for i := range currentClusterConfiguration.Status.ClusterProfileResources {
			if currentClusterConfiguration.Status.ClusterProfileResources[i].ClusterProfileName == clusterProfile.Name {
				return nil
			}
		}

		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			currentClusterConfiguration, err = getClusterConfiguration(ctx, r.Client,
				clusterConfiguration.Namespace, clusterConfiguration.Name)
			if err != nil {
				return err
			}

			currentClusterConfiguration.Status.ClusterProfileResources = append(currentClusterConfiguration.Status.ClusterProfileResources,
				configv1alpha1.ClusterProfileResource{ClusterProfileName: clusterProfile.Name})

			return r.Status().Update(ctx, currentClusterConfiguration)
		})
		return err
	})
	return err
}

func (r *ClusterProfileReconciler) cleanMaps(clusterProfileScope *scope.ClusterProfileScope) {
	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterProfileInfo := getKeyFromObject(r.Scheme, clusterProfileScope.ClusterProfile)

	delete(r.ClusterProfileMap, *clusterProfileInfo)
	delete(r.ClusterProfiles, *clusterProfileInfo)

	for i := range r.ClusterMap {
		clusterProfileSet := r.ClusterMap[i]
		clusterProfileSet.Erase(clusterProfileInfo)
	}
}

func (r *ClusterProfileReconciler) updateMaps(clusterProfileScope *scope.ClusterProfileScope) {
	currentClusters := &libsveltosset.Set{}
	for i := range clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs {
		cluster := clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name, Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		currentClusters.Insert(clusterInfo)
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	clusterProfileInfo := getKeyFromObject(r.Scheme, clusterProfileScope.ClusterProfile)

	// Get list of Clusters not matched anymore by ClusterProfile
	var toBeRemoved []corev1.ObjectReference
	if v, ok := r.ClusterProfileMap[*clusterProfileInfo]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add ClusterProfile as consumer
	for i := range clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs {
		cluster := clusterProfileScope.ClusterProfile.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name, Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		r.getClusterMapForEntry(clusterInfo).Insert(clusterProfileInfo)
	}

	// For each Cluster not matched anymore, remove ClusterProfile as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		r.getClusterMapForEntry(&clusterName).Erase(clusterProfileInfo)
	}

	// Update list of WorklaodRoles currently referenced by ClusterSummary
	r.ClusterProfileMap[*clusterProfileInfo] = currentClusters
	r.ClusterProfiles[*clusterProfileInfo] = clusterProfileScope.ClusterProfile.Spec.ClusterSelector
}

func (r *ClusterProfileReconciler) getClusterMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ClusterMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ClusterMap[*entry] = s
	}
	return s
}

// canRemoveFinalizer returns true if there is no ClusterSummary left created by this
// ClusterProfile instance
func (r *ClusterProfileReconciler) canRemoveFinalizer(ctx context.Context,
	clusterProfileScope *scope.ClusterProfileScope,
) bool {

	return r.allClusterSummariesGone(ctx, clusterProfileScope)
}

// allClusterSummariesGone returns true if all ClusterSummaries owned by
// a clusterprofile instances are gone.
func (r *ClusterProfileReconciler) allClusterSummariesGone(ctx context.Context,
	clusterProfileScope *scope.ClusterProfileScope,
) bool {

	listOptions := []client.ListOption{
		client.MatchingLabels{ClusterProfileLabelName: clusterProfileScope.Name()},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := r.List(ctx, clusterSummaryList, listOptions...); err != nil {
		clusterProfileScope.Logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list clustersummaries. err %v", err))
		return false
	}

	return len(clusterSummaryList.Items) == 0
}
