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

package controllers

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	managementClusterClient client.Client
	managementClusterConfig *rest.Config
	driftdetectionConfigMap string
	luaConfigMap            string
	capiOnboardAnnotation   string
	driftDetectionRegistry  string
	agentInMgmtCluster      bool
	luaCallStackSize        int
	luaRegistrySize         int
)

func SetManagementClusterAccess(c client.Client, config *rest.Config) {
	managementClusterClient = c
	managementClusterConfig = config
}

func SetDriftdetectionConfigMap(name string) {
	driftdetectionConfigMap = name
}

func SetLuaConfigMap(name string) {
	luaConfigMap = name
}

func SetLuaCallStackSize(callStackSize int) {
	luaCallStackSize = callStackSize
}

func SetLuaRegistrySize(registrySize int) {
	luaRegistrySize = registrySize
}

func SetCAPIOnboardAnnotation(key string) {
	capiOnboardAnnotation = key
}

func SetDriftDetectionRegistry(reg string) {
	driftDetectionRegistry = reg
}

func SetAgentInMgmtCluster(isInMgmtCluster bool) {
	agentInMgmtCluster = isInMgmtCluster
}

func getManagementClusterConfig() *rest.Config {
	return managementClusterConfig
}

func getManagementClusterClient() client.Client {
	return managementClusterClient
}

func getDriftDetectionConfigMap() string {
	return driftdetectionConfigMap
}

func getLuaConfigMap() string {
	return luaConfigMap
}

func getLuaCallStackSize() int {
	return luaCallStackSize
}

func getLuaRegistrySize() int {
	return luaRegistrySize
}

func getCAPIOnboardAnnotation() string {
	return capiOnboardAnnotation
}

func getDriftDetectionRegistry() string {
	return driftDetectionRegistry
}

func getAgentInMgmtCluster() bool {
	return agentInMgmtCluster
}

func collectDriftDetectionConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	c := getManagementClusterClient()
	configMap := &corev1.ConfigMap{}

	err := c.Get(ctx, types.NamespacedName{Namespace: projectsveltos, Name: getDriftDetectionConfigMap()},
		configMap)
	if err != nil {
		return nil, err
	}

	return configMap, nil
}

func collectLuaConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	c := getManagementClusterClient()
	configMap := &corev1.ConfigMap{}

	err := c.Get(ctx, types.NamespacedName{Namespace: projectsveltos, Name: getLuaConfigMap()},
		configMap)
	if err != nil {
		return nil, err
	}

	return configMap, nil
}
