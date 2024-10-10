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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

func (m *clusterCache) GetConfigFromMap(cluster *corev1.ObjectReference) *rest.Config {
	return m.configs[*cluster]
}

func (m *clusterCache) GetSecretForCluster(cluster *corev1.ObjectReference) *corev1.ObjectReference {
	return m.clusters[*cluster]
}

func (m *clusterCache) GetClusterFromSecret(secret *corev1.ObjectReference) *corev1.ObjectReference {
	set := m.secrets[*secret]
	if set == nil {
		return nil
	}

	if set.Len() == 0 {
		return nil
	}

	items := set.Items()
	return &items[0]
}
