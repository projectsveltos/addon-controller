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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
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
      name: "{{ .Cluster.metadata.name }}-test"`

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			libsveltosv1beta1.ClusterTypeCapi, cluster.Namespace, cluster.Name, randomString(), values,
			nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(fmt.Sprintf("%s-test", cluster.Name)))
	})

	It("instantiateTemplateValues returns correct values (spec section)", func() {
		values := `valuesTemplate: |
    controller:
      name: "{{ .Cluster.metadata.name }}-test"
	  cidrs: {{ index .Cluster.spec.clusterNetwork.pods.cidrBlocks 0 }} 
	  `

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			libsveltosv1beta1.ClusterTypeCapi, cluster.Namespace, cluster.Name, randomString(), values,
			nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(fmt.Sprintf("%s-test", cluster.Name)))
		Expect(result).To(ContainSubstring(cluster.Spec.ClusterNetwork.Pods.CIDRBlocks[0]))
	})

	//nolint: dupl // Not refactored intentionally. See next test for the
	// explanation.
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

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string][]byte{
				"password": []byte(pwd),
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, secret)).To(Succeed())

		values := `{{ $pwd := printf "%s" (index .MgmtResources "Secret").data.password }}
valuesTemplate: |
		password: "{{b64dec $pwd}}"`

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(secret)
		Expect(err).To(BeNil())

		var u unstructured.Unstructured
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Secret": &u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			libsveltosv1beta1.ClusterTypeCapi, cluster.Namespace, cluster.Name, randomString(), values,
			mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(pwd))
	})

	//nolint: dupl // This is a copy of the previous test with getResource
	// instead of index. We want the old behavior to be tested as well. We could
	// refactor this to a common function. But, that would make the tests hard
	// to follow and be evolved independently.
	It("instantiateTemplateValues returns correct values using getResource (spec section)", func() {
		namespace := randomString()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, ns)).To(Succeed())

		pwd := randomString()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string][]byte{
				"password": []byte(pwd),
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, secret)).To(Succeed())

		values := `{{ $pwd := printf "%s" (getResource "Secret").data.password }}
valuesTemplate: |
		password: "{{b64dec $pwd}}"`

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(secret)
		Expect(err).To(BeNil())

		var u unstructured.Unstructured
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Secret": &u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			libsveltosv1beta1.ClusterTypeCapi, cluster.Namespace, cluster.Name, randomString(), values,
			mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(pwd))
	})
})
