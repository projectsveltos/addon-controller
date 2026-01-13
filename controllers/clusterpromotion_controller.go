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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	"github.com/pkg/errors"
	"github.com/robfig/cron"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	license "github.com/projectsveltos/libsveltos/lib/licenses"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

var (
	clusterPromotionNameLabel          = "config.projectsveltos.io/promotionname"
	stageNameAnnotation                = "config.projectsveltos.io/stagename"
	preVerificationClusterProfileLabel = "config.projectsveltos.io/promotion-verification"
)

const (
	// normalStageRequeueAfter is the default time to wait before re-queuing the ClusterPromotion
	normalStageRequeueAfter = 2 * time.Minute
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

func (r *ClusterPromotionReconciler) reconcileNormal(
	ctx context.Context,
	promotionScope *scope.ClusterPromotionScope) reconcile.Result {

	logger := promotionScope.Logger
	logger.V(logs.LogDebug).Info("Reconciling ClusterPromotion")

	if !controllerutil.ContainsFinalizer(promotionScope.ClusterPromotion, configv1beta1.ClusterPromotionFinalizer) {
		if err := r.addFinalizer(ctx, promotionScope); err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to add finalizer")
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
		}
	}

	isEligible, err := r.verifyStageEligibility(ctx, promotionScope, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	if !isEligible {
		logger.V(logs.LogInfo).Info("license is required")
		r.updateStatusWithMissingLicenseError(promotionScope)
		return reconcile.Result{}
	}

	err = r.removeStaleClusterProfiles(ctx, promotionScope)
	if err != nil {
		promotionScope.Error(err, "Failed to remove stale ClusterProfiles")
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	// Checks whether the **Stage** configuration (e.g., cluster selector, trigger)
	// or the **ClusterProfile** resource applied by the current stage has changed.
	// In either case, a restart of the promotion process is necessary.
	restart, newProfileSpecHash, newStagesHash, err := r.needsPromotionRestart(promotionScope)
	if err != nil {
		promotionScope.Error(err, "Failed to check if promotion needs restart")
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	r.setPromotionHashesInStatus(promotionScope, newProfileSpecHash, newStagesHash)

	if restart {
		return r.handlePromotionRestart(ctx, promotionScope, logger)
	}

	currentStageName := getCurrentStage(promotionScope.ClusterPromotion)
	l := logger.WithValues("stage", currentStageName)
	if !r.isStageProvisioned(promotionScope.ClusterPromotion, currentStageName) {
		deployed, message, err := r.checkCurrentStageDeployment(ctx, promotionScope.ClusterPromotion, l)
		if err != nil {
			promotionScope.Error(err, "Failed to verify current stage deployment status")
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
		}
		if !deployed {
			updateStageStatus(promotionScope.ClusterPromotion, currentStageName, false, &message)
			return reconcile.Result{Requeue: true, RequeueAfter: normalStageRequeueAfter}
		}

		l.V(logs.LogDebug).Info(fmt.Sprintf("Stage %s provisioned", currentStageName))
		r.eventRecorder.Eventf(promotionScope.ClusterPromotion, nil, corev1.EventTypeNormal, "Sveltos",
			configv1beta1.ClusterPromotionKind, fmt.Sprintf("Stage %s provisioned", currentStageName))
		// Stage is successfully deployed
		updateStageStatus(promotionScope.ClusterPromotion, currentStageName, true, nil)
		updateStageDescription(promotionScope.ClusterPromotion, currentStageName, message)
	}

	return r.advanceToNextStage(ctx, promotionScope, l)
}

// handlePromotionRestart restarts the promotion process from Stage 1.
func (r *ClusterPromotionReconciler) handlePromotionRestart(ctx context.Context,
	promotionScope *scope.ClusterPromotionScope, logger logr.Logger) reconcile.Result {

	// The minimum length of stages is 1 so accessing the first stage is safe
	firstStage := promotionScope.ClusterPromotion.Spec.Stages[0]

	l := logger.WithValues("stage", firstStage.Name)

	if err := r.reconcileStageProfile(ctx, promotionScope, &firstStage, l); err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
	}

	r.eventRecorder.Eventf(promotionScope.ClusterPromotion, nil, corev1.EventTypeNormal, "Sveltos",
		configv1beta1.ClusterPromotionKind, fmt.Sprintf("Provisioning stage %s (Restart)", firstStage.Name))

	resetStageStatuses(promotionScope.ClusterPromotion)
	addStageStatus(promotionScope.ClusterPromotion, firstStage.Name)
	updateStageDescription(promotionScope.ClusterPromotion, firstStage.Name,
		"Clusters are being provisioned")

	return reconcile.Result{Requeue: true, RequeueAfter: normalStageRequeueAfter}
}

// advanceToNextStage checks if promotion criteria are met and moves to the next stage if possible.
func (r *ClusterPromotionReconciler) advanceToNextStage(ctx context.Context,
	promotionScope *scope.ClusterPromotionScope, logger logr.Logger) reconcile.Result {

	currentStageName := getCurrentStage(promotionScope.ClusterPromotion)

	// Current Stage is provisioned. Evaluates whether moving to next stage is needed.
	canAdvance, err := r.doMoveToNextStage(ctx, promotionScope.ClusterPromotion, logger)
	if err != nil {
		return reconcile.Result{Requeue: true, RequeueAfter: normalStageRequeueAfter}
	}

	if !canAdvance {
		// Not ready to advance, requeue and wait.
		return reconcile.Result{Requeue: true, RequeueAfter: normalStageRequeueAfter}
	}

	updateStageStatus(promotionScope.ClusterPromotion, currentStageName, true, nil)
	updateStageDescription(promotionScope.ClusterPromotion, currentStageName,
		"Successfully provisioned")

	if err := r.deleteCheckDeploymentClusterProfile(ctx, promotionScope.ClusterPromotion,
		currentStageName, logger); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to delete preHealthCheckDeployment profile")
		return reconcile.Result{Requeue: true, RequeueAfter: deleteRequeueAfter}
	}

	nextStage := r.getNextStage(promotionScope.ClusterPromotion, currentStageName)
	if nextStage != nil {
		// Move to next stage
		logger.V(logs.LogInfo).Info("ClusterPromotion advancing to next stage",
			"nexStage", nextStage.Name)

		if err := r.reconcileStageProfile(ctx, promotionScope, nextStage, logger); err != nil {
			return reconcile.Result{Requeue: true, RequeueAfter: normalRequeueAfter}
		}

		addStageStatus(promotionScope.ClusterPromotion, nextStage.Name)
		r.eventRecorder.Eventf(promotionScope.ClusterPromotion, nil, corev1.EventTypeNormal, "Sveltos",
			configv1beta1.ClusterPromotionKind, fmt.Sprintf("Provisioning stage %s", nextStage.Name))

		return reconcile.Result{Requeue: true, RequeueAfter: normalStageRequeueAfter}
	}

	// No more stages — promotion is complete
	promotionScope.Logger.V(logs.LogInfo).Info("ClusterPromotion is complete")
	promotionScope.Logger.V(logs.LogDebug).Info("Reconcile success")

	return reconcile.Result{}
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

// StageHashable defines the structure used *only* for hashing,
// intentionally omitting the 'Trigger' field.
type StageHashable struct {
	Name            string                     `json:"name"`
	ClusterSelector libsveltosv1beta1.Selector `json:"clusterSelector,omitempty"`
}

func (r *ClusterPromotionReconciler) getStagesHash(spec *configv1beta1.ClusterPromotionSpec) ([]byte, error) {
	// Create a new slice of the hashable stage structure.
	// The Trigger field in the Stage struct is used for runtime progression logic (like manual approval),
	// changes to it shouldn't trigger a re-hash. So exclude Trigger field.
	stagesForHash := make([]StageHashable, len(spec.Stages))
	for i, stage := range spec.Stages {
		stagesForHash[i] = StageHashable{
			Name:            stage.Name,
			ClusterSelector: stage.ClusterSelector,
		}
	}

	stagesBytes, err := json.Marshal(stagesForHash)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Stages for hashing: %w", err)
	}

	h := sha256.New()
	if _, err := h.Write(stagesBytes); err != nil {
		return nil, fmt.Errorf("failed to write Stages bytes to total: %w", err)
	}

	return h.Sum(nil), nil
}

func (r *ClusterPromotionReconciler) getProfileSpecHash(profileSpec *configv1beta1.ProfileSpec,
) ([]byte, error) {

	h := sha256.New()

	specCopy := profileSpec

	specCopy.HelmCharts = getSortedHelmCharts(profileSpec.HelmCharts)
	specCopy.KustomizationRefs = getSortedKustomizationRefs(profileSpec.KustomizationRefs)
	specCopy.PolicyRefs = getSortedPolicyRefs(profileSpec.PolicyRefs)

	// Use sort.Strings for []string
	specCopy.DependsOn = make([]string, len(profileSpec.DependsOn))
	copy(specCopy.DependsOn, profileSpec.DependsOn)
	sort.Strings(specCopy.DependsOn)

	specCopy.TemplateResourceRefs = getSortedTemplateResourceRefs(profileSpec.TemplateResourceRefs)
	specCopy.ValidateHealths = getSortedValidateHealths(profileSpec.ValidateHealths)
	specCopy.Patches = getSortedPatches(profileSpec.Patches)
	specCopy.DriftExclusions = getSortedDriftExclusions(profileSpec.DriftExclusions)

	jsonBytes, err := json.Marshal(specCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ProfileSpec for hashing: %w", err)
	}

	if _, err := h.Write(jsonBytes); err != nil {
		return nil, fmt.Errorf("failed to write ProfileSpec bytes to hash: %w", err)
	}

	return h.Sum(nil), nil
}

// profileSpecChanged compares the old profileSpecHash from the status with the
// currently computed hash from the spec. It returns true if the hash has changed
// (meaning the ProfileSpec configuration has been modified).
func (r *ClusterPromotionReconciler) profileSpecChanged(promotionScope *scope.ClusterPromotionScope,
) (
	changed bool,
	newProfileSpecHash []byte,
	err error,
) {

	oldHash := promotionScope.ProfileSpecHash()

	currentHash, err := r.getProfileSpecHash(&promotionScope.ClusterPromotion.Spec.ProfileSpec)
	if err != nil {
		promotionScope.Logger.V(logs.LogInfo).Info("getProfileSpecHash failed", "error", err)
		return false, oldHash, err
	}

	return !bytes.Equal(oldHash, currentHash), currentHash, nil
}

func (r *ClusterPromotionReconciler) setPromotionHashesInStatus(promotionScope *scope.ClusterPromotionScope,
	newProfileSpecHash, newStagesHash []byte) {

	promotionScope.ClusterPromotion.Status.StagesHash = newStagesHash
	promotionScope.ClusterPromotion.Status.ProfileSpecHash = newProfileSpecHash
}

// stagesChanged compares the old stages hash from the status with the
// currently computed hash from the spec. It returns true if the hash has changed
// (meaning the ProfileSpec configuration has been modified).
func (r *ClusterPromotionReconciler) stagesChanged(promotionScope *scope.ClusterPromotionScope,
) (
	changed bool,
	newStagesHash []byte,
	err error,
) {

	oldHash := promotionScope.StagesHash()

	currentHash, err := r.getStagesHash(&promotionScope.ClusterPromotion.Spec)
	if err != nil {
		promotionScope.Logger.V(logs.LogInfo).Info("getStagesHash failed", "error", err)
		return false, oldHash, err
	}

	return !bytes.Equal(oldHash, currentHash), currentHash, nil
}

func (r *ClusterPromotionReconciler) needsPromotionRestart(promotionScope *scope.ClusterPromotionScope,
) (
	restart bool,
	newProfileSpecHash []byte,
	newStagesHash []byte,
	err error,
) {

	// If ProfileSpec or Stages changed or Stages has changed, restart
	// the promotion cycle

	// 1. Check for Stages changes and calculate new hash
	stagesChanged, newStagesHash, err := r.stagesChanged(promotionScope)
	if err != nil {
		return false, nil, nil, err
	}

	// 2. Check for ProfileSpec changes and calculate new hash
	profileSpecChanged, newProfileSpecHash, err := r.profileSpecChanged(promotionScope)
	if err != nil {
		return false, nil, nil, err
	}

	//  Determine if a restart is needed
	restart = stagesChanged || profileSpecChanged

	return restart, newProfileSpecHash, newStagesHash, nil
}

// reconcileStageProfile creates/updates the ClusterProfile for the given stage
func (r *ClusterPromotionReconciler) reconcileStageProfile(ctx context.Context,
	promotionScope *scope.ClusterPromotionScope, stage *configv1beta1.Stage, logger logr.Logger,
) error {

	if err := r.deleteCheckDeploymentClusterProfile(ctx, promotionScope.ClusterPromotion, stage.Name,
		logger); err != nil {
		return err
	}

	logger.V(logs.LogInfo).Info("Reconciling ClusterProfile for stage")

	addTypeInformationToObject(r.Scheme, promotionScope.ClusterPromotion)

	clusterProfile := &configv1beta1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: mainDeploymentClusterProfileName(promotionScope.Name(), stage.Name),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: configv1beta1.GroupVersion.String(),
					Kind:       promotionScope.ClusterPromotion.GetObjectKind().GroupVersionKind().Kind,
					Name:       promotionScope.ClusterPromotion.GetName(),
					UID:        promotionScope.ClusterPromotion.GetUID(),
				},
			},
			Labels:      getMainDeploymentClusterProfileLabels(promotionScope.ClusterPromotion),
			Annotations: getStageAnnotation(stage.Name),
		},
	}

	// The desired ClusterProfile Spec is the combination of the ClusterPromotion's
	// global ProfileSpec and the stage-specific ClusterSelector.
	desiredSpec := configv1beta1.Spec{
		// 1. Copy all fields from ClusterPromotionSpec.ProfileSpec
		SyncMode:               promotionScope.ClusterPromotion.Spec.ProfileSpec.SyncMode,
		Tier:                   promotionScope.ClusterPromotion.Spec.ProfileSpec.Tier,
		ContinueOnConflict:     promotionScope.ClusterPromotion.Spec.ProfileSpec.ContinueOnConflict,
		ContinueOnError:        promotionScope.ClusterPromotion.Spec.ProfileSpec.ContinueOnError,
		MaxUpdate:              promotionScope.ClusterPromotion.Spec.ProfileSpec.MaxUpdate,
		StopMatchingBehavior:   promotionScope.ClusterPromotion.Spec.ProfileSpec.StopMatchingBehavior,
		Reloader:               promotionScope.ClusterPromotion.Spec.ProfileSpec.Reloader,
		TemplateResourceRefs:   promotionScope.ClusterPromotion.Spec.ProfileSpec.TemplateResourceRefs,
		DependsOn:              promotionScope.ClusterPromotion.Spec.ProfileSpec.DependsOn,
		PolicyRefs:             promotionScope.ClusterPromotion.Spec.ProfileSpec.PolicyRefs,
		HelmCharts:             promotionScope.ClusterPromotion.Spec.ProfileSpec.HelmCharts,
		KustomizationRefs:      promotionScope.ClusterPromotion.Spec.ProfileSpec.KustomizationRefs,
		ValidateHealths:        promotionScope.ClusterPromotion.Spec.ProfileSpec.ValidateHealths,
		Patches:                promotionScope.ClusterPromotion.Spec.ProfileSpec.Patches,
		PatchesFrom:            promotionScope.ClusterPromotion.Spec.ProfileSpec.PatchesFrom,
		DriftExclusions:        promotionScope.ClusterPromotion.Spec.ProfileSpec.DriftExclusions,
		MaxConsecutiveFailures: promotionScope.ClusterPromotion.Spec.ProfileSpec.MaxConsecutiveFailures,
		PreDeleteChecks:        promotionScope.ClusterPromotion.Spec.ProfileSpec.PreDeleteChecks,
		PostDeleteChecks:       promotionScope.ClusterPromotion.Spec.ProfileSpec.PostDeleteChecks,

		// 2. Set the stage-specific ClusterSelector
		ClusterSelector: stage.ClusterSelector,
	}

	clusterProfile.Spec = desiredSpec

	clusterProfile.SetGroupVersionKind(configv1beta1.GroupVersion.WithKind(configv1beta1.ClusterProfileKind))

	dr, err := k8s_utils.GetDynamicResourceInterface(r.Config, clusterProfile.GroupVersionKind(), "")
	if err != nil {
		promotionScope.Logger.V(logs.LogInfo).Info("failed to get dynamic ResourceInterface", "error", err)
		return err
	}

	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(clusterProfile)
	if err != nil {
		promotionScope.Logger.V(logs.LogInfo).Info("failed to convert ClusterProfile to Unstructured", "error", err)
		return err
	}

	// Convert the Unstructured object to a byte slice for the Patch data.
	patchData, err := json.Marshal(unstructuredObj)
	if err != nil {
		promotionScope.Logger.V(logs.LogInfo).Info("failed to marshal Unstructured object", "error", err)
		return err
	}

	forceConflict := true
	options := metav1.PatchOptions{
		FieldManager: "application/apply-patch",
		Force:        &forceConflict,
	}

	_, err = dr.Patch(ctx, clusterProfile.Name, types.ApplyPatchType, patchData, options)
	if err != nil {
		promotionScope.Error(err, "failed to apply patch for ClusterProfile", "clusterProfileName", clusterProfile.Name)
		return err
	}

	return nil
}

func (r *ClusterPromotionReconciler) checkCurrentStageDeployment(ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion, logger logr.Logger,
) (deployed bool, message string, err error) {

	// Get the Name of the current stage from the status
	currentStageName := clusterPromotion.Status.CurrentStageName
	if currentStageName == "" {
		logger.V(logs.LogDebug).Info("No current stage is set, nothing to check.")
		return true, "No current stage to check.", nil
	}

	clusterProfileName := mainDeploymentClusterProfileName(clusterPromotion.Name, currentStageName)

	currentClusterProfile := &configv1beta1.ClusterProfile{}

	// Check if the ClusterProfile exists
	if err := r.Get(ctx, types.NamespacedName{Name: clusterProfileName}, currentClusterProfile); err != nil {
		logger.Error(err, "failed to fetch ClusterProfile", "clusterProfileName", clusterProfileName)
		return false, "Failed to fetch ClusterProfile.", err
	}

	// Assuming addTypeInformationToObject is a helper that ensures GVK is set
	addTypeInformationToObject(managementClusterClient.Scheme(), currentClusterProfile)

	tempProfileScope, scopeError := scope.NewProfileScope(scope.ProfileScopeParams{
		Client:         r.Client,
		Logger:         logger,
		Profile:        currentClusterProfile,
		ControllerName: "clusterpromotion",
	})

	if scopeError != nil {
		logger.Error(scopeError, "failed to create ProfileScope", "clusterProfileName", clusterProfileName)
		return false, "Failed to create ProfileScope.", scopeError
	}

	// Check deployment status of ClusterSummaries
	isDeployed, message, checkErr := allClusterSummariesDeployed(ctx, r.Client, tempProfileScope, logger)

	if checkErr != nil {
		logger.Error(checkErr, "Error checking ClusterSummary status for stage", "stageName", currentStageName)
		return false, message, checkErr
	}

	if !isDeployed {
		logger.V(logs.LogDebug).Info("Stage deployment still pending.", "stageName", currentStageName, "status", message)
	} else {
		logger.V(logs.LogDebug).Info("Stage deployment is provisioned.", "stageName", currentStageName, "status", message)
	}
	// Return the result of the deployment check
	return isDeployed, message, nil
}

// Returns the name of the ClusterProfile Sveltos creates for the primary resources
// being deployed in the current promotion stage.
func mainDeploymentClusterProfileName(promotionClusterName, stageName string) string {
	return fmt.Sprintf("%s-%s", promotionClusterName, stageName)
}

// Returns the name of the ClusterProfile Sveltos creates for the PreHealthCheckDeployment
// resources (e.g., a validation Job) in the current promotion stage.
func preCheckDeploymentClusterProfileName(promotionClusterName, stageName string) string {
	return fmt.Sprintf("preverification-%s-%s", promotionClusterName, stageName)
}

// getStageAnnotation returns the annotations added to the managed ClusterProfile
// created for a given ClusterPromotion's stage.
func getStageAnnotation(stageName string,
) map[string]string {

	return map[string]string{
		stageNameAnnotation: stageName,
	}
}

// getMainDeploymentClusterProfileLabels returns the labels added to the main ClusterProfile
// created for a given ClusterPromotion instance.
func getMainDeploymentClusterProfileLabels(clusterPromotion *configv1beta1.ClusterPromotion,
) map[string]string {

	return map[string]string{
		clusterPromotionNameLabel: clusterPromotion.Name,
	}
}

// getPreCheckDeploymentClusterProfileLabels returns the labels added to the ClusterProfile
// created for the PreHealthCheckDeployment step (e.g., validation Job).
func getPreCheckDeploymentClusterProfileLabels(clusterPromotion *configv1beta1.ClusterPromotion,
) map[string]string {

	return map[string]string{
		clusterPromotionNameLabel: clusterPromotion.Name,
		// Adding a distinct label to easily identify this ClusterProfile as part of the pre-verification step.
		preVerificationClusterProfileLabel: "true",
	}
}

func resetStageStatuses(clusterPromotion *configv1beta1.ClusterPromotion) {
	clusterPromotion.Status.Stages = []configv1beta1.StageStatus{}
	clusterPromotion.Status.LastPromotionTime = nil
}

func addStageStatus(clusterPromotion *configv1beta1.ClusterPromotion, stageName string) {
	clusterPromotion.Status.Stages = append(clusterPromotion.Status.Stages,
		configv1beta1.StageStatus{
			Name:                      stageName,
			LastUpdateReconciledTime:  &metav1.Time{Time: time.Now()},
			FailureMessage:            nil,
			LastSuccessfulAppliedTime: nil,
			LastStatusCheckTime:       nil,
		},
	)
	clusterPromotion.Status.CurrentStageName = stageName
	clusterPromotion.Status.LastPromotionTime = &metav1.Time{Time: time.Now()}
}

func updateStageStatus(clusterPromotion *configv1beta1.ClusterPromotion, stageName string,
	allProvisioned bool, failureMessage *string) {

	now := &metav1.Time{Time: time.Now()}

	for i := range clusterPromotion.Status.Stages {
		if clusterPromotion.Status.Stages[i].Name == stageName {
			clusterPromotion.Status.Stages[i].FailureMessage = failureMessage
			clusterPromotion.Status.Stages[i].LastStatusCheckTime = now

			if allProvisioned {
				clusterPromotion.Status.Stages[i].LastSuccessfulAppliedTime = now
			}
		}
	}
}

func updateStageDescription(clusterPromotion *configv1beta1.ClusterPromotion,
	stageName string, description string) {

	if description == "" {
		return
	}

	nowTime := time.Now().UTC().Truncate(time.Second)
	formattedTime := nowTime.Format("2006-01-02T15:04:05Z")
	message := fmt.Sprintf("%s (%s)", description, formattedTime)

	for i := range clusterPromotion.Status.Stages {
		if clusterPromotion.Status.Stages[i].Name == stageName {
			clusterPromotion.Status.Stages[i].CurrentStatusDescription = &message

			return
		}
	}
}

func getStageSpecByName(clusterPromotion *configv1beta1.ClusterPromotion, name string) *configv1beta1.Stage {
	stages := clusterPromotion.Spec.Stages
	for i := range stages {
		if stages[i].Name == name {
			return &stages[i]
		}
	}
	return nil
}

func getStageStatusByName(clusterPromotion *configv1beta1.ClusterPromotion, name string) *configv1beta1.StageStatus {
	stages := clusterPromotion.Status.Stages
	for i := range stages {
		if stages[i].Name == name {
			return &stages[i]
		}
	}
	return nil
}

func getCurrentStage(clusterPromotion *configv1beta1.ClusterPromotion) string {
	return clusterPromotion.Status.CurrentStageName
}

// isStageProvisioned checks if the stage has completed deployment.
func (r *ClusterPromotionReconciler) isStageProvisioned(clusterPromotion *configv1beta1.ClusterPromotion,
	stageName string) bool {

	if stageName == "" {
		// If no stage is current, assume either finished or not started (not provisioned yet)
		return false
	}

	// Find the StageStatus entry for the current stage
	stageStatus := getStageStatusByName(clusterPromotion, stageName)
	if stageStatus == nil {
		// Status entry hasn't been created yet (shouldn't happen if CurrentStageName is set)
		return false
	}

	// Provisioned if LastSuccessfulAppliedTime is set (non-nil)
	return stageStatus.LastSuccessfulAppliedTime != nil
}

// getNextStage returns the name of the stage following the given stageName.
// If stageName is the last one or not found, it returns an empty string.
func (r *ClusterPromotionReconciler) getNextStage(clusterPromotion *configv1beta1.ClusterPromotion,
	stageName string) *configv1beta1.Stage {

	if stageName == "" {
		return nil
	}

	for i := range clusterPromotion.Spec.Stages {
		if clusterPromotion.Spec.Stages[i].Name != stageName {
			continue
		}

		// Found current stage. Check if there's a next one.
		if i+1 < len(clusterPromotion.Spec.Stages) {
			return &clusterPromotion.Spec.Stages[i+1]
		}
		// No next stage (current is last)
		return nil
	}

	// stageName not found in the list
	return nil
}

// doMoveToNextStage returns true if Sveltos should move to next stage
func (r *ClusterPromotionReconciler) doMoveToNextStage(ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion, logger logr.Logger) (bool, error) {

	currentStageName := getCurrentStage(clusterPromotion)

	if !r.isStageProvisioned(clusterPromotion, currentStageName) {
		return false, nil
	}

	currentStageSpec := getStageSpecByName(clusterPromotion, currentStageName)
	if currentStageSpec == nil {
		errorMsg := fmt.Sprintf("spec not present for stage %s", currentStageName)
		logger.V(logs.LogDebug).Info(errorMsg)
		return false, errors.New(errorMsg)
	}

	if currentStageSpec.Trigger == nil {
		logger.V(logs.LogDebug).Info("stage is completed as no trigger is defined")
		return true, nil
	}

	if currentStageSpec.Trigger.Auto != nil {
		logger.V(logs.LogDebug).Info("trigger is auto")
		return r.canAutoAdvance(ctx, clusterPromotion, currentStageName, currentStageSpec.Trigger.Auto, logger)
	}

	if currentStageSpec.Trigger.Manual != nil {
		logger.V(logs.LogDebug).Info("trigger is manual")
		return r.canManualAdvance(clusterPromotion, currentStageName, currentStageSpec.Trigger.Manual, logger), nil
	}

	return true, nil
}

// canAutoAdvance returns true if Sveltos should move to next stage based on Auto Trigger
func (r *ClusterPromotionReconciler) canAutoAdvance(ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion, currentStageName string,
	autoTrigger *configv1beta1.AutoTrigger, logger logr.Logger) (bool, error) {

	if autoTrigger.Delay != nil {
		currentStageStatus := getStageStatusByName(clusterPromotion, currentStageName)
		if currentStageStatus == nil {
			errorMsg := fmt.Sprintf("status not present for stage %s", currentStageName)
			logger.V(logs.LogDebug).Info(errorMsg)
			return false, errors.New(errorMsg)
		}

		now := time.Now()

		// Convert metav1.Duration (Delay) to time.Duration
		delayDuration := autoTrigger.Delay.Duration

		// Calculate the required wait time (Success Time + Delay Duration)
		requiredReadyTime := currentStageStatus.LastSuccessfulAppliedTime.Add(delayDuration)

		// 2. Check the delay condition
		if now.Before(requiredReadyTime) {
			// The required delay time has NOT yet passed.
			message := fmt.Sprintf("Delayed: Waiting for Time Window: %s",
				requiredReadyTime.Format(time.RFC3339))
			logger.V(logs.LogDebug).Info(message,
				"stage", currentStageName,
				"delay", delayDuration.String(),
				"ready_at", requiredReadyTime.Format(time.RFC3339),
			)

			updateStageDescription(clusterPromotion, currentStageName, message)
			return false, nil
		}
	}

	// --- DELAY HAS PASSED: Deploy PreHealthCheckDeployment --
	stage := getStageSpecByName(clusterPromotion, currentStageName)
	if err := r.reconcilePreHealthCheckDeployment(ctx, clusterPromotion, stage, logger); err != nil {
		return false, err
	}

	// --- DELAY HAS PASSED: Reconcile Post-Delay Health Checks ---
	if err := r.reconcilePostDelayHealthChecks(ctx, clusterPromotion, currentStageName,
		autoTrigger.PostDelayHealthChecks, logger); err != nil {
		logger.V(logs.LogDebug).Info("Running Post-Promotion Health Checks")
		updateStageDescription(clusterPromotion, currentStageName, "Running Post-Promotion Health Checks")

		return false, err
	}

	// Verify ClusterSummary instances are all Provisioned (which means PostDelayHealthChecks) are passing
	deployed, message, err := r.checkCurrentStageDeployment(ctx, clusterPromotion, logger)
	if err != nil {
		logger.Error(err, "Failed to verify current stage deployment status")
		return false, err
	}
	if !deployed {
		logger.V(logs.LogDebug).Info("Awaiting Health Checks to Pass")
		updateStageStatus(clusterPromotion, currentStageName, false, &message)
		updateStageDescription(clusterPromotion, currentStageName, "Awaiting Health Checks to Pass")
		return false, nil
	}

	// Enforce Promotion Window (If Defined)
	if autoTrigger.PromotionWindow != nil {
		isOpen, nextOpenTime, err := r.isPromotionWindowOpen(autoTrigger.PromotionWindow, time.Now(), logger)
		if err != nil {
			logger.V(logs.LogInfo).Error(err, "failed to evaluate promotion window")
			return false, err
		}

		if !isOpen {
			message := fmt.Sprintf("Window Closed: Waiting for Next Schedule (%s).",
				nextOpenTime.Format(time.RFC3339))
			// Promotion Window is currently closed. Block advancement.
			logger.V(logs.LogInfo).Info(message,
				"stage", currentStageName,
				"next_open_time", nextOpenTime.Format(time.RFC3339),
			)
			updateStageDescription(clusterPromotion, currentStageName, message)
			return false, nil
		}
	}

	return true, nil
}

func (r *ClusterPromotionReconciler) isPromotionWindowOpen(promotionWindow *configv1beta1.TimeWindow,
	anchorTime time.Time, logger logr.Logger) (isOpen bool, nextOpenTime *time.Time, err error) {

	schedFrom, err := cron.ParseStandard(promotionWindow.From)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to parse promotionWindow", "from", promotionWindow.From)
		return false, nil, fmt.Errorf("unparseable schedule %q: %w", promotionWindow.From, err)
	}

	schedTo, err := cron.ParseStandard(promotionWindow.To)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to parse promotionWindow", "to", promotionWindow.To)
		return false, nil, fmt.Errorf("unparseable schedule %q: %w", promotionWindow.To, err)
	}

	// Use the cluster's local time for evaluation
	now := anchorTime

	// 1. Establish a Robust Historical Reference Point (a year lookback)
	const safeLookback = 366 * 24 * time.Hour
	referenceTime := now.Add(-safeLookback)

	// 2. Iterate forward from the reference point to find the latest time <= now.
	var prevOpenTime time.Time

	t := schedFrom.Next(referenceTime)

	// Loop until the next scheduled open time is in the future (> now).
	for t.Before(now) || t.Equal(now) {
		prevOpenTime = t
		t = schedFrom.Next(t)
	}

	// 3. Handle Error/Malformity (No 'From' time found in the last year)
	if prevOpenTime.IsZero() {
		// Schedule is extremely infrequent or malformed. Block now and schedule for the absolute next open time.
		nextOpen := schedFrom.Next(now)
		nextOpenTime = &nextOpen
		logger.V(logs.LogInfo).Info("Schedule 'From' is too infrequent or malformed, deferring until next absolute open time.",
			"next_open_time", nextOpenTime.Format(time.RFC3339))
		return false, nextOpenTime, nil
	}

	// --- Determine Next Close Time (nextCloseTime) ---

	// 4. Find the next close time relative to the last time the window opened.
	// This ensures we calculate the end time of the *current* cycle.
	nextCloseTime := schedTo.Next(prevOpenTime)

	// If nextCloseTime is in the past, advance both nextCloseTime and nextCloseTime
	if nextCloseTime.Before(now) || nextCloseTime.Equal(now) {
		prevOpenTime = schedFrom.Next(prevOpenTime)
		nextCloseTime = schedTo.Next(nextCloseTime)
	}

	// 5. Handle Wrapping/Short Windows: Ensure nextCloseTime is *after* the current moment if we missed the open.
	// If the next calculated close time is in the past, step forward until it's in the future.
	for nextCloseTime.Before(now) || nextCloseTime.Equal(now) {
		nextCloseTime = schedTo.Next(nextCloseTime)
	}

	// --- Final Evaluation ---

	// 6. Check if 'now' is within the window: [prevOpenTime, nextCloseTime)
	if now.After(prevOpenTime) && now.Before(nextCloseTime) {
		isOpen = true
		// If open, we requeue the controller when the window closes.
		nextOpenTime = &nextCloseTime
	} else {
		isOpen = false
		// If closed, we must requeue at the next time the window opens.
		nextOpen := schedFrom.Next(now)
		nextOpenTime = &nextOpen
	}

	logger.V(logs.LogDebug).Info("Promotion window evaluated",
		"is_open", isOpen,
		"prev_open", prevOpenTime.Format(time.RFC3339),
		"next_close", nextCloseTime.Format(time.RFC3339),
		"next_requeue_time", nextOpenTime.Format(time.RFC3339),
	)

	return isOpen, nextOpenTime, nil
}

func (r *ClusterPromotionReconciler) deleteCheckDeploymentClusterProfile(ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion, stageName string, logger logr.Logger,
) error {

	profileName := preCheckDeploymentClusterProfileName(clusterPromotion.Name, stageName)
	clusterProfile := &configv1beta1.ClusterProfile{}
	err := r.Get(ctx, types.NamespacedName{Name: profileName}, clusterProfile)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		logger.V(logs.LogInfo).Error(err, "failed to get preCheckDeployment ClusterProfile")
		return err
	}

	logger.V(logs.LogDebug).Info("deleting preCheckDeployment ClusterProfile")
	return r.Delete(ctx, clusterProfile)
}

// reconcilePreHealthCheckDeployment creates/updates the PreHealthCheck ClusterProfile
// for the given stage
func (r *ClusterPromotionReconciler) reconcilePreHealthCheckDeployment(ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion, stage *configv1beta1.Stage,
	logger logr.Logger) error {

	if stage.Trigger == nil || stage.Trigger.Auto == nil ||
		len(stage.Trigger.Auto.PreHealthCheckDeployment) == 0 {

		return nil
	}

	logger.V(logs.LogDebug).Info("create/update PreHealthCheckDeployment ClusterProfile")
	clusterProfile := &configv1beta1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: preCheckDeploymentClusterProfileName(clusterPromotion.Name, stage.Name),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: configv1beta1.GroupVersion.String(),
					Kind:       clusterPromotion.GetObjectKind().GroupVersionKind().Kind,
					Name:       clusterPromotion.GetName(),
					UID:        clusterPromotion.GetUID(),
				},
			},
			Labels:      getPreCheckDeploymentClusterProfileLabels(clusterPromotion),
			Annotations: getStageAnnotation(stage.Name),
		},
	}

	// The desired ClusterProfile Spec is the combination of the ClusterPromotion's
	// global ProfileSpec and the stage-specific ClusterSelector.
	desiredSpec := configv1beta1.Spec{
		// 1. Copy all fields from ClusterPromotionSpec.ProfileSpec
		SyncMode:           configv1beta1.SyncModeContinuous,
		Tier:               clusterPromotion.Spec.ProfileSpec.Tier,
		ContinueOnConflict: clusterPromotion.Spec.ProfileSpec.ContinueOnConflict,
		ContinueOnError:    clusterPromotion.Spec.ProfileSpec.ContinueOnError,

		// 2. Set the stage-specific ClusterSelector
		ClusterSelector: stage.ClusterSelector,

		// 3. Set PreHealthCheckDeployment
		PolicyRefs: stage.Trigger.Auto.PreHealthCheckDeployment,
	}

	clusterProfile.Spec = desiredSpec

	clusterProfile.SetGroupVersionKind(configv1beta1.GroupVersion.WithKind(configv1beta1.ClusterProfileKind))

	dr, err := k8s_utils.GetDynamicResourceInterface(r.Config, clusterProfile.GroupVersionKind(), "")
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to get dynamic ResourceInterface", "error", err)
		return err
	}

	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(clusterProfile)
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to convert ClusterProfile to Unstructured", "error", err)
		return err
	}

	// Convert the Unstructured object to a byte slice for the Patch data.
	patchData, err := json.Marshal(unstructuredObj)
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to marshal Unstructured object", "error", err)
		return err
	}

	forceConflict := true
	options := metav1.PatchOptions{
		FieldManager: "application/apply-patch",
		Force:        &forceConflict,
	}

	_, err = dr.Patch(ctx, clusterProfile.Name, types.ApplyPatchType, patchData, options)
	if err != nil {
		logger.Error(err, "failed to apply patch for ClusterProfile", "clusterProfileName", clusterProfile.Name)
		return err
	}

	return nil
}

// reconcilePostDelayHealthChecks updates the ClusterProfile's Spec.ValidateHealths
// to include the PostDelayHealthChecks if they are not already present.
func (r *ClusterPromotionReconciler) reconcilePostDelayHealthChecks(
	ctx context.Context,
	clusterPromotion *configv1beta1.ClusterPromotion,
	currentStageName string,
	postDelayChecks []libsveltosv1beta1.ValidateHealth,
	logger logr.Logger,
) error {

	clusterProfileName := mainDeploymentClusterProfileName(clusterPromotion.Name, currentStageName)
	currentClusterProfile := &configv1beta1.ClusterProfile{}

	if err := r.Get(ctx, types.NamespacedName{Name: clusterProfileName}, currentClusterProfile); err != nil {
		logger.Error(err, "failed to fetch ClusterProfile", "clusterProfileName", clusterProfileName)
		return err
	}

	// 1. Check if the post-delay checks are already included in the Spec.
	if r.postDelayChecksAlreadyIncluded(currentClusterProfile.Spec.ValidateHealths, postDelayChecks) {
		logger.V(logs.LogDebug).Info("PostDelayHealthChecks already reconciled into ClusterProfile spec.")
		return nil
	}

	// 2. Add the PostDelayHealthChecks to the desired ClusterProfile Spec.
	desiredClusterProfile := currentClusterProfile.DeepCopy()
	desiredClusterProfile.Spec.ValidateHealths = append(
		desiredClusterProfile.Spec.ValidateHealths,
		postDelayChecks...,
	)

	if err := r.Update(ctx, desiredClusterProfile); err != nil {
		logger.Error(err, "failed to apply patch for PostDelayHealthChecks")
		return err
	}

	logger.V(logs.LogInfo).Info("Successfully reconciled PostDelayHealthChecks into ClusterProfile spec.")

	// Return an error. Since we just updated the ClusterProfile, we need to wait for it to be reconcilied
	return fmt.Errorf("updated ClusterProfile %s with PostDelayHealthChecks", clusterProfileName)
}

// postDelayChecksAlreadyIncluded is a helper to check if the full set of post-delay checks
// is already present in the existing health checks.
func (r *ClusterPromotionReconciler) postDelayChecksAlreadyIncluded(
	existingChecks []libsveltosv1beta1.ValidateHealth,
	postDelayChecks []libsveltosv1beta1.ValidateHealth,
) bool {
	// Check length: if existing checks are shorter than post-delay checks, they can't be included.
	if len(existingChecks) < len(postDelayChecks) {
		return false
	}

	// 1. Create a map of existing check names for quick lookup.
	existingNames := make(map[string]struct{})
	for i := range existingChecks {
		check := &existingChecks[i]
		existingNames[check.Name] = struct{}{}
	}

	// 2. Iterate through the post-delay checks and verify every name exists
	// in the map of existing names.
	for i := range postDelayChecks {
		postCheck := &postDelayChecks[i]
		if _, exists := existingNames[postCheck.Name]; !exists {
			// Found a PostDelayHealthCheck name that is not in the existing list.
			return false
		}
	}

	// All post-delay check names were found in the existing list.
	return true
}

// canManualAdvance returns true if Sveltos should move to next stage based on Manual Trigger
func (r *ClusterPromotionReconciler) canManualAdvance(clusterPromotion *configv1beta1.ClusterPromotion,
	currentStageName string, manualTrigger *configv1beta1.ManualTrigger, logger logr.Logger) bool {

	if manualTrigger.Approved == nil ||
		!(*manualTrigger.Approved) {

		message := "Paused: Awaiting Manual Approva"
		logger.V(logs.LogDebug).Info(message)
		updateStageDescription(clusterPromotion, currentStageName, message)
		return false
	}

	if manualTrigger.AutomaticReset {
		manualTrigger.Approved = nil
	}

	return true
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

func (r *ClusterPromotionReconciler) removeStaleClusterProfiles(ctx context.Context,
	promotionScope *scope.ClusterPromotionScope) error {

	expectedClusterProfiles := make(map[string]struct{}, len(promotionScope.ClusterPromotion.Spec.Stages))

	for i := range promotionScope.ClusterPromotion.Spec.Stages {
		stage := &promotionScope.ClusterPromotion.Spec.Stages[i]
		expectedClusterProfiles[mainDeploymentClusterProfileName(promotionScope.Name(), stage.Name)] = struct{}{}
		expectedClusterProfiles[preCheckDeploymentClusterProfileName(promotionScope.Name(), stage.Name)] = struct{}{}
	}

	listOptions := []client.ListOption{
		client.MatchingLabels(getMainDeploymentClusterProfileLabels(promotionScope.ClusterPromotion)),
	}
	clusterProfiles := &configv1beta1.ClusterProfileList{}
	err := r.List(ctx, clusterProfiles, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterProfiles.Items {
		if _, ok := expectedClusterProfiles[clusterProfiles.Items[i].Name]; !ok {
			if k8s_utils.IsOwnerReference(&clusterProfiles.Items[i], promotionScope.ClusterPromotion) {
				promotionScope.V(logs.LogInfo).Info("deleting stale ClusterProfile",
					"clusterProfile", clusterProfiles.Items[i])
				_ = r.Delete(ctx, &clusterProfiles.Items[i])
			}
		}
	}

	return nil
}

func (r *ClusterPromotionReconciler) verifyStageEligibility(ctx context.Context,
	promotionScope *scope.ClusterPromotionScope, logger logr.Logger) (bool, error) {

	// ClusterPromotion requires a valid license
	publicKey, err := license.GetPublicKey()
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get public key: %v", err))
		return false, err
	}

	result := license.VerifyLicenseSecret(ctx, r.Client, publicKey, logger)
	if result.Message != "" {
		logger.V(logs.LogDebug).Info(result.Message)
	}

	if result.IsValid || result.IsInGracePeriod {
		return true, nil
	}

	licenseManagerInstance := GetLicenseManager()

	// Without license, 2 clusterPromotions instances are still free
	const maxFreeStages = 2
	return licenseManagerInstance.IsClusterPromotionInTopX("", promotionScope.ClusterPromotion.Name,
		maxFreeStages), nil
}

func (r *ClusterPromotionReconciler) updateStatusWithMissingLicenseError(
	promotionScope *scope.ClusterPromotionScope) {

	notEligibleError := "license is required to manage ClusterPromotion"

	// The minimum length of stages is 1 so accessing the first stage is safe
	firstStage := promotionScope.ClusterPromotion.Spec.Stages[0]
	addStageStatus(promotionScope.ClusterPromotion, firstStage.Name)
	updateStageStatus(promotionScope.ClusterPromotion, firstStage.Name, false, &notEligibleError)
}
