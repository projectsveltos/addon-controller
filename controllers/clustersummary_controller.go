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
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/annotations"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	"github.com/projectsveltos/libsveltos/lib/sharding"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

const (
	// deleteRequeueAfter is how long to wait before checking again to see if the cluster still has
	// children during deletion.
	deleteRequeueAfter = 10 * time.Second

	// normalRequeueAfter is how long to wait before checking again to see if the cluster can be moved
	// to ready after or workload features (for instance ingress or reporter) have failed
	normalRequeueAfter = 10 * time.Second

	// dryRunRequeueAfter is how long to wait before reconciling a ClusterSummary in DryRun mode
	dryRunRequeueAfter = 20 * time.Second
)

type ReportMode int

const (
	// Default mode. In this mode, addon-controller running
	// in the management cluster periodically collects/processes
	// ResourceSummaries from Sveltos/CAPI Clusters
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
	Logger               logr.Logger
	ReportMode           ReportMode
	AgentInMgmtCluster   bool   // if true, indicates drift-detection-manager needs to be started in the management cluster
	ShardKey             string // when set, only clusters matching the ShardKey will be reconciled
	Version              string
	Deployer             deployer.DeployerInterface
	ConcurrentReconciles int
	PolicyMux            sync.Mutex                                    // use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	ReferenceMap         map[corev1.ObjectReference]*libsveltosset.Set // key: Referenced object; value: set of all ClusterSummaries referencing the resource
	ClusterMap           map[corev1.ObjectReference]*libsveltosset.Set // key: Sveltos/Cluster; value: set of all ClusterSummaries for that Cluster

	ConflictRetryTime time.Duration
	ctrl              controller.Controller

	provisioningDryRun map[types.NamespacedName]time.Time
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
	clusterSummary := &configv1beta1.ClusterSummary{}
	if err := r.Get(ctx, req.NamespacedName, clusterSummary); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch clusterSummary")
		return reconcile.Result{}, fmt.Errorf(
			"failed to fetch clusterSummary %s: %w",
			req.NamespacedName, err,
		)
	}

	// Fetch the (Cluster)Profile.
	profile, _, err := configv1beta1.GetProfileOwnerAndTier(ctx, r.Client, clusterSummary)
	if err != nil {
		logger.Error(err, "Failed to get owner clusterProfile")
		return reconcile.Result{}, fmt.Errorf(
			"failed to get owner clusterProfile for %s: %w",
			req.NamespacedName, err,
		)
	}
	if profile == nil {
		logger.Error(err, "Failed to get owner (Cluster)Profile")
		return reconcile.Result{}, fmt.Errorf("failed to get owner (Cluster)Profile for %s",
			req.NamespacedName)
	}

	clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
		Client:         r.Client,
		Logger:         logger,
		ClusterSummary: clusterSummary,
		Profile:        profile,
		ControllerName: "clustersummary",
	})
	if err != nil {
		logger.Error(err, "Failed to create clusterProfileScope")
		return reconcile.Result{}, fmt.Errorf(
			"unable to create clusterprofile scope for %s: %w",
			req.NamespacedName, err,
		)
	}

	if r.skipReconciliation(clusterSummaryScope, req) {
		logger.V(logs.LogInfo).Info("ignore update")
		return reconcile.Result{Requeue: true, RequeueAfter: dryRunRequeueAfter}, nil
	}

	var isMatch bool
	isMatch, err = r.isClusterAShardMatch(ctx, clusterSummary, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	} else if !isMatch {
		// This addon-controller pod is not a shard match, yet we need to refresh internal state by:
		// - removing any helm chart registration made by this ClusterSummary
		// - update internal maps. This is needed cause when cluster changes, shard annotation changes,
		// this addon-controller might become the new shard match and so it must reconcile this ClusterSummary instance
		return reconcile.Result{}, r.refreshInternalState(ctx, clusterSummaryScope, logger)
	}

	err = r.updateClusterShardPair(ctx, clusterSummary, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	// Always close the scope when exiting this function so we can persist any ClusterSummary
	// changes.
	defer func() {
		if clusterSummaryScope.IsDryRunSync() {
			r.provisioningDryRun[req.NamespacedName] = time.Now()
		}
		if err = clusterSummaryScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted clusterSummary
	if !clusterSummary.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterSummaryScope, logger)
	}

	isReady, err := r.isReady(ctx, clusterSummary, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}
	if !isReady {
		logger.V(logs.LogInfo).Info("cluster is not ready.")
		r.setFailureMessage(clusterSummaryScope, "cluster is not ready")
		r.resetFeatureStatus(clusterSummaryScope, configv1beta1.FeatureStatusFailed)
		// if cluster is not ready, do nothing and don't queue for reconciliation.
		// When cluster becomes ready, all matching clusterSummaries will be requeued for reconciliation
		_ = r.updateMaps(clusterSummaryScope, logger)

		return reconcile.Result{}, nil
	}

	return r.reconcileNormal(ctx, clusterSummaryScope, logger)
}

func (r *ClusterSummaryReconciler) reconcileDelete(
	ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger,
) (reconcile.Result, error) {

	logger.V(logs.LogInfo).Info("Reconciling ClusterSummary delete")

	isReady, err := r.isReady(ctx, clusterSummaryScope.ClusterSummary, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	// If Sveltos/Cluster is not found, there is nothing to clean up.
	isPresent, isDeleted, err := r.isClusterPresent(ctx, clusterSummaryScope)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}
	if isPresent && isReady { // if cluster is not ready, do not try to clean up. It would fail.
		// Cleanup
		paused, err := r.isPaused(ctx, clusterSummaryScope.ClusterSummary)
		if err != nil {
			return reconcile.Result{}, err
		}
		if paused {
			logger.V(logs.LogInfo).Info("cluster is paused. Do nothing.")
			return reconcile.Result{}, nil
		}

		if !isDeleted {
			// if cluster is marked for deletion do not try to remove ResourceSummaries.
			// those are only deployed in the managed cluster so no need to cleanup on a deleted cluster
			err = r.removeResourceSummary(ctx, clusterSummaryScope, logger)
			if err != nil {
				logger.V(logs.LogInfo).Error(err, "failed to remove ResourceSummary.")
				return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
			}
		}

		// still call undeploy even if cluster is deleted. Sveltos might have deployed resources
		// in the management cluster and those need to be removed.
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
	}

	// If cluster is not present anymore or is it marked for deletion
	if !isPresent || isDeleted {
		// in case cleanup operations were already queued, before removing ClusterSummary
		// remove those
		r.cleanupQueuedCleanOperations(clusterSummaryScope.ClusterSummary)

		logger.V(logs.LogDebug).Info("remove drift-detection-manager resources from management cluster")
		cs := clusterSummaryScope.ClusterSummary
		if err := removeDriftDetectionManagerFromManagementCluster(ctx,
			cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cs.Spec.ClusterType, logger); err != nil {
			logger.V(logs.LogInfo).Info(
				fmt.Sprintf("failed to remove drift-detection-manager resources from management cluster: %v", err))
			return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
		}
	}

	// Cluster is not present anymore or cleanup succeeded
	logger.V(logs.LogInfo).Info("Removing finalizer")
	if controllerutil.ContainsFinalizer(clusterSummaryScope.ClusterSummary, configv1beta1.ClusterSummaryFinalizer) {
		if finalizersUpdated := controllerutil.RemoveFinalizer(clusterSummaryScope.ClusterSummary,
			configv1beta1.ClusterSummaryFinalizer); !finalizersUpdated {
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer")
		}
	}

	if err := r.deleteChartMap(ctx, clusterSummaryScope, logger); err != nil {
		return reconcile.Result{}, err
	}

	r.cleanMaps(clusterSummaryScope)

	manager := getManager()
	manager.stopStaleWatchForTemplateResourceRef(clusterSummaryScope.ClusterSummary, true)

	logger.V(logs.LogInfo).Info("Reconcile delete success")

	return reconcile.Result{}, nil
}

func (r *ClusterSummaryReconciler) reconcileNormal(
	ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger,
) (reconcile.Result, error) {

	logger.V(logs.LogInfo).Info("Reconciling ClusterSummary")

	if !controllerutil.ContainsFinalizer(clusterSummaryScope.ClusterSummary, configv1beta1.ClusterSummaryFinalizer) {
		if err := r.addFinalizer(ctx, clusterSummaryScope); err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to add finalizer")
			return reconcile.Result{}, err
		}
	}

	if !r.shouldReconcile(clusterSummaryScope, logger) {
		logger.V(logs.LogInfo).Info("ClusterSummary does not need a reconciliation")
		return reconcile.Result{}, nil
	}

	err := r.updateMaps(clusterSummaryScope, logger)
	if err != nil {
		return reconcile.Result{}, err
	}

	paused, err := r.isPaused(ctx, clusterSummaryScope.ClusterSummary)
	if err != nil {
		return reconcile.Result{}, err
	}
	if paused {
		logger.V(logs.LogInfo).Info("cluster is paused. Do nothing.")
		return reconcile.Result{}, nil
	}

	err = r.startWatcherForTemplateResourceRefs(ctx, clusterSummaryScope.ClusterSummary)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to start watcher on resources referenced in TemplateResourceRefs.")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	allDeployed, msg, err := r.areDependenciesDeployed(ctx, clusterSummaryScope, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}
	clusterSummaryScope.SetDependenciesMessage(&msg)
	if !allDeployed {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	err = r.updateChartMap(ctx, clusterSummaryScope, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	if !clusterSummaryScope.IsContinuousWithDriftDetection() {
		err = r.removeResourceSummary(ctx, clusterSummaryScope, logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to remove ResourceSummary.")
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
		}
	}

	err = r.deploy(ctx, clusterSummaryScope, logger)
	if err != nil {
		var conflictErr *deployer.ConflictError
		ok := errors.As(err, &conflictErr)
		if ok {
			logger.V(logs.LogInfo).Error(err, "failed to deploy because of conflict")
			return reconcile.Result{Requeue: true, RequeueAfter: r.ConflictRetryTime}, nil
		}
		logger.V(logs.LogInfo).Error(err, "failed to deploy")
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	logger.V(logs.LogInfo).Info("Reconciling ClusterSummary success")

	if clusterSummaryScope.IsDryRunSync() {
		r.resetFeatureStatusToProvisioning(clusterSummaryScope)
		// we need to keep retrying in DryRun ClusterSummaries
		return reconcile.Result{Requeue: true, RequeueAfter: dryRunRequeueAfter}, nil
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSummaryReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1beta1.ClusterSummary{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Watches(&libsveltosv1beta1.SveltosCluster{},
			handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForSveltosCluster),
			builder.WithPredicates(
				SveltosClusterPredicates(mgr.GetLogger().WithValues("predicate", "sveltosclusterpredicate")),
			),
		).
		Watches(&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForReference),
			builder.WithPredicates(
				ConfigMapPredicates(mgr.GetLogger().WithValues("predicate", "configmappredicate")),
			),
		).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForReference),
			builder.WithPredicates(
				SecretPredicates(mgr.GetLogger().WithValues("predicate", "secretpredicate")),
			),
		).
		Build(r)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}

	// At this point we don't know yet whether CAPI is present in the cluster.
	// Later on, in main, we detect that and if CAPI is present WatchForCAPI will be invoked.

	if r.ReportMode == CollectFromManagementCluster {
		go collectAndProcessResourceSummaries(ctx, mgr.GetClient(), r.ShardKey, r.Version, mgr.GetLogger())
	}

	initializeManager(ctrl.Log.WithName("watchers"), mgr.GetConfig(), mgr.GetClient())

	r.provisioningDryRun = make(map[types.NamespacedName]time.Time)
	r.ctrl = c

	return err
}

func (r *ClusterSummaryReconciler) WatchForCAPI(mgr ctrl.Manager, c controller.Controller) error {
	sourceCluster := source.Kind[*clusterv1.Cluster](
		mgr.GetCache(),
		&clusterv1.Cluster{},
		handler.TypedEnqueueRequestsFromMapFunc(r.requeueClusterSummaryForCluster),
		ClusterPredicate{Logger: mgr.GetLogger().WithValues("predicate", "clusterpredicate")},
	)

	// When cluster-api cluster changes, according to ClusterPredicates,
	// one or more ClusterProfiles need to be reconciled.
	if err := c.Watch(sourceCluster); err != nil {
		return err
	}

	return nil
}

func (r *ClusterSummaryReconciler) WatchForFlux(mgr ctrl.Manager, c controller.Controller) error {
	// When a Flux source (GitRepository/OCIRepository/Bucket) changes, one or more ClusterSummaries
	// need to be reconciled.

	sourceGitRepository := source.Kind[*sourcev1.GitRepository](
		mgr.GetCache(),
		&sourcev1.GitRepository{},
		handler.TypedEnqueueRequestsFromMapFunc(r.requeueClusterSummaryForFluxGitRepository),
		FluxGitRepositoryPredicate{Logger: mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")},
	)
	if err := c.Watch(sourceGitRepository); err != nil {
		return err
	}

	sourceOCIRepository := source.Kind[*sourcev1b2.OCIRepository](
		mgr.GetCache(),
		&sourcev1b2.OCIRepository{},
		handler.TypedEnqueueRequestsFromMapFunc(r.requeueClusterSummaryForFluxOCIRepository),
		FluxOCIRepositoryPredicate{Logger: mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")},
	)
	if err := c.Watch(sourceOCIRepository); err != nil {
		return err
	}

	sourceBucket := source.Kind[*sourcev1b2.Bucket](
		mgr.GetCache(),
		&sourcev1b2.Bucket{},
		handler.TypedEnqueueRequestsFromMapFunc(r.requeueClusterSummaryForFluxBucket),
		FluxBucketPredicate{Logger: mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")},
	)
	if err := c.Watch(sourceBucket); err != nil {
		return err
	}

	return nil
}

func (r *ClusterSummaryReconciler) skipReconciliation(clusterSummaryScope *scope.ClusterSummaryScope,
	req ctrl.Request) bool {

	if clusterSummaryScope.IsDryRunSync() {
		// Skip and requeue ClusterSummary DryRun if too soon.
		v := r.provisioningDryRun[req.NamespacedName]
		thresholdTime := v.Add(normalRequeueAfter)
		if !time.Now().After(thresholdTime) {
			return true
		}
	}
	return false
}

func (r *ClusterSummaryReconciler) addFinalizer(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope) error {
	// If the SveltosCluster doesn't have our finalizer, add it.
	controllerutil.AddFinalizer(clusterSummaryScope.ClusterSummary, configv1beta1.ClusterSummaryFinalizer)
	// Register the finalizer immediately to avoid orphaning clusterprofile resources on delete
	if err := clusterSummaryScope.PatchObject(ctx); err != nil {
		clusterSummaryScope.Error(err, "Failed to add finalizer")
		return fmt.Errorf(
			"failed to add finalizer for %s: %w",
			clusterSummaryScope.Name(), err,
		)
	}
	return nil
}

func (r *ClusterSummaryReconciler) deploy(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	clusterSummary := clusterSummaryScope.ClusterSummary
	logger = logger.WithValues("clusternamespace", clusterSummary.Spec.ClusterNamespace, "clustername", clusterSummary.Spec.ClusterName)

	var errs []error

	resourceErr := r.deployResources(ctx, clusterSummaryScope, logger)

	helmErr := r.deployHelm(ctx, clusterSummaryScope, logger)

	kustomizeError := r.deployKustomizeRefs(ctx, clusterSummaryScope, logger)

	if resourceErr != nil {
		errs = append(errs, fmt.Errorf("deploying resources failed: %w", resourceErr))
	}

	if helmErr != nil {
		errs = append(errs, fmt.Errorf("deploying helm charts failed: %w", helmErr))
	}

	if kustomizeError != nil {
		errs = append(errs, fmt.Errorf("deploying kustomize resources failed: %w", kustomizeError))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (r *ClusterSummaryReconciler) deployKustomizeRefs(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs == nil {
		logger.V(logs.LogDebug).Info("no kustomize policy configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope.ClusterSummary, configv1beta1.FeatureKustomize) {
			logger.V(logs.LogDebug).Info("no policy status. Do not reconcile this")
			return nil
		}
	}

	f := getHandlersForFeature(configv1beta1.FeatureKustomize)

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployResources(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs == nil {
		logger.V(logs.LogDebug).Info("no policy configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope.ClusterSummary, configv1beta1.FeatureResources) {
			logger.V(logs.LogDebug).Info("no policy status. Do not reconcile this")
			return nil
		}
	}

	f := getHandlersForFeature(configv1beta1.FeatureResources)

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployHelm(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts == nil {
		logger.V(logs.LogDebug).Info("no helm configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope.ClusterSummary, configv1beta1.FeatureHelm) {
			logger.V(logs.LogDebug).Info("no helm status. Do not reconcile this")
			return nil
		}
	}

	f := getHandlersForFeature(configv1beta1.FeatureHelm)

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) isClusterPresent(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope) (present, deleted bool, err error) {

	cs := clusterSummaryScope.ClusterSummary

	var cluster client.Object
	cluster, err = clusterproxy.GetCluster(ctx, r.Client, cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cs.Spec.ClusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, false, nil
		}
	}

	return true, !cluster.GetDeletionTimestamp().IsZero(), err
}

func (r *ClusterSummaryReconciler) undeploy(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) error {

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
	f := getHandlersForFeature(configv1beta1.FeatureResources)
	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeployKustomizeResources(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := getHandlersForFeature(configv1beta1.FeatureKustomize)
	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeployHelm(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := getHandlersForFeature(configv1beta1.FeatureHelm)
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
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
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
	// chart are removed from Sveltos/Cluster. That is done as part of deployHelmCharts and
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

func (r *ClusterSummaryReconciler) updateMaps(clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeOneTime {
		logger.V(logs.LogDebug).Info("sync mode is one time. No need to reconcile on policies change.")
		return nil
	}
	logger.V(logs.LogDebug).Info("update policy map")
	currentReferences, err := r.getCurrentReferences(clusterSummaryScope)
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to get current references: %v", err)
		return err
	}

	// If there is a ConfigMap with drift-detection-manager configuration, if the syncMode is ContinuousWithDriftDetection
	// this clusterSummary needs to be redeployed when the ConfigMap changes
	if clusterSummaryScope.IsContinuousWithDriftDetection() {
		if driftDetectionConfigMap := getDriftDetectionConfigMap(); driftDetectionConfigMap != "" {
			currentReferences.Insert(&corev1.ObjectReference{
				APIVersion: corev1.SchemeGroupVersion.String(),
				Kind:       string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace:  projectsveltos,
				Name:       driftDetectionConfigMap,
			})
		}
	}

	cs := clusterSummaryScope.ClusterSummary
	var kind, apiVersion string
	if cs.Spec.ClusterType == libsveltosv1beta1.ClusterTypeSveltos {
		kind = libsveltosv1beta1.SveltosClusterKind
		apiVersion = libsveltosv1beta1.GroupVersion.String()
	} else {
		kind = clusterv1.ClusterKind
		apiVersion = clusterv1.GroupVersion.String()
	}

	clusterInfo := &corev1.ObjectReference{
		Namespace:  cs.Spec.ClusterNamespace,
		Name:       cs.Spec.ClusterName,
		Kind:       kind,
		APIVersion: apiVersion,
	}

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	clusterSummaryInfo := corev1.ObjectReference{APIVersion: configv1beta1.GroupVersion.String(),
		Kind: configv1beta1.ClusterSummaryKind, Namespace: clusterSummaryScope.Namespace(),
		Name: clusterSummaryScope.Name()}
	r.getClusterMapForEntry(clusterInfo).Insert(&clusterSummaryInfo)

	for k, l := range r.ReferenceMap {
		l.Erase(&clusterSummaryInfo)
		if l.Len() == 0 {
			delete(r.ReferenceMap, k)
		}
	}

	// For each currently referenced instance, add ClusterSummary as consumer
	for _, referencedResource := range currentReferences.Items() {
		tmpResource := referencedResource
		r.getReferenceMapForEntry(&tmpResource).Insert(
			&corev1.ObjectReference{
				APIVersion: configv1beta1.GroupVersion.String(),
				Kind:       configv1beta1.ClusterSummaryKind,
				Namespace:  clusterSummaryScope.Namespace(),
				Name:       clusterSummaryScope.Name(),
			},
		)
	}
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

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuous ||
		clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection {

		logger.V(logs.LogDebug).Info(fmt.Sprintf("Mode set to %s. Reconciliation is needed.",
			clusterSummary.Spec.ClusterProfileSpec.SyncMode))
		return true
	}

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun {
		logger.V(logs.LogDebug).Info("Mode set to dryRun. Reconciliation is needed.")
		return true
	}

	if len(clusterSummary.Spec.ClusterProfileSpec.PolicyRefs) != 0 {
		if !r.isFeatureDeployed(clusterSummaryScope.ClusterSummary, configv1beta1.FeatureResources) {
			logger.V(logs.LogDebug).Info("Mode set to one time. Resources not deployed yet. Reconciliation is needed.")
			return true
		}
	}

	if len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts) != 0 {
		if !r.isFeatureDeployed(clusterSummaryScope.ClusterSummary, configv1beta1.FeatureHelm) {
			logger.V(logs.LogDebug).Info("Mode set to one time. Helm Charts not deployed yet. Reconciliation is needed.")
			return true
		}
	}

	if len(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs) != 0 {
		if !r.isFeatureDeployed(clusterSummaryScope.ClusterSummary, configv1beta1.FeatureKustomize) {
			logger.V(logs.LogDebug).Info("Mode set to one time. Kustomization resources not deployed yet. Reconciliation is needed.")
			return true
		}
	}

	return false
}

func (r *ClusterSummaryReconciler) getCurrentReferences(clusterSummaryScope *scope.ClusterSummaryScope,
) (*libsveltosset.Set, error) {

	currentReferences, err := r.getPolicyRefReferences(clusterSummaryScope)
	if err != nil {
		return nil, err
	}

	kustomizationRefs, err := r.getKustomizationRefReferences(clusterSummaryScope)
	if err != nil {
		return nil, err
	}
	currentReferences.Append(kustomizationRefs)

	helmRefs, err := r.getHelmChartsReferences(clusterSummaryScope)
	if err != nil {
		return nil, err
	}
	currentReferences.Append(helmRefs)

	return currentReferences, nil
}

// getPolicyRefReferences get all references considering the PolicyRef section
func (r *ClusterSummaryReconciler) getPolicyRefReferences(clusterSummaryScope *scope.ClusterSummaryScope,
) (*libsveltosset.Set, error) {

	currentReferences := &libsveltosset.Set{}
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs {
		referencedNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Namespace
		namespace := libsveltostemplate.GetReferenceResourceNamespace(clusterSummaryScope.Namespace(), referencedNamespace)

		cs := clusterSummaryScope.ClusterSummary
		referencedName, err := libsveltostemplate.GetReferenceResourceName(cs.Spec.ClusterNamespace, cs.Spec.ClusterName,
			string(cs.Spec.ClusterType), clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Name)
		if err != nil {
			return nil, err
		}

		currentReferences.Insert(&corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(), // the only resources that can be referenced are Secret and ConfigMap
			Kind:       clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Kind,
			Namespace:  namespace,
			Name:       referencedName,
		})
	}
	return currentReferences, nil
}

// getKustomizationRefReferences get all references considering the KustomizationRef section
func (r *ClusterSummaryReconciler) getKustomizationRefReferences(clusterSummaryScope *scope.ClusterSummaryScope,
) (*libsveltosset.Set, error) {

	currentReferences := &libsveltosset.Set{}
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs {
		kr := &clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i]

		referencedNamespace := kr.Namespace

		namespace := libsveltostemplate.GetReferenceResourceNamespace(
			clusterSummaryScope.Namespace(), referencedNamespace)

		cs := clusterSummaryScope.ClusterSummary
		referencedName, err := libsveltostemplate.GetReferenceResourceName(cs.Spec.ClusterNamespace,
			cs.Spec.ClusterName, string(cs.Spec.ClusterType), kr.Name)
		if err != nil {
			return nil, err
		}

		var apiVersion string
		switch kr.Kind {
		case sourcev1.GitRepositoryKind:
			apiVersion = sourcev1.GroupVersion.String()
		case sourcev1b2.OCIRepositoryKind:
			apiVersion = sourcev1b2.GroupVersion.String()
		case sourcev1b2.BucketKind:
			apiVersion = sourcev1b2.GroupVersion.String()
		default:
			apiVersion = corev1.SchemeGroupVersion.String()
		}
		currentReferences.Insert(&corev1.ObjectReference{
			APIVersion: apiVersion,
			Kind:       kr.Kind,
			Namespace:  namespace,
			Name:       referencedName,
		})

		valuesFromReferences, err := getKustomizationValueFrom(clusterSummaryScope, kr)
		if err != nil {
			return nil, err
		}
		currentReferences.Append(valuesFromReferences)
	}
	return currentReferences, nil
}

// getKustomizationValueFrom gets referenced ConfigMap/Secret in a KustomizationRef.
// KustomizationRef can reference both ConfigMap/Secret each containing key-value pairs that will be used, if defined,
// to replace placeholder value in the output generated by Kustomize SDK.
func getKustomizationValueFrom(clusterSummaryScope *scope.ClusterSummaryScope, kr *configv1beta1.KustomizationRef,
) (*libsveltosset.Set, error) {

	currentValuesFromReferences := &libsveltosset.Set{}

	for i := range kr.ValuesFrom {
		referencedNamespace := kr.ValuesFrom[i].Namespace
		namespace := libsveltostemplate.GetReferenceResourceNamespace(
			clusterSummaryScope.Namespace(), referencedNamespace)

		cs := clusterSummaryScope.ClusterSummary
		referencedName, err := libsveltostemplate.GetReferenceResourceName(cs.Spec.ClusterNamespace,
			cs.Spec.ClusterName, string(cs.Spec.ClusterType), kr.ValuesFrom[i].Name)
		if err != nil {
			return nil, err
		}

		currentValuesFromReferences.Insert(&corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       kr.ValuesFrom[i].Kind,
			Namespace:  namespace,
			Name:       referencedName,
		})
	}

	return currentValuesFromReferences, nil
}

// getHelmChartsReferences get all references considering the HelmChart section
func (r *ClusterSummaryReconciler) getHelmChartsReferences(clusterSummaryScope *scope.ClusterSummaryScope,
) (*libsveltosset.Set, error) {

	currentReferences := &libsveltosset.Set{}
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		hc := &clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		valuesFromReferences, err := getHelmChartValueFrom(clusterSummaryScope, hc)
		if err != nil {
			return nil, err
		}
		currentReferences.Append(valuesFromReferences)
	}
	return currentReferences, nil
}

// getHelmChartValueFrom gets referenced ConfigMap/Secret in a HelmChart.
// HelmChart can reference both ConfigMap/Secret each containing configuration for the helm release.
func getHelmChartValueFrom(clusterSummaryScope *scope.ClusterSummaryScope, hc *configv1beta1.HelmChart,
) (*libsveltosset.Set, error) {

	currentValuesFromReferences := &libsveltosset.Set{}

	for i := range hc.ValuesFrom {
		referencedNamespace := hc.ValuesFrom[i].Namespace
		namespace := libsveltostemplate.GetReferenceResourceNamespace(
			clusterSummaryScope.Namespace(), referencedNamespace)

		cs := clusterSummaryScope.ClusterSummary
		referencedName, err := libsveltostemplate.GetReferenceResourceName(cs.Spec.ClusterNamespace,
			cs.Spec.ClusterName, string(cs.Spec.ClusterType), hc.ValuesFrom[i].Name)
		if err != nil {
			return nil, err
		}

		currentValuesFromReferences.Insert(&corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       hc.ValuesFrom[i].Kind,
			Namespace:  namespace,
			Name:       referencedName,
		})
	}

	return currentValuesFromReferences, nil
}

func (r *ClusterSummaryReconciler) getReferenceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ReferenceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ReferenceMap[*entry] = s
	}
	return s
}

// isReady returns true if Sveltos/Cluster is ready
func (r *ClusterSummaryReconciler) isReady(ctx context.Context,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) (bool, error) {

	clusterRef := &corev1.ObjectReference{
		Namespace: clusterSummary.Spec.ClusterNamespace,
		Name:      clusterSummary.Spec.ClusterName,
	}
	if clusterSummary.Spec.ClusterType == libsveltosv1beta1.ClusterTypeSveltos {
		clusterRef.Kind = libsveltosv1beta1.SveltosClusterKind
		clusterRef.APIVersion = libsveltosv1beta1.GroupVersion.String()
	} else {
		clusterRef.Kind = clusterKind
		clusterRef.APIVersion = clusterv1.GroupVersion.String()
	}

	isClusterReady, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, r.Client, clusterRef, logger)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return isClusterReady, nil
}

// isPaused returns true if Sveltos/Cluster is paused or ClusterSummary has paused annotation.
func (r *ClusterSummaryReconciler) isPaused(ctx context.Context,
	clusterSummary *configv1beta1.ClusterSummary) (bool, error) {

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
		logger.V(logs.LogInfo).Info("DryRun mode. Can only be deleted if Profile/ClusterProfile is marked for deletion.")
		// A ClusterSummary in DryRun mode can only be removed if also ClusterProfile is marked
		// for deletion. Otherwise ClusterSummary has to stay and list what would happen if owning
		// ClusterProfile is moved away from DryRun mode.
		profile, _, err := configv1beta1.GetProfileOwnerAndTier(ctx, r.Client, clusterSummaryScope.ClusterSummary)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get ClusterProfile %v", err))
			return false
		}

		if profile == nil {
			logger.V(logs.LogInfo).Info("failed to get ClusterProfile")
			return false
		}

		if !profile.GetDeletionTimestamp().IsZero() {
			return true
		}
		logger.V(logs.LogInfo).Info("ClusterProfile not marked for deletion")
		return false
	}

	for i := range clusterSummaryScope.ClusterSummary.Status.FeatureSummaries {
		fs := &clusterSummaryScope.ClusterSummary.Status.FeatureSummaries[i]
		if fs.Status != configv1beta1.FeatureStatusRemoved {
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

func (r *ClusterSummaryReconciler) updateClusterShardPair(ctx context.Context,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) error {

	if hasShardChanged, err := sharding.RegisterClusterShard(ctx, r.Client, libsveltosv1beta1.ComponentAddonManager,
		string(configv1beta1.FeatureHelm), r.ShardKey, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Spec.ClusterType); err != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to check/update cluster:shard pair %v", err))
		return err
	} else if hasShardChanged {
		// internal in-memory state must be rebuilt. Restart pod
		logger.V(logs.LogInfo).Info("restarting pod to rebuild internal state")
		if killErr := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); killErr != nil {
			panic("kill -TERM failed")
		}
	}

	return nil
}

// isClusterAShardMatch checks if cluster is matching this addon-controller deployment shard.
func (r *ClusterSummaryReconciler) isClusterAShardMatch(ctx context.Context,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) (bool, error) {

	cluster, err := clusterproxy.GetCluster(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
	if err != nil {
		// If Cluster does not exist anymore, make it match any shard
		if apierrors.IsNotFound(err) {
			return true, nil
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to get cluster: %v", err))
		return false, err
	}

	if !sharding.IsShardAMatch(r.ShardKey, cluster) {
		logger.V(logs.LogDebug).Info("not a shard match")
		return false, nil
	}

	return true, nil
}

// refreshInternalState updates internal maps in this addon-controller pod:
// - remove any chart subscription handled by the clusterSummary
// - update cluster to clusterSummary maps. This is needed cause if cluster shard changes,
// we want this clusterSummary to be reconciled)
func (r *ClusterSummaryReconciler) refreshInternalState(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {

	if err := r.deleteChartMap(ctx, clusterSummaryScope, logger); err != nil {
		return err
	}

	clusterInfo := &corev1.ObjectReference{
		Namespace: clusterSummaryScope.ClusterSummary.Spec.ClusterNamespace,
		Name:      clusterSummaryScope.ClusterSummary.Spec.ClusterName,
	}

	switch clusterSummaryScope.ClusterSummary.Spec.ClusterType {
	case libsveltosv1beta1.ClusterTypeSveltos:
		clusterInfo.Kind = string(libsveltosv1beta1.ClusterTypeSveltos)
		clusterInfo.APIVersion = libsveltosv1beta1.GroupVersion.String()
	case libsveltosv1beta1.ClusterTypeCapi:
		clusterInfo.Kind = clusterKind
		clusterInfo.APIVersion = clusterv1.GroupVersion.String()
	}

	clusterSummaryInfo := corev1.ObjectReference{APIVersion: configv1beta1.GroupVersion.String(),
		Kind: configv1beta1.ClusterProfileKind, Namespace: clusterSummaryScope.Namespace(),
		Name: clusterSummaryScope.Name()}

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	// Even if this addon-controller is not a shard match now, we need to keep internal map
	// updated. If cluster sharding annotation, all addon-controllers (including this one)
	// needs to be requeued. For instance, this specific addon-controller might become the new
	// shard match and it has to take it over.
	r.getClusterMapForEntry(clusterInfo).Insert(&clusterSummaryInfo)

	return nil
}

// areDependenciesDeployed checks dependencies. All must be provisioned for this ClusterSummary to proceed further
// reconciling add-ons and applications
func (r *ClusterSummaryReconciler) areDependenciesDeployed(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) (allDeployed bool, dependencyMessage string, err error) {

	profileReference, err := configv1beta1.GetProfileOwnerReference(clusterSummaryScope.ClusterSummary)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get profile owner: %v", err))
		return false, "", fmt.Errorf("failed to get profile owner: %w", err)
	}

	if profileReference == nil {
		return false, "", fmt.Errorf("profile owner not found: %w", err)
	}

	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.DependsOn {
		profileName := clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.DependsOn[i]
		logger.V(logs.LogDebug).Info(fmt.Sprintf("Considering %s %s", profileReference.Kind, profileName))
		var cs *configv1beta1.ClusterSummary
		cs, err = getClusterSummary(ctx, r.Client, profileReference.Kind, profileName,
			clusterSummaryScope.ClusterSummary.Spec.ClusterNamespace, clusterSummaryScope.ClusterSummary.Spec.ClusterName,
			clusterSummaryScope.ClusterSummary.Spec.ClusterType)
		if err != nil {
			if apierrors.IsNotFound(err) {
				msg := fmt.Sprintf("ClusterSummary for %s %s not found", profileReference.Kind, profileName)
				logger.V(logs.LogInfo).Info(msg)
				return false, msg, nil
			}

			return false, "", err
		}

		if !isCluterSummaryProvisioned(cs) {
			msg := fmt.Sprintf("%s %s is not fully deployed yet", profileReference.Kind, profileName)
			logger.V(logs.LogInfo).Info(msg)
			return false, msg, nil
		}
	}

	dependencyMessage = "All dependencies deployed"
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.DependsOn == nil {
		dependencyMessage = "no dependencies"
	}

	return true, dependencyMessage, nil
}

func (r *ClusterSummaryReconciler) setFailureMessage(clusterSummaryScope *scope.ClusterSummaryScope, failureMessage string) {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts != nil {
		clusterSummaryScope.SetFailureMessage(configv1beta1.FeatureHelm, &failureMessage)
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs != nil {
		clusterSummaryScope.SetFailureMessage(configv1beta1.FeatureResources, &failureMessage)
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs != nil {
		clusterSummaryScope.SetFailureMessage(configv1beta1.FeatureKustomize, &failureMessage)
	}
}

func (r *ClusterSummaryReconciler) resetFeatureStatus(clusterSummaryScope *scope.ClusterSummaryScope, status configv1beta1.FeatureStatus) {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts != nil {
		clusterSummaryScope.SetFeatureStatus(configv1beta1.FeatureHelm, status, nil)
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.PolicyRefs != nil {
		clusterSummaryScope.SetFeatureStatus(configv1beta1.FeatureResources, status, nil)
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs != nil {
		clusterSummaryScope.SetFeatureStatus(configv1beta1.FeatureKustomize, status, nil)
	}
}

func (r *ClusterSummaryReconciler) GetController() controller.Controller {
	return r.ctrl
}

func (r *ClusterSummaryReconciler) startWatcherForTemplateResourceRefs(ctx context.Context,
	clusterSummary *configv1beta1.ClusterSummary) error {

	manager := getManager()
	for i := range clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs {
		ref := &clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs[i]

		gvk := schema.FromAPIVersionAndKind(ref.Resource.APIVersion, ref.Resource.Kind)

		if err := manager.startWatcherForTemplateResourceRef(ctx, gvk, ref, clusterSummary); err != nil {
			return err
		}
	}

	manager.stopStaleWatchForTemplateResourceRef(clusterSummary, false)
	return nil
}

// Removes any cleanup job
func (r *ClusterSummaryReconciler) cleanupQueuedCleanOperations(clusterSummary *configv1beta1.ClusterSummary) {
	r.Deployer.CleanupEntries(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(configv1beta1.FeatureHelm), clusterSummary.Spec.ClusterType, true)

	r.Deployer.CleanupEntries(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(configv1beta1.FeatureKustomize), clusterSummary.Spec.ClusterType, true)

	r.Deployer.CleanupEntries(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
		string(configv1beta1.FeatureResources), clusterSummary.Spec.ClusterType, true)
}

// resetFeatureStatusToProvisioning reset status from Provisioned to Provisioning
func (r *ClusterSummaryReconciler) resetFeatureStatusToProvisioning(clusterSummaryScope *scope.ClusterSummaryScope) {
	status := configv1beta1.FeatureStatusProvisioning
	for i := range clusterSummaryScope.ClusterSummary.Status.FeatureSummaries {
		fs := &clusterSummaryScope.ClusterSummary.Status.FeatureSummaries[i]
		if fs.Status == configv1beta1.FeatureStatusProvisioned {
			fs.Status = status
		}
	}
}
