/*
Copyright 2022-23. projectsveltos.io. All rights reserved.

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
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/annotations"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	"github.com/projectsveltos/addon-manager/controllers/chartmanager"
	"github.com/projectsveltos/addon-manager/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

const (
	// deleteRequeueAfter is how long to wait before checking again to see if the cluster still has
	// children during deletion.
	deleteRequeueAfter = 20 * time.Second

	// normalRequeueAfter is how long to wait before checking again to see if the cluster can be moved
	// to ready after or workload features (for instance ingress or reporter) have failed
	normalRequeueAfter = 20 * time.Second
)

type ReportMode int

const (
	// Default mode. In this mode, addon-manager running
	// in the management cluster periodically collects/processes
	// ResourceSummaries from Sveltos/CAPI clusters
	CollectFromManagementCluster ReportMode = iota

	// In this mode, drift detection manager sends ResourceSummaries
	// updates to management cluster.
	AgentSendUpdatesNoGateway
)

// ClusterSummaryReconciler reconciles a ClusterSummary object
type ClusterSummaryReconciler struct {
	*rest.Config
	client.Client
	Scheme               *runtime.Scheme
	ReportMode           ReportMode
	Deployer             deployer.DeployerInterface
	ConcurrentReconciles int
	PolicyMux            sync.Mutex                                    // use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	ReferenceMap         map[corev1.ObjectReference]*libsveltosset.Set // key: Referenced object; value: set of all ClusterSummaries referencing the resource
	ClusterSummaryMap    map[types.NamespacedName]*libsveltosset.Set   // key: ClusterSummary namespace/name; value: set of referenced resources

	// key: Sveltos/CAPI Cluster; value: set of all ClusterSummaries for that Cluster
	ClusterMap map[corev1.ObjectReference]*libsveltosset.Set

	// Reason for the two maps:
	// ClusterSummary references ConfigMaps/Secrets containing policies to be deployed in a Sveltos/CAPI Cluster.
	// When a ConfigMap/Secret changes, all the ClusterSummaries referencing it need to be reconciled.
	// In order to achieve so, ClusterSummary reconciler could watch for ConfigMaps/Secrets. When a ConfigMap/Secret spec changes,
	// find all the ClusterSummaries currently referencing it and reconcile those. Problem is no I/O should be present inside a MapFunc
	// (given a ConfigMap/Secret, return all the ClusterSummary referencing such ConfigMap/Secret).
	// In the MapFunc, if the list ClusterSummaries operation failed, we would be unable to retry or re-enqueue the ClusterSummaries
	// referencing the ConfigMap that changed.
	// Instead the approach taken is following:
	// - when a ClusterSummary is reconciled, update the ReferenceMap;
	// - in the MapFunc, given the ConfigMap/Secret that changed, we can immeditaly get all the ClusterSummaries needing a reconciliation (by
	// using the ReferenceMap);
	// - if a ClusterSummary is referencing a ConfigMap/Secret but its reconciliation is still queued, when ConfigMap/Secret changes,
	// ReferenceMap won't have such ClusterSummary. This is not a problem as ClusterSummary reconciliation is already queued and will happen.
	//
	// The ClusterSummaryMap is used to update ReferenceMap. Consider following scenarios to understand the need:
	// 1. ClusterSummary A references ConfigMaps 1 and 2. When reconciled, ReferenceMap will have 1 => A and 2 => A;
	// and ClusterSummaryMap A => 1,2
	// 2. ClusterSummary A changes and now references ConfigMap 1 only. We ned to remove the entry 2 => A in ReferenceMap. But
	// when we reconcile ClusterSummary we have its current version we don't have its previous version. So we use ClusterSummaryMap (at this
	// point value stored here corresponds to reconciliation #1. We know currently ClusterSummary references ConfigMap 1 only and looking
	// at ClusterSummaryMap we know it used to reference ConfigMap 1 and 2. So we can remove 2 => A from ReferenceMap. Only after this
	// update, we update ClusterSummaryMap (so new value will be A => 1)
}

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterconfigurations/status,verbs=get;list;update
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterreports,verbs=get;list;watch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterreports/status,verbs=get;list;update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kubeadmcontrolplanes,verbs=get;watch;list
//+kubebuilder:rbac:groups="infrastructure.cluster.x-k8s.io",resources="*",verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=gitrepositories,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=gitrepositories/status,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=ocirepositories,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=ocirepositories/status,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=buckets,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=buckets/status,verbs=get;watch;list

func (r *ClusterSummaryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

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

	// Fetch the ClusterProfile.
	clusterProfile, err := configv1alpha1.GetClusterProfileOwner(ctx, r.Client, clusterSummary)
	if err != nil {
		logger.Error(err, "Failed to get owner clusterProfile")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to get owner clusterProfile for %s",
			req.NamespacedName,
		)
	}
	if clusterProfile == nil {
		logger.Error(err, "Failed to get owner clusterProfile")
		return reconcile.Result{}, fmt.Errorf("failed to get owner clusterProfile for %s",
			req.NamespacedName)
	}

	clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
		Client:         r.Client,
		Logger:         logger,
		ClusterSummary: clusterSummary,
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

	// Always close the scope when exiting this function so we can persist any ClusterSummary
	// changes.
	defer func() {
		if err := clusterSummaryScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted clusterSummary
	if !clusterSummary.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterSummaryScope, logger)
	}

	// Handle non-deleted clusterSummary
	return r.reconcileNormal(ctx, clusterSummaryScope, logger)
}

func (r *ClusterSummaryReconciler) reconcileDelete(
	ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger,
) (reconcile.Result, error) {

	logger.V(logs.LogInfo).Info("Reconciling ClusterSummary delete")

	paused, err := r.isPaused(ctx, clusterSummaryScope.ClusterSummary)
	if err != nil {
		return reconcile.Result{}, err
	}
	if paused {
		logger.V(logs.LogInfo).Info("cluster is paused. Do nothing.")
		return reconcile.Result{}, nil
	}

	err = r.removeResourceSummary(ctx, clusterSummaryScope, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to remove ResourceSummary.")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	err = r.undeploy(ctx, clusterSummaryScope, logger)
	if err != nil {
		// In DryRun mode it is expected to always get an error back
		if !clusterSummaryScope.IsDryRunSync() {
			logger.V(logs.LogInfo).Error(err, "failed to undeploy")
			return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
		}
	}

	if !r.canRemoveFinalizer(ctx, clusterSummaryScope, logger) {
		logger.V(logs.LogInfo).Error(err, "cannot remove finalizer yet")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	// Cluster is deleted so remove the finalizer.
	logger.V(logs.LogInfo).Info("Removing finalizer")
	if controllerutil.ContainsFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer) {
		if finalizersUpdated := controllerutil.RemoveFinalizer(clusterSummaryScope.ClusterSummary,
			configv1alpha1.ClusterSummaryFinalizer); !finalizersUpdated {
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer")
		}
	}

	if err := r.deleteChartMap(ctx, clusterSummaryScope, logger); err != nil {
		return reconcile.Result{}, err
	}

	r.cleanMaps(clusterSummaryScope)

	logger.V(logs.LogInfo).Info("Reconcile delete success")

	return reconcile.Result{}, nil
}

func (r *ClusterSummaryReconciler) reconcileNormal(
	ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger,
) (reconcile.Result, error) {

	logger.V(logs.LogInfo).Info("Reconciling ClusterSummary")

	if !controllerutil.ContainsFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer) {
		if err := r.addFinalizer(ctx, clusterSummaryScope); err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to add finalizer")
			return reconcile.Result{}, err
		}
	}

	if !r.shouldReconcile(clusterSummaryScope, logger) {
		logger.V(logs.LogInfo).Info("ClusterSummary does not need a reconciliation")
		return reconcile.Result{}, nil
	}

	if err := r.updateMaps(ctx, clusterSummaryScope, logger); err != nil {
		logger.V(logs.LogInfo).Info("Failed to update maps")
		return reconcile.Result{}, nil
	}

	paused, err := r.isPaused(ctx, clusterSummaryScope.ClusterSummary)
	if err != nil {
		return reconcile.Result{}, err
	}
	if paused {
		logger.V(logs.LogInfo).Info("cluster is paused. Do nothing.")
		return reconcile.Result{}, nil
	}

	if err := r.updateChartMap(ctx, clusterSummaryScope, logger); err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	if err := r.deploy(ctx, clusterSummaryScope, logger); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to deploy")
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	logger.V(logs.LogInfo).Info("Reconciling ClusterSummary success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSummaryReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager,
) (controller.Controller, error) {

	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterSummary{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// At this point we don't know yet whether CAPI is present in the cluster.
	// Later on, in main, we detect that and if CAPI is present WatchForCAPI will be invoked.

	// When Sveltos Cluster changes (from paused to unpaused), one or more ClusterSummaries
	// need to be reconciled.
	err = c.Watch(&source.Kind{Type: &libsveltosv1alpha1.SveltosCluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForCluster),
		SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	)
	if err != nil {
		return nil, err
	}

	// When ConfigMap changes, according to ConfigMapPredicates,
	// one or more ClusterSummaries need to be reconciled.
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForReference),
		ConfigMapPredicates(mgr.GetLogger().WithValues("predicate", "configmappredicate")),
	)
	if err != nil {
		return nil, err
	}

	// When Secret changes, according to SecretPredicates,
	// one or more ClusterSummaries need to be reconciled.
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForReference),
		SecretPredicates(mgr.GetLogger().WithValues("predicate", "secretpredicate")),
	)

	if r.ReportMode == CollectFromManagementCluster {
		go collectAndProcessResourceSummaries(ctx, mgr.GetClient(), mgr.GetLogger())
	}

	initializeManager(ctrl.Log.WithName("watchers"), mgr.GetConfig(), mgr.GetClient())

	return c, err
}

func (r *ClusterSummaryReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	// When CAPI Cluster changes (from paused to unpaused), one or more ClusterSummaries
	// need to be reconciled.
	return c.Watch(&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForCluster),
		ClusterPredicates(mgr.GetLogger().WithValues("predicate", "clusterpredicate")),
	)
}

func (r *ClusterSummaryReconciler) WatchForFlux(mgr ctrl.Manager, c controller.Controller) error {
	// When a Flux source (GitRepository/OCIRepository/Bucket) changes, one or more ClusterSummaries
	// need to be reconciled.

	err := c.Watch(&source.Kind{Type: &sourcev1.GitRepository{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForFluxSource),
		FluxSourcePredicates(mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")),
	)
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &sourcev1b2.OCIRepository{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForFluxSource),
		FluxSourcePredicates(mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")),
	)
	if err != nil {
		return err
	}

	return c.Watch(&source.Kind{Type: &sourcev1b2.Bucket{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForFluxSource),
		FluxSourcePredicates(mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")),
	)
}

func (r *ClusterSummaryReconciler) addFinalizer(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope) error {
	// If the SveltosCluster doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer)
	// Register the finalizer immediately to avoid orphaning clusterprofile resources on delete
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
	clusterSummary := clusterSummaryScope.ClusterSummary
	logger = logger.WithValues("clusternamespace", clusterSummary.Spec.ClusterNamespace, "clustername", clusterSummary.Spec.ClusterName)

	coreResourceErr := r.deployResources(ctx, clusterSummaryScope, logger)

	helmErr := r.deployHelm(ctx, clusterSummaryScope, logger)

	kustomizeError := r.deployKustomizeRefs(ctx, clusterSummaryScope, logger)

	if coreResourceErr != nil {
		return coreResourceErr
	}

	if helmErr != nil {
		return helmErr
	}

	if kustomizeError != nil {
		return kustomizeError
	}

	return nil
}

func (r *ClusterSummaryReconciler) deployKustomizeRefs(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs == nil {
		logger.V(logs.LogDebug).Info("no policy configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope, configv1alpha1.FeatureKustomize) {
			logger.V(logs.LogDebug).Info("no policy status. Do not reconcile this")
			return nil
		}
	}

	f := getHandlersForFeature(configv1alpha1.FeatureKustomize)

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployResources(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs == nil {
		logger.V(logs.LogDebug).Info("no policy configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope, configv1alpha1.FeatureResources) {
			logger.V(logs.LogDebug).Info("no policy status. Do not reconcile this")
			return nil
		}
	}

	f := getHandlersForFeature(configv1alpha1.FeatureResources)

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployHelm(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts == nil {
		logger.V(logs.LogDebug).Info("no helm configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope, configv1alpha1.FeatureHelm) {
			logger.V(logs.LogDebug).Info("no helm status. Do not reconcile this")
			return nil
		}
	}

	f := getHandlersForFeature(configv1alpha1.FeatureHelm)

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) isClusterPresent(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope) (bool, error) {
	var err error
	cs := clusterSummaryScope.ClusterSummary

	_, err = clusterproxy.GetCluster(ctx, r.Client, cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cs.Spec.ClusterType)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
	}

	return true, err
}

func (r *ClusterSummaryReconciler) undeploy(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	// If Sveltos/CAPI Cluster is not found, there is nothing to clean up.
	isPresent, err := r.isClusterPresent(ctx, clusterSummaryScope)
	if err != nil {
		return err
	}
	if !isPresent {
		return nil
	}

	resourceErr := r.undeployResources(ctx, clusterSummaryScope, logger)

	kustomizeResourceErr := r.undeployKustomizeResources(ctx, clusterSummaryScope, logger)

	helmErr := r.undeployHelm(ctx, clusterSummaryScope, logger)

	if resourceErr != nil {
		return resourceErr
	}

	if kustomizeResourceErr != nil {
		return kustomizeResourceErr
	}

	if helmErr != nil {
		return helmErr
	}

	return nil
}

func (r *ClusterSummaryReconciler) undeployResources(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := getHandlersForFeature(configv1alpha1.FeatureResources)
	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeployKustomizeResources(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := getHandlersForFeature(configv1alpha1.FeatureKustomize)
	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeployHelm(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := getHandlersForFeature(configv1alpha1.FeatureHelm)
	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) updateChartMap(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) error {

	// When in DryRun mode, ClusterSummary won't update (install/upgrade/uninstall) any helm chart.
	// So it does not update helm chart registration. Whatever registrations it had, are still there (if it was
	// managing an helm chart, that information still holds as dryrun means change nothing).
	// Let's say currently no ClusterProfile is managing an helm chart, if we allowed a ClusterSummary in DryRun to
	// register then:
	// 1) this ClusterSummary would be elected as manager
	// 2) ClusterSummary is in DryRun mode so it actually won't deploy anything
	// 3) If another ClusterProfile in not DryRun mode tried to manage same helm chart, it would not be allowed.
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return nil
	}

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, r.Client)
	if err != nil {
		return err
	}

	// First try to be elected manager. Only if that succeeds, manage an helm chart.
	logger.V(logs.LogDebug).Info("register clustersummary with helm chart manager")
	chartManager.RegisterClusterSummaryForCharts(clusterSummaryScope.ClusterSummary)

	// Registration for helm chart not referenced anymore, are cleaned only after such helm
	// chart are removed from Sveltos/CAPI Cluster. That is done as part of deployHelmCharts and
	// undeployHelmCharts (RemoveStaleRegistrations).
	// That is because we need to make sure managed helm charts are successfully uninstalled
	// before any registration with chartManager is cleared.

	return nil
}

// deleteChartMap removes any registration with chartManager.
// Call it only when ClusterSummary is ready to be deleted (finalizer is removed)
func (r *ClusterSummaryReconciler) deleteChartMap(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) error {

	chartManager, err := chartmanager.GetChartManagerInstance(ctx, r.Client)
	if err != nil {
		return err
	}

	logger.V(logs.LogDebug).Info("remove clustersummary with helm chart manager")
	chartManager.RemoveAllRegistrations(clusterSummaryScope.ClusterSummary)

	return nil
}

func (r *ClusterSummaryReconciler) cleanMaps(clusterSummaryScope *scope.ClusterSummaryScope) {
	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	delete(r.ClusterSummaryMap, types.NamespacedName{Namespace: clusterSummaryScope.Namespace(), Name: clusterSummaryScope.Name()})

	clusterSummaryInfo := getKeyFromObject(r.Scheme, clusterSummaryScope.ClusterSummary)

	for i := range r.ClusterMap {
		clusterSummarySet := r.ClusterMap[i]
		clusterSummarySet.Erase(clusterSummaryInfo)
	}

	for i := range r.ReferenceMap {
		clusterSummarySet := r.ReferenceMap[i]
		clusterSummarySet.Erase(clusterSummaryInfo)
	}
}

func (r *ClusterSummaryReconciler) updateMaps(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) error {

	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeOneTime {
		logger.V(logs.LogDebug).Info("sync mode is one time. No need to reconcile on policies change.")
		return nil
	}
	logger.V(logs.LogDebug).Info("update policy map")
	currentReferences := r.getCurrentReferences(clusterSummaryScope)

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	cs := clusterSummaryScope.ClusterSummary
	clusterObject, err := clusterproxy.GetCluster(ctx, r.Client, cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cs.Spec.ClusterType)
	if err != nil {
		return err
	}

	clusterInfo := getKeyFromObject(r.Scheme, clusterObject)

	clusterSummaryInfo := corev1.ObjectReference{APIVersion: configv1alpha1.GroupVersion.String(),
		Kind: configv1alpha1.ClusterProfileKind, Namespace: clusterSummaryScope.Namespace(),
		Name: clusterSummaryScope.Name()}
	r.getClusterMapForEntry(clusterInfo).Insert(&clusterSummaryInfo)

	// Get list of References not referenced anymore by ClusterSummary
	var toBeRemoved []corev1.ObjectReference
	clusterSummaryName := types.NamespacedName{Namespace: clusterSummaryScope.Namespace(), Name: clusterSummaryScope.Name()}
	if v, ok := r.ClusterSummaryMap[clusterSummaryName]; ok {
		toBeRemoved = v.Difference(currentReferences)
	}

	// For each currently referenced instance, add ClusterSummary as consumer
	for _, referencedResource := range currentReferences.Items() {
		tmpResource := referencedResource
		r.getReferenceMapForEntry(&tmpResource).Insert(
			&corev1.ObjectReference{
				APIVersion: configv1alpha1.GroupVersion.String(),
				Kind:       configv1alpha1.ClusterSummaryKind,
				Namespace:  clusterSummaryScope.Namespace(),
				Name:       clusterSummaryScope.Name(),
			},
		)
	}

	// For each resource not reference anymore, remove ClusterSummary as consumer
	for i := range toBeRemoved {
		referencedResource := toBeRemoved[i]
		r.getReferenceMapForEntry(&referencedResource).Erase(
			&corev1.ObjectReference{
				APIVersion: configv1alpha1.GroupVersion.String(),
				Kind:       configv1alpha1.ClusterSummaryKind,
				Namespace:  clusterSummaryScope.Namespace(),
				Name:       clusterSummaryScope.Name(),
			},
		)
	}

	// Update list of resources currently referenced by ClusterSummary
	r.ClusterSummaryMap[clusterSummaryName] = currentReferences
	return nil
}

func (r *ClusterSummaryReconciler) getClusterMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ClusterMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ClusterMap[*entry] = s
	}
	return s
}

// shouldReconcile returns true if a reconciliation is needed.
// When syncMode is set to one time, if features are marked as provisioned, no reconciliation is needed.
func (r *ClusterSummaryReconciler) shouldReconcile(clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) bool {
	clusterSummary := clusterSummaryScope.ClusterSummary

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuous ||
		clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeContinuousWithDriftDetection {

		logger.V(logs.LogDebug).Info(fmt.Sprintf("Mode set to %s. Reconciliation is needed.",
			clusterSummary.Spec.ClusterProfileSpec.SyncMode))
		return true
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		logger.V(logs.LogDebug).Info("Mode set to dryRun. Reconciliation is needed.")
		return true
	}

	if len(clusterSummary.Spec.ClusterProfileSpec.PolicyRefs) != 0 {
		if !r.isFeatureDeployed(clusterSummaryScope, configv1alpha1.FeatureResources) {
			logger.V(logs.LogDebug).Info("Mode set to one time. Resources not deployed yet. Reconciliation is needed.")
			return true
		}
	}

	if len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts) != 0 {
		if !r.isFeatureDeployed(clusterSummaryScope, configv1alpha1.FeatureHelm) {
			logger.V(logs.LogDebug).Info("Mode set to one time. Helm Charts not deployed yet. Reconciliation is needed.")
			return true
		}
	}

	return false
}

func (r *ClusterSummaryReconciler) getCurrentReferences(clusterSummaryScope *scope.ClusterSummaryScope) *libsveltosset.Set {
	currentReferences := &libsveltosset.Set{}
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs {
		referencedNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Namespace
		referencedName := clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Name

		namespace := getReferenceResourceNamespace(clusterSummaryScope.Namespace(), referencedNamespace)

		currentReferences.Insert(&corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(), // the only resources that can be referenced are Secret and ConfigMap
			Kind:       clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Kind,
			Namespace:  namespace,
			Name:       referencedName,
		})
	}
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs {
		referencedNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i].Namespace
		referencedName := clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i].Name

		namespace := getReferenceResourceNamespace(clusterSummaryScope.Namespace(), referencedNamespace)

		var apiVersion string
		switch clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i].Kind {
		case sourcev1.GitRepositoryKind:
			apiVersion = sourcev1.GroupVersion.String()
		case sourcev1b2.OCIRepositoryKind:
			apiVersion = sourcev1b2.GroupVersion.String()
		case sourcev1b2.BucketKind:
			apiVersion = sourcev1b2.GroupVersion.String()
		}
		currentReferences.Insert(&corev1.ObjectReference{
			APIVersion: apiVersion,
			Kind:       clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i].Kind,
			Namespace:  namespace,
			Name:       referencedName,
		})
	}
	return currentReferences
}

func (r *ClusterSummaryReconciler) getReferenceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ReferenceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ReferenceMap[*entry] = s
	}
	return s
}

// isPaused returns true if Sveltos/CAPI Cluster is paused or ClusterSummary has paused annotation.
func (r *ClusterSummaryReconciler) isPaused(ctx context.Context,
	clusterSummary *configv1alpha1.ClusterSummary) (bool, error) {

	isClusterPaused, err := clusterproxy.IsClusterPaused(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	if isClusterPaused {
		return true, nil
	}

	return annotations.HasPaused(clusterSummary), nil
}

// canRemoveFinalizer returns true if finalizer can be removed.
// A ClusterSummary in DryRun mode can be removed if deleted and ClusterProfile is also marked for deletion.
// A ClusterSummary in not DryRun mode can be removed if deleted and all features are undeployed.
func (r *ClusterSummaryReconciler) canRemoveFinalizer(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) bool {

	clusterSummary := clusterSummaryScope.ClusterSummary

	if clusterSummaryScope.ClusterSummary.DeletionTimestamp.IsZero() {
		logger.V(logs.LogDebug).Info("ClusterSummary not marked for deletion")
		return false
	}

	_, err := clusterproxy.GetCluster(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("cluster %s/%s not found. Nothing to do.",
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName))
			return true
		}
		return false
	}

	if clusterSummaryScope.IsDryRunSync() {
		logger.V(logs.LogInfo).Info("DryRun mode. Can only be deleted if ClusterProfile is marked for deletion.")
		// A ClusterSummary in DryRun mode can only be removed if also ClusterProfile is marked
		// for deletion. Otherwise ClusterSummary has to stay and list what would happen if owning
		// ClusterProfile is moved away from DryRun mode.
		clusterProfile, err := configv1alpha1.GetClusterProfileOwner(ctx, r.Client, clusterSummaryScope.ClusterSummary)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterProfile %v", err))
			return false
		}

		if clusterProfile == nil {
			logger.V(logs.LogInfo).Info("failed to get ClusterProfile")
			return false
		}

		if !clusterProfile.DeletionTimestamp.IsZero() {
			return true
		}
		logger.V(logs.LogInfo).Info("ClusterProfile not marked for deletion")
		return false
	}

	for i := range clusterSummaryScope.ClusterSummary.Status.FeatureSummaries {
		fs := &clusterSummaryScope.ClusterSummary.Status.FeatureSummaries[i]
		if fs.Status != configv1alpha1.FeatureStatusRemoved {
			logger.V(logs.LogInfo).Info("Not all features marked as removed")
			return false
		}
	}
	return true
}

// removeResourceSummary removes, if still present, ResourceSummary corresponding
// to this ClusterSummary instance
func (r *ClusterSummaryReconciler) removeResourceSummary(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {

	// ResourceSummary is a Sveltos resource deployed in managed clusters.
	// Such resources are always created, removed using cluster-admin roles.
	cs := clusterSummaryScope.ClusterSummary
	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, r.Client, cs.Spec.ClusterNamespace,
		cs.Spec.ClusterName, "", "", cs.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	err = unDeployResourceSummaryInstance(ctx, remoteClient, cs.Spec.ClusterNamespace,
		cs.Name, logger)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		// ResourceSummaries are only installed when in ContinuousWithDriftDetection mode
		if meta.IsNoMatchError(err) {
			return nil
		}
	}
	return err
}
