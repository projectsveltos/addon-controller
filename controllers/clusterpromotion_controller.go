/*
Copyright 2025. projectsveltos.io. All rights reserved.

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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// maxFreeClusterPromotionStages is how many ClusterPromotion instances remain usable
	// without a license, absent a valid license (top-X by creation order).
	maxFreeClusterPromotionStages = 2
)

// ClusterPromotionEnterpriseDeps bundles what the Sveltos Enterprise ClusterPromotion
// implementation needs.
type ClusterPromotionEnterpriseDeps struct {
	Client           client.Client
	Config           *rest.Config
	Scheme           *runtime.Scheme
	EventRecorder    events.EventRecorder
	SveltosNamespace string
}

// reconcileClusterPromotionNormal implements ClusterPromotion's stage-advancement business
// logic (a Sveltos Enterprise feature). The default (clusterpromotion_default.go) is a stub
// that reports the feature is unavailable. A Sveltos Enterprise build wires in the real
// implementation via SetClusterPromotionReconciler before starting the manager; this package
// never imports anything private itself.
var (
	reconcileClusterPromotionNormal func(ctx context.Context, deps ClusterPromotionEnterpriseDeps,
		promotionScope *scope.ClusterPromotionScope, isInFreeTopX bool, logger logr.Logger) reconcile.Result
)

// SetClusterPromotionReconciler overrides the ClusterPromotion stage-advancement implementation.
// Called by a Sveltos Enterprise build's composition root before starting the manager.
func SetClusterPromotionReconciler(fn func(ctx context.Context, deps ClusterPromotionEnterpriseDeps,
	promotionScope *scope.ClusterPromotionScope, isInFreeTopX bool, logger logr.Logger) reconcile.Result) {

	reconcileClusterPromotionNormal = fn
}

var (
	clusterPromotionNameLabel = "config.projectsveltos.io/promotionname"
)

// ClusterPromotionReconciler reconciles a ClusterPromotion object
type ClusterPromotionReconciler struct {
	client.Client
	Config               *rest.Config
	Scheme               *runtime.Scheme
	eventRecorder        events.EventRecorder
	ConcurrentReconciles int
}

// +kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterpromotions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterpromotions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterpromotions/finalizers,verbs=update
// +kubebuilder:rbac:groups=config.projectsveltos.io,resources=clusterprofiles,verbs=get;list;watch;create;update;patch;delete

func (r *ClusterPromotionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogDebug).Info("Reconciling")

	// Fecth the ClusterPromotion instance
	clusterPromotion := &configv1beta1.ClusterPromotion{}
	if err := r.Get(ctx, req.NamespacedName, clusterPromotion); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ClusterPromotion")
		return reconcile.Result{}, fmt.Errorf("failed to fetch ClusterPromotion %s: %w", req.NamespacedName, err)
	}

	promotionScope, err := scope.NewClusterPromotionScope(
		&scope.ClusterPromotionScopeParams{
			Client:           r.Client,
			Logger:           logger,
			ClusterPromotion: clusterPromotion,
			ControllerName:   "clusterPromotion",
		})
	if err != nil {
		logger.Error(err, "Failed to create promotionScope")
		return reconcile.Result{}, fmt.Errorf("unable to create promotion scope for %s: %w", req.NamespacedName, err)
	}

	licenseManagerInstance := GetLicenseManager()

	if !clusterPromotion.DeletionTimestamp.IsZero() {
		licenseManagerInstance.RemoveClusterPromotion(req.Namespace, req.Name)
	} else {
		licenseManagerInstance.AddClusterPromotion(clusterPromotion)
	}

	// Always close the scope when exiting this function so we can persist any ClusterPromotion
	// changes.
	defer func() {
		if err := promotionScope.Close(ctx); err != nil {
			reterr = err
		}
	}()

	// Handle deleted instance
	if !clusterPromotion.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, promotionScope), nil
	}

	// Handle non-deleted instance
	return r.reconcileNormal(ctx, promotionScope), nil
}

func (r *ClusterPromotionReconciler) reconcileDelete(
	ctx context.Context,
	promotionScope *scope.ClusterPromotionScope) reconcile.Result {

	logger := promotionScope.Logger
	logger.V(logs.LogDebug).Info("Reconciling ClusterPromotion delete")

	if !promotionScope.ClusterPromotion.Spec.PreserveClusterProfilesOnDelete {
		if err := r.cleanClusterProfiles(ctx, promotionScope.ClusterPromotion); err != nil {
			promotionScope.V(logs.LogInfo).Error(err, "failed to clean ClusterProfiles")
			return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}
		}

		if !r.allClusterProfilesGone(ctx, promotionScope.ClusterPromotion, promotionScope.Logger) {
			msg := "not all ClusterProfiles are gone"
			promotionScope.V(logs.LogInfo).Info(msg)
			return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}
		}
	}

	finalizer := configv1beta1.ClusterPromotionFinalizer
	if controllerutil.ContainsFinalizer(promotionScope.ClusterPromotion, finalizer) {
		controllerutil.RemoveFinalizer(promotionScope.ClusterPromotion, finalizer)
	}

	return reconcile.Result{}
}

// reconcileNormal ensures the finalizer is present, then delegates ClusterPromotion's
// stage-advancement business logic (and its license entitlement check) to the Sveltos
// Enterprise library. isInFreeTopX is computed here, since only addon-controller tracks
// every ClusterPromotion instance across reconciles.
func (r *ClusterPromotionReconciler) reconcileNormal(
	ctx context.Context,
	promotionScope *scope.ClusterPromotionScope) reconcile.Result {

	logger := promotionScope.Logger

	if !controllerutil.ContainsFinalizer(promotionScope.ClusterPromotion, configv1beta1.ClusterPromotionFinalizer) {
		if err := r.addFinalizer(ctx, promotionScope); err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to add finalizer")
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
		}
	}

	isInFreeTopX := GetLicenseManager().IsClusterPromotionInTopX("", promotionScope.ClusterPromotion.Name,
		maxFreeClusterPromotionStages)

	deps := ClusterPromotionEnterpriseDeps{
		Client:           r.Client,
		Config:           r.Config,
		Scheme:           r.Scheme,
		EventRecorder:    r.eventRecorder,
		SveltosNamespace: getSveltosNamespace(),
	}

	return reconcileClusterPromotionNormal(ctx, deps, promotionScope, isInFreeTopX, logger)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterPromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&configv1beta1.ClusterPromotion{}, builder.WithPredicates(
			predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
				predicate.AnnotationChangedPredicate{},
				DependenciesHashChangedPredicate{},
			),
		)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.ConcurrentReconciles,
		}).
		Build(r)

	r.eventRecorder = mgr.GetEventRecorder("event-recorder")
	return err
}

func (r *ClusterPromotionReconciler) addFinalizer(ctx context.Context, promotionScope *scope.ClusterPromotionScope) error {
	controllerutil.AddFinalizer(promotionScope.ClusterPromotion, configv1beta1.ClusterPromotionFinalizer)
	// Register the finalizer immediately to avoid orphaning clusterprofile resources on delete
	if err := promotionScope.PatchObject(ctx); err != nil {
		promotionScope.Error(err, "Failed to add finalizer")
		return fmt.Errorf(
			"failed to add finalizer for %s: %w",
			promotionScope.Name(), err,
		)
	}
	return nil
}

// getMainDeploymentClusterProfileLabels returns the labels added to the main ClusterProfile
// created for a given ClusterPromotion instance. Deletion needs this to find the
// ClusterProfiles a ClusterPromotion owns regardless of license status, so it stays open
// source; the Sveltos Enterprise library duplicates this tiny helper for the creation path.
func getMainDeploymentClusterProfileLabels(clusterPromotion *configv1beta1.ClusterPromotion,
) map[string]string {

	return map[string]string{
		clusterPromotionNameLabel: clusterPromotion.Name,
	}
}

func (r *ClusterPromotionReconciler) cleanClusterProfiles(ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion) error {

	listOptions := []client.ListOption{
		client.MatchingLabels(getMainDeploymentClusterProfileLabels(clusterPromotion)),
	}
	clusterProfiles := &configv1beta1.ClusterProfileList{}
	err := r.List(ctx, clusterProfiles, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterProfiles.Items {
		_ = r.Delete(ctx, &clusterProfiles.Items[i])
	}

	return nil
}

func (r *ClusterPromotionReconciler) allClusterProfilesGone(ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion, logger logr.Logger) bool {

	listOptions := []client.ListOption{
		client.MatchingLabels(getMainDeploymentClusterProfileLabels(clusterPromotion)),
	}
	clusterProfiles := &configv1beta1.ClusterProfileList{}
	err := r.List(ctx, clusterProfiles, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to query clusterProfiles", "error", err)
		return false
	}

	if len(clusterProfiles.Items) > 0 {
		logger.V(logs.LogInfo).Info(
			fmt.Sprintf("not all clusterProfiles are gone. %d still left",
				len(clusterProfiles.Items)))
		return false
	}

	return true
}
