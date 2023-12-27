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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

func getMatchingClusters(ctx context.Context, c client.Client, namespace string,
	profileScope *scope.ProfileScope, logger logr.Logger) ([]corev1.ObjectReference, error) {

	var matchingCluster []corev1.ObjectReference
	var err error
	if profileScope.GetSelector() != "" {
		parsedSelector, _ := labels.Parse(profileScope.GetSelector())
		matchingCluster, err = clusterproxy.GetMatchingClusters(ctx, c, parsedSelector, namespace, logger)
		if err != nil {
			return nil, err
		}
	}

	matchingCluster = append(matchingCluster, profileScope.GetSpec().ClusterRefs...)

	return matchingCluster, nil
}

// allClusterSummariesGone returns true if all ClusterSummaries owned by a
// ClusterProfile/Profile instances are gone.
func allClusterSummariesGone(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) bool {
	listOptions := []client.ListOption{}

	// Originally only ClusterProfile was present and ClusterProfileLabelName was set.
	// With the addition of Profiles, different label is set ProfileLabelName
	if profileScope.Profile.GetObjectKind().GroupVersionKind().Kind == configv1alpha1.ClusterProfileKind {
		listOptions = append(listOptions, client.MatchingLabels{ClusterProfileLabelName: profileScope.Name()})
	} else {
		listOptions = append(listOptions, client.MatchingLabels{ProfileLabelName: profileScope.Name()})
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := c.List(ctx, clusterSummaryList, listOptions...); err != nil {
		profileScope.Logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list clustersummaries. err %v", err))
		return false
	}

	return len(clusterSummaryList.Items) == 0
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
	profile client.Object, clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	profileKind := profile.GetObjectKind().GroupVersionKind().Kind
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentClusterConfiguration, err := getClusterConfiguration(ctx, c,
			clusterConfiguration.Namespace, clusterConfiguration.Name)
		if err != nil {
			return err
		}

		if profileKind == configv1alpha1.ClusterProfileKind {
			for i := range currentClusterConfiguration.Status.ClusterProfileResources {
				cpr := &currentClusterConfiguration.Status.ClusterProfileResources[i]
				if cpr.ClusterProfileName == profile.GetName() {
					return nil
				}
			}
			currentClusterConfiguration.Status.ClusterProfileResources = append(
				currentClusterConfiguration.Status.ClusterProfileResources,
				configv1alpha1.ClusterProfileResource{
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
				configv1alpha1.ProfileResource{
					ProfileName: profile.GetName(),
				})
		}

		return c.Status().Update(ctx, currentClusterConfiguration)
	})
	return err
}

// updateClusterConfigurationOwnerReferences adds profile as owner of ClusterConfiguration
func updateClusterConfigurationOwnerReferences(ctx context.Context, c client.Client,
	profile client.Object, clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	if util.IsOwnedByObject(clusterConfiguration, profile) {
		return nil
	}

	ownerRef := metav1.OwnerReference{
		Kind:       profile.GetObjectKind().GroupVersionKind().Kind,
		UID:        profile.GetUID(),
		APIVersion: configv1alpha1.GroupVersion.String(),
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
			profileScope.Logger.Error(err, fmt.Sprintf("failed to create ClusterConfiguration for cluster %s/%s",
				cluster.Namespace, cluster.Name))
			return err
		}

		// Update ClusterConfiguration
		err = updateClusterConfigurationWithProfile(ctx, c, profileScope.Profile, &cluster)
		if err != nil {
			profileScope.Logger.Error(err, fmt.Sprintf("failed to update ClusterConfiguration for cluster %s/%s",
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
	clusterConfiguratioList := &configv1alpha1.ClusterConfigurationList{}

	matchingClusterMap := make(map[string]bool)

	info := func(namespace, clusterConfigurationName string) string {
		return fmt.Sprintf("%s--%s", namespace, clusterConfigurationName)
	}

	for i := range profileScope.GetStatus().MatchingClusterRefs {
		ref := &profileScope.GetStatus().MatchingClusterRefs[i]
		matchingClusterMap[info(ref.Namespace, getClusterConfigurationName(ref.Name, clusterproxy.GetClusterType(ref)))] = true
	}

	err := c.List(ctx, clusterConfiguratioList)
	if err != nil {
		return err
	}

	for i := range clusterConfiguratioList.Items {
		cc := &clusterConfiguratioList.Items[i]

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
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

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
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

	ownerRef := metav1.OwnerReference{
		Kind:       profile.GetObjectKind().GroupVersionKind().Kind,
		UID:        profile.GetUID(),
		APIVersion: configv1alpha1.GroupVersion.String(),
		Name:       profile.GetName(),
	}

	if !util.IsOwnedByObject(clusterConfiguration, profile) {
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
	clusterConfiguration *configv1alpha1.ClusterConfiguration) error {

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
		if profileKind == configv1alpha1.ClusterProfileKind {
			return cleanClusterProfileResources(ctx, c, profile, currentClusterConfiguration)
		} else {
			return cleanProfileResources(ctx, c, profile, currentClusterConfiguration)
		}
	})
	return err
}

func cleanClusterProfileResources(ctx context.Context, c client.Client, profile client.Object,
	currentClusterConfiguration *configv1alpha1.ClusterConfiguration) error {

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
	currentClusterConfiguration *configv1alpha1.ClusterConfiguration) error {

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

	clusterSummary, err := getClusterSummary(ctx, c, profileScope.GetKind(), profileScope.Name(),
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

func addClusterSummaryLabels(clusterSummary *configv1alpha1.ClusterSummary, profileScope *scope.ProfileScope,
	cluster *corev1.ObjectReference) {

	if profileScope.Profile.GetObjectKind().GroupVersionKind().Kind == configv1alpha1.ClusterProfileKind {
		addLabel(clusterSummary, ClusterProfileLabelName, profileScope.Name())
	} else {
		addLabel(clusterSummary, ProfileLabelName, profileScope.Name())
	}
	addLabel(clusterSummary, configv1alpha1.ClusterNameLabel, cluster.Name)
	if cluster.APIVersion == libsveltosv1alpha1.GroupVersion.String() {
		addLabel(clusterSummary, configv1alpha1.ClusterTypeLabel, string(libsveltosv1alpha1.ClusterTypeSveltos))
	} else {
		addLabel(clusterSummary, configv1alpha1.ClusterTypeLabel, string(libsveltosv1alpha1.ClusterTypeCapi))
	}

	clusterProfileLabels := profileScope.Profile.GetLabels()
	if clusterProfileLabels != nil {
		v, ok := clusterProfileLabels[libsveltosv1alpha1.ServiceAccountNameLabel]
		if ok {
			addLabel(clusterSummary, libsveltosv1alpha1.ServiceAccountNameLabel, v)
		}
		v, ok = clusterProfileLabels[libsveltosv1alpha1.ServiceAccountNamespaceLabel]
		if ok {
			addLabel(clusterSummary, libsveltosv1alpha1.ServiceAccountNamespaceLabel, v)
		}
	}
}

// deleteClusterSummary deletes ClusterSummary given a ClusterProfile/Profile and a matching Sveltos/Cluster
func deleteClusterSummary(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary) error {
	return c.Delete(ctx, clusterSummary)
}

func updateClusterSummarySyncMode(ctx context.Context, c client.Client,
	profile *scope.ProfileScope, clusterSummary *configv1alpha1.ClusterSummary) error {

	currentClusterSummary := &configv1alpha1.ClusterSummary{}
	err := c.Get(ctx, types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
		currentClusterSummary)
	if err != nil {
		return err
	}
	currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = profile.GetSpec().SyncMode
	return c.Update(ctx, currentClusterSummary)
}

// createClusterSummary creates ClusterSummary given a ClusterProfile and a matching Sveltos/Cluster
func createClusterSummary(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	cluster *corev1.ObjectReference) error {

	clusterSummaryName := GetClusterSummaryName(profileScope.GetKind(), profileScope.Name(),
		cluster.Name, cluster.APIVersion == libsveltosv1alpha1.GroupVersion.String())

	clusterSummary := &configv1alpha1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterSummaryName,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: configv1alpha1.GroupVersion.String(),
					Kind:       profileScope.Profile.GetObjectKind().GroupVersionKind().Kind,
					Name:       profileScope.Profile.GetName(),
					UID:        profileScope.Profile.GetUID(),
				},
			},
			Annotations: profileScope.Profile.GetAnnotations(),
		},
		Spec: configv1alpha1.ClusterSummarySpec{
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
		profileScope.GetStatus().UpdatedClusters = configv1alpha1.Clusters{
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

	maxUpdate := getMaxUpdate(profileScope)

	skippedUpdate := false
	// Consider matchingCluster number and MaxUpdate, walk remaining matching clusters.  If more clusters can be
	// updated, update ClusterSummary and add it to UpdatingClusters
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]

		logger := profileScope.Logger
		logger = logger.WithValues("cluster", fmt.Sprintf("%s:%s/%s", cluster.Kind, cluster.Namespace, cluster.Name))

		ready, err := clusterproxy.IsClusterReadyToBeConfigured(ctx, c, &cluster, profileScope.Logger)
		if err != nil {
			return err
		}
		if !ready {
			logger.V(logs.LogDebug).Info("Cluster is not ready yet")
			continue
		}

		if updatedClusters.Has(&cluster) {
			logger.V(logs.LogDebug).Info("Cluster is already updated")
			continue
		}

		// if maxUpdate is set no more than maxUpdate clusters can be updated in parallel by ClusterProfile
		if maxUpdate != 0 && !updatingClusters.Has(&cluster) && int32(updatingClusters.Len()) >= maxUpdate {
			logger.V(logs.LogDebug).Info(fmt.Sprintf("Already %d being updating", updatingClusters.Len()))
			skippedUpdate = true
			continue
		}

		// ClusterProfile does not look at whether Cluster is paused or not.
		// If a Cluster exists and it is a match, ClusterSummary is created (and ClusterSummary.Spec kept in sync if mode is
		// continuous).
		// ClusterSummary won't program cluster in paused state.
		_, err = getClusterSummary(ctx, c, profileScope.GetKind(), profileScope.Name(), cluster.Namespace,
			cluster.Name, clusterproxy.GetClusterType(&cluster))
		if err != nil {
			if apierrors.IsNotFound(err) {
				err = createClusterSummary(ctx, c, profileScope, &cluster)
				if err != nil {
					profileScope.Logger.Error(err, fmt.Sprintf("failed to create ClusterSummary for cluster %s/%s",
						cluster.Namespace, cluster.Name))
				}
			} else {
				profileScope.Logger.Error(err, "failed to get ClusterSummary for cluster %s/%s",
					cluster.Namespace, cluster.Name)
				return err
			}
		} else {
			err = updateClusterSummary(ctx, c, profileScope, &cluster)
			if err != nil {
				profileScope.Logger.Error(err, "failed to update ClusterSummary for cluster %s/%s",
					cluster.Namespace, cluster.Name)
				return err
			}
		}

		if !updatingClusters.Has(&cluster) {
			updatingClusters.Insert(&cluster)
			profileScope.GetStatus().UpdatingClusters.Clusters =
				append(profileScope.GetStatus().UpdatingClusters.Clusters,
					cluster)
		}

		profileScope.GetStatus().UpdatingClusters.Hash = currentHash
	}

	if skippedUpdate {
		return fmt.Errorf("Not all clusters updated yet. %d still being updated",
			len(profileScope.GetStatus().UpdatingClusters.Clusters))
	}

	// If all ClusterSummaries have been updated, reset Updated and Updating
	profileScope.GetStatus().UpdatedClusters = configv1alpha1.Clusters{}
	profileScope.GetStatus().UpdatingClusters = configv1alpha1.Clusters{}

	return nil
}

// cleanClusterSummaries finds all ClusterSummary currently owned by ClusterProfile/Profile.
// For each such ClusterSummary, if corresponding Sveltos/Cluster is not a match anymore, deletes ClusterSummary
func cleanClusterSummaries(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	matching := make(map[string]bool)

	getClusterInfo := func(clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
		return fmt.Sprintf("%s-%s-%s", clusterType, clusterNamespace, clusterName)
	}

	for i := range profileScope.GetStatus().MatchingClusterRefs {
		reference := profileScope.GetStatus().MatchingClusterRefs[i]
		clusterName := getClusterInfo(reference.Namespace, reference.Name, clusterproxy.GetClusterType(&reference))
		matching[clusterName] = true
	}

	listOptions := []client.ListOption{}
	if profileScope.Profile.GetObjectKind().GroupVersionKind().Kind == configv1alpha1.ClusterProfileKind {
		listOptions = append(listOptions,
			client.MatchingLabels{ClusterProfileLabelName: profileScope.Name()})
	} else {
		listOptions = append(listOptions,
			client.MatchingLabels{ProfileLabelName: profileScope.Name()})
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := c.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return err
	}

	foundClusterSummaries := false
	for i := range clusterSummaryList.Items {
		cs := &clusterSummaryList.Items[i]
		if util.IsOwnedByObject(cs, profileScope.Profile) {
			if _, ok := matching[getClusterInfo(cs.Spec.ClusterNamespace, cs.Spec.ClusterName, cs.Spec.ClusterType)]; !ok {
				err := deleteClusterSummary(ctx, c, cs)
				if err != nil {
					profileScope.Logger.Error(err, fmt.Sprintf("failed to update ClusterSummary for cluster %s/%s",
						cs.Namespace, cs.Name))
					return err
				}
				foundClusterSummaries = true
			}
			// update SyncMode
			err := updateClusterSummarySyncMode(ctx, c, profileScope, cs)
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

// ClusterReports

// updateClusterReports for each Sveltos/Cluster currently matching ClusterProfile/Profile:
// - if syncMode is DryRun, creates corresponding ClusterReport if one does not exist already;
// - if syncMode is DryRun, deletes ClusterReports for any Sveltos/Cluster not matching anymore;
// - if syncMode is not DryRun, deletes ClusterReports created by this ClusterProfile instance
func updateClusterReports(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	if profileScope.IsDryRunSync() {
		err := createClusterReports(ctx, c, profileScope)
		if err != nil {
			profileScope.Logger.Error(err, "failed to create ClusterReports")
			return err
		}
	} else {
		// delete all ClusterReports created by this ClusterProfile/Profile instance
		err := cleanClusterReports(ctx, c, profileScope.Profile)
		if err != nil {
			profileScope.Logger.Error(err, "failed to delete ClusterReports")
			return err
		}
	}

	return nil
}

// createClusterReports creates a ClusterReport for each matching Cluster
func createClusterReports(ctx context.Context, c client.Client, profileScope *scope.ProfileScope) error {
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]

		// Create ClusterConfiguration if not already existing.
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

	clusterReport := &configv1alpha1.ClusterReport{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name: getClusterReportName(profile.GetObjectKind().GroupVersionKind().Kind, profile.GetName(),
				cluster.Name, clusterType),
			Labels: map[string]string{
				ClusterProfileLabelName:         profile.GetName(),
				configv1alpha1.ClusterNameLabel: cluster.Name,
				configv1alpha1.ClusterTypeLabel: string(clusterproxy.GetClusterType(cluster)),
			},
		},
		Spec: configv1alpha1.ClusterReportSpec{
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
func cleanClusterReports(ctx context.Context, c client.Client, profile client.Object) error {
	listOptions := []client.ListOption{}

	if profile.GetObjectKind().GroupVersionKind().Kind == configv1alpha1.ClusterProfileKind {
		listOptions = append(listOptions,
			client.MatchingLabels{ClusterProfileLabelName: profile.GetName()})
	} else {
		listOptions = append(listOptions,
			client.MatchingLabels{ProfileLabelName: profile.GetName()})
	}

	clusterReportList := &configv1alpha1.ClusterReportList{}
	err := c.List(ctx, clusterReportList, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterReportList.Items {
		err = c.Delete(ctx, &clusterReportList.Items[i])
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

	updatedClusters := &libsveltosset.Set{}
	currentUpdatedClusters := make([]corev1.ObjectReference, 0)
	for i := range profileScope.GetStatus().UpdatedClusters.Clusters {
		cluster := &profileScope.GetStatus().UpdatedClusters.Clusters[i]
		if matchingCluster.Has(cluster) {
			currentUpdatedClusters = append(currentUpdatedClusters, *cluster)
			updatedClusters.Insert(cluster)
		}
	}

	profileScope.GetStatus().UpdatedClusters.Clusters = currentUpdatedClusters

	updatingClusters := &libsveltosset.Set{}
	currentUpdatingClusters := make([]corev1.ObjectReference, 0)
	for i := range profileScope.GetStatus().UpdatingClusters.Clusters {
		cluster := &profileScope.GetStatus().UpdatingClusters.Clusters[i]
		if matchingCluster.Has(cluster) {
			currentUpdatingClusters = append(currentUpdatingClusters, *cluster)
			updatingClusters.Insert(cluster)
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
			profileScope.Logger.V(logs.LogInfo).Info(fmt.Sprintf("incorrect MaxUpdate %s: %v",
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

	// Walk UpdatingClusters:
	// - if hash is same and cluster is provisioned, moved to UpdatedClusters. Remove from UpdatingClusters
	// - if hash is different, update ClusterSummary and leave it in UpdatingClusters
	updatingClusters := []corev1.ObjectReference{}
	for i := range profileScope.GetStatus().UpdatingClusters.Clusters {
		cluster := &profileScope.GetStatus().UpdatingClusters.Clusters[i]
		clusterType := clusterproxy.GetClusterType(cluster)
		clusterSumary, err := getClusterSummary(ctx, c, profileScope.GetKind(), profileScope.Name(),
			cluster.Namespace, cluster.Name, clusterType)
		if err != nil {
			return err
		}
		if isCluterSummaryProvisioned(clusterSumary) {
			profileScope.GetStatus().UpdatedClusters.Clusters =
				append(profileScope.GetStatus().UpdatedClusters.Clusters, *cluster)
		} else {
			updatingClusters = append(updatingClusters, *cluster)
		}
	}
	profileScope.GetStatus().UpdatingClusters.Clusters = updatingClusters

	return nil
}

func addFinalizer(ctx context.Context, profileScope *scope.ProfileScope, finalizer string) error {
	controllerutil.AddFinalizer(profileScope.Profile, finalizer)
	if err := profileScope.PatchObject(ctx); err != nil {
		profileScope.Error(err, "Failed to add finalizer")
		return errors.Wrapf(
			err,
			"Failed to add finalizer for %s",
			profileScope.Name(),
		)
	}
	return nil
}

func reconcileDeleteCommon(ctx context.Context, c client.Client, profileScope *scope.ProfileScope,
	finalizer string, logger logr.Logger) error {

	profileScope.SetMatchingClusterRefs(nil)

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
	if err := cleanClusterReports(ctx, c, profile); err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to clean ClusterReports")
		return err
	}

	if !canRemoveFinalizer(ctx, c, profileScope) {
		msg := "cannot rremove finalizer yet"
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

	return nil
}

func getCurrentClusterSet(profileScope *scope.ProfileScope) *libsveltosset.Set {
	currentClusters := &libsveltosset.Set{}
	for i := range profileScope.GetStatus().MatchingClusterRefs {
		cluster := profileScope.GetStatus().MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			Kind:       cluster.Kind,
			APIVersion: cluster.APIVersion}
		currentClusters.Insert(clusterInfo)
	}

	return currentClusters
}

func getClusterMapForEntry(clusterMap map[corev1.ObjectReference]*libsveltosset.Set,
	entry *corev1.ObjectReference) *libsveltosset.Set {

	s := clusterMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		clusterMap[*entry] = s
	}
	return s
}
