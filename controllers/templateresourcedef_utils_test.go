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
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("TemplateResourceDef utils ", func() {
	var cluster *clusterv1.Cluster
	var namespace string
	var labelKey, labelValue string
	var annotationKey, annotationValue string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = randomString()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		labelKey = randomString()
		labelValue = randomString()
		annotationKey = randomString()
		annotationValue = randomString()
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					labelKey: labelValue,
				},
				Annotations: map[string]string{
					annotationKey: annotationValue,
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())
	})

	It("GetTemplateResourceName returns the correct name (uses ClusterNamespace and ClusterName)", func() {
		ref := &configv1beta1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: "{{ .ClusterNamespace }}-{{ .ClusterName }}",
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceName(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(cluster.Namespace + "-" + cluster.Name))
	})

	It("GetTemplateResourceName returns the correct name (uses cluster label)", func() {
		name := fmt.Sprintf("{{ index .Cluster.metadata.labels %q }}-{{ index .Cluster.metadata.annotations %q }}",
			labelKey, annotationKey)
		ref := &configv1beta1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: name,
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceName(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(fmt.Sprintf("%s-%s", labelValue, annotationValue)))
	})

	It("GetTemplateResourceNamespace returns the correct namespace (uses Cluster)", func() {
		ref := &configv1beta1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: "{{ .Cluster.metadata.namespace }}-{{ .Cluster.metadata.name }}",
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceName(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(cluster.Namespace + "-" + cluster.Name))
	})

	It("GetTemplateResourceNamespace returns the correct namespace", func() {
		ref := &configv1beta1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: randomString(),
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceNamespace(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(cluster.Namespace))

		ref.Resource.Namespace = randomString()
		value, err = controllers.GetTemplateResourceNamespace(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(ref.Resource.Namespace))
	})

	It("GetTemplateResourceNamespace returns the correct namespace (template version)", func() {
		ref := &configv1beta1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name:      randomString(),
				Namespace: "{{ .Cluster.metadata.namespace }}-foo",
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceNamespace(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(cluster.Namespace + "-foo"))

		ref.Resource.Namespace = randomString()
		value, err = controllers.GetTemplateResourceNamespace(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(ref.Resource.Namespace))
	})

	It("GetTemplateResourceNamespace returns the correct namespace (template version with labels)", func() {
		ref := &configv1beta1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name:      randomString(),
				Namespace: fmt.Sprintf("{{ index .Cluster.metadata.labels %q }}", labelKey),
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetTemplateResourceNamespace(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(labelValue))

		ref.Resource.Namespace = randomString()
		value, err = controllers.GetTemplateResourceNamespace(context.TODO(), clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(ref.Resource.Namespace))
	})
})
