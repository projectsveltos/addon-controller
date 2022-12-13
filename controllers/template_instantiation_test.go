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

package controllers_test

import (
	"context"
	"fmt"

	"encoding/base64"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/controllers"
)

var _ = Describe("Template instantiation", func() {
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = "template-values" + randomString()

		By("Create the cluster's namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, ns)).To(Succeed())

		By("Create the cluster")
		replicas := int32(3)
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
			Spec: clusterv1.ClusterSpec{
				Topology: &clusterv1.Topology{
					Version: "v1.22.2",
					ControlPlane: clusterv1.ControlPlaneTopology{
						Replicas: &replicas,
					},
				},
				ClusterNetwork: &clusterv1.ClusterNetwork{
					Pods: &clusterv1.NetworkRanges{
						CIDRBlocks: []string{"192.168.10.1", "192.169.10.1"},
					},
				},
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, cluster)).To(Succeed())
	})

	It("instantiateTemplateValues returns correct values (metadata section)", func() {
		values := `valuesTemplate: |
    controller:
      name: "{{ .Cluster.Name }}-test"`

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			configv1alpha1.ClusterTypeCapi, cluster.Namespace, cluster.Name, randomString(), values, nil, klogr.New())
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(fmt.Sprintf("%s-test", cluster.Name)))
	})

	It("instantiateTemplateValues returns correct values (spec section)", func() {
		values := `valuesTemplate: |
    controller:
      name: "{{ .Cluster.Name }}-test"
	  cidrs: {{ index .Cluster.Spec.ClusterNetwork.Pods.CIDRBlocks 0 }} 
	  `

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			configv1alpha1.ClusterTypeCapi, cluster.Namespace, cluster.Name, randomString(), values, nil, klogr.New())
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(fmt.Sprintf("%s-test", cluster.Name)))
		Expect(result).To(ContainSubstring(cluster.Spec.ClusterNetwork.Pods.CIDRBlocks[0]))
	})

	It("instantiateTemplateValues returns correct values (spec section)", func() {
		namespace := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, ns)).To(Succeed())

		pwd := randomString()
		sEnc := base64.StdEncoding.EncodeToString([]byte(pwd))

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string][]byte{
				"password": []byte(sEnc),
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, secret)).To(Succeed())

		values := `valuesTemplate: |
    password: "{{ printf "%s" .SecretRef.Data.password | b64dec }}"`

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			configv1alpha1.ClusterTypeCapi, cluster.Namespace, cluster.Name, randomString(), values,
			&corev1.ObjectReference{Namespace: secret.Namespace, Name: secret.Name}, klogr.New())
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(pwd))
	})
})
