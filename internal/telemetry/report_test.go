/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

package telemetry

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nodeWithLabel(key, value string) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{key: value}}}
}

func nodeWithProviderID(providerID string) corev1.Node {
	return corev1.Node{Spec: corev1.NodeSpec{ProviderID: providerID}}
}

func nodeWithKubeletVersion(version string) corev1.Node {
	return corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: version}}}
}

func TestDetectClusterProvider(t *testing.T) {
	tests := []struct {
		name string
		node corev1.Node
		want string
	}{
		{"eks label", nodeWithLabel("eks.amazonaws.com/nodegroup", "ng-1"), providerEKS},
		{"gke label", nodeWithLabel("cloud.google.com/gke-nodepool", "pool-1"), providerGKE},
		{"aks label", nodeWithLabel("kubernetes.azure.com/agentpool", "pool-1"), providerAKS},
		{"openshift label", nodeWithLabel("node.openshift.io/os_id", "rhcos"), "openshift"},
		{"aws providerID", nodeWithProviderID("aws:///us-east-1a/i-1234"), providerEKS},
		{"gce providerID", nodeWithProviderID("gce://project/zone/instance"), providerGKE},
		{"azure providerID", nodeWithProviderID("azure:///subscriptions/x/vm"), providerAKS},
		{"vsphere providerID", nodeWithProviderID("vsphere://42000000-0000-0000-0000-000000000000"), "vsphere"},
		{"k3s kubelet version", nodeWithKubeletVersion("v1.28.2+k3s1"), "k3s"},
		{"k0s kubelet version", nodeWithKubeletVersion("v1.28.2-k0s.1"), "k0s"},
		{"rke2 kubelet version", nodeWithKubeletVersion("v1.28.2+rke2r1"), "rke2"},
		{"rke kubelet version", nodeWithKubeletVersion("v1.28.2+rke1"), "rke"},
		{"eks kubelet version", nodeWithKubeletVersion("v1.28.2-eks-1234abc"), providerEKS},
		{"gke kubelet version", nodeWithKubeletVersion("v1.28.2-gke.100"), providerGKE},
		{"kind hostname fallback", nodeWithLabel("kubernetes.io/hostname", "kind-control-plane"), "kind"},
		{"unrecognized node", corev1.Node{}, "unknown"},
		{"label takes precedence over conflicting providerID", func() corev1.Node {
			n := nodeWithLabel("cloud.google.com/gke-nodepool", "pool-1")
			n.Spec.ProviderID = "aws:///us-east-1a/i-1234"
			return n
		}(), providerGKE},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectClusterProvider([]corev1.Node{tt.node}); got != tt.want {
				t.Errorf("detectClusterProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}
