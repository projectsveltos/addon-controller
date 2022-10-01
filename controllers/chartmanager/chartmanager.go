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

package chartmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

// Multiple ClusterProfiles can:
// - match same CAPI Clusters
// - reference same set of Helm Chart(s)/Version(s)
// Above is a misconfiguration/conflict that needs to be detected.
// Only one ClusterSummary (there is one ClusterSummary for each pair ClusterProfile/CAPI Cluster) can manage an helm
// version.
// Following client is used to solve such scenarios. One ClusterSummary will get the manager role for a given helm version
// in a given CAPI Cluster. All other ClusterSummaries will report a conflict that requires admin intervention to be resolved.

type instance struct {
	chartMux sync.Mutex // use a Mutex to update chart maps as ClusterSummary MaxConcurrentReconciles is higher than one

	// Multiple ClusterSummaries might match same CAPI Cluster and try to deploy same helm release in the same namespace.
	// That needs to be flagged as a misconfiguration.
	// In order to achieve that following map is used. It contains:
	// - per CAPI Cluster (key: clusterNamespace/clusterName)
	//     - per Release (key: releaseNamespace/releaseName)
	//         - list of ClusterSummaries that want to deploy above helm release in CAPI Clustem.
	// First ClusterSummary able to add entry for a given CAPI Cluster/Release is allowed to manage that release
	// canManageChart answers whether a ClusterSummary can manage an helm feature.
	// Any other ClusterSummary will report the misconfiguration.
	//
	// When ClusterSummary managing a release in a CAPI Cluster is deleted or stops managing that release, the next
	// ClusterSummary in line will become the new manager.
	// That is achieved as part of ClusterSummaryReconciler. Such reconciler watches for ClusterSummary changes, when
	// a ClusterSummary.Spec.ClusterProfileSpec.HelmCharts changes, requeues all ClusterSummaries currently registered
	// for at least one helm chart.
	//
	// Such map is also used in following scenario to detect helm charts which used to be managed by a ClusterSummary
	// but are not referenced by ClusterSummary anymore:
	// - ClusterProfileSpec is listing an Helm chart to be deployed => this helm release is provisioned in the CAPI Cluster;
	// - without asking for helm chart to be released, the Helm chart is removed from the ClusterProfileSpec;
	// - helm chart needs to be withdrawn if no other ClusterSummary is trying to manage it.
	perClusterChartMap map[string]map[string][]string
}

type HelmReleaseInfo struct {
	Namespace string
	Name      string
}

var (
	managerInstance *instance
	lock            = &sync.Mutex{}
)

const (
	keySeparator = "/"
)

// GetChartManagerInstance return chartManager instance
func GetChartManagerInstance(ctx context.Context, c client.Client) (*instance, error) {
	if managerInstance == nil {
		lock.Lock()
		defer lock.Unlock()
		if managerInstance == nil {
			managerInstance = &instance{
				perClusterChartMap: make(map[string]map[string][]string),
				chartMux:           sync.Mutex{},
			}
			if err := managerInstance.rebuildRegistrations(ctx, c); err != nil {
				managerInstance = nil
				return nil, err
			}
		}
	}

	return managerInstance, nil
}

// RegisterClusterSummaryForCharts for all the HelmCharts ClusterSummary currently references,
// regisetrs ClusterSummary as one requestor to manage each one of those helm chart in a given
// CAPI Cluster.
// Only first ClusterSummary registering for a given Helm release in a given CAPI Cluster is given
// the manager role.
func (m *instance) RegisterClusterSummaryForCharts(clusterSummary *configv1alpha1.ClusterSummary) {
	if len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts) == 0 {
		// Nothing to do
		return
	}

	clusterKey := m.getClusterKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	clusterSummaryKey := m.getClusterSummaryKey(clusterSummary.Name)

	m.chartMux.Lock()
	defer m.chartMux.Unlock()

	for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
		chart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
		releaseKey := m.GetReleaseKey(chart.ReleaseNamespace, chart.ReleaseName)
		m.addClusterEntry(clusterKey)
		m.addReleaseEntry(clusterKey, releaseKey)
		m.addClusterSummaryEntry(clusterKey, releaseKey, clusterSummaryKey)
	}
}

// RemoveStaleRegistrations removes stale registrations.
// It considers all the helm releases the provided clusterSummary is currently registered.
// Any helm release, not referenced anymore by clusterSummary, for which clusterSummary is currently
// registered is considered stale and removed.
func (m *instance) RemoveStaleRegistrations(clusterSummary *configv1alpha1.ClusterSummary) {
	// No-op in DryRun mode
	if clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1alpha1.SyncModeDryRun {
		return
	}

	m.cleanRegistrations(clusterSummary, false)
}

// RemoveAllRegistrations removes all registrations for a clusterSummary.
func (m *instance) RemoveAllRegistrations(clusterSummary *configv1alpha1.ClusterSummary) {
	m.cleanRegistrations(clusterSummary, true)
}

// cleanRegistrations removes ClusterSummary's registrations.
// If removeAll is set to true, all registrations are removed. Otherwise only registration for
// helm releases not referenced anymore are.
func (m *instance) cleanRegistrations(clusterSummary *configv1alpha1.ClusterSummary, removeAll bool) {
	clusterKey := m.getClusterKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	clusterSummaryKey := m.getClusterSummaryKey(clusterSummary.Name)

	currentReferencedReleases := make(map[string]bool)
	if !removeAll {
		for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			chart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			releaseKey := m.GetReleaseKey(chart.ReleaseNamespace, chart.ReleaseName)
			currentReferencedReleases[releaseKey] = true
		}
	}

	m.chartMux.Lock()
	defer m.chartMux.Unlock()

	for releaseKey := range m.perClusterChartMap[clusterKey] {
		if _, ok := currentReferencedReleases[releaseKey]; ok {
			// ClusterSummary is still referencing this helm release.
			// Nothing to do.
			continue
		}
		// If ClusterSummary was previously registered to manage this release, consider this entry stale
		// and remove it.
		for i := range m.perClusterChartMap[clusterKey][releaseKey] {
			if m.perClusterChartMap[clusterKey][releaseKey][i] == clusterSummaryKey {
				// Order is not important. So move the element at index i with last one in order to avoid moving all elements.
				length := len(m.perClusterChartMap[clusterKey][releaseKey])
				m.perClusterChartMap[clusterKey][releaseKey][i] =
					m.perClusterChartMap[clusterKey][releaseKey][length-1]
				m.perClusterChartMap[clusterKey][releaseKey] = m.perClusterChartMap[clusterKey][releaseKey][:length-1]
				break
			}
		}
	}
}

// GetManagedHelmReleases returns info on all the helm releases currently managed by clusterSummary
func (m *instance) GetManagedHelmReleases(clusterSummary *configv1alpha1.ClusterSummary) []HelmReleaseInfo {
	info := make([]HelmReleaseInfo, 0)

	clusterKey := m.getClusterKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	clusterSummaryKey := m.getClusterSummaryKey(clusterSummary.Name)

	m.chartMux.Lock()
	defer m.chartMux.Unlock()

	for releaseKey := range m.perClusterChartMap[clusterKey] {
		for i := range m.perClusterChartMap[clusterKey][releaseKey] {
			if m.perClusterChartMap[clusterKey][releaseKey][i] == clusterSummaryKey {
				if m.isCurrentlyManager(clusterKey, releaseKey, clusterSummaryKey) {
					rInfo := getReleaseInfoFromKey(releaseKey)
					info = append(info, *rInfo)
				}
			}
		}
	}

	return info
}

// CanManageChart returns true if a ClusterSummary can manage the helm chart.
// Only the first ClusterSummary registered for a given helm release in a given cluster can manage it.
func (m *instance) CanManageChart(clusterSummary *configv1alpha1.ClusterSummary,
	chart *configv1alpha1.HelmChart) bool {

	clusterKey := m.getClusterKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	releaseKey := m.GetReleaseKey(chart.ReleaseNamespace, chart.ReleaseName)
	clusterSummaryKey := m.getClusterSummaryKey(clusterSummary.Name)

	m.chartMux.Lock()
	defer m.chartMux.Unlock()

	return m.isCurrentlyManager(clusterKey, releaseKey, clusterSummaryKey)
}

// GetManagerForChart returns the name of the ClusterSummary currently in charge of managing
// helm chart
// Returns an error if no CLusterSummary is currently managing the chart
func (m *instance) GetManagerForChart(clusterNamespace, clusterName string,
	chart *configv1alpha1.HelmChart) (string, error) {

	clusterKey := m.getClusterKey(clusterNamespace, clusterName)
	releaseKey := m.GetReleaseKey(chart.ReleaseNamespace, chart.ReleaseName)

	if _, ok := m.perClusterChartMap[clusterKey]; !ok {
		return "", fmt.Errorf("no ClusterSummary manging helm chart %s (release %s)",
			chart.ChartName, chart.ReleaseName)
	}

	if _, ok := m.perClusterChartMap[clusterKey][releaseKey]; !ok {
		return "", fmt.Errorf("no ClusterSummary manging helm chart %s (release %s)",
			chart.ChartName, chart.ReleaseName)
	}

	if len(m.perClusterChartMap[clusterKey][releaseKey]) == 0 {
		return "", fmt.Errorf("no ClusterSummary manging helm chart %s (release %s)",
			chart.ChartName, chart.ReleaseName)
	}

	return m.perClusterChartMap[clusterKey][releaseKey][0], nil
}

// GetRegisteredClusterSummaries returns all ClusterSummary currently registered for at
// at least one helm chart in the provided CAPI cluster
func (m *instance) GetRegisteredClusterSummaries(clusterNamespace, clusterName string) []string {
	clusterKey := m.getClusterKey(clusterNamespace, clusterName)

	clusterSummaries := make(map[string]bool)

	m.chartMux.Lock()
	defer m.chartMux.Unlock()

	if _, ok := m.perClusterChartMap[clusterKey]; !ok {
		return nil
	}

	for registry := range m.perClusterChartMap[clusterKey] {
		for i := range m.perClusterChartMap[clusterKey][registry] {
			clusterSummaries[m.perClusterChartMap[clusterKey][registry][i]] = true
		}
	}

	result := make([]string, len(clusterSummaries))
	i := 0
	for cs := range clusterSummaries {
		result[i] = cs
		i++
	}

	return result
}

// isCurrentlyManager returns true if clusterSummaryKey is currently the designed manager
// for helm release releaseKey in the CAPI cluster clusterKey
func (m *instance) isCurrentlyManager(clusterKey, releaseKey, clusterSummaryKey string) bool {
	if _, ok := m.perClusterChartMap[clusterKey]; !ok {
		return false
	}

	if _, ok := m.perClusterChartMap[clusterKey][releaseKey]; !ok {
		return false
	}

	if len(m.perClusterChartMap[clusterKey][releaseKey]) > 0 &&
		m.perClusterChartMap[clusterKey][releaseKey][0] == clusterSummaryKey {

		return true
	}

	return false
}

// getClusterKey returns the Key representing a CAPI Cluster
func (m *instance) getClusterKey(clusterNamespace, clusterName string) string {
	return fmt.Sprintf("%s%s%s", clusterNamespace, keySeparator, clusterName)
}

// GetReleaseKey returns the key representing Helm release
func (m *instance) GetReleaseKey(releaseNamespace, releaseName string) string {
	return fmt.Sprintf("%s%s%s", releaseNamespace, keySeparator, releaseName)
}

// getClusterSummaryKey returns the key for a ClusterSummary
func (m *instance) getClusterSummaryKey(clusterSummaryName string) string {
	return clusterSummaryName
}

// getReleaseInfoFromKey returns helm release given a key
func getReleaseInfoFromKey(releaseKey string) *HelmReleaseInfo {
	info := strings.Split(releaseKey, keySeparator)
	return &HelmReleaseInfo{
		Namespace: info[0],
		Name:      info[1],
	}
}

// addClusterEntry adds an entry for clusterKey
func (m *instance) addClusterEntry(clusterKey string) {
	if _, ok := m.perClusterChartMap[clusterKey]; !ok {
		m.perClusterChartMap[clusterKey] = make(map[string][]string)
	}
}

// addReleaseEntry adds an entry for releaseKey
func (m *instance) addReleaseEntry(clusterKey, releaseKey string) {
	if _, ok := m.perClusterChartMap[clusterKey]; !ok {
		m.perClusterChartMap[clusterKey] = make(map[string][]string)
	}

	if _, ok := m.perClusterChartMap[clusterKey][releaseKey]; !ok {
		m.perClusterChartMap[clusterKey][releaseKey] = make([]string, 0)
	}
}

// addClusterSummaryEntry adds an entry for clusterSummary for a given release.
// Method is idempotent. If ClusterSummary is already registered for a given release, it won't be added
// again
func (m *instance) addClusterSummaryEntry(clusterKey, releaseKey, clusterSummaryKey string) {
	if _, ok := m.perClusterChartMap[clusterKey]; !ok {
		m.perClusterChartMap[clusterKey] = make(map[string][]string)
	}

	if _, ok := m.perClusterChartMap[clusterKey][releaseKey]; !ok {
		m.perClusterChartMap[clusterKey][releaseKey] = make([]string, 0)
	}

	if isClusterSummaryAlreadyRegistered(m.perClusterChartMap[clusterKey][releaseKey], clusterSummaryKey) {
		return
	}

	m.perClusterChartMap[clusterKey][releaseKey] = append(m.perClusterChartMap[clusterKey][releaseKey], clusterSummaryKey)
}

// isClusterSummaryAlreadyRegistered returns true if a given ClusterSummary is already present in the slice
func isClusterSummaryAlreadyRegistered(clusterSummaries []string, clusterSummaryKey string) bool {
	for i := range clusterSummaries {
		if clusterSummaries[i] == clusterSummaryKey {
			return true
		}
	}

	return false
}

// rebuildRegistrations rebuilds internal structures to identify ClusterSummaries managing
// helm charts and ClusterSummaries currently just registered but not matching.
// Relies completely on ClusterSummary.Status
func (m *instance) rebuildRegistrations(ctx context.Context, c client.Client) error {
	// Lock here
	m.chartMux.Lock()
	defer m.chartMux.Unlock()

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	err := c.List(ctx, clusterSummaryList)
	if err != nil {
		return err
	}

	for i := range clusterSummaryList.Items {
		cs := &clusterSummaryList.Items[i]
		m.addManagers(cs)
	}

	for i := range clusterSummaryList.Items {
		cs := &clusterSummaryList.Items[i]
		m.addNonManagers(cs)
	}

	return nil
}

// addManagers walks clusterSummary's status and registers it for each helm chart currently managed
func (m *instance) addManagers(clusterSummary *configv1alpha1.ClusterSummary) {
	clusterKey := m.getClusterKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	clusterSummaryKey := m.getClusterSummaryKey(clusterSummary.Name)

	for i := range clusterSummary.Status.HelmReleaseSummaries {
		summary := &clusterSummary.Status.HelmReleaseSummaries[i]
		if summary.Status == configv1alpha1.HelChartStatusManaging {
			releaseKey := m.GetReleaseKey(summary.ReleaseNamespace, summary.ReleaseName)
			m.addClusterEntry(clusterKey)
			m.addReleaseEntry(clusterKey, releaseKey)
			m.addClusterSummaryEntry(clusterKey, releaseKey, clusterSummaryKey)
		}
	}
}

// addNonManagers walks clusterSummary's status and registers it for each helm chart currently not managed
// (not managed because other ClusterSummary is)
func (m *instance) addNonManagers(clusterSummary *configv1alpha1.ClusterSummary) {
	clusterKey := m.getClusterKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
	clusterSummaryKey := m.getClusterSummaryKey(clusterSummary.Name)

	for i := range clusterSummary.Status.HelmReleaseSummaries {
		summary := &clusterSummary.Status.HelmReleaseSummaries[i]
		if summary.Status != configv1alpha1.HelChartStatusManaging {
			releaseKey := m.GetReleaseKey(summary.ReleaseNamespace, summary.ReleaseName)
			m.addClusterEntry(clusterKey)
			m.addReleaseEntry(clusterKey, releaseKey)
			m.addClusterSummaryEntry(clusterKey, releaseKey, clusterSummaryKey)
		}
	}
}
