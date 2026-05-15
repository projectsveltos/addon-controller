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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

var _ = Describe("collectTemplateResourceRefs", func() {
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var ns *corev1.Namespace
	var nsName string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		nsName = randomString()
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: nsName,
			},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: nsName,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: nsName,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
	})

	It("returns a descriptive NotFound error for a required missing resource", func() {
		clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
			{
				Resource: corev1.ObjectReference{
					Kind:       "ConfigMap",
					APIVersion: "v1",
					Namespace:  nsName,
					Name:       "does-not-exist-" + randomString(),
				},
				Identifier: "MissingResource",
				Optional:   false,
			},
		}

		result, err := controllers.CollectTemplateResourceRefs(context.TODO(), clusterSummary)
		Expect(result).To(BeNil())
		Expect(err).NotTo(BeNil())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("referenced resource: ConfigMap"))
		Expect(err.Error()).To(ContainSubstring("does not exist"))
	})

	It("skips an optional missing resource without returning an error", func() {
		clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
			{
				Resource: corev1.ObjectReference{
					Kind:       "ConfigMap",
					APIVersion: "v1",
					Namespace:  nsName,
					Name:       "does-not-exist-" + randomString(),
				},
				Identifier: "OptionalResource",
				Optional:   true,
			},
		}

		result, err := controllers.CollectTemplateResourceRefs(context.TODO(), clusterSummary)
		Expect(err).To(BeNil())
		Expect(result).To(BeEmpty())
	})

	It("returns the resource when it exists", func() {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: nsName,
				Name:      randomString(),
			},
			Data: map[string]string{"key": "value"},
		}
		Expect(testEnv.Create(context.TODO(), cm)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())

		identifier := randomString()
		clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{
			{
				Resource: corev1.ObjectReference{
					Kind:       "ConfigMap",
					APIVersion: "v1",
					Namespace:  nsName,
					Name:       cm.Name,
				},
				Identifier: identifier,
			},
		}

		result, err := controllers.CollectTemplateResourceRefs(context.TODO(), clusterSummary)
		Expect(err).To(BeNil())
		Expect(result).To(HaveKey(identifier))
		Expect(result[identifier].GetName()).To(Equal(cm.Name))
	})
})

var _ = Describe("extractWatchedFields", func() {
	It("returns only the listed top-level field", func() {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"spec":   map[string]interface{}{"replicas": int64(3)},
			"status": map[string]interface{}{"readyReplicas": int64(3)},
		}}

		result := controllers.ExtractWatchedFields(u, []string{"status"})

		_, found, err := unstructured.NestedMap(result.Object, "status")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		_, found, err = unstructured.NestedMap(result.Object, "spec")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("extracts a specific nested field, excluding sibling fields", func() {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"status": map[string]interface{}{
				"readyReplicas": int64(2),
				"conditions":    []interface{}{"cond1", "cond2"},
			},
		}}

		result := controllers.ExtractWatchedFields(u, []string{"status.readyReplicas"})

		val, found, err := unstructured.NestedInt64(result.Object, "status", "readyReplicas")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(val).To(Equal(int64(2)))

		_, found, err = unstructured.NestedSlice(result.Object, "status", "conditions")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("handles multiple fields across different sections", func() {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"labels": map[string]interface{}{"env": "prod"}},
			"spec":     map[string]interface{}{"replicas": int64(3)},
			"status":   map[string]interface{}{"readyReplicas": int64(3)},
		}}

		result := controllers.ExtractWatchedFields(u, []string{"status.readyReplicas", "metadata.labels"})

		_, found, err := unstructured.NestedInt64(result.Object, "status", "readyReplicas")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		_, found, err = unstructured.NestedMap(result.Object, "metadata", "labels")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		_, found, err = unstructured.NestedMap(result.Object, "spec")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("returns an empty object when all paths are missing", func() {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"status": map[string]interface{}{"readyReplicas": int64(3)},
		}}

		result := controllers.ExtractWatchedFields(u, []string{"status.nonExistent"})
		Expect(result.Object).To(BeEmpty())
	})

	It("includes existing paths and silently skips missing ones", func() {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"status": map[string]interface{}{"readyReplicas": int64(3)},
		}}

		result := controllers.ExtractWatchedFields(u, []string{"status.readyReplicas", "status.nonExistent"})

		val, found, err := unstructured.NestedInt64(result.Object, "status", "readyReplicas")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(val).To(Equal(int64(3)))

		_, found, _ = unstructured.NestedFieldNoCopy(result.Object, "status", "nonExistent")
		Expect(found).To(BeFalse())
	})
})
