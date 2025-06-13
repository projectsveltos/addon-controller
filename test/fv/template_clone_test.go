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

package fv_test

import (
	"context"
	"encoding/base64"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Template with copy", func() {
	const (
		namePrefix = "template-copy"
	)

	It("Template copy function", Label("FV", "PULLMODE", "EXTENDED"), func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
		Byf("Create namespace %s where configuration is stored", ns.Name)
		Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

		username := base64.StdEncoding.EncodeToString([]byte("username"))
		pwd := base64.StdEncoding.EncodeToString([]byte("password"))
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      namePrefix + randomString(),
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				"username": []byte(username),
				"pwd":      []byte(pwd),
			},
		}

		Expect(k8sClient.Create(context.TODO(), secret)).To(Succeed())

		Byf("Add configMap containing a template policy with copy function.")
		configMap := createConfigMapWithPolicy(ns.Name, namePrefix+randomString(), `{{ (copy "ExternalSecret") }}`)
		configMap.Annotations = map[string]string{
			libsveltosv1beta1.PolicyTemplateAnnotation: "ok",
		}
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		// ClusterProfile fetches the Secret created above and reference it with ExternalSecret ID
		clusterProfile.Spec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
			{
				Resource: corev1.ObjectReference{
					Kind:       "Secret",
					APIVersion: "v1",
					Namespace:  secret.Namespace,
					Name:       secret.Name,
				},
				Identifier: "ExternalSecret",
			},
		}
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
			clusterProfile.Name, &clusterProfile.Spec,
			kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for resources", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name,
			libsveltosv1beta1.FeatureResources)

		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verify Secret is copied to the managed cluster")
		copiedSecret := &corev1.Secret{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name},
			copiedSecret)).To(Succeed())

		Expect(reflect.DeepEqual(copiedSecret.Data, secret.Data)).To(BeTrue())

		deleteClusterProfile(clusterProfile)

		currentNs := &corev1.Namespace{}
		Expect(workloadClient.Get(context.TODO(),
			types.NamespacedName{Name: ns.Name},
			currentNs)).To(Succeed())
		Expect(k8sClient.Delete(context.TODO(), currentNs)).To(Succeed())
	})
})
