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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/controllers"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Cluster utils", func() {
	var namespace string
	var cluster *clusterv1.Cluster
	var sveltosCluster *libsveltosv1alpha1.SveltosCluster

	BeforeEach(func() {
		namespace = "cluster-utils" + randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: clusterv1.ClusterSpec{
				Paused: true,
			},
		}

		sveltosCluster = &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: libsveltosv1alpha1.SveltosClusterSpec{
				Paused: true,
			},
		}
	})

	It("isClusterPaused returns true when Spec.Paused is set to true", func() {
		initObjects := []client.Object{
			cluster, sveltosCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		paused, err := controllers.IsClusterPaused(context.TODO(), c, cluster.Namespace,
			cluster.Name, libsveltosv1alpha1.ClusterTypeCapi)
		Expect(err).To(BeNil())
		Expect(paused).To(BeTrue())

		paused, err = controllers.IsClusterPaused(context.TODO(), c, sveltosCluster.Namespace,
			sveltosCluster.Name, libsveltosv1alpha1.ClusterTypeSveltos)
		Expect(err).To(BeNil())
		Expect(paused).To(BeTrue())
	})

	It("isClusterPaused returns false when Spec.Paused is set to false", func() {
		cluster.Spec.Paused = false
		sveltosCluster.Spec.Paused = false
		initObjects := []client.Object{
			cluster, sveltosCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		paused, err := controllers.IsClusterPaused(context.TODO(), c, cluster.Namespace,
			cluster.Name, libsveltosv1alpha1.ClusterTypeCapi)
		Expect(err).To(BeNil())
		Expect(paused).To(BeFalse())

		paused, err = controllers.IsClusterPaused(context.TODO(), c, sveltosCluster.Namespace,
			sveltosCluster.Name, libsveltosv1alpha1.ClusterTypeSveltos)
		Expect(err).To(BeNil())
		Expect(paused).To(BeFalse())
	})

	It("getSecretData returns kubeconfig data", func() {
		randomData := []byte(randomString())
		sveltosSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: sveltosCluster.Namespace,
				Name:      sveltosCluster.Name + "-sveltos-kubeconfig",
			},
			Data: map[string][]byte{
				"data": randomData,
			},
		}

		capiSecret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + "-kubeconfig",
			},
			Data: map[string][]byte{
				"data": randomData,
			},
		}

		initObjects := []client.Object{
			sveltosCluster,
			cluster,
			&sveltosSecret,
			&capiSecret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		data, err := controllers.GetSecretData(context.TODO(), c, cluster.Namespace, cluster.Name,
			libsveltosv1alpha1.ClusterTypeCapi, klogr.New())
		Expect(err).To(BeNil())
		Expect(data).To(Equal(randomData))

		data, err = controllers.GetSecretData(context.TODO(), c, sveltosCluster.Namespace, sveltosCluster.Name,
			libsveltosv1alpha1.ClusterTypeSveltos, klogr.New())
		Expect(err).To(BeNil())
		Expect(data).To(Equal(randomData))
	})
})
