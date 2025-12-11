/*
Copyright 2023. projectsveltos.io. All rights reserved.

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
	"crypto/sha256"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/dariubs/percent"
	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers/dependencymanager"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func getMatchingClusters(ctx context.Context, c client.Client, namespace string, clusterSelector *metav1.LabelSelector,
	clusterRefs []corev1.ObjectReference, logger logr.Logger) ([]corev1.ObjectReference, error) {

	var matchingCluster []corev1.ObjectReference
	if clusterSelector != nil {
		var err error
		matchingCluster, err = clusterproxy.GetMatchingClusters(ctx, c, clusterSelector, namespace,
			getCAPIOnboardAnnotation(), logger)
		if err != nil {
			return nil, err
		}
	}

	matchingCluster = append(matchingCluster, clusterRefs...)

	return matchingCluster, nil
}

// allClusterSummariesGone returns true if all ClusterSummaries owned by a
// ClusterProfile/Profile instances are gone.
func allClusterSummariesGone(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) bool {
	listOptions := []client.ListOption{}

	// The current Kind being processed (ClusterProfile or Profile)
	currentKind := profileScope.GetKind()

	// Determine labels based on Kind
	switch currentKind {
	case configv1beta1.ClusterProfileKind:
		listOptions = append(listOptions, client.MatchingLabels{clusterops.ClusterProfileLabelName: profileScope.Name()})
	case configv1beta1.ProfileKind: // Explicitly check for ProfileKind
		listOptions = append(listOptions,
			client.MatchingLabels{clusterops.ProfileLabelName: profileScope.Name()},
			// Only Profile (namespaced) requires a namespace match. ClusterProfile (cluster-scoped) does not.
			client.InNamespace(profileScope.Profile.GetNamespace()))
	}

	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	if err := c.List(ctx, clusterSummaryList, listOptions...); err != nil {
		profileScope.V(logs.LogInfo).Info(fmt.Sprintf("failed to list clustersummaries. err %v", err))
		return false
	}

	if len(clusterSummaryList.Items) > 0 {
		profileScope.V(logs.LogInfo).Info("not all clusterSummaries are gone")
	}
	return len(clusterSummaryList.Items) == 0
}

// allClusterSummariesDeployed returns true if all ClusterSummaries owned by a
// ClusterProfile/Profile instance are provisioned. It also returns a status message
// and an error if one occurs during API interaction.
func allClusterSummariesDeployed(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	logger logr.Logger) (deployed bool, message string, err error) {

	listOptions := []client.ListOption{}

	// The current Kind being processed (ClusterProfile or Profile)
	currentKind := profileScope.GetKind()

	// Determine labels based on Kind
	switch currentKind {
	case configv1beta1.ClusterProfileKind:
		listOptions = append(listOptions, client.MatchingLabels{clusterops.ClusterProfileLabelName: profileScope.Name()})
	case configv1beta1.ProfileKind: // Explicitly check for ProfileKind
		listOptions = append(listOptions,
			client.MatchingLabels{clusterops.ProfileLabelName: profileScope.Name()},
			// Only Profile (namespaced) requires a namespace match. ClusterProfile (cluster-scoped) does not.
			client.InNamespace(profileScope.Profile.GetNamespace()))
	}

	clusterSummaryList := &configv1beta1.ClusterSummaryList{}

	// List ClusterSummaries
	if errList := c.List(ctx, clusterSummaryList, listOptions...); errList != nil {
		// Return API error immediately
		message = fmt.Sprintf("failed to list ClusterSummaries for %s %s. Error: %v", currentKind, profileScope.Name(), errList)
		profileScope.V(logs.LogInfo).Info(message)
		return false, message, errList // Use errList instead of shadowing the named 'err'
	}

	// Check if any ClusterSummaries were found (edge case: selector matched no clusters)
	if len(clusterSummaryList.Items) == 0 {
		message = fmt.Sprintf("No matching clusters found for %s %s. Deployment considered complete.",
			currentKind, profileScope.Name())
		profileScope.V(logs.LogInfo).Info(message)
		return true, message, nil
	}

	// Check provisioning status
	for i := range clusterSummaryList.Items {
		// Verify ClusterSummary is in sync
		deployed, message = isClusterSummarySyncedAndProvisioned(profileScope, &clusterSummaryList.Items[i], logger)
		if !deployed {
			return deployed, message, nil
		}
	}

	// Success case: all found ClusterSummaries are provisioned
	return true, "All matching clusters are successfully deployed.", nil
}

func isClusterSummarySyncedAndProvisioned(profileScope *scope.ProfileScope,
	clusterSummary *configv1beta1.ClusterSummary, logger logr.Logger) (provisioned bool, message string) {

	// Verify ClusterSummary is in sync
	if !reflect.DeepEqual(clusterSummary.Spec.ClusterProfileSpec.HelmCharts,
		profileScope.GetSpec().HelmCharts) {

		message = fmt.Sprintf("ClusterSummary HelmCharts %s is not in sync.",
			clusterSummary.Name)
		logger.V(logs.LogDebug).Info(message)
		return false, message
	}
	if !reflect.DeepEqual(clusterSummary.Spec.ClusterProfileSpec.PolicyRefs,
		profileScope.GetSpec().PolicyRefs) {

		message = fmt.Sprintf("ClusterSummary PolicyRefs %s is not in sync.",
			clusterSummary.Name)
		logger.V(logs.LogDebug).Info(message)
		return false, message
	}
	if !reflect.DeepEqual(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs,
		profileScope.GetSpec().KustomizationRefs) {

		message = fmt.Sprintf("ClusterSummaryKustomizationRefs %s is not in sync.",
			clusterSummary.Name)
		logger.V(logs.LogDebug).Info(message)
		return false, message
	}

	// The helper should check the status of the individual ClusterSummary
	if !isCluterSummaryProvisioned(clusterSummary) {
		// Found one that is not provisioned: return false with a message
		message = fmt.Sprintf("ClusterSummary %s is not yet provisioned.", clusterSummary.Name)
		logger.V(logs.LogDebug).Info(message)
		return false, message
	}

	return true, ""
}

// canRemoveFinalizer returns true if there is no ClusterSummary left created by this
// ClusterProfile/Profile instance
func canRemoveFinalizer(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) bool {
	return allClusterSummariesGone(ctx, c, profileScope)
}

// ClusterConfigurations
// - Name depends just on Cluster name
// - Each ClusterProfile/Profile matching corresponding Cluster and deploying add-ons/applications
//   is added as OwnerReference

// updateClusterConfigurationProfileResources adds a section for ClusterProfile/Profile
// in clusterConfiguration Status.(Cluster)ProfileResources
func updateClusterConfigurationProfileResources(ctx context.Context, c client.Client,
	profile client.Object, clusterConfiguration *configv1beta1.ClusterConfiguration) error {

	profileKind := profile.GetObjectKind().GroupVersionKind().Kind
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, c,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			return err
		}

		if profileKind == configv1beta1.ClusterProfileKind {
			for i := range currentClusterConfiguration.Status.ClusterProfileResources {
				cpr := &currentClusterConfiguration.Status.ClusterProfileResources[i]
				if cpr.ClusterProfileName == profile.GetName() {
					return nil
				}
			}
			currentClusterConfiguration.Status.ClusterProfileResources = append(
				currentClusterConfiguration.Status.ClusterProfileResources,
				configv1beta1.ClusterProfileResource{
					ClusterProfileName: profile.GetName(),
				})
		} else {
			for i := range currentClusterConfiguration.Status.ProfileResources {
				cpr := &currentClusterConfiguration.Status.ProfileResources[i]
				if cpr.ProfileName == profile.GetName() {
					return nil
				}
			}
			currentClusterConfiguration.Status.ProfileResources = append(
				currentClusterConfiguration.Status.ProfileResources,
				configv1beta1.ProfileResource{
					ProfileName: profile.GetName(),
				})
		}

		return c.Status().Update(ctx, currentClusterConfiguration)
	})
	return err
}

// updateClusterConfigurationOwnerReferences adds profile as owner of ClusterConfiguration
func updateClusterConfigurationOwnerReferences(ctx context.Context, c client.Client,
	profile client.Object, clusterConfiguration *configv1beta1.ClusterConfiguration) error {

	targetGK := schema.GroupKind{
		Group: configv1beta1.GroupVersion.Group,
		Kind:  profile.GetObjectKind().GroupVersionKind().Kind,
	}

	if util.IsOwnedByObject(clusterConfiguration, profile, targetGK) {
		return nil
	}

	ownerRef := metav1.OwnerReference{
		Kind:       profile.GetObjectKind().GroupVersionKind().Kind,
		UID:        profile.GetUID(),
		APIVersion: configv1beta1.GroupVersion.String(),
		Name:       profile.GetName(),
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, c,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			return err
		}

		currentClusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences, ownerRef)
		return c.Update(ctx, currentClusterConfiguration)
	})
	return err
}

// updateClusterConfiguration updates if necessary ClusterConfiguration given a
// ClusterProfile/Profile and a matching Sveltos/Cluster.
// Update consists in:
// - adding ClusterProfile/Profile as one of OwnerReferences for ClusterConfiguration
// - adding a section in Status.(Cluster)ProfileResources for this (Cluster)Profile
func updateClusterConfigurationWithProfile(ctx context.Context, c client.Client, profile client.Object,
	cluster *corev1.ObjectReference) error {

	clusterConfiguration, err := getClusterConfiguration(ctx, c, cluster.Namespace,
		getClusterConfigurationName(cluster.Name, clusterproxy.GetClusterType(cluster)))
	if err != nil {
		return err
	}

	err = updateClusterConfigurationOwnerReferences(ctx, c, profile, clusterConfiguration)
	if err != nil {
		return err
	}

	err = updateClusterConfigurationProfileResources(ctx, c, profile, clusterConfiguration)
	if err != nil {
		return err
	}

	return nil
}

// createClusterConfiguration creates ClusterConfiguration given a Sveltos/Cluster.
// If already existing, return nil
func createClusterConfiguration(ctx context.Context, c client.Client, cluster *corev1.ObjectReference) error {
	clusterConfiguration := &configv1beta1.ClusterConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      getClusterConfigurationName(cluster.Name, clusterproxy.GetClusterType(cluster)),
			Labels: map[string]string{
				configv1beta1.ClusterNameLabel: cluster.Name,
				configv1beta1.ClusterTypeLabel: string(clusterproxy.GetClusterType(cluster)),
			},
		},
	}

	err := c.Create(ctx, clusterConfiguration)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
	}

	return err
}

// updateClusterConfigurations for each Sveltos/Cluster currently matching ClusterProfile/Profile:
// - creates corresponding ClusterConfiguration if one does not exist already
// - updates (eventually) corresponding ClusterConfiguration if one already exists
// Both create and update only add ClusterProfile/Profile as OwnerReference for ClusterConfiguration
func updateClusterConfigurations(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]

		// Create ClusterConfiguration if not already existing.
		err := createClusterConfiguration(ctx, c, &cluster)
		if err != nil {
			profileScope.Error(err, fmt.Sprintf("failed to create ClusterConfiguration for cluster %s/%s",
				cluster.Namespace, cluster.Name))
			return err
		}

		// Update ClusterConfiguration
		err = updateClusterConfigurationWithProfile(ctx, c, profileScope.Profile, &cluster)
		if err != nil {
			profileScope.Error(err, fmt.Sprintf("failed to update ClusterConfiguration for cluster %s/%s",
				cluster.Namespace, cluster.Name))
			return err
		}
	}

	return nil
}

// cleanClusterConfigurations finds all ClusterConfigurations currently owned by ClusterProfile/Profile.
// For each such ClusterConfigurations, if corresponding Cluster is not a match anymore:
// - remove (Cluster)Profile as OwnerReference
// - if no more OwnerReferences are left, delete ClusterConfigurations
func cleanClusterConfigurations(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	clusterConfigurationList := &configv1beta1.ClusterConfigurationList{}

	listOptions := []client.ListOption{}
	if profileScope.Profile.GetObjectKind().GroupVersionKind().Kind == configv1beta1.ProfileKind {
		listOptions = append(listOptions,
			client.InNamespace(profileScope.Profile.GetNamespace()))
	}

	matchingClusterMap := make(map[string]bool)

	info := func(namespace, clusterConfigurationName string) string {
		return fmt.Sprintf("%s--%s", namespace, clusterConfigurationName)
	}

	for i := range profileScope.GetStatus().MatchingClusterRefs {
		ref := &profileScope.GetStatus().MatchingClusterRefs[i]
		matchingClusterMap[info(ref.Namespace, getClusterConfigurationName(ref.Name, clusterproxy.GetClusterType(ref)))] = true
	}

	err := c.List(ctx, clusterConfigurationList, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterConfigurationList.Items {
		cc := &clusterConfigurationList.Items[i]

		// If Sveltos/Cluster is still a match, continue (don't remove (Cluster)Profile as OwnerReference)
		if _, ok := matchingClusterMap[info(cc.Namespace, cc.Name)]; ok {
			continue
		}

		err = cleanClusterConfiguration(ctx, c, profileScope.Profile, cc)
		if err != nil {
			return err
		}
	}
	return nil
}

func cleanClusterConfiguration(ctx context.Context, c client.Client, profile client.Object,
	clusterConfiguration *configv1beta1.ClusterConfiguration) error {

	// remove ClusterProfile/Profile as one of the ClusterConfiguration's owners
	err := cleanClusterConfigurationOwnerReferences(ctx, c, profile, clusterConfiguration)
	if err != nil {
		return err
	}

	// remove the section in ClusterConfiguration.Status.ProfileResource used for this ClusterProfile/Profile
	err = cleanClusterConfigurationProfileResources(ctx, c, profile, clusterConfiguration)
	if err != nil {
		return err
	}

	return nil
}

func cleanClusterConfigurationOwnerReferences(ctx context.Context, c client.Client, profile client.Object,
	clusterConfiguration *configv1beta1.ClusterConfiguration) error {

	ownerRef := metav1.OwnerReference{
		Kind:       profile.GetObjectKind().GroupVersionKind().Kind,
		UID:        profile.GetUID(),
		APIVersion: configv1beta1.GroupVersion.String(),
		Name:       profile.GetName(),
	}

	targetGK := schema.GroupKind{
		Group: configv1beta1.GroupVersion.Group,
		Kind:  profile.GetObjectKind().GroupVersionKind().Kind,
	}

	if !util.IsOwnedByObject(clusterConfiguration, profile, targetGK) {
		return nil
	}

	clusterConfiguration.OwnerReferences = util.RemoveOwnerRef(clusterConfiguration.OwnerReferences, ownerRef)
	if len(clusterConfiguration.OwnerReferences) == 0 {
		return c.Delete(ctx, clusterConfiguration)
	} else {
		return c.Update(ctx, clusterConfiguration)
	}
}

func cleanClusterConfigurationProfileResources(ctx context.Context, c client.Client, profile client.Object,
	clusterConfiguration *configv1beta1.ClusterConfiguration) error {

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, c,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			// If ClusterConfiguration is not found, nothing to do here.
			// ClusterConfiguration is removed if (Cluster)Profile was the last owner.
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}

		profileKind := profile.GetObjectKind().GroupVersionKind().Kind
		if profileKind == configv1beta1.ClusterProfileKind {
			return cleanClusterProfileResources(ctx, c, profile, currentClusterConfiguration)
		} else {
			return cleanProfileResources(ctx, c, profile, currentClusterConfiguration)
		}
	})
	return err
}

func cleanClusterProfileResources(ctx context.Context, c client.Client, profile client.Object,
	currentClusterConfiguration *configv1beta1.ClusterConfiguration) error {

	toBeUpdated := false

	for i := range currentClusterConfiguration.Status.ClusterProfileResources {
		cpr := &currentClusterConfiguration.Status.ClusterProfileResources[i]
		if cpr.ClusterProfileName != profile.GetName() {
			continue
		}
		// Order is not important. So move the element at index i with last one in order to avoid moving all elements.
		length := len(currentClusterConfiguration.Status.ClusterProfileResources)
		currentClusterConfiguration.Status.ClusterProfileResources[i] =
			currentClusterConfiguration.Status.ClusterProfileResources[length-1]
		currentClusterConfiguration.Status.ClusterProfileResources =
			currentClusterConfiguration.Status.ClusterProfileResources[:length-1]
		toBeUpdated = true
		break
	}

	if toBeUpdated {
		return c.Status().Update(ctx, currentClusterConfiguration)
	}
	return nil
}

func cleanProfileResources(ctx context.Context, c client.Client, profile client.Object,
	currentClusterConfiguration *configv1beta1.ClusterConfiguration) error {

	toBeUpdated := false

	for i := range currentClusterConfiguration.Status.ProfileResources {
		cpr := &currentClusterConfiguration.Status.ProfileResources[i]
		if cpr.ProfileName != profile.GetName() {
			continue
		}
		// Order is not important. So move the element at index i with last one in order to avoid moving all elements.
		length := len(currentClusterConfiguration.Status.ProfileResources)
		currentClusterConfiguration.Status.ProfileResources[i] =
			currentClusterConfiguration.Status.ProfileResources[length-1]
		currentClusterConfiguration.Status.ProfileResources =
			currentClusterConfiguration.Status.ProfileResources[:length-1]
		toBeUpdated = true
		break
	}

	if toBeUpdated {
		return c.Status().Update(ctx, currentClusterConfiguration)
	}
	return nil
}

// ClusterSummary
// - Name is based on cluster type/name and matching ClusterProfile/Profile type/name
// - Labels: * ClusterProfileLabelName or ProfileLabelName
//           * ClusterTypeLabel and ClusterNameLabel

// updateClusterSummary updates if necessary ClusterSummary given a ClusterProfile/Profile
// and a matching Sveltos/Cluster.
// If ClusterProfile/Profile Spec.SyncMode is set to one time, nothing will happen
func updateClusterSummary(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	cluster *corev1.ObjectReference) error {

	if profileScope.IsOneTimeSync() {
		return nil
	}

	clusterSummary, err := clusterops.GetClusterSummary(ctx, c, profileScope.GetKind(), profileScope.Name(),
		cluster.Namespace, cluster.Name, clusterproxy.GetClusterType(cluster))
	if err != nil {
		return err
	}

	if reflect.DeepEqual(profileScope.GetSpec(), clusterSummary.Spec.ClusterProfileSpec) &&
		reflect.DeepEqual(profileScope.Profile.GetAnnotations(), clusterSummary.Annotations) {
		// Nothing has changed
		return nil
	}

	clusterSummary.Annotations = profileScope.Profile.GetAnnotations()
	clusterSummary.Spec.ClusterProfileSpec = *profileScope.GetSpec()
	clusterSummary.Spec.ClusterType = clusterproxy.GetClusterType(cluster)
	addClusterSummaryLabels(clusterSummary, profileScope, cluster)
	// Copy annotation. Paused annotation might be set on ClusterProfile.
	clusterSummary.Annotations = profileScope.Profile.GetAnnotations()
	return c.Update(ctx, clusterSummary)
}

func addClusterSummaryLabels(clusterSummary *configv1beta1.ClusterSummary, profileScope *scope.ProfileScope,
	cluster *corev1.ObjectReference) {

	if profileScope.Profile.GetObjectKind().GroupVersionKind().Kind == configv1beta1.ClusterProfileKind {
		deployer.AddLabel(clusterSummary, clusterops.ClusterProfileLabelName, profileScope.Name())
	} else {
		deployer.AddLabel(clusterSummary, clusterops.ProfileLabelName, profileScope.Name())
	}
	deployer.AddLabel(clusterSummary, configv1beta1.ClusterNameLabel, cluster.Name)
	if cluster.APIVersion == libsveltosv1beta1.GroupVersion.String() {
		deployer.AddLabel(clusterSummary, configv1beta1.ClusterTypeLabel, string(libsveltosv1beta1.ClusterTypeSveltos))
	} else {
		deployer.AddLabel(clusterSummary, configv1beta1.ClusterTypeLabel, string(libsveltosv1beta1.ClusterTypeCapi))
	}

	clusterProfileLabels := profileScope.Profile.GetLabels()
	if clusterProfileLabels != nil {
		v, ok := clusterProfileLabels[libsveltosv1beta1.ServiceAccountNameLabel]
		if ok {
			deployer.AddLabel(clusterSummary, libsveltosv1beta1.ServiceAccountNameLabel, v)
		}
		v, ok = clusterProfileLabels[libsveltosv1beta1.ServiceAccountNamespaceLabel]
		if ok {
			deployer.AddLabel(clusterSummary, libsveltosv1beta1.ServiceAccountNamespaceLabel, v)
		}
	}
}

// createClusterSummary creates ClusterSummary given a ClusterProfile and a matching Sveltos/Cluster
func createClusterSummary(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	cluster *corev1.ObjectReference) error {

	clusterSummaryName := clusterops.GetClusterSummaryName(profileScope.GetKind(), profileScope.Name(),
		cluster.Name, cluster.APIVersion == libsveltosv1beta1.GroupVersion.String())

	clusterSummary := &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterSummaryName,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: configv1beta1.GroupVersion.String(),
					Kind:       profileScope.Profile.GetObjectKind().GroupVersionKind().Kind,
					Name:       profileScope.Profile.GetName(),
					UID:        profileScope.Profile.GetUID(),
				},
			},
			Annotations: profileScope.Profile.GetAnnotations(),
		},
		Spec: configv1beta1.ClusterSummarySpec{
			ClusterNamespace:   cluster.Namespace,
			ClusterName:        cluster.Name,
			ClusterProfileSpec: *profileScope.GetSpec(),
		},
	}

	clusterSummary.Spec.ClusterType = clusterproxy.GetClusterType(cluster)
	clusterSummary.Labels = profileScope.Profile.GetLabels()
	addClusterSummaryLabels(clusterSummary, profileScope, cluster)
	// Copy annotation. Paused annotation might be set on ClusterProfile.
	clusterSummary.Annotations = profileScope.Profile.GetAnnotations()

	return c.Create(ctx, clusterSummary)
}

// updateClusterSummaries for each Sveltos/Cluster currently matching ClusterProfile/Profile:
// - creates corresponding ClusterSummary if one does not exist already
// - updates (eventually) corresponding ClusterSummary if one already exists
// Return an error if due to MaxUpdate not all ClusterSummaries are synced
func updateClusterSummaries(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	currentHash := getProfileSpecHash(profileScope)

	// Remove Status.UpdatedClusters if hash is different
	if !reflect.DeepEqual(profileScope.GetStatus().UpdatedClusters.Hash, currentHash) {
		profileScope.GetStatus().UpdatedClusters = configv1beta1.Clusters{
			Hash:     currentHash,
			Clusters: []corev1.ObjectReference{},
		}
	}

	// Remove clusters non matching anynmore from UpdatedClusters and UpdatingClusters
	reviseUpdatedAndUpdatingClusters(profileScope)

	if err := reviseUpdatingClusterList(ctx, c, profileScope, currentHash); err != nil {
		return err
	}

	updatedClusters, updatingClusters := getUpdatedAndUpdatingClusters(profileScope)

	matchingClusterSummaryDeleted := false

	skippedUpdate := false
	// Consider matchingCluster number and MaxUpdate, walk remaining matching clusters.  If more clusters can be
	// updated, update ClusterSummary and add it to UpdatingClusters
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := &profileScope.GetStatus().MatchingClusterRefs[i]

		logger := profileScope.Logger
		logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", cluster.Kind, cluster.Namespace, cluster.Name))

		if updatedClusters.Has(cluster) {
			logger.V(logs.LogDebug).Info("Cluster is already updated")
			continue
		}

		tmpSkippedUpdate, err := updateClusterSummaryInstanceForCluster(ctx, c, cluster, profileScope,
			updatingClusters, logger)
		if err != nil {
			var clusterSummaryDeletedError *ClusterSummaryDeletedError
			if errors.As(err, &clusterSummaryDeletedError) {
				matchingClusterSummaryDeleted = true
				continue
			}
			return err
		}

		if tmpSkippedUpdate {
			skippedUpdate = true
			continue
		}

		if !updatingClusters.Has(cluster) {
			updatingClusters.Insert(cluster)
			profileScope.GetStatus().UpdatingClusters.Clusters =
				append(profileScope.GetStatus().UpdatingClusters.Clusters,
					*cluster)
		}
	}

	profileScope.GetStatus().UpdatingClusters.Hash = currentHash

	if skippedUpdate {
		return fmt.Errorf("not all clusters updated yet. %d still being updated",
			len(profileScope.GetStatus().UpdatingClusters.Clusters))
	}

	// If one or more ClusterSummary instances are being deleted, reconcile.
	// We cannot update those while being deleted.
	if matchingClusterSummaryDeleted {
		return fmt.Errorf("some clusterSummaries are being deleted. Must reconcile again")
	}

	// If all ClusterSummaries have been updated, reset Updated and Updating
	profileScope.GetStatus().UpdatedClusters = configv1beta1.Clusters{}
	profileScope.GetStatus().UpdatingClusters = configv1beta1.Clusters{}

	return nil
}

func updateClusterSummaryInstanceForCluster(ctx context.Context, c client.Client, cluster *corev1.ObjectReference,
	profileScope *scope.ProfileScope, updatingClusters *libsveltosset.Set, logger logr.Logger,
) (bool, error) {

	ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, cluster, logger)
	if err != nil {
		return false, err
	}
	if !ready {
		logger.V(logs.LogDebug).Info("Cluster is not ready yet")
		return false, nil
	}

	maxUpdate := getMaxUpdate(profileScope)

	if maxUpdate != 0 {
		// maxUpdate is set. Skip paused clusters (which would not be updated anyhow as set to paused)
		// and try to pcik any non paused cluster
		isClusterPaused, err := clusterproxy.IsClusterPaused(ctx, c, cluster.Namespace,
			cluster.Name, clusterproxy.GetClusterType(cluster))
		if err != nil {
			logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to verify if cluster is paused: %v", err))
			return false, err
		}
		if isClusterPaused {
			// No need to set skippedUpdated. Profile will react to a cluster switching from paused to unpaused
			logger.V(logs.LogDebug).Info("Cluster is paused and maxUpdate is set. Ignore this cluster.")
			return false, nil
		}
	}

	// if maxUpdate is set no more than maxUpdate clusters can be updated in parallel by ClusterProfile
	if maxUpdate != 0 && !updatingClusters.Has(cluster) && updatingClusters.Len() >= int(maxUpdate) {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("Already %d being updating", updatingClusters.Len()))
		return true, nil
	}

	// ClusterProfile does not look at whether Cluster is paused or not.
	// If a Cluster exists and it is a match, ClusterSummary is created (and ClusterSummary.Spec kept in sync if mode is
	// continuous).
	// ClusterSummary won't program cluster in paused state.
	return false, patchClusterSummary(ctx, c, profileScope, cluster, logger)
}

func patchClusterSummary(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	cluster *corev1.ObjectReference, logger logr.Logger) error {

	// ClusterProfile does not look at whether Cluster is paused or not.
	// If a Cluster exists and it is a match, ClusterSummary is created (and ClusterSummary.Spec kept in sync if mode is
	// continuous).
	// ClusterSummary won't program cluster in paused state.
	cs, err := clusterops.GetClusterSummary(ctx, c, profileScope.GetKind(), profileScope.Name(), cluster.Namespace,
		cluster.Name, clusterproxy.GetClusterType(cluster))
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = createClusterSummary(ctx, c, profileScope, cluster)
			if err != nil {
				logger.Error(err, "failed to create ClusterSummary")
			}
		} else {
			logger.Error(err, "failed to get ClusterSummary")
			return err
		}
	} else {
		if !cs.DeletionTimestamp.IsZero() {
			msg := fmt.Sprintf("ClusterSummary %s/%s being deleted", cs.Namespace, cs.Name)
			logger.V(logs.LogInfo).Info(msg)
			return &ClusterSummaryDeletedError{Message: msg}
		}
		err = updateClusterSummary(ctx, c, profileScope, cluster)
		if err != nil {
			logger.Error(err, "failed to update ClusterSummary")
			return err
		}
	}

	return nil
}

// cleanClusterSummaries finds all ClusterSummary currently owned by ClusterProfile/Profile.
// For each such ClusterSummary, if corresponding Sveltos/Cluster is not a match anymore, deletes ClusterSummary
func cleanClusterSummaries(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	matching := make(map[string]bool)

	getClusterInfo := func(clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType) string {
		return fmt.Sprintf("%s-%s-%s", clusterType, clusterNamespace, clusterName)
	}

	for i := range profileScope.GetStatus().MatchingClusterRefs {
		reference := profileScope.GetStatus().MatchingClusterRefs[i]
		clusterName := getClusterInfo(reference.Namespace, reference.Name, clusterproxy.GetClusterType(&reference))
		matching[clusterName] = true
	}

	listOptions := []client.ListOption{}
	if profileScope.Profile.GetObjectKind().GroupVersionKind().Kind == configv1beta1.ClusterProfileKind {
		listOptions = append(listOptions, client.MatchingLabels{clusterops.ClusterProfileLabelName: profileScope.Name()})
	} else {
		listOptions = append(listOptions,
			client.MatchingLabels{clusterops.ProfileLabelName: profileScope.Name()},
			client.InNamespace(profileScope.Profile.GetNamespace()))
	}

	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	if err := c.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return err
	}

	targetGK := schema.GroupKind{
		Group: configv1beta1.GroupVersion.Group,
		Kind:  profileScope.Profile.GetObjectKind().GroupVersionKind().Kind,
	}

	// Check if any ClusterSummary instance that needs to be removed is still present
	foundClusterSummaries := false
	for i := range clusterSummaryList.Items {
		cs := &clusterSummaryList.Items[i]

		if util.IsOwnedByObject(cs, profileScope.Profile, targetGK) {
			if _, ok := matching[getClusterInfo(cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cs.Spec.ClusterType)]; !ok {
				foundClusterSummaries = true
				err := c.Delete(ctx, cs)
				if err != nil {
					profileScope.Error(err, fmt.Sprintf("failed to update ClusterSummary for cluster %s/%s",
						cs.Namespace, cs.Name))
					return err
				}
			}
		}
		if err := updateClusterSummarySyncMode(ctx, c, cs, profileScope.GetSpec().SyncMode); err != nil {
			return err
		}
	}

	if foundClusterSummaries {
		return fmt.Errorf("clusterSummaries still present")
	}
	return nil
}

func updateClusterSummarySyncMode(ctx context.Context, c client.Client,
	clusterSummary *configv1beta1.ClusterSummary, syncMode configv1beta1.SyncMode) error {

	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == syncMode {
		return nil
	}

	currentClusterSummary := &configv1beta1.ClusterSummary{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
		currentClusterSummary)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	clusterSummary.Spec.ClusterProfileSpec.SyncMode = syncMode
	return c.Update(ctx, clusterSummary)
}

// ClusterReports

// updateClusterReports for each Sveltos/Cluster currently matching ClusterProfile/Profile:
// - if syncMode is DryRun, creates corresponding ClusterReport if one does not exist already;
// - if syncMode is DryRun, deletes ClusterReports for any Sveltos/Cluster not matching anymore;
// - if syncMode is not DryRun, deletes ClusterReports created by this ClusterProfile instance
func updateClusterReports(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	if profileScope.IsDryRunSync() {
		err := createClusterReports(ctx, c, profileScope)
		if err != nil {
			profileScope.Error(err, "failed to create ClusterReports")
			return err
		}
	} else {
		// delete all ClusterReports created by this ClusterProfile/Profile instance
		err := cleanClusterReports(ctx, c, profileScope)
		if err != nil {
			profileScope.Error(err, "failed to delete ClusterReports")
			return err
		}
	}

	return nil
}

// createClusterReports creates a ClusterReport for each matching Cluster
func createClusterReports(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]

		// Create ClusterReport if not already existing.
		err := createClusterReport(ctx, c, profileScope.Profile, &cluster)
		if err != nil {
			return err
		}
	}
	return nil
}

// createClusterReport creates ClusterReport given a Sveltos/Cluster.
// If already existing, return nil
func createClusterReport(ctx context.Context, c client.Client, profile client.Object,
	cluster *corev1.ObjectReference) error {

	clusterType := clusterproxy.GetClusterType(cluster)

	lbls := map[string]string{
		configv1beta1.ClusterNameLabel: cluster.Name,
		configv1beta1.ClusterTypeLabel: string(clusterproxy.GetClusterType(cluster)),
	}
	if profile.GetObjectKind().GroupVersionKind().Kind == configv1beta1.ClusterProfileKind {
		lbls[clusterops.ClusterProfileLabelName] = profile.GetName()
	} else {
		lbls[clusterops.ProfileLabelName] = profile.GetName()
	}

	clusterReport := &configv1beta1.ClusterReport{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name: clusterops.GetClusterReportName(profile.GetObjectKind().GroupVersionKind().Kind, profile.GetName(),
				cluster.Name, clusterType),
			Labels: lbls,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       profile.GetObjectKind().GroupVersionKind().Kind,
					UID:        profile.GetUID(),
					APIVersion: configv1beta1.GroupVersion.String(),
					Name:       profile.GetName(),
				},
			},
		},
		Spec: configv1beta1.ClusterReportSpec{
			ClusterNamespace: cluster.Namespace,
			ClusterName:      cluster.Name,
		},
	}

	err := c.Create(ctx, clusterReport)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
	}

	return err
}

// cleanClusterReports deletes ClusterReports created by this ClusterProfile/Profile instance.
func cleanClusterReports(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	listOptions := []client.ListOption{}

	if profileScope.IsDryRunSync() {
		return nil
	}

	if profileScope.GetKind() == configv1beta1.ClusterProfileKind {
		listOptions = append(listOptions, client.MatchingLabels{clusterops.ClusterProfileLabelName: profileScope.Name()})
	} else {
		listOptions = append(listOptions,
			client.MatchingLabels{clusterops.ProfileLabelName: profileScope.Name()},
			client.InNamespace(profileScope.Namespace()))
	}

	clusterReportList := &configv1beta1.ClusterReportList{}
	err := c.List(ctx, clusterReportList, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterReportList.Items {
		cr := &clusterReportList.Items[i]

		err = c.Delete(ctx, cr)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

// getProfileSpecHash returns hash of current clusterProfile/Profile Spec
func getProfileSpecHash(profileScope *scope.ProfileScope) []byte {
	h := sha256.New()
	var config string

	config += render.AsCode(profileScope.GetSpec())

	h.Write([]byte(config))
	return h.Sum(nil)
}

// reviseUpdatedAndUpdatingClusters updates ClusterProfile Status UpdatedClusters and UpdatingClusters fields
// by removing clusters non matching ClusterProfile/Profile anymore.
// - UpdatedClusters represents list of matching clusters already updated since last ClusterProfile/Profile change
// - UpdatingClusters represents list of matching clusters being updated since last ClusterProfile/Profile change
func reviseUpdatedAndUpdatingClusters(profileScope *scope.ProfileScope) {
	matchingCluster := libsveltosset.Set{}
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]
		matchingCluster.Insert(&cluster)
	}

	currentUpdatedClusters := make([]corev1.ObjectReference, 0, len(profileScope.GetStatus().UpdatedClusters.Clusters))
	for i := range profileScope.GetStatus().UpdatedClusters.Clusters {
		cluster := &profileScope.GetStatus().UpdatedClusters.Clusters[i]
		if matchingCluster.Has(cluster) {
			currentUpdatedClusters = append(currentUpdatedClusters, *cluster)
		}
	}

	profileScope.GetStatus().UpdatedClusters.Clusters = currentUpdatedClusters

	currentUpdatingClusters := make([]corev1.ObjectReference, 0, len(profileScope.GetStatus().UpdatingClusters.Clusters))
	for i := range profileScope.GetStatus().UpdatingClusters.Clusters {
		cluster := &profileScope.GetStatus().UpdatingClusters.Clusters[i]
		if matchingCluster.Has(cluster) {
			currentUpdatingClusters = append(currentUpdatingClusters, *cluster)
		}
	}

	profileScope.GetStatus().UpdatingClusters.Clusters = currentUpdatingClusters
}

func getMaxUpdate(profileScope *scope.ProfileScope) int32 {
	if profileScope.GetSpec().MaxUpdate == nil {
		return int32(0)
	}

	// Check the Type field to determine if it is an int or a string.
	switch profileScope.GetSpec().MaxUpdate.Type {
	case intstr.Int:
		return profileScope.GetSpec().MaxUpdate.IntVal
	case intstr.String:
		maxUpdateString := profileScope.GetSpec().MaxUpdate.StrVal
		maxUpdateString = strings.ReplaceAll(maxUpdateString, "%s", "")
		var maxUpdateInt int
		if _, err := fmt.Sscanf(maxUpdateString, "%d", &maxUpdateInt); err != nil {
			// There is a validation on format accepted so this should never happen
			profileScope.V(logs.LogInfo).Info(fmt.Sprintf("incorrect MaxUpdate %s: %v",
				profileScope.GetSpec().MaxUpdate.StrVal, err))
			return int32(0)
		}
		return int32(math.Ceil(percent.Percent(
			maxUpdateInt,
			len(profileScope.GetStatus().MatchingClusterRefs),
		)))
	}

	return int32(0)
}

func getUpdatedAndUpdatingClusters(profileScope *scope.ProfileScope,
) (updatedClusters, updatingClusters *libsveltosset.Set) {

	updatedClusters = &libsveltosset.Set{}
	for i := range profileScope.GetStatus().UpdatedClusters.Clusters {
		cluster := &profileScope.GetStatus().UpdatedClusters.Clusters[i]
		updatedClusters.Insert(cluster)
	}

	updatingClusters = &libsveltosset.Set{}
	for i := range profileScope.GetStatus().UpdatingClusters.Clusters {
		cluster := &profileScope.GetStatus().UpdatingClusters.Clusters[i]
		updatingClusters.Insert(cluster)
	}

	return
}

// reviseUpdatingClusterList walks over list of clusters in updating state.
// If all features are marked as provisioned, cluster is moved to updated list.
// Otherwise it is left in the updating list.
func reviseUpdatingClusterList(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	currentHash []byte) error {

	if !reflect.DeepEqual(profileScope.GetStatus().UpdatingClusters.Hash, currentHash) {
		// Since ClusterProfile/Profile Spec hash has changed, even if ClusterSummary are in
		// Provisioned state those cannot be moved to UpdatedClusters. Due to ClusterProfile/Profile
		// Spec change new updates are needed
		return nil
	}

	updatedClusters := libsveltosset.Set{}
	for i := range profileScope.GetStatus().UpdatedClusters.Clusters {
		updatedClusters.Insert(&profileScope.GetStatus().UpdatedClusters.Clusters[i])
	}

	// Walk UpdatingClusters:
	// - if hash is same and cluster is provisioned, moved to UpdatedClusters. Remove from UpdatingClusters
	// - if hash is different, update ClusterSummary and leave it in UpdatingClusters
	updatingClusters := []corev1.ObjectReference{}
	for i := range profileScope.GetStatus().UpdatingClusters.Clusters {
		cluster := profileScope.GetStatus().UpdatingClusters.Clusters[i]
		clusterType := clusterproxy.GetClusterType(&cluster)
		clusterSumary, err := clusterops.GetClusterSummary(ctx, c, profileScope.GetKind(), profileScope.Name(),
			cluster.Namespace, cluster.Name, clusterType)
		if err != nil {
			return err
		}
		if isCluterSummaryProvisioned(clusterSumary) {
			if !updatedClusters.Has(&cluster) {
				profileScope.GetStatus().UpdatedClusters.Clusters =
					append(profileScope.GetStatus().UpdatedClusters.Clusters, cluster)
			}
		} else {
			updatingClusters = append(updatingClusters, cluster)
		}
	}

	profileScope.GetStatus().UpdatingClusters.Clusters = updatingClusters

	return nil
}

func addFinalizer(ctx context.Context, profileScope *scope.ProfileScope, finalizer string) error {
	controllerutil.AddFinalizer(profileScope.Profile, finalizer)
	if err := profileScope.PatchObject(ctx); err != nil {
		profileScope.Error(err, "Failed to add finalizer")
		return fmt.Errorf("failed to add finalizer for %s: %w", profileScope.Name(), err)
	}
	return nil
}

func isProfilePaused(profileScope *scope.ProfileScope) bool {
	return hasAnnotation(profileScope.Profile, configv1beta1.ProfilePausedAnnotation)
}

func reconcileDeleteCommon(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	finalizer string, logger logr.Logger) error {

	profileScope.SetMatchingClusterRefs(nil)

	depManager, err := dependencymanager.GetManagerInstance()
	if err != nil {
		return err
	}
	profileRef := getKeyFromObject(c.Scheme(), profileScope.Profile)
	depManager.UpdateDependencies(profileRef, nil, nil, logger)

	if err := cleanClusterSummaries(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterSummaries")
		return err
	}

	if !allClusterSummariesGone(ctx, c, profileScope) {
		msg := "not all clusterSummaries are gone"
		logger.V(logs.LogInfo).Info(msg)
		return fmt.Errorf("%s", msg)
	}

	if err := cleanClusterConfigurations(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterConfigurations")
		return err
	}

	profile := profileScope.Profile
	if err := cleanClusterReports(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterReports")
		return err
	}

	if !canRemoveFinalizer(ctx, c, profileScope) {
		msg := "cannot remove finalizer yet"
		logger.V(logs.LogInfo).Info(msg)
		return fmt.Errorf("%s", msg)
	}

	if controllerutil.ContainsFinalizer(profile, finalizer) {
		controllerutil.RemoveFinalizer(profile, finalizer)
	}

	return nil
}

func reconcileNormalCommon(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	logger logr.Logger) error {

	// For each matching Sveltos/Cluster, create/update corresponding ClusterConfiguration
	if err := updateClusterConfigurations(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterConfigurations")
		return err
	}
	// For each matching Sveltos/Cluster, create or delete corresponding ClusterReport if needed
	if err := updateClusterReports(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterReports")
		return err
	}
	// For each matching Sveltos/Cluster, create/update corresponding ClusterSummary
	if err := updateClusterSummaries(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to update ClusterSummaries")
		return err
	}

	// For Sveltos/Cluster not matching, deletes corresponding ClusterSummary
	if err := cleanClusterSummaries(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterSummaries")
		return err
	}

	// For Sveltos/Cluster not matching, removes ClusterProfile/Profile as OwnerReference
	// from corresponding ClusterConfiguration
	if err := cleanClusterConfigurations(ctx, c, profileScope); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterConfigurations")
		return err
	}

	depManager, err := dependencymanager.GetManagerInstance()
	if err != nil {
		return err
	}
	profileRef := getKeyFromObject(c.Scheme(), profileScope.Profile)
	depManager.UpdateDependencies(profileRef, profileScope.GetStatus().MatchingClusterRefs,
		getPrerequesites(profileScope), logger)

	return nil
}

func getCurrentClusterSet(matchingClusterRefs []corev1.ObjectReference) *libsveltosset.Set {
	currentClusters := &libsveltosset.Set{}
	for i := range matchingClusterRefs {
		cluster := matchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			Kind:       cluster.Kind,
			APIVersion: cluster.APIVersion}
		currentClusters.Insert(clusterInfo)
	}

	return currentClusters
}

func getConsumersForEntry(currentMap map[corev1.ObjectReference]*libsveltosset.Set,
	entry *corev1.ObjectReference) *libsveltosset.Set {

	s := currentMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		currentMap[*entry] = s
	}
	return s
}

func getPrerequesites(profileScope *scope.ProfileScope) []corev1.ObjectReference {
	spec := profileScope.GetSpec()

	prerequesites := make([]corev1.ObjectReference, len(spec.DependsOn))
	for i := range spec.DependsOn {
		prerequesites[i] = corev1.ObjectReference{
			Namespace:  profileScope.Namespace(),
			Name:       spec.DependsOn[i],
			Kind:       profileScope.GetKind(),
			APIVersion: configv1beta1.GroupVersion.String(),
		}
	}

	return prerequesites
}
