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
	"sync"

	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// The LicenseManager is responsible for tracking two specific resource types:
// -SveltosCluster instances operating in Pull Mode.
// - ClusterPromotion instances.
// It is important to note that the order in which these resources are processed may
// change if the Add-on Controller restarts. However, if the count of either
// SveltosClusters in Pull Mode or unlicensed ClusterPromotions exceeds the allowed
// limit (X), the configuration is considered invalid.
type LicenseManager struct {
	mu                sync.RWMutex // Read-write mutex to protect access to the clusters slice
	clusters          []types.NamespacedName
	clusterpromotions []types.NamespacedName

	clusterMap          map[types.NamespacedName]struct{}
	clusterPromotionMap map[types.NamespacedName]struct{}
}

var (
	licenseManagerInstance *LicenseManager
)

// NewLicenseManager creates and returns a new initialized LicenseManager.
func NewLicenseManager() *LicenseManager {
	licenseManagerInstance = &LicenseManager{
		clusters:          make([]types.NamespacedName, 0), // Initialize an empty slice
		clusterpromotions: make([]types.NamespacedName, 0), // Initialize an empty slice

		clusterMap:          map[types.NamespacedName]struct{}{},
		clusterPromotionMap: map[types.NamespacedName]struct{}{},
	}

	return licenseManagerInstance
}

func GetLicenseManager() *LicenseManager {
	return licenseManagerInstance
}

// AddCluster adds a SveltosCluster to the manager's collection.
// It ensures that the cluster is not added if it already exists (based on Name and Namespace).
// This method is thread-safe.
func (m *LicenseManager) AddCluster(cluster *libsveltosv1beta1.SveltosCluster) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if the cluster already exists to avoid duplicates
	_, ok := m.clusterMap[types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}]
	if ok {
		return
	}

	m.clusterMap[types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}] = struct{}{}
	m.clusters = append(m.clusters, types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name})
}

// RemoveCluster removes a SveltosCluster from the manager's collection
// identified by its name and namespace.
// This method is thread-safe.
func (m *LicenseManager) RemoveCluster(namespace, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.clusterMap[types.NamespacedName{Namespace: namespace, Name: name}]
	if !ok {
		return
	}

	for i := range m.clusters {
		if m.clusters[i].Name == name && m.clusters[i].Namespace == namespace {
			if i == len(m.clusters)-1 {
				m.clusters = m.clusters[:i]
			} else {
				m.clusters = append(m.clusters[:i], m.clusters[i+1:]...)
			}
			break
		}
	}

	delete(m.clusterMap, types.NamespacedName{Namespace: namespace, Name: name})
}

// IsInTopX checks if a SveltosCluster, identified by its name and namespace,
// is among the first 'x' registered clusters in the manager's collection.
// This method is thread-safe.
func (m *LicenseManager) IsClusterInTopX(namespace, name string, x int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	limit := x
	if limit > len(m.clusters) {
		return true
	}

	// Iterate through the first 'limit' clusters
	for i := range limit {
		if m.clusters[i].Name == name && m.clusters[i].Namespace == namespace {
			return true
		}
	}

	return false
}

// AddClusterPromotion adds a ClusterPromotion to the manager's collection.
// It ensures that the clusterPromotion is not added if it already exists
// (based on Name and Namespace). This method is thread-safe.
func (m *LicenseManager) AddClusterPromotion(clusterpromotion *configv1beta1.ClusterPromotion) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if the clusterPromotion already exists to avoid duplicates
	_, ok := m.clusterPromotionMap[types.NamespacedName{Name: clusterpromotion.Name}]
	if ok {
		return
	}

	m.clusterPromotionMap[types.NamespacedName{Name: clusterpromotion.Name}] = struct{}{}
	m.clusterpromotions = append(m.clusterpromotions, types.NamespacedName{Name: clusterpromotion.Name})
}

// RemoveClusterPromotion removes a ClusterPromotion from the manager's collection
// identified by its name and namespace.
// This method is thread-safe.
func (m *LicenseManager) RemoveClusterPromotion(namespace, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.clusterPromotionMap[types.NamespacedName{Name: name}]
	if !ok {
		return
	}

	for i := range m.clusterpromotions {
		if m.clusterpromotions[i].Name == name && m.clusterpromotions[i].Namespace == namespace {
			if i == len(m.clusterpromotions)-1 {
				m.clusterpromotions = m.clusterpromotions[:i]
			} else {
				m.clusterpromotions = append(m.clusterpromotions[:i], m.clusterpromotions[i+1:]...)
			}
			break
		}
	}

	delete(m.clusterPromotionMap, types.NamespacedName{Name: name})
}

// IsClusterPromotionInTopX checks if a ClusterPromotion, identified by its name and namespace,
// is among the first 'x' registered clusterPromotions in the manager's collection.
// This method is thread-safe.
func (m *LicenseManager) IsClusterPromotionInTopX(namespace, name string, x int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	limit := x
	if limit > len(m.clusterpromotions) {
		return true
	}

	// Iterate through the first 'limit' clusters
	for i := range limit {
		if m.clusterpromotions[i].Name == name && m.clusterpromotions[i].Namespace == namespace {
			return true
		}
	}

	return false
}
