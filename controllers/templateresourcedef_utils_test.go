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

package controllers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("TemplateResourceDef utils ", func() {
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					randomString(): randomString(),
				},
			},
		}
	})

	It("GetTemplateResourceName returns the correct name (uses ClusterNamespace and ClusterName)", func() {
		ref := &configv1alpha1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: "{{ .ClusterNamespace }}-{{ .ClusterName }}",
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceName(clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(cluster.Namespace + "-" + cluster.Name))
	})

	It("GetTemplateResourceNamespace returns the correct namespace (uses Cluster)", func() {
		ref := &configv1alpha1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: "{{ .Cluster.metadata.namespace }}-{{ .Cluster.metadata.name }}",
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceName(clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(cluster.Namespace + "-" + cluster.Name))
	})

	It("GetTemplateResourceNamespace returns the correct namespace", func() {
		ref := &configv1alpha1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: randomString(),
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		value := controllers.GetTemplateResourceNamespace(clusterSummary, ref)
		Expect(value).To(Equal(cluster.Namespace))

		ref.Resource.Namespace = randomString()
		value = controllers.GetTemplateResourceNamespace(clusterSummary, ref)
		Expect(value).To(Equal(ref.Resource.Namespace))
	})

})
