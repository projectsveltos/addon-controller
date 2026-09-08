/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// This file backs TransitionFrom: a ClusterProfile/Profile can name predecessor profile(s) it is
// replacing on a cluster, so that a plain label swap (moving a cluster from one profile to
// another that deploys mostly the same resources) does not undeploy-then-redeploy shared
// resources. See doc/feature_request_clusterprofile_transition.md for the full design.

// transitionSuccessor is a (Cluster)Profile that declares, via TransitionFrom, that it is
// replacing another (Cluster)Profile.
type transitionSuccessor struct {
	kind            string
	name            string
	clusterSelector libsveltosv1beta1.Selector
	clusterRefs     []corev1.ObjectReference
}

// getTransitionSuccessors returns every (Cluster)Profile that names predecessorName in its
// TransitionFrom. Same-kind only, matching TransitionFrom's own restriction: a ClusterProfile
// can only be succeeded by another ClusterProfile, a Profile only by another Profile in the
// same namespace (a Profile can only ever match clusters in its own namespace to begin with,
// so scoping this lookup to predecessorNamespace is what keeps the two kinds/namespaces from
// ever being able to cross).
func (r *ClusterSummaryReconciler) getTransitionSuccessors(ctx context.Context,
	predecessorKind, predecessorName, predecessorNamespace string) ([]transitionSuccessor, error) {

	successors := make([]transitionSuccessor, 0)

	if predecessorKind == configv1beta1.ProfileKind {
		profiles := &configv1beta1.ProfileList{}
		if err := r.List(ctx, profiles, client.InNamespace(predecessorNamespace)); err != nil {
			return nil, err
		}
		for i := range profiles.Items {
			p := &profiles.Items[i]
			if containsName(p.Spec.TransitionFrom, predecessorName) {
				successors = append(successors, transitionSuccessor{
					kind: configv1beta1.ProfileKind, name: p.Name,
					clusterSelector: p.Spec.ClusterSelector, clusterRefs: p.Spec.ClusterRefs,
				})
			}
		}
		return successors, nil
	}

	clusterProfiles := &configv1beta1.ClusterProfileList{}
	if err := r.List(ctx, clusterProfiles); err != nil {
		return nil, err
	}
	for i := range clusterProfiles.Items {
		cp := &clusterProfiles.Items[i]
		if containsName(cp.Spec.TransitionFrom, predecessorName) {
			successors = append(successors, transitionSuccessor{
				kind: configv1beta1.ClusterProfileKind, name: cp.Name,
				clusterSelector: cp.Spec.ClusterSelector, clusterRefs: cp.Spec.ClusterRefs,
			})
		}
	}
	return successors, nil
}

// isChartTransitioningFrom returns true if claimingHelmManager's owning profile declares, via
// TransitionFrom, that it is replacing currentHelmManager's owning profile. Mirrors
// isTransitioningFrom in libsveltos/lib/deployer (used for raw resources), but reads ownership
// off the two ClusterSummary's own OwnerReferences instead of an annotation on the deployed
// object, since chartmanager tracks Helm ownership by ClusterSummary rather than by annotation.
func isChartTransitioningFrom(currentHelmManager, claimingHelmManager *configv1beta1.ClusterSummary) bool {
	transitionFrom := claimingHelmManager.Spec.ClusterProfileSpec.TransitionFrom
	if len(transitionFrom) == 0 {
		return false
	}

	currentOwner, err := configv1beta1.GetProfileOwnerReference(currentHelmManager)
	if err != nil || currentOwner == nil {
		return false
	}
	claimingOwner, err := configv1beta1.GetProfileOwnerReference(claimingHelmManager)
	if err != nil || claimingOwner == nil {
		return false
	}
	// Same-kind only, matching TransitionFrom's own restriction.
	if currentOwner.Kind != claimingOwner.Kind {
		return false
	}

	return containsName(transitionFrom, currentOwner.Name)
}

func containsName(names []string, target string) bool {
	for i := range names {
		if names[i] == target {
			return true
		}
	}
	return false
}

// areSuccessorsProvisioned is the mirror of areDependentsRemoved for TransitionFrom: it defers
// this ClusterSummary's teardown while another (Cluster)Profile that currently matches this
// cluster names this profile in TransitionFrom and has not reached Provisioned yet, letting the
// successor take over shared resources in place instead of undeploy-then-redeploy.
//
// This cannot key off ClusterSummary existence the way areDependentsRemoved keys off DependsOn:
// the predecessor and successor profiles are woken by the same label-change event on independent
// reconcile loops, so the predecessor's teardown can run before the successor has even created
// its ClusterSummary. At that instant "successor ClusterSummary not found" is indistinguishable
// from "successor was never going to match this cluster." So this gates on whether the
// successor's own ClusterSelector/ClusterRefs match this cluster directly instead - that answer
// is available the instant this reconciles, regardless of whether the successor has reconciled
// yet.
//
// Multiple successors can independently name the same predecessor (fan-in): teardown waits until
// every currently-matching successor reaches Provisioned, not just one, since each may be
// responsible for a different subset of the same resources.
func (r *ClusterSummaryReconciler) areSuccessorsProvisioned(ctx context.Context,
	clusterSummaryScope *scope.ClusterSummaryScope, logger logr.Logger) (allProvisioned bool, message string, err error) {

	clusterSummary := clusterSummaryScope.ClusterSummary

	profileReference, err := configv1beta1.GetProfileOwnerReference(clusterSummary)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get profile owner: %v", err))
		return false, "", fmt.Errorf("failed to get profile owner: %w", err)
	}
	if profileReference == nil {
		return false, "", fmt.Errorf("profile owner not found")
	}

	successors, err := r.getTransitionSuccessors(ctx, profileReference.Kind, profileReference.Name,
		clusterSummary.Namespace)
	if err != nil {
		return false, "", err
	}
	if len(successors) == 0 {
		return true, "no transitions pending", nil
	}

	cluster, err := clusterproxy.GetCluster(ctx, r.Client, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Cluster is gone. Nothing can still be matching it.
			return true, "cluster not found", nil
		}
		return false, "", err
	}
	clusterLabels := labels.Set(cluster.GetLabels())

	for i := range successors {
		successor := successors[i]
		if !selectorMatchesCluster(successor.clusterSelector, successor.clusterRefs, clusterSummary, clusterLabels) {
			// This successor does not (or no longer) match this cluster: it is not part of a
			// transition on this cluster, whether or not it is still Provisioned elsewhere.
			continue
		}

		successorClusterSummary, err := clusterops.GetClusterSummary(ctx, r.Client, successor.kind, successor.name,
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType)
		if err != nil {
			if apierrors.IsNotFound(err) {
				msg := fmt.Sprintf("%s %s is replacing this profile on this cluster but has not deployed yet",
					successor.kind, successor.name)
				logger.V(logs.LogInfo).Info(msg)
				return false, msg, nil
			}
			return false, "", err
		}

		if !isCluterSummaryProvisioned(successorClusterSummary) {
			msg := fmt.Sprintf("%s %s is replacing this profile on this cluster but is not fully deployed yet",
				successor.kind, successor.name)
			logger.V(logs.LogInfo).Info(msg)
			return false, msg, nil
		}
	}

	return true, "all transitioning successors are provisioned", nil
}
