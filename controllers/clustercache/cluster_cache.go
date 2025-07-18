/*
Copyright 2024. projectsveltos.io. All rights reserved.

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

package clustercache

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/secret"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var (
	managerInstance *clusterCache
	lock            = &sync.Mutex{}
)

type clusterCache struct {
	rwMux sync.RWMutex
	// Keeps cache of rest.Config for existing clusters
	configs map[corev1.ObjectReference]*rest.Config

	// key: cluster, value: Secret with kubeconfig
	clusters map[corev1.ObjectReference]*corev1.ObjectReference

	// key: secret, value: set of clusters
	// A secret can potentially contain kubeconfig for one or more clusters
	secrets map[corev1.ObjectReference]*libsveltosset.Set
}

// GetManager return manager instance
func GetManager() *clusterCache {
	if managerInstance == nil {
		lock.Lock()
		defer lock.Unlock()
		if managerInstance == nil {
			managerInstance = &clusterCache{
				configs:  make(map[corev1.ObjectReference]*rest.Config),
				clusters: make(map[corev1.ObjectReference]*corev1.ObjectReference),
				secrets:  make(map[corev1.ObjectReference]*libsveltosset.Set),
				rwMux:    sync.RWMutex{},
			}
		}
	}

	return managerInstance
}

// RemoveCluster removes restConfig cached data for the cluster
func (m *clusterCache) RemoveCluster(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) {

	cluster := getClusterObjectReference(clusterNamespace, clusterName, clusterType)

	m.rwMux.Lock()
	defer m.rwMux.Unlock()

	// Remove from cache the restConfig for this cluster
	delete(m.configs, *cluster)

	if sec, ok := m.clusters[*cluster]; ok {
		m.updateSecretMap(sec, cluster)
	}

	// Do not track this cluster anymore
	delete(m.clusters, *cluster)
}

// RemoveSecret removes any in-memory data related to secret
func (m *clusterCache) RemoveSecret(sec *corev1.ObjectReference) {
	m.rwMux.Lock()
	defer m.rwMux.Unlock()

	v, ok := m.secrets[*sec]
	if !ok {
		return
	}

	clusters := v.Items()
	for i := range clusters {
		delete(m.configs, clusters[i])
		delete(m.clusters, clusters[i])
	}
}

// GetKubernetesRestConfig returns managed cluster restConfig.
// If result is cached, it will be returned immediately. Otherwise it will be built
// by fetching the Secret containing the cluster kubeconfig.
// Admins restConfig are never cached.
func (m *clusterCache) GetKubernetesRestConfig(ctx context.Context, mgmtClient client.Client,
	clusterNamespace, clusterName, adminNamespace, adminName string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) (*rest.Config, error) {

	if adminNamespace != "" || adminName != "" {
		// cluster configs for admins are not cached
		return clusterproxy.GetKubernetesRestConfig(ctx, mgmtClient, clusterNamespace, clusterName,
			adminNamespace, adminName, clusterType, logger)
	}

	m.rwMux.Lock()
	defer m.rwMux.Unlock()

	cluster := getClusterObjectReference(clusterNamespace, clusterName, clusterType)

	config, ok := m.configs[*cluster]
	if ok {
		if config != nil {
			logger.V(logs.LogInfo).Info("remote restConfig cache hit")
		} else {
			logger.V(logs.LogInfo).Info("remote restConfig cache hit: cluster in pull mode")
		}
		return config, nil
	}

	logger.V(logs.LogDebug).Info("remote restConfig cache miss")
	remoteRestConfig, err := clusterproxy.GetKubernetesRestConfig(ctx, mgmtClient, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterType, logger)
	if err != nil {
		return nil, err
	}

	secretInfo, err := getSecretObjectReference(ctx, mgmtClient, clusterNamespace, clusterName, clusterType)
	if err == nil {
		// Either all internal structures are updated or none is
		m.configs[*cluster] = remoteRestConfig
		m.clusters[*cluster] = secretInfo
		v, ok := m.secrets[*secretInfo]
		if !ok {
			v = &libsveltosset.Set{}
		}
		v.Insert(cluster)
		m.secrets[*secretInfo] = v
	}

	return remoteRestConfig, nil
}

func (m *clusterCache) GetKubernetesClient(ctx context.Context, mgmtClient client.Client,
	clusterNamespace, clusterName, adminNamespace, adminName string,
	clusterType libsveltosv1beta1.ClusterType, logger logr.Logger) (client.Client, error) {

	if adminNamespace != "" || adminName != "" {
		// cluster configs for admins are not cached
		return clusterproxy.GetKubernetesClient(ctx, mgmtClient, clusterNamespace, clusterName,
			adminNamespace, adminName, clusterType, logger)
	}

	config, err := m.GetKubernetesRestConfig(ctx, mgmtClient, clusterNamespace, clusterName,
		adminNamespace, adminName, clusterType, logger)
	if err != nil {
		return nil, err
	}

	logger.V(logs.LogVerbose).Info("return new client")
	return client.New(config, client.Options{Scheme: mgmtClient.Scheme()})
}

func (m *clusterCache) StoreRestConfig(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType, config *rest.Config) {

	cluster := getClusterObjectReference(clusterNamespace, clusterName, clusterType)

	m.rwMux.Lock()
	defer m.rwMux.Unlock()

	m.configs[*cluster] = config
}

func (m *clusterCache) updateSecretMap(sec, cluster *corev1.ObjectReference) {
	set, ok := m.secrets[*sec]
	if ok {
		set.Erase(cluster)
		if set.Len() == 0 {
			delete(m.secrets, *sec)
		}
	}
}

func getClusterObjectReference(clusterNamespace, clusterName string,
	clusterType libsveltosv1beta1.ClusterType) *corev1.ObjectReference {

	cluster := &corev1.ObjectReference{
		Namespace:  clusterNamespace,
		Name:       clusterName,
		Kind:       clusterv1.ClusterKind,
		APIVersion: clusterv1.GroupVersion.String(),
	}
	if clusterType == libsveltosv1beta1.ClusterTypeSveltos {
		cluster.Kind = libsveltosv1beta1.SveltosClusterKind
		cluster.APIVersion = libsveltosv1beta1.GroupVersion.String()
	}

	return cluster
}

func getSecretObjectReference(ctx context.Context, mgmtClient client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType) (*corev1.ObjectReference, error) {

	secretKind := "Secret"
	if clusterType == libsveltosv1beta1.ClusterTypeCapi {
		return &corev1.ObjectReference{
			Namespace:  clusterNamespace,
			Name:       getClusterAPISecretName(clusterName),
			Kind:       secretKind,
			APIVersion: corev1.SchemeGroupVersion.String(),
		}, nil
	}

	logger := textlogger.NewLogger(textlogger.NewConfig())
	secretName, _, err := clusterproxy.GetSveltosSecretNameAndKey(ctx, logger, mgmtClient, clusterNamespace, clusterName)
	if err != nil {
		return nil, err
	}
	return &corev1.ObjectReference{
		Namespace:  clusterNamespace,
		Name:       secretName,
		Kind:       secretKind,
		APIVersion: corev1.SchemeGroupVersion.String(),
	}, nil
}

func getClusterAPISecretName(clusterName string) string {
	return fmt.Sprintf("%s-%s", clusterName, secret.Kubeconfig)
}
