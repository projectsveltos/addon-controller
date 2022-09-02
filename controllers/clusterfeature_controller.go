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
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2/klogr"
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
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

// ClusterFeatureReconciler reconciles a ClusterFeature object
type ClusterFeatureReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ConcurrentReconciles int
	Mux                  sync.Mutex                         // use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	ClusterMap           map[string]*Set                    // key: CAPI Cluster namespace/name; value: set of all ClusterFeatures matching the Cluster
	ClusterFeatureMap    map[string]*Set                    // key: ClusterFeature name; value: set of CAPI Clusters matched
	ClusterFeatures      map[string]configv1alpha1.Selector // key: ClusterFeature name; value ClusterFeature Selector

	// Reason for the two maps:
	// ClusterFeature, via ClusterSelector, matches CAPI Clusters based on Cluster labels.
	// When a CAPI Cluster labels change, one or more ClusterFeature needs to be reconciled.
	// In order to achieve so, ClusterFeature reconciler watches for CAPI Clusters. When a CAPI Cluster label changes,
	// find all the ClusterFeatures currently referencing it and reconcile those.
	// Problem is no I/O should be present inside a MapFunc (given a CAPI Cluster, return all the ClusterFeatures matching it).
	// In the MapFunc, if the list ClusterFeatures operation failed, we would be unable to retry or re-enqueue the rigth set of
	// ClusterFeatures.
	// Instead the approach taken is following:
	// - when a ClusterFeature is reconciled, update the ClusterFeatures amd the ClusterMap;
	// - in the MapFunc, given the CAPI Cluster that changed:
	//		* use ClusterFeatures to find all ClusterFeature matching the Cluster and reconcile those;
	// - in order to reconcile ClusterFeatures previously matching the Cluster and not anymore, use ClusterMap.
	//
	// The ClusterFeatureMap is used to update ClusterMap. Consider following scenarios to understand the need:
	// 1. ClusterFeature A references Clusters 1 and 2. When reconciled, ClusterMap will have 1 => A and 2 => A;
	// and ClusterFeatureMap A => 1,2
	// 2. Cluster 2 label changes and now ClusterFeature matches Cluster 1 only. We ned to remove the entry 2 => A in ClusterMap. But
	// when we reconcile ClusterFeature we have its current version we don't have its previous version. So we know ClusterFeature A
	// now matches CAPI Cluster 1, but we don't know it used to match CAPI Cluster 2.
	// So we use ClusterFeatureMap (at this point value stored here corresponds to reconciliation #1. We know currently
	// ClusterFeature matches CAPI Cluster 1 only and looking at ClusterFeatureMap we know it used to reference CAPI Cluster 1 and 2.
	// So we can remove 2 => A from ClusterMap. Only after this update, we update ClusterFeatureMap (so new value will be A => 1)
}

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterfeatures,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterfeatures/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterfeatures/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;update;create;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterconfigurations,verbs=get;list;update;create;watch
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;watch;list
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;watch;list

func (r *ClusterFeatureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
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

	// Handle deleted clusterFeature
	if !clusterFeature.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterFeatureScope)
	}

	// Handle non-deleted clusterFeature
	return r.reconcileNormal(ctx, clusterFeatureScope)
}

func (r *ClusterFeatureReconciler) reconcileDelete(
	ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
) (reconcile.Result, error) {

	logger := clusterFeatureScope.Logger
	logger.Info("Reconciling ClusterFeature delete")

	clusterFeatureScope.SetMatchingClusterRefs(nil)

	if err := r.cleanClusterSummaries(ctx, clusterFeatureScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterSummaries")
		return reconcile.Result{}, err
	}

	if !r.allClusterSummariesGone(ctx, clusterFeatureScope) {
		logger.V(logs.LogInfo).Info("Not all cluster summaries are gone")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	if err := r.cleanClusterConfigurations(ctx, clusterFeatureScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterConfigurations")
		return reconcile.Result{}, err
	}

	if !r.canRemoveFinalizer(ctx, clusterFeatureScope) {
		logger.V(logs.LogInfo).Info("Cannot remove finalizer yet")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	if controllerutil.ContainsFinalizer(clusterFeatureScope.ClusterFeature, configv1alpha1.ClusterFeatureFinalizer) {
		controllerutil.RemoveFinalizer(clusterFeatureScope.ClusterFeature, configv1alpha1.ClusterFeatureFinalizer)
	}

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

	clusterFeatureScope.SetMatchingClusterRefs(matchingCluster)

	r.updatesMaps(clusterFeatureScope)

	// For each matching CAPI Cluster, create/update corresponding ClusterConfiguration
	if err := r.updateClusterConfigurations(ctx, clusterFeatureScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterConfigurations")
		return reconcile.Result{}, err
	}
	// For each matching CAPI Cluster, create/update corresponding ClusterSummary
	if err := r.updateClusterSummaries(ctx, clusterFeatureScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterSummaries")
		return reconcile.Result{}, err
	}

	// For CAPI Cluster not matching ClusterFeature, deletes corresponding ClusterSummary
	if err := r.cleanClusterSummaries(ctx, clusterFeatureScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterSummaries")
		return reconcile.Result{}, err
	}
	// For CAPI Cluster not matching ClusterFeature, removes ClusterFeature as OwnerReference
	// from corresponding ClusterConfiguration
	if err := r.cleanClusterConfigurations(ctx, clusterFeatureScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterConfigurations")
		return reconcile.Result{}, err
	}

	logger.Info("Reconcile success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterFeatureReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterFeature{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "error creating controller")
	}

	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterFeatures need to be reconciled.
	if err := c.Watch(&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterFeatureForCluster),
		ClusterPredicates(klogr.New().WithValues("predicate", "clusterpredicate")),
	); err != nil {
		return err
	}

	// When cluster-api machine changes, according to ClusterPredicates,
	// one or more ClusterFeatures need to be reconciled.
	return c.Watch(&source.Kind{Type: &clusterv1.Machine{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterFeatureForMachine),
		MachinePredicates(klogr.New().WithValues("predicate", "machinepredicate")),
	)
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

		if !cluster.DeletionTimestamp.IsZero() {
			// Only existing cluster can match
			continue
		}

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
	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs {
		cluster := clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs[i]
		ready, err := r.isClusterReadyToBeConfigured(ctx, clusterFeatureScope, &cluster)
		if err != nil {
			return err
		}
		if !ready {
			clusterFeatureScope.Logger.V(logs.LogDebug).Info(fmt.Sprintf("Cluster %s/%s is not ready yet",
				cluster.Namespace, cluster.Name))
			continue
		}

		// ClusterFeature does not look at whether Cluster is paused or not.
		// If a Cluster exists and it is a match, ClusterSummary is created (and ClusterSummary.Spec kept in sync if mode is
		// continuous).
		// ClusterSummary won't program cluster in paused state.

		_, err = getClusterSummary(ctx, r.Client, clusterFeatureScope.Name(), cluster.Namespace, cluster.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				err = r.createClusterSummary(ctx, clusterFeatureScope, &cluster)
				if err != nil {
					clusterFeatureScope.Logger.Error(err, fmt.Sprintf("failed to create ClusterSummary for cluster %s/%s",
						cluster.Namespace, cluster.Name))
				}
			} else {
				clusterFeatureScope.Logger.Error(err, "failed to get ClusterSummary for cluster %s/%s",
					cluster.Namespace, cluster.Name)
				return err
			}
		} else {
			err = r.updateClusterSummary(ctx, clusterFeatureScope, &cluster)
			if err != nil {
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

	getClusterInfo := func(clusterNamespace, clusterName string) string {
		return fmt.Sprintf("%s-%s", clusterNamespace, clusterName)
	}

	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs {
		reference := clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs[i]
		clusterName := getClusterInfo(reference.Namespace, reference.Name)
		matching[clusterName] = true
	}

	listOptions := []client.ListOption{
		client.MatchingLabels{
			ClusterFeatureLabelName: clusterFeatureScope.Name(),
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := r.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return err
	}

	for i := range clusterSummaryList.Items {
		cs := &clusterSummaryList.Items[i]
		if util.IsOwnedByObject(cs, clusterFeatureScope.ClusterFeature) {
			if _, ok := matching[getClusterInfo(cs.Spec.ClusterNamespace, cs.Spec.ClusterName)]; !ok {
				if err := r.deleteClusterSummary(ctx, clusterFeatureScope, cs); err != nil {
					clusterFeatureScope.Logger.Error(err, "failed to update ClusterSummary for cluster %s/%s",
						cs.Namespace, cs.Name)
					return err
				}
			}
		}
	}

	return nil
}

// cleanClusterConfigurations finds all ClusterConfigurations currently owned by ClusterFeature.
// For each such ClusterConfigurations:
// - remove ClusterFeature as OwnerReference
// -if no more OwnerReferences are left, delete ClusterConfigurations
func (r *ClusterFeatureReconciler) cleanClusterConfigurations(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope) error {
	clusterConfiguratioList := &configv1alpha1.ClusterConfigurationList{}

	if !r.allClusterSummariesGone(ctx, clusterFeatureScope) {
		return fmt.Errorf("not all ClusterSummaries owned by ClusterFeature are gone. Wait")
	}

	matchingClusterMap := make(map[string]bool)

	info := func(namespace, name string) string {
		return fmt.Sprintf("%s--%s", namespace, name)
	}

	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs {
		ref := &clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs[i]
		matchingClusterMap[info(ref.Namespace, ref.Name)] = true
	}

	err := r.List(ctx, clusterConfiguratioList)
	if err != nil {
		return err
	}

	for i := range clusterConfiguratioList.Items {
		cc := &clusterConfiguratioList.Items[i]

		// If CAPI Cluster is still a match, continue (don't remove ClusterFeature as OwnerReference)
		if _, ok := matchingClusterMap[info(cc.Namespace, cc.Name)]; ok {
			continue
		}

		err = r.cleanClusterConfiguration(ctx, clusterFeatureScope.ClusterFeature, cc)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *ClusterFeatureReconciler) cleanClusterConfiguration(ctx context.Context, clusterFeature *configv1alpha1.ClusterFeature,
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	// remove ClusterFeature as one of the ClusterConfiguration's owners
	err := r.cleanClusterConfigurationOwnerReferences(ctx, clusterFeature, clusterConfiguration)
	if err != nil {
		return err
	}

	// remove the section in ClusterConfiguration.Status.ClusterFeatureResource used for this ClusterFeature
	err = r.cleanClusterConfigurationClusterFeatureResources(ctx, clusterFeature, clusterConfiguration)
	if err != nil {
		return err
	}

	return nil
}

func (r *ClusterFeatureReconciler) cleanClusterConfigurationOwnerReferences(ctx context.Context, clusterFeature *configv1alpha1.ClusterFeature,
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	ownerRef := metav1.OwnerReference{
		Kind:       clusterFeature.Kind,
		UID:        clusterFeature.UID,
		APIVersion: clusterFeature.APIVersion,
		Name:       clusterFeature.Name,
	}

	if !util.IsOwnedByObject(clusterConfiguration, clusterFeature) {
		return nil
	}

	clusterConfiguration.OwnerReferences = util.RemoveOwnerRef(clusterConfiguration.OwnerReferences, ownerRef)
	if len(clusterConfiguration.OwnerReferences) == 0 {
		return r.Delete(ctx, clusterConfiguration)
	} else {
		return r.Update(ctx, clusterConfiguration)
	}
}

func (r *ClusterFeatureReconciler) cleanClusterConfigurationClusterFeatureResources(ctx context.Context, clusterFeature *configv1alpha1.ClusterFeature,
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, r.Client,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			// If ClusterConfiguration is not found, nothing to do here.
			// ClusterConfiguration is removed if ClusterFeature was the last owner.
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		toBeUpdated := false
		for i := range currentClusterConfiguration.Status.ClusterFeatureResources {
			if currentClusterConfiguration.Status.ClusterFeatureResources[i].ClusterFeatureName != clusterFeature.Name {
				continue
			}
			// Order is not important. So move the element at index i with last one in order to avoid moving all elements.
			length := len(currentClusterConfiguration.Status.ClusterFeatureResources)
			currentClusterConfiguration.Status.ClusterFeatureResources[i] = currentClusterConfiguration.Status.ClusterFeatureResources[length-1]
			currentClusterConfiguration.Status.ClusterFeatureResources = currentClusterConfiguration.Status.ClusterFeatureResources[:length-1]
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

// createClusterSummary creates ClusterSummary given a ClusterFeature and a matching CAPI Cluster
func (r *ClusterFeatureReconciler) createClusterSummary(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope,
	cluster *corev1.ObjectReference) error {

	clusterSummaryName := GetClusterSummaryName(clusterFeatureScope.Name(), cluster.Namespace, cluster.Name)

	clusterSummary := &configv1alpha1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterSummaryName,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: clusterFeatureScope.ClusterFeature.APIVersion,
					Kind:       clusterFeatureScope.ClusterFeature.Kind,
					Name:       clusterFeatureScope.ClusterFeature.Name,
					UID:        clusterFeatureScope.ClusterFeature.UID,
				},
			},
			// Copy annotation. Paused annotation might be set on ClusterFeature.
			Annotations: clusterFeatureScope.ClusterFeature.Annotations,
		},
		Spec: configv1alpha1.ClusterSummarySpec{
			ClusterNamespace:   cluster.Namespace,
			ClusterName:        cluster.Name,
			ClusterFeatureSpec: clusterFeatureScope.ClusterFeature.Spec,
		},
	}

	addLabel(clusterSummary, ClusterFeatureLabelName, clusterFeatureScope.Name())
	addLabel(clusterSummary, ClusterLabelNamespace, cluster.Namespace)
	addLabel(clusterSummary, ClusterLabelName, cluster.Name)

	return r.Create(ctx, clusterSummary)
}

// updateClusterSummary updates if necessary ClusterSummary given a ClusterFeature and a matching CAPI Cluster.
// If ClusterFeature.Spec.SyncMode is set to one time, nothing will happen
func (r *ClusterFeatureReconciler) updateClusterSummary(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope,
	cluster *corev1.ObjectReference) error {

	if !clusterFeatureScope.IsContinuousSync() {
		return nil
	}

	clusterSummary, err := getClusterSummary(ctx, r.Client, clusterFeatureScope.Name(), cluster.Namespace, cluster.Name)
	if err != nil {
		return err
	}

	if reflect.DeepEqual(clusterFeatureScope.ClusterFeature.Spec, clusterSummary.Spec.ClusterFeatureSpec) &&
		reflect.DeepEqual(clusterFeatureScope.ClusterFeature.Annotations, clusterSummary.Annotations) {
		// Nothing has changed
		return nil
	}

	clusterSummary.Annotations = clusterFeatureScope.ClusterFeature.Annotations
	clusterSummary.Spec.ClusterFeatureSpec = clusterFeatureScope.ClusterFeature.Spec
	return r.Update(ctx, clusterSummary)
}

// deleteClusterSummary deletes ClusterSummary given a ClusterFeature and a matching CAPI Cluster
func (r *ClusterFeatureReconciler) deleteClusterSummary(ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
	clusterSummary *configv1alpha1.ClusterSummary) error {

	return r.Delete(ctx, clusterSummary)
}

// updateClusterConfigurations for each CAPI Cluster currently matching ClusterFeature:
// - creates corresponding ClusterConfiguration if one does not exist already
// - updates (eventually) corresponding ClusterConfiguration if one already exists
// Both create and update only add ClusterFeature as OwnerReference for ClusterConfiguration
func (r *ClusterFeatureReconciler) updateClusterConfigurations(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope) error {
	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs {
		cluster := clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs[i]

		// Create ClusterConfiguration if not already existing.
		err := r.createClusterConfiguration(ctx, &cluster)
		if err != nil {
			clusterFeatureScope.Logger.Error(err, fmt.Sprintf("failed to create ClusterConfiguration for cluster %s/%s",
				cluster.Namespace, cluster.Name))
			return err
		}

		// Update ClusterConfiguration
		err = r.updateClusterConfiguration(ctx, clusterFeatureScope, &cluster)
		if err != nil {
			clusterFeatureScope.Logger.Error(err, fmt.Sprintf("failed to update ClusterConfiguration for cluster %s/%s",
				cluster.Namespace, cluster.Name))
			return err
		}
	}

	return nil
}

// createClusterConfiguration creates ClusterConfiguration given a CAPI Cluster.
// If already existing, return nil
func (r *ClusterFeatureReconciler) createClusterConfiguration(ctx context.Context, cluster *corev1.ObjectReference) error {
	clusterConfiguration := &configv1alpha1.ClusterConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name,
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

// updateClusterConfiguration updates if necessary ClusterConfiguration given a ClusterFeature and a matching CAPI Cluster.
// Update consists in:
// - adding ClusterFeature as one of OwnerReferences for ClusterConfiguration
// - adding a section in Status.ClusterFeatureResources for this ClusterFeature
func (r *ClusterFeatureReconciler) updateClusterConfiguration(ctx context.Context, clusterFeatureScope *scope.ClusterFeatureScope,
	cluster *corev1.ObjectReference) error {

	clusterConfiguration, err := getClusterConfiguration(ctx, r.Client, cluster.Namespace, cluster.Name)
	if err != nil {
		return err
	}

	// add ClusterFeature as one of the ClusterConfiguration's owners
	err = r.updateClusterConfigurationOwnerReferences(ctx, clusterFeatureScope.ClusterFeature, clusterConfiguration)
	if err != nil {
		return err
	}

	// add a section in ClusterConfiguration.Status.ClusterFeatureResource for ClusterFeature
	err = r.updateClusterConfigurationClusterFeatureResources(ctx, clusterFeatureScope.ClusterFeature, clusterConfiguration)
	if err != nil {
		return err
	}

	return nil
}

// updateClusterConfigurationOwnerReferences adds clusterFeature as owner of ClusterConfiguration
func (r *ClusterFeatureReconciler) updateClusterConfigurationOwnerReferences(ctx context.Context,
	clusterFeature *configv1alpha1.ClusterFeature, clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	if util.IsOwnedByObject(clusterConfiguration, clusterFeature) {
		return nil
	}

	ownerRef := metav1.OwnerReference{
		Kind:       clusterFeature.Kind,
		UID:        clusterFeature.UID,
		APIVersion: clusterFeature.APIVersion,
		Name:       clusterFeature.Name,
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

// updateClusterConfigurationClusterFeatureResources adds a section for ClusterFeature in clusterConfiguration
// Status.ClusterFeatureResources
func (r *ClusterFeatureReconciler) updateClusterConfigurationClusterFeatureResources(ctx context.Context,
	clusterFeature *configv1alpha1.ClusterFeature, clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	currentClusterConfiguration, err := getClusterConfiguration(ctx, r.Client,
		clusterConfiguration.Namespace, clusterConfiguration.Name)
	if err != nil {
		return err
	}

	for i := range currentClusterConfiguration.Status.ClusterFeatureResources {
		if currentClusterConfiguration.Status.ClusterFeatureResources[i].ClusterFeatureName == clusterFeature.Name {
			return nil
		}
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err = getClusterConfiguration(ctx, r.Client,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			return err
		}

		currentClusterConfiguration.Status.ClusterFeatureResources = append(currentClusterConfiguration.Status.ClusterFeatureResources,
			configv1alpha1.ClusterFeatureResource{ClusterFeatureName: clusterFeature.Name})

		return r.Status().Update(ctx, currentClusterConfiguration)
	})
	return err
}

// isClusterReadyToBeConfigured gets all Machines for a given CAPI Cluster and returns true
// if at least one control plane machine is in running phase
func (r *ClusterFeatureReconciler) isClusterReadyToBeConfigured(
	ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
	cluster *corev1.ObjectReference,
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
	cluster *corev1.ObjectReference,
) (*clusterv1.MachineList, error) {

	listOptions := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{clusterv1.ClusterLabelName: cluster.Name},
	}
	var machineList clusterv1.MachineList
	if err := r.Client.List(ctx, &machineList, listOptions...); err != nil {
		clusterFeatureScope.Error(err, fmt.Sprintf("unable to list Machines for CAPI Cluster %s/%s",
			cluster.Namespace, cluster.Name))
		return nil, err
	}
	clusterFeatureScope.V(logs.LogDebug).Info(fmt.Sprintf("Found %d machine", len(machineList.Items)))

	return &machineList, nil
}

func (r *ClusterFeatureReconciler) updatesMaps(clusterFeatureScope *scope.ClusterFeatureScope) {
	currentClusters := &Set{}
	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs {
		cluster := clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs[i]
		clusterName := cluster.Namespace + "/" + cluster.Name
		currentClusters.insert(clusterName)
	}

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Get list of Clusters not matched anymore by ClusterFeature
	var toBeRemoved []string
	if v, ok := r.ClusterFeatureMap[clusterFeatureScope.Name()]; ok {
		toBeRemoved = v.difference(currentClusters)
	}

	// For each currently matching Cluster, add ClusterFeature as consumer
	for i := range clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs {
		cluster := clusterFeatureScope.ClusterFeature.Status.MatchingClusterRefs[i]
		clusterName := cluster.Namespace + "/" + cluster.Name
		r.getClusterMapForEntry(clusterName).insert(clusterFeatureScope.Name())
	}

	// For each Cluster not matched anymore, remove ClusterFeature as consumer
	for i := range toBeRemoved {
		clusterName := toBeRemoved[i]
		r.getClusterMapForEntry(clusterName).erase(clusterFeatureScope.Name())
	}

	// Update list of WorklaodRoles currently referenced by ClusterSummary
	r.ClusterFeatureMap[clusterFeatureScope.Name()] = currentClusters
	r.ClusterFeatures[clusterFeatureScope.Name()] = clusterFeatureScope.ClusterFeature.Spec.ClusterSelector
}

func (r *ClusterFeatureReconciler) getClusterMapForEntry(entry string) *Set {
	s := r.ClusterMap[entry]
	if s == nil {
		s = &Set{}
		r.ClusterMap[entry] = s
	}
	return s
}

// canRemoveFinalizer returns true if there is no ClusterSummary left created by this
// ClusterFeature instance
func (r *ClusterFeatureReconciler) canRemoveFinalizer(ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
) bool {

	return r.allClusterSummariesGone(ctx, clusterFeatureScope)
}

// allClusterSummariesGone returns true if all ClusterSummaries owned by
// a clusterfeature instances are gone.
func (r *ClusterFeatureReconciler) allClusterSummariesGone(ctx context.Context,
	clusterFeatureScope *scope.ClusterFeatureScope,
) bool {

	listOptions := []client.ListOption{
		client.MatchingLabels{ClusterFeatureLabelName: clusterFeatureScope.Name()},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := r.List(ctx, clusterSummaryList, listOptions...); err != nil {
		clusterFeatureScope.Logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list clustersummaries. err %v", err))
		return false
	}

	return len(clusterSummaryList.Items) == 0
}
