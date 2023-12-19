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

package compliances

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	notReadyErr = "not ready yet: compliances for this cluster not loaded yet. addons cannot be deployed yet"
)

var (
	getManagerLock  = &sync.Mutex{}
	managerInstance *manager
)

type manager struct {
	log logr.Logger
	client.Client
	config *rest.Config

	reEvaluate  atomic.Value
	ready       atomic.Value
	capiPresent bool

	muMap *sync.RWMutex
	// luaValidations contains all lua validations for a given cluster
	luaValidations map[string]map[string][]byte

	// list of clusters ready to be programmed, i.e., addon compliances for each of
	// those clusters have been loaded
	clusters map[string]bool

	// interval is the interval at which compliances are re-evaluated assuming
	// at least an AddonCompliance instance has changed since last evaluation
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
			managerInstance.luaValidations = make(map[string]map[string][]byte)
			managerInstance.clusters = make(map[string]bool)

			go watchAddonCompliance(ctx, managerInstance.config, managerInstance.log)

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

// IsReady returns true if manager is ready, i.e, all AddonCompliances have
// been evaluated since pod started
func (m *manager) IsReady() bool {
	return m.ready.Load().(bool)
}

// GetClusterLuaPolicies returns current lua policies for a given cluster.
// Returns an error if manager has not had a chance to evaluate addonCompliances yet
func (m *manager) GetClusterLuaPolicies(clusterNamespace, clusterName string,
	clusterType *libsveltosv1alpha1.ClusterType) (map[string][]byte, error) {

	logger := m.log.WithValues("cluster",
		fmt.Sprintf("%s:%s/%s", string(*clusterType), clusterNamespace, clusterName))

	if !m.ready.Load().(bool) {
		errMsg := "not ready yet : lua policies not processed yet"
		logger.V(logs.LogInfo).Info(errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	if !m.canAddonBeDeployed(clusterNamespace, clusterName, clusterType) {
		logger.V(logs.LogInfo).Info(notReadyErr)
		return nil, fmt.Errorf("%s", notReadyErr)
	}

	clusterKey := libsveltosv1alpha1.GetClusterLabel(clusterNamespace, clusterName, clusterType)

	m.muMap.RLock()
	defer m.muMap.RUnlock()
	return m.luaValidations[clusterKey], nil
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

// evaluate gets all AddonCompliance instances and build openapi validations
// per cluster
func (m *manager) evaluate(ctx context.Context) {
	for {
		var err error
		m.capiPresent, err = isCAPIInstalled(ctx, m.Client)
		if err != nil {
			// Sleep before next evaluation
			time.Sleep(m.interval)
			continue
		}

		select {
		case <-ctx.Done():
			m.log.V(logs.LogDebug).Info("Context canceled. Exiting goroutine.")
			return
		default:
			m.log.V(logs.LogDebug).Info("Evaluating AddonCompliance")

			if err := m.reEvaluateAddonCompliances(ctx); err == nil {
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

func (m *manager) reEvaluateAddonCompliances(ctx context.Context) error {
	// Cluster: addonCompliance + key: openapi validation
	currentLuaMap := make(map[string]map[string][]byte)

	addonCompliances := &libsveltosv1alpha1.AddonComplianceList{}
	err := m.Client.List(ctx, addonCompliances)
	if err != nil {
		m.log.V(logs.LogInfo).Info(fmt.Sprintf("failed to get addonCompliances: %v", err))
		return err
	}

	for i := range addonCompliances.Items {
		if !addonCompliances.Items[i].DeletionTimestamp.IsZero() {
			// Skip deleted addonCompliance instances
			continue
		}
		m.processAddonCompliancePolicies(currentLuaMap, &addonCompliances.Items[i])
	}

	m.muMap.Lock()
	defer m.muMap.Unlock()
	// Update per cluster openapi validation map
	managerInstance.luaValidations = currentLuaMap

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
	if !m.capiPresent {
		return currentClusterMap
	}

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

// isClusterAnnotated returns true if cluster has "addon-compliances-ready"
// annotation.
func (m *manager) isClusterAnnotated(cluster client.Object) bool {
	annotations := cluster.GetAnnotations()
	if annotations == nil {
		return false
	}
	// This annotation is added by addon-constraint controller.
	// It indicates addon-constraint controller has loaded all
	// AddonCompliance instances matching this cluster when cluster was
	// first discovered
	_, ok := annotations[libsveltosv1alpha1.GetClusterAnnotation()]
	return ok
}

func (m *manager) processAddonCompliancePolicies(currentLuaMap map[string]map[string][]byte,
	addonCompliance *libsveltosv1alpha1.AddonCompliance) {

	m.processAddonComplianceLuaPolicies(currentLuaMap, addonCompliance)
}

func (m *manager) processAddonComplianceLuaPolicies(currentLuaMap map[string]map[string][]byte,
	addonCompliance *libsveltosv1alpha1.AddonCompliance) {

	policies := m.getLuaPolicies(addonCompliance)

	for i := range addonCompliance.Status.MatchingClusterRefs {
		cluster := &addonCompliance.Status.MatchingClusterRefs[i]
		clusterType := clusterproxy.GetClusterType(cluster)
		clusterKey := libsveltosv1alpha1.GetClusterLabel(cluster.Namespace, cluster.Name, &clusterType)
		if _, ok := currentLuaMap[clusterKey]; !ok {
			currentLuaMap[clusterKey] = map[string][]byte{}
		}
		m.appendMap(currentLuaMap[clusterKey], policies)
	}
}

// getOpenapiPolicies returns all openAPI policies contained in the AddonCompliance
func (m *manager) getOpenapiPolicies(addonConstrain *libsveltosv1alpha1.AddonCompliance) map[string][]byte {
	policies := make(map[string][]byte)
	for policyKey := range addonConstrain.Status.OpenapiValidations {
		key := fmt.Sprintf("%s-%s", addonConstrain.Name, policyKey)
		policies[key] = addonConstrain.Status.OpenapiValidations[policyKey]
	}

	return policies
}

// getLuaPolicies returns all lua policies contained in the AddonCompliance
func (m *manager) getLuaPolicies(addonConstrain *libsveltosv1alpha1.AddonCompliance) map[string][]byte {
	policies := make(map[string][]byte)
	for policyKey := range addonConstrain.Status.LuaValidations {
		key := fmt.Sprintf("%s-%s", addonConstrain.Name, policyKey)
		policies[key] = addonConstrain.Status.LuaValidations[policyKey]
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

// isCAPIInstalled returns true if CAPI is installed, false otherwise
func isCAPIInstalled(ctx context.Context, c client.Client) (bool, error) {
	clusterCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "clusters.cluster.x-k8s.io"}, clusterCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}
