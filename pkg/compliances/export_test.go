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
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

var (
	GetOpenapiPolicies         = (*manager).getOpenapiPolicies
	ReEvaluateAddonCompliances = (*manager).reEvaluateAddonCompliances
	ReEvaluateClusters         = (*manager).reEvaluateClusters
	CanAddonBeDeployed         = (*manager).canAddonBeDeployed

	WatchAddonCompliance = watchAddonCompliance
)

func InitializeManagerWithSkip(ctx context.Context, l logr.Logger, config *rest.Config, c client.Client,
	intervalInSecond uint) {

	// Used only for testing purposes (so to avoid using testEnv when not required by test)
	if managerInstance == nil {
		getManagerLock.Lock()
		defer getManagerLock.Unlock()
		if managerInstance == nil {
			l.V(logs.LogInfo).Info(fmt.Sprintf("Creating manager now. Interval (in seconds): %d", intervalInSecond))
			managerInstance = &manager{log: l, Client: c, config: config}
			managerInstance.interval = time.Duration(intervalInSecond) * time.Second
			managerInstance.ready.Store(true)
			managerInstance.reEvaluate.Store(true)

			managerInstance.muMap = &sync.RWMutex{}
			managerInstance.luaValidations = make(map[string]map[string][]byte)
			managerInstance.clusters = make(map[string]bool)

			managerInstance.capiPresent, _ = isCAPIInstalled(ctx, c)
		}
	}
}

func (m *manager) GetReEvaluate() bool {
	m.muMap.RLock()
	defer m.muMap.RUnlock()

	return m.reEvaluate.Load().(bool)
}

func (m *manager) SetReEvaluate(v bool) {
	m.muMap.RLock()
	defer m.muMap.RUnlock()

	m.reEvaluate.Store(v)
}

func (m *manager) MarkClusterReady(clusterNamespace, clusterName string, clusterType *libsveltosv1alpha1.ClusterType) {
	key := m.getClusterKey(clusterNamespace, clusterName, clusterType)
	m.clusters[key] = true
}

func Reset() {
	managerInstance = nil
}
