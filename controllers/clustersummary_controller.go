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
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
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
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

const (
	// deleteRequeueAfter is how long to wait before checking again to see if the cluster still has
	// children during deletion.
	deleteRequeueAfter = 20 * time.Second

	// normalRequeueAfter is how long to wait before checking again to see if the cluster can be moved
	// to ready after or workload features (for instance ingress or reporter) have failed
	normalRequeueAfter = 20 * time.Second
)

// ClusterSummaryReconciler reconciles a ClusterSummary object
type ClusterSummaryReconciler struct {
	*rest.Config
	client.Client
	Scheme               *runtime.Scheme
	Deployer             deployer.DeployerInterface
	ConcurrentReconciles int
	Mux                  sync.Mutex      // use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	ReferenceMap         map[string]*Set // key: Referenced object  name; value: set of all ClusterSummaries referencing the resource
	// Refenced object name is: <kind>-<namespace>-<name> or <kind>-<name>
	ClusterSummaryMap map[string]*Set // key: ClusterSummary name; value: set of referenced resources

	// Reason for the two maps:
	// ClusterSummary references ConfigMaps containing policies to be deployed in a CAPI Cluster.
	// When a ConfigMap changes, all the ClusterSummaries referencing it need to be reconciled.
	// In order to achieve so, ClusterSummary reconciler could watch for ConfigMaps. When a ConfigMap spec changes,
	// find all the ClusterSummaries currently referencing it and reconcile those. Problem is no I/O should be present inside a MapFunc
	// (given a ConfigMap, return all the ClusterSummary referencing such ConfigMap).
	// In the MapFunc, if the list ClusterSummaries operation failed, we would be unable to retry or re-enqueue the ClusterSummaries
	// referencing the ConfigMap that changed.
	// Instead the approach taken is following:
	// - when a ClusterSummary is reconciled, update the ReferenceMap;
	// - in the MapFunc, given the ConfigMap that changed, we can immeditaly get all the ClusterSummaries needing a reconciliation (by
	// using the ReferenceMap);
	// - if a ClusterSummary is referencing a ConfigMap but its reconciliation is still queued, when ConfigMap changes, ReferenceMap
	// won't have such ClusterSummary. This is not a problem as ClusterSummary reconciliation is already queued and will happen.
	//
	// The ClusterSummaryMap is used to update ReferenceMap. Consider following scenarios to understand the need:
	// 1. ClusterSummary A references ConfigMaps 1 and 2. When reconciled, ReferenceMap will have 1 => A and 2 => A;
	// and ClusterSummaryMap A => 1,2
	// 2. ClusterSummary A changes and now references ConfigMap 1 only. We ned to remove the entry 2 => A in ReferenceMap. But
	// when we reconcile ClusterSummary we have its current version we don't have its previous version. So we use ClusterSummaryMap (at this
	// point value stored here corresponds to reconciliation #1. We know currently ClusterSummary references ConfigMap 1 only and looking
	// at ClusterSummaryMap we know it used to reference ConfigMap 1 and 2. So we can remove 2 => A from ReferenceMap. Only after this
	// update, we update ClusterSummaryMap (so new value will be A => 1)
	//
	// Same logic applies to Kyverno (kyverno configuration references configmaps containing kyverno policies)
}

//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.projectsveltos.io,resources=clustersummaries/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

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

	clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
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

	logger.Info("Reconciling ClusterSummary delete")

	if err := r.undeploy(ctx, clusterSummaryScope, logger); err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}, nil
	}

	// Cluster is deleted so remove the finalizer.
	logger.Info("Removing finalizer")
	if controllerutil.ContainsFinalizer(clusterSummaryScope.ClusterSummary, configv1alpha1.ClusterSummaryFinalizer) {
		if finalizersUpdated := controllerutil.RemoveFinalizer(clusterSummaryScope.ClusterSummary,
			configv1alpha1.ClusterSummaryFinalizer); !finalizersUpdated {
			return reconcile.Result{}, fmt.Errorf("failed to remove finalizer")
		}
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
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}, nil
	}

	logger.Info("Reconciling ClusterSummary success")
	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterSummaryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.ClusterSummary{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "error creating controller")
	}

	// When ConfigMap changes, according to ConfigMapPredicates,
	// one or more ClusterSummaries need to be reconciled.
	return c.Watch(&source.Kind{Type: &corev1.ConfigMap{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterSummaryForConfigMap),
		ConfigMapPredicates(klogr.New().WithValues("predicate", "configmappredicate")),
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
	coreResourceErr := r.deployResources(ctx, clusterSummaryScope, logger)

	kyvernoErr := r.deployKyverno(ctx, clusterSummaryScope, logger)

	gatekeeperErr := r.deployGatekeeper(ctx, clusterSummaryScope, logger)

	prometheusErr := r.deployPrometheus(ctx, clusterSummaryScope, logger)

	contourErr := r.deployContour(ctx, clusterSummaryScope, logger)

	if coreResourceErr != nil {
		return coreResourceErr
	}

	if kyvernoErr != nil {
		return kyvernoErr
	}

	if gatekeeperErr != nil {
		return gatekeeperErr
	}

	if prometheusErr != nil {
		return prometheusErr
	}

	if contourErr != nil {
		return contourErr
	}

	return nil
}

func (r *ClusterSummaryReconciler) deployResources(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeatureResources,
		currentHash: resourcesHash,
		deploy:      deployResources,
		getRefs:     getResourceRefs,
	}

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployKyverno(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration == nil {
		logger.V(logs.LogDebug).Info("no kyverno configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope, configv1alpha1.FeatureKyverno) {
			logger.V(logs.LogDebug).Info("no kyverno status. Do not reconcile this")
			return nil
		}
	}

	f := feature{
		id:          configv1alpha1.FeatureKyverno,
		currentHash: kyvernoHash,
		deploy:      deployKyverno,
		getRefs:     getKyvernoRefs,
	}

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployGatekeeper(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration == nil {
		logger.V(logs.LogDebug).Info("no gatekeeper configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope, configv1alpha1.FeatureGatekeeper) {
			logger.V(logs.LogDebug).Info("no gatekeeper status. Do not reconcile this")
			return nil
		}
	}

	f := feature{
		id:          configv1alpha1.FeatureGatekeeper,
		currentHash: gatekeeperHash,
		deploy:      deployGatekeeper,
		getRefs:     getGatekeeperRefs,
	}

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployPrometheus(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration == nil {
		logger.V(logs.LogDebug).Info("no prometheus configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope, configv1alpha1.FeaturePrometheus) {
			logger.V(logs.LogDebug).Info("no prometheus status. Do not reconcile this")
			return nil
		}
	}

	f := feature{
		id:          configv1alpha1.FeaturePrometheus,
		currentHash: prometheusHash,
		deploy:      deployPrometheus,
		getRefs:     getPrometheusRefs,
	}

	return r.deployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) deployContour(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration == nil {
		logger.V(logs.LogDebug).Info("no contour configuration")
		if !r.isFeatureStatusPresent(clusterSummaryScope, configv1alpha1.FeatureContour) {
			logger.V(logs.LogDebug).Info("no contour status. Do not reconcile this")
			return nil
		}
	}

	f := feature{
		id:          configv1alpha1.FeatureContour,
		currentHash: contourHash,
		deploy:      deployContour,
		getRefs:     getContourRefs,
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
			logger.V(logs.LogInfo).Info(fmt.Sprintf("cluster %s/%s not found. Nothing to do.", clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName))
			return nil
		}
		return err
	}

	resourceErr := r.undeployResources(ctx, clusterSummaryScope, logger)

	kyvernoErr := r.undeployKyverno(ctx, clusterSummaryScope, logger)

	gatekeeperErr := r.unDeployGatekeeper(ctx, clusterSummaryScope, logger)

	prometheusErr := r.undeployPrometheus(ctx, clusterSummaryScope, logger)

	contourErr := r.undeployContour(ctx, clusterSummaryScope, logger)

	if resourceErr != nil {
		return resourceErr
	}

	if kyvernoErr != nil {
		return kyvernoErr
	}

	if gatekeeperErr != nil {
		return gatekeeperErr
	}

	if prometheusErr != nil {
		return prometheusErr
	}

	if contourErr != nil {
		return contourErr
	}

	return nil
}

func (r *ClusterSummaryReconciler) undeployResources(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeatureResources,
		currentHash: resourcesHash,
		deploy:      undeployResources,
	}

	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeployKyverno(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeatureKyverno,
		currentHash: kyvernoHash,
		deploy:      unDeployKyverno,
	}

	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) unDeployGatekeeper(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeatureGatekeeper,
		currentHash: gatekeeperHash,
		deploy:      unDeployGatekeeper,
	}

	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeployPrometheus(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeaturePrometheus,
		currentHash: prometheusHash,
		deploy:      unDeployPrometheus,
	}

	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) undeployContour(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) error {
	f := feature{
		id:          configv1alpha1.FeatureContour,
		currentHash: contourHash,
		deploy:      unDeployContour,
	}

	return r.undeployFeature(ctx, clusterSummaryScope, f, logger)
}

func (r *ClusterSummaryReconciler) updatesMaps(clusterSummaryScope *scope.ClusterSummaryScope) {
	currentReferences := r.getCurrentReferences(clusterSummaryScope)

	r.Mux.Lock()
	defer r.Mux.Unlock()

	// Get list of References not referenced anymore by ClusterSummary
	var toBeRemoved []string
	if v, ok := r.ClusterSummaryMap[clusterSummaryScope.Name()]; ok {
		toBeRemoved = v.difference(currentReferences)
	}

	// For each currently referenced instance, add ClusterSummary as consumer
	for referencedResource := range currentReferences.data {
		r.getReferenceMapForEntry(referencedResource).insert(clusterSummaryScope.Name())
	}

	// For each resource not reference anymore, remove ClusterSummary as consumer
	for i := range toBeRemoved {
		referencedResource := toBeRemoved[i]
		r.getReferenceMapForEntry(referencedResource).erase(clusterSummaryScope.Name())
	}

	// Update list of WorklaodRoles currently referenced by ClusterSummary
	r.ClusterSummaryMap[clusterSummaryScope.Name()] = currentReferences
}

func (r *ClusterSummaryReconciler) getCurrentReferences(clusterSummaryScope *scope.ClusterSummaryScope) *Set {
	currentReferences := &Set{}
	for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ResourceRefs {
		cmNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ResourceRefs[i].Namespace
		cmName := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ResourceRefs[i].Name
		currentReferences.insert(getEntryKey(ConfigMap, cmNamespace, cmName))
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration != nil {
		for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs {
			cmNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs[i].Namespace
			cmName := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs[i].Name
			currentReferences.insert(getEntryKey(ConfigMap, cmNamespace, cmName))
		}
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration != nil {
		for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs {
			cmNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs[i].Namespace
			cmName := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs[i].Name
			currentReferences.insert(getEntryKey(ConfigMap, cmNamespace, cmName))
		}
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration != nil {
		for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs {
			cmNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs[i].Namespace
			cmName := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs[i].Name
			currentReferences.insert(getEntryKey(ConfigMap, cmNamespace, cmName))
		}
	}
	if clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration != nil {
		for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.PolicyRefs {
			cmNamespace := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.PolicyRefs[i].Namespace
			cmName := clusterSummaryScope.ClusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration.PolicyRefs[i].Name
			currentReferences.insert(getEntryKey(ConfigMap, cmNamespace, cmName))
		}
	}
	return currentReferences
}

func (r *ClusterSummaryReconciler) getReferenceMapForEntry(entry string) *Set {
	s := r.ReferenceMap[entry]
	if s == nil {
		s = &Set{}
		r.ReferenceMap[entry] = s
	}
	return s
}
