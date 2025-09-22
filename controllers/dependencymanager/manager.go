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

package dependencymanager

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

type RequestingProfiles struct {
	Dependents map[corev1.ObjectReference]bool `json:"dependents,omitempty"`
}

// AddDependent adds a dependent to the requesting profiles.
func (rp *RequestingProfiles) addDependent(dependent *corev1.ObjectReference) {
	if rp.Dependents == nil {
		rp.Dependents = make(map[corev1.ObjectReference]bool)
	}
	rp.Dependents[*dependent] = true
}

// RemoveDependent removes a dependent from the requesting profiles.
func (rp *RequestingProfiles) removeDependent(dependent *corev1.ObjectReference) {
	if rp.Dependents != nil {
		delete(rp.Dependents, *dependent)
	}
}

// ClusterDeployments maps clusters to profiles requesting deployment.
type ClusterDeployments struct {
	Clusters map[corev1.ObjectReference]RequestingProfiles `json:"clusters,omitempty"`
}

func (cd *ClusterDeployments) addCluster(cluster *corev1.ObjectReference) {
	if cd.Clusters == nil {
		cd.Clusters = make(map[corev1.ObjectReference]RequestingProfiles)
	}
	if _, ok := cd.Clusters[*cluster]; !ok {
		cd.Clusters[*cluster] = RequestingProfiles{}
	}
}

// hasCluster checks if a cluster exists.
func (cd *ClusterDeployments) hasCluster(cluster *corev1.ObjectReference) bool {
	if cd.Clusters == nil {
		return false
	}
	_, ok := cd.Clusters[*cluster]
	return ok
}

// GetRequestingProfiles returns the requesting profiles for a cluster.
func (cd *ClusterDeployments) getRequestingProfiles(cluster *corev1.ObjectReference) RequestingProfiles {
	if cd.Clusters == nil {
		return RequestingProfiles{}
	}
	profiles := cd.Clusters[*cluster]
	return profiles
}

func (cd *ClusterDeployments) setRequestingProfiles(cluster *corev1.ObjectReference, dependents RequestingProfiles) {
	cd.Clusters[*cluster] = dependents
}

type ProfileDeployments struct {
	Profiles map[corev1.ObjectReference]ClusterDeployments `json:"profiles,omitempty"`
}

// AddProfile adds a profile and its cluster deployments.
func (pd *ProfileDeployments) addProfile(profile *corev1.ObjectReference, deployments ClusterDeployments) {
	if pd.Profiles == nil {
		pd.Profiles = make(map[corev1.ObjectReference]ClusterDeployments)
	}
	pd.Profiles[*profile] = deployments
}

// RemoveProfile removes a profile.
func (pd *ProfileDeployments) removeProfile(profile *corev1.ObjectReference) {
	if pd.Profiles != nil {
		delete(pd.Profiles, *profile)
	}
}

// hasProfile checks if a profile exists
func (pd *ProfileDeployments) hasProfile(profile *corev1.ObjectReference) bool {
	if pd.Profiles == nil {
		return false
	}
	_, ok := pd.Profiles[*profile]
	return ok
}

// GetClusterDeployments returns the cluster deployments for a profile.
func (pd *ProfileDeployments) getClusterDeployments(profile *corev1.ObjectReference) ClusterDeployments {
	if pd.Profiles == nil {
		return ClusterDeployments{}
	}
	return pd.Profiles[*profile]
}

// DependencyManager manages cluster profile dependencies, ensuring profiles are deployed
// to clusters based on their dependencies.
type instance struct {
	chartMux sync.RWMutex // use a Mutex to update internal structures MaxConcurrentReconciles is higher than one for ClusterProfile/Profile

	// ProfileClusterRequests tracks cluster deployment requirements for profiles,
	// including the profiles that initiated those requests. This data structure maintains
	// a comprehensive view of which profiles are required to be deployed to specific
	// clusters and the reasons behind these deployment requests, specifically which
	// dependent profiles have triggered them.
	//
	// Example Scenario:
	//   - profileA (deployed to cluster1, cluster2) depends on profile0.
	//   - profileB (deployed to cluster1, cluster3) depends on profile0.
	//
	// In this scenario, profileA requires profile0 to be deployed to cluster1 and cluster2,
	// while profileB necessitates profile0's deployment to cluster1 and cluster3.
	//
	// This results in:
	//   - profile0:
	//     - cluster1: Requested by profileA, profileB.
	//     - cluster2: Requested by profileA.
	//     - cluster3: Requested by profileB.
	//
	// The ProfileClusterRequests map will reflect that profile0 needs to be deployed to
	// cluster1 because both profileA and profileB depend on it, to cluster2 because profileA
	// depends on it, and to cluster3 because profileB depends on it. This ensures that
	// all dependencies are met across the various clusters.
	//
	// The structure captures which profiles need to be deployed to which clusters,
	// and which dependent profiles triggered those deployment requests, allowing for
	// efficient management and reconciliation of cluster profile dependencies.
	profileClusterRequests ProfileDeployments

	// profileDependencies tracks the prerequisites of each profile (the other profiles current profile depends on)
	// This is used for efficient cleanup. If profileA depends on profileB,
	// this dependency is recorded. If profileA no longer depends on profileB,
	// profileB needs to be removed from clusters where profileA was previously deployed
	// assuming profileB was only deployed there due to profileA's dependency.
	profilePrerequisites map[corev1.ObjectReference]*libsveltosset.Set

	profileToBeUpdated map[corev1.ObjectReference]bool

	// enabled indicates whether auto deployment of pre-requesities is enabled or not
	enabled bool

	// initialzied indicates whether manager is fully initialized (if enabled on boot, it
	// reads profile instances to rebuild internal state)
	initialized int32 // 0 = false, 1 = true
}

var (
	managerInstance *instance
	lock            = &sync.Mutex{}
)

// InitializeManagerInstance returns the singleton instance of the instance struct.
func InitializeManagerInstance(ctx context.Context, c client.Client, enabled bool, logger logr.Logger) {
	if managerInstance == nil {
		lock.Lock()
		defer lock.Unlock()
		if managerInstance == nil {
			managerInstance = &instance{
				chartMux:               sync.RWMutex{},
				profileClusterRequests: ProfileDeployments{},
				profilePrerequisites:   make(map[corev1.ObjectReference]*libsveltosset.Set),
				profileToBeUpdated:     make(map[corev1.ObjectReference]bool),
				enabled:                enabled,
			}

			if enabled {
				atomic.StoreInt32(&managerInstance.initialized, 0)
				go managerInstance.rebuildState(ctx, c, logger)
			} else {
				atomic.StoreInt32(&managerInstance.initialized, 1)
			}
		}
	}
}

// GetManagerInstance returns the singleton instance of the instance struct.
func GetManagerInstance() (*instance, error) {
	if managerInstance == nil {
		return nil, fmt.Errorf("dependency manager not initialized yet")
	}

	if atomic.LoadInt32(&managerInstance.initialized) == 0 {
		return nil, fmt.Errorf("dependency manager not initialized yet")
	}

	return managerInstance, nil
}

// GetClusterDeployments returns the list of clusters where profiles needs to be deployed based on
// dependent profile instances
func (m *instance) GetClusterDeployments(profile *corev1.ObjectReference) []corev1.ObjectReference {
	if !m.enabled {
		return nil
	}

	m.chartMux.RLock()
	clusters := m.profileClusterRequests.getClusterDeployments(profile)
	m.chartMux.RUnlock()

	results := make([]corev1.ObjectReference, len(clusters.Clusters))
	i := 0
	for c := range clusters.Clusters {
		results[i] = c
		i++
	}

	return results
}

// UpdatePrerequisites registers the prerequisites of a dependent (i.e a profile) as needed on the
// dependent's matching clusters.
//
// Parameters:
//   - dependent:        The profile requesting the update.
//   - matchingClusters: The clusters the profile currently matches.
//   - prerequisites:     The list of profiles this profile depends on.
func (m *instance) UpdateDependencies(dependent *corev1.ObjectReference,
	matchingClusters []corev1.ObjectReference, prerequisites []corev1.ObjectReference, logger logr.Logger) {

	if !m.enabled {
		return
	}

	m.chartMux.Lock()
	defer m.chartMux.Unlock()

	currentDependencies := &libsveltosset.Set{}
	// Iterate through each prerequisite
	for i := range prerequisites {
		prerequisite := &prerequisites[i]
		// Associates the specified prerequisite (i.e a profile) with the matching clusters for the given dependent.
		// This ensures the profile is deployed to all necessary clusters to satisfy the dependent's requirements.
		m.scheduleProfileDeploymentToClusters(prerequisite, dependent, matchingClusters, logger)
		currentDependencies.Insert(prerequisite)
	}

	// Remove deployments of prerequisite no longer required by the dependent.
	// This function cleans up stale dependencies, ensuring that clusters are only configured
	// with the prerequisite (i.e, profiles) currently needed by the dependent.
	m.cleanStaleDependencies(dependent, currentDependencies, matchingClusters, logger)

	// Update current dependencies for profile
	if _, ok := m.profilePrerequisites[*dependent]; !ok {
		m.profilePrerequisites[*dependent] = &libsveltosset.Set{}
	}
	for i := range prerequisites {
		profilePrerequisite := prerequisites[i]
		m.profilePrerequisites[*dependent].Insert(&profilePrerequisite)
	}
}

// registerProfileClusterDependency records that a prerequisite needs to be deployed to a specific cluster
// due to a dependency from a given dependent. This function updates the internal data structures
// to reflect the dependency relationship.
func (m *instance) registerProfileClusterDependency(prerequisite, cluster, dependent *corev1.ObjectReference,
	logger logr.Logger) {

	if m.profileClusterRequests.Profiles == nil {
		m.profileClusterRequests.Profiles = make(map[corev1.ObjectReference]ClusterDeployments)
	}

	clusterDeployments := m.profileClusterRequests.getClusterDeployments(prerequisite)

	if !clusterDeployments.hasCluster(cluster) {
		l := logger.WithValues("prerequisite", fmt.Sprintf("%s/%s", prerequisite.Namespace, prerequisite.Name)).
			WithValues("dependent", fmt.Sprintf("%s/%s", dependent.Namespace, dependent.Name)).
			WithValues("cluster", fmt.Sprintf("%s:%s/%s", cluster.Kind, cluster.Namespace, cluster.Name))
		l.V(logs.LogDebug).Info("add dependency")
		m.profileToBeUpdated[*prerequisite] = true
	}
	clusterDeployments.addCluster(cluster)

	requestingProfiles := clusterDeployments.getRequestingProfiles(cluster)
	requestingProfiles.addDependent(dependent)
	clusterDeployments.setRequestingProfiles(cluster, requestingProfiles)

	if m.profileClusterRequests.hasProfile(prerequisite) {
		l := logger.WithValues("prerequisite", fmt.Sprintf("%s/%s", prerequisite.Namespace, prerequisite.Name)).
			WithValues("dependent", fmt.Sprintf("%s/%s", dependent.Namespace, dependent.Name)).
			WithValues("cluster", fmt.Sprintf("%s:%s/%s", cluster.Kind, cluster.Namespace, cluster.Name))
		l.V(logs.LogDebug).Info("add dependency")
		m.profileToBeUpdated[*prerequisite] = true
	}
	m.profileClusterRequests.addProfile(prerequisite, clusterDeployments)
}

// scheduleProfileDeploymentToClusters schedules the deployment of a prerequisite profile to the specified
// matching clusters, based on the dependency from the given dependent profile.
// This function registers the dependency relationship between the prerequisite and dependent profiles
// for each matching cluster, ensuring that the prerequisite profile is deployed before the dependent.
func (m *instance) scheduleProfileDeploymentToClusters(prerequisite, dependent *corev1.ObjectReference,
	matchingClusters []corev1.ObjectReference, logger logr.Logger) {

	for i := range matchingClusters {
		m.registerProfileClusterDependency(prerequisite, &matchingClusters[i], dependent, logger)
	}
}

func (m *instance) cleanStaleDependencies(dependent *corev1.ObjectReference,
	currentDependencies *libsveltosset.Set, matchingClusters []corev1.ObjectReference, logger logr.Logger) {

	matchingClustersMap := map[corev1.ObjectReference]bool{}
	for i := range matchingClusters {
		mc := matchingClusters[i]
		matchingClustersMap[mc] = true
	}
	items := currentDependencies.Items()
	for i := range items {
		m.removeDependent(&items[i], dependent, matchingClustersMap, logger)
	}

	if m.profilePrerequisites == nil {
		return
	}

	if _, ok := m.profilePrerequisites[*dependent]; !ok {
		return
	}

	previousDependencies := m.profilePrerequisites[*dependent]
	staleDependencies := previousDependencies.Difference(currentDependencies)

	for i := range staleDependencies {
		stalePrerequisite := &staleDependencies[i]
		m.removeDependent(stalePrerequisite, dependent, map[corev1.ObjectReference]bool{}, logger)
	}
}

func (m *instance) removeDependent(prerequisite, dependent *corev1.ObjectReference,
	excludeClusters map[corev1.ObjectReference]bool, logger logr.Logger) {

	if m.profileClusterRequests.Profiles == nil {
		return
	}

	clusterDeployments := m.profileClusterRequests.getClusterDeployments(prerequisite)

	for clusterRef, requestingProfiles := range clusterDeployments.Clusters {
		if _, ok := excludeClusters[clusterRef]; ok {
			continue
		}

		l := logger.WithValues("prerequisite", fmt.Sprintf("%s/%s", prerequisite.Namespace, prerequisite.Name)).
			WithValues("dependent", fmt.Sprintf("%s/%s", dependent.Namespace, dependent.Name)).
			WithValues("cluster", fmt.Sprintf("%s:%s/%s", clusterRef.Kind, clusterRef.Namespace, clusterRef.Name))

		requestingProfiles.removeDependent(dependent)
		if len(requestingProfiles.Dependents) == 0 {
			l.V(logs.LogDebug).Info("remove dependency")
			m.profileToBeUpdated[*prerequisite] = true
			delete(clusterDeployments.Clusters, clusterRef)
		} else {
			clusterDeployments.Clusters[clusterRef] = requestingProfiles
		}
	}

	if len(clusterDeployments.Clusters) == 0 {
		m.profileClusterRequests.removeProfile(prerequisite)
	} else {
		m.profileClusterRequests.addProfile(prerequisite, clusterDeployments)
	}
}

func (m *instance) updateProfileInstance(ctx context.Context, c client.Client, profile *corev1.ObjectReference,
	clusters ClusterDeployments) error {

	if profile.Kind == configv1beta1.ProfileKind {
		return m.updateProfile(ctx, c, profile, clusters)
	}

	return m.updateClusterProfile(ctx, c, profile, clusters)
}

func calculateHash(data map[corev1.ObjectReference]RequestingProfiles) []byte {
	h := sha256.New()
	var config string

	// 1. Extract all keys into a slice.
	keys := make([]corev1.ObjectReference, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}

	sort.Sort(SortedCorev1ObjectReference(keys))

	for k := range keys {
		config += render.AsCode(k)
	}

	h.Write([]byte(config))
	return h.Sum(nil)
}

func (m *instance) updateProfile(ctx context.Context, c client.Client, profile *corev1.ObjectReference,
	clusters ClusterDeployments) error {

	clusterRef := make([]corev1.ObjectReference, len(clusters.Clusters))
	i := 0
	for cluster := range clusters.Clusters {
		clusterRef[i] = cluster
		i++
	}

	currentProfile := &configv1beta1.Profile{}

	err := c.Get(ctx, types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name}, currentProfile)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if !currentProfile.DeletionTimestamp.IsZero() {
		return nil
	}

	currentProfile.Status.DependenciesHash = calculateHash(clusters.Clusters)
	return c.Status().Update(ctx, currentProfile)
}

func (m *instance) updateClusterProfile(ctx context.Context, c client.Client, clusterProfile *corev1.ObjectReference,
	clusters ClusterDeployments) error {

	clusterRef := make([]corev1.ObjectReference, len(clusters.Clusters))
	i := 0
	for cluster := range clusters.Clusters {
		clusterRef[i] = cluster
		i++
	}

	currentClusterProfile := &configv1beta1.ClusterProfile{}

	err := c.Get(ctx, types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if !currentClusterProfile.DeletionTimestamp.IsZero() {
		return nil
	}

	currentClusterProfile.Status.DependenciesHash = calculateHash(clusters.Clusters)
	return c.Status().Update(ctx, currentClusterProfile)
}

func (m *instance) updateProfiles(ctx context.Context, c client.Client, logger logr.Logger) {
	for {
		m.chartMux.Lock()

		for profile := range m.profileToBeUpdated {
			clusters := m.profileClusterRequests.getClusterDeployments(&profile)
			logger.V(logs.LogDebug).Info(fmt.Sprintf("updating prerequestite profile %s/%s", profile.Namespace, profile.Name))
			err := m.updateProfileInstance(ctx, c, &profile, clusters)
			if err == nil {
				delete(m.profileToBeUpdated, profile)
			}
		}

		m.chartMux.Unlock()

		const interval = 30 * time.Second
		time.Sleep(interval)
	}
}

func (m *instance) rebuildState(ctx context.Context, c client.Client, logger logr.Logger) {
	for {
		if err := m.rebuildStateWithProfiles(ctx, c, logger); err != nil {
			continue
		}
		if err := m.rebuildStateWithClusterProfiles(ctx, c, logger); err != nil {
			continue
		}
		break
	}

	for profile := range m.profileToBeUpdated {
		clusters := m.profileClusterRequests.getClusterDeployments(&profile)
		logger.V(logs.LogDebug).Info(fmt.Sprintf("updating prerequestite profile %s/%s", profile.Namespace, profile.Name))
		err := m.updateProfileInstance(ctx, c, &profile, clusters)
		if err == nil {
			delete(m.profileToBeUpdated, profile)
		}
	}

	go managerInstance.updateProfiles(ctx, c, logger)

	atomic.StoreInt32(&managerInstance.initialized, 1)
}

func (m *instance) rebuildStateWithProfiles(ctx context.Context, c client.Client, logger logr.Logger) error {
	profiles := &configv1beta1.ProfileList{}
	if err := c.List(ctx, profiles); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list profiles: %v", err))
		return err
	}

	for i := range profiles.Items {
		profile := profiles.Items[i]
		if len(profile.Spec.DependsOn) == 0 {
			continue
		}

		profileRef := &corev1.ObjectReference{Kind: configv1beta1.ProfileKind, APIVersion: configv1beta1.GroupVersion.String(),
			Namespace: profile.Namespace, Name: profile.Name}
		m.UpdateDependencies(profileRef, profile.Status.MatchingClusterRefs,
			getPrerequesites(configv1beta1.ProfileKind, profile.Namespace, profile.Spec.DependsOn), logger)
	}
	return nil
}

func (m *instance) rebuildStateWithClusterProfiles(ctx context.Context, c client.Client, logger logr.Logger) error {
	clusterProfiles := &configv1beta1.ClusterProfileList{}
	if err := c.List(ctx, clusterProfiles); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list clusterProfiles: %v", err))
		return err
	}

	for i := range clusterProfiles.Items {
		clusterProfile := clusterProfiles.Items[i]
		if len(clusterProfile.Spec.DependsOn) == 0 {
			continue
		}

		clusterProfileRef := &corev1.ObjectReference{Kind: configv1beta1.ClusterProfileKind,
			APIVersion: configv1beta1.GroupVersion.String(), Name: clusterProfile.Name}
		m.UpdateDependencies(clusterProfileRef, clusterProfile.Status.MatchingClusterRefs,
			getPrerequesites(configv1beta1.ClusterProfileKind, "", clusterProfile.Spec.DependsOn), logger)
	}

	return nil
}

func getPrerequesites(kind, namespace string, dependsOn []string) []corev1.ObjectReference {
	prerequesites := make([]corev1.ObjectReference, len(dependsOn))
	for i := range dependsOn {
		prerequesites[i] = corev1.ObjectReference{
			Namespace:  namespace,
			Name:       dependsOn[i],
			Kind:       kind,
			APIVersion: configv1beta1.GroupVersion.String(),
		}
	}

	return prerequesites
}
