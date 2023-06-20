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

package constraints

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

var (
	getManagerLock  = &sync.Mutex{}
	managerInstance *manager
)

type manager struct {
	log logr.Logger
	client.Client
	config *rest.Config

	reEvaluate atomic.Value
	ready      atomic.Value

	muMap *sync.RWMutex
	// openAPIValidations contains all openapi validations for a given cluster
	openAPIValidations map[string]map[string][]byte

	// list of clusters ready to be programmed, i.e., addon constraints for each of
	// those clusters have been loaded
	clusters map[string]bool

	// interval is the interval at which constraints are re-evaluated assuming
	// at least an AddonConstraint instance has changed since last evaluation
	interval time.Duration
}

// InitializeManager initializes a manager
func InitializeManager(ctx context.Context, l logr.Logger, config *rest.Config, c client.Client,
	intervalInSecond uint) {

	if managerInstance == nil {
		getManagerLock.Lock()
		defer getManagerLock.Unlock()
		if managerInstance == nil {
			l.V(logs.LogInfo).Info(fmt.Sprintf("Creating manager now. Interval (in seconds): %d", intervalInSecond))
			managerInstance = &manager{log: l, Client: c, config: config}
			managerInstance.interval = time.Duration(intervalInSecond) * time.Second
			managerInstance.ready.Store(false)
			managerInstance.reEvaluate.Store(true)

			managerInstance.muMap = &sync.RWMutex{}
			managerInstance.openAPIValidations = make(map[string]map[string][]byte)
			managerInstance.clusters = make(map[string]bool)

			go watchAddonConstraint(ctx, managerInstance.config, managerInstance.log)

			go managerInstance.evaluate(ctx)
		}
	}
}

// GetManager returns the manager instance implementing the ClassifierInterface.
// Returns nil if manager has not been initialized yet
func GetManager() *manager {
	if managerInstance != nil {
		return managerInstance
	}
	return nil
}

// IsReady returns true if manager is ready, i.e, all AddonConstraints have
// been evaluated since pod started
func (m *manager) IsReady() bool {
	return m.ready.Load().(bool)
}

// GetClusterOpenapiPolicies returns current openAPI policies for a given cluster.
// Returns an error if manager has not had a chance to evaluate addonConstraints yet
func (m *manager) GetClusterOpenapiPolicies(clusterNamespace, clusterName string,
	clusterType *libsveltosv1alpha1.ClusterType) (map[string][]byte, error) {

	logger := m.log.WithValues("cluster",
		fmt.Sprintf("%s:%s/%s", string(*clusterType), clusterNamespace, clusterName))

	if !m.ready.Load().(bool) {
		errMsg := "not ready yet : openAPI policies not processed yet"
		logger.V(logs.LogInfo).Info(errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	if !m.canAddonBeDeployed(clusterNamespace, clusterName, clusterType) {
		errMsg := "not ready yet: constraints for this cluster not loaded yet. addons cannot be deployed yet"
		logger.V(logs.LogInfo).Info(errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	clusterKey := libsveltosv1alpha1.GetClusterLabel(clusterNamespace, clusterName, clusterType)

	m.muMap.RLock()
	defer m.muMap.RUnlock()
	return m.openAPIValidations[clusterKey], nil
}

func (m *manager) canAddonBeDeployed(clusterNamespace, clusterName string,
	clusterType *libsveltosv1alpha1.ClusterType) bool {

	clusterKey := m.getClusterKey(clusterNamespace, clusterName, clusterType)
	m.muMap.RLock()
	defer m.muMap.RUnlock()
	return m.clusters[clusterKey]
}

func (m *manager) setReEvaluate() {
	m.reEvaluate.Store(true)
}

// evaluate gets all AddonConstraint instances and build openapi validations
// per cluster
func (m *manager) evaluate(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			m.log.V(logs.LogDebug).Info("Context canceled. Exiting goroutine.")
			return
		default:
			m.log.V(logs.LogDebug).Info("Evaluating AddonConstraint")

			if err := m.reEvaluateAddonConstraints(ctx); err == nil {
				m.ready.Store(true)
				m.reEvaluate.Store(false)
			}

			m.log.V(logs.LogDebug).Info("Evaluating Clusters")
			m.reEvaluateClusters(ctx)

			// Sleep before next evaluation
			time.Sleep(m.interval)
		}
	}
}

func (m *manager) reEvaluateAddonConstraints(ctx context.Context) error {
	// Cluster: addonConstraint + key: openapi validation
	currentMap := make(map[string]map[string][]byte)

	addonConstrains := &libsveltosv1alpha1.AddonConstraintList{}
	err := m.Client.List(ctx, addonConstrains)
	if err != nil {
		m.log.V(logs.LogInfo).Info(fmt.Sprintf("failed to get addonConstraints: %v", err))
		return err
	}

	for i := range addonConstrains.Items {
		if !addonConstrains.Items[i].DeletionTimestamp.IsZero() {
			// Skip deleted addonConstraint instances
			continue
		}
		m.processAddConstraint(currentMap, &addonConstrains.Items[i])
	}

	m.muMap.Lock()
	defer m.muMap.Unlock()
	// Update per cluster openapi validation map
	managerInstance.openAPIValidations = currentMap

	return nil
}

func (m *manager) reEvaluateClusters(ctx context.Context) {
	currentClusterMap := make(map[string]bool)

	currentClusterMap = m.updateCurrentClusters(ctx, currentClusterMap)
	currentClusterMap = m.updateCurrentSveltosClusters(ctx, currentClusterMap)

	m.muMap.Lock()
	defer m.muMap.Unlock()
	m.clusters = currentClusterMap
}

func (m *manager) updateCurrentClusters(ctx context.Context, currentClusterMap map[string]bool) map[string]bool {
	clusters := &clusterv1.ClusterList{}
	if err := m.Client.List(ctx, clusters); err != nil {
		m.log.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
		return currentClusterMap
	}

	clusterType := libsveltosv1alpha1.ClusterTypeCapi
	for i := range clusters.Items {
		if m.isClusterAnnotated(&clusters.Items[i]) {
			key := m.getClusterKey(clusters.Items[i].Namespace, clusters.Items[i].Name, &clusterType)
			currentClusterMap[key] = true
		}
	}
	return currentClusterMap
}

func (m *manager) updateCurrentSveltosClusters(ctx context.Context, currentClusterMap map[string]bool) map[string]bool {
	clusters := &libsveltosv1alpha1.SveltosClusterList{}
	if err := m.Client.List(ctx, clusters); err != nil {
		m.log.V(logs.LogInfo).Info(fmt.Sprintf("failed to get clusters: %v", err))
		return currentClusterMap
	}

	clusterType := libsveltosv1alpha1.ClusterTypeSveltos
	for i := range clusters.Items {
		if m.isClusterAnnotated(&clusters.Items[i]) {
			key := m.getClusterKey(clusters.Items[i].Namespace, clusters.Items[i].Name, &clusterType)
			currentClusterMap[key] = true
		}
	}

	return currentClusterMap
}

// isClusterAnnotated returns true if cluster has "addon-constraints-ready"
// annotation.
func (m *manager) isClusterAnnotated(cluster client.Object) bool {
	annotations := cluster.GetAnnotations()
	if annotations == nil {
		return false
	}
	// This annotation is added by addon-constraint controller.
	// It indicates addon-constraint controller has loaded all
	// AddonConstraint instances matching this cluster when cluster was
	// first discovered
	_, ok := annotations[libsveltosv1alpha1.GetClusterAnnotation()]
	return ok
}

func (m *manager) processAddConstraint(currentMap map[string]map[string][]byte,
	addonConstrain *libsveltosv1alpha1.AddonConstraint) {

	policies := m.getOpenapiPolicies(addonConstrain)

	for i := range addonConstrain.Status.MatchingClusterRefs {
		cluster := &addonConstrain.Status.MatchingClusterRefs[i]
		clusterType := clusterproxy.GetClusterType(cluster)
		clusterKey := libsveltosv1alpha1.GetClusterLabel(cluster.Namespace, cluster.Name, &clusterType)
		if _, ok := currentMap[clusterKey]; !ok {
			currentMap[clusterKey] = map[string][]byte{}
		}
		m.appendMap(currentMap[clusterKey], policies)
	}
}

// getOpenapiPolicies returns all openAPI policies contained in the AddonConstraint
func (m *manager) getOpenapiPolicies(addonConstrain *libsveltosv1alpha1.AddonConstraint) map[string][]byte {
	policies := make(map[string][]byte)
	for policyKey := range addonConstrain.Status.OpenapiValidations {
		key := fmt.Sprintf("%s-%s", addonConstrain.Name, policyKey)
		policies[key] = addonConstrain.Status.OpenapiValidations[policyKey]
	}

	return policies
}

// appendMap appends policies map to clusterPolicyMap
func (m *manager) appendMap(clusterPolicyMap, policies map[string][]byte) {
	for k := range policies {
		clusterPolicyMap[k] = policies[k]
	}
}

func (m *manager) getClusterKey(clusterNamespace, clusterName string,
	clusterType *libsveltosv1alpha1.ClusterType) string {

	return fmt.Sprintf("%s:%s/%s", string(*clusterType), clusterNamespace, clusterName)
}