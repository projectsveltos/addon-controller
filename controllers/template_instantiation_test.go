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
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

var _ = Describe("Template instantiation", func() {
	var cluster *clusterv1.Cluster
	var clusterSummary *configv1beta1.ClusterSummary
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

		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      randomString(), // for those test name does not really matter
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
	})

	It("instantiateTemplateValues returns correct values (metadata section)", func() {
		values := `valuesTemplate: |
    controller:
      name: "{{ .Cluster.metadata.name }}-test"`

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(fmt.Sprintf("%s-test", cluster.Name)))
	})

	It("instantiateTemplateValues with getField", func() {
		values := `{{ $replicasValue := getField "Deployment" "spec.replicas" }}
{{ toYaml $replicasValue }}`

		var replicas int32 = 3
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
			},
		}

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())
		u := &unstructured.Unstructured{}
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Deployment": u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		value, err := strconv.Atoi(strings.ReplaceAll(result, "\n", ""))
		Expect(err).To(BeNil())
		Expect(value).To(Equal(3))
	})

	It("instantiateTemplateValues with setField with int, string and bool", func() {
		values := `{{ setField "Deployment" "spec.replicas" (int64 7) }}`

		var replicas int32 = 3
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
			},
		}
		Expect(addTypeInformationToObject(scheme, depl)).To(Succeed())

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())
		u := &unstructured.Unstructured{}
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Deployment": u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring("replicas: 7"))

		modifiedDepl := appsv1.Deployment{}
		Expect(yaml.Unmarshal([]byte(result), &modifiedDepl)).To(Succeed())
		Expect(modifiedDepl.Spec.Replicas).ToNot(BeNil())

		values = `{{ setField "Deployment" "spec.paused" false }}`
		result, err = controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring("paused: false"))

		Expect(yaml.Unmarshal([]byte(result), &modifiedDepl)).To(Succeed())
		Expect(modifiedDepl.Spec.Paused).To(BeFalse())

		namespace := randomString()
		values = fmt.Sprintf(`{{ setField "Deployment" "metadata.namespace" %q }}`, namespace)

		result, err = controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(fmt.Sprintf("namespace: %s", namespace)))

		policy, err := k8s_utils.GetUnstructured([]byte(result))
		Expect(err).To(BeNil())
		Expect(policy.GetNamespace()).To(Equal(namespace))
	})

	It("instantiateTemplateValues with setField with slice", func() {
		// When dealing with slice, get slice (line 1)
		// Create new slice modifying image value
		// Use this modified slice, to set the field in the original struct
		values := `{{ $currentContainers := (getField "Deployment" "spec.template.spec.containers") }}
{{ $modifiedContainers := list }}
{{- range $element := $currentContainers }}
  {{ $modifiedContainers = append $modifiedContainers (chainSetField $element "image" "nginx:1.13" ) }}
{{- end }}
{{ setField "Deployment" "spec.template.spec.containers" $modifiedContainers }}`

		var replicas int32 = 3
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:            randomString(),
								Image:           randomString(),
								ImagePullPolicy: corev1.PullAlways,
							},
						},
						ServiceAccountName: randomString(),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, depl)).To(Succeed())

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())
		u := &unstructured.Unstructured{}
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Deployment": u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		modifiedDepl := &appsv1.Deployment{}
		Expect(yaml.Unmarshal([]byte(result), modifiedDepl)).To(Succeed())
		Expect(len(modifiedDepl.Spec.Template.Spec.Containers)).To(Equal(1))
		Expect(modifiedDepl.Spec.Template.Spec.Containers[0].Image).To(Equal("nginx:1.13"))
	})

	It("instantiateTemplateValues with setField with a map", func() {
		values := `{{ $data := (getField "ConfigMap" "data")}}
{{ $data = (chainRemoveField $data "id") }}
{{ setField "ConfigMap" "data" $data }}`

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Data: map[string]string{
				"user": randomString(),
				"id":   randomString(),
			},
		}
		Expect(addTypeInformationToObject(scheme, cm)).To(Succeed())

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cm)
		Expect(err).To(BeNil())
		u := &unstructured.Unstructured{}
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"ConfigMap": u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).ToNot(ContainSubstring("replicas"))

		modifiedCm := &corev1.ConfigMap{}
		Expect(yaml.Unmarshal([]byte(result), modifiedCm)).To(Succeed())
		Expect(modifiedCm.Data).ToNot(BeNil())
		Expect(modifiedCm.Data["user"]).ToNot(BeEmpty())
		Expect(modifiedCm.Data["id"]).To(BeEmpty())
	})

	It("instantiateTemplateValues with removeField", func() {
		values := `{{ removeField "Deployment" "spec.replicas" }}`

		var replicas int32 = 3
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
			},
		}
		Expect(addTypeInformationToObject(scheme, depl)).To(Succeed())

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())
		u := &unstructured.Unstructured{}
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Deployment": u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).ToNot(ContainSubstring("replicas"))

		modifiedDepl := &appsv1.Deployment{}
		Expect(yaml.Unmarshal([]byte(result), modifiedDepl)).To(Succeed())
		Expect(modifiedDepl.Spec.Replicas).To(BeNil())
	})

	It("instantiateTemplateValues with chainSetField", func() {
		values := `{{ $depl := (getResource "Deployment") }}
{{ $depl := (chainSetField $depl "spec.replicas" (int64 5) ) }}
{{ $depl := (chainSetField $depl "metadata.namespace" .Cluster.metadata.namespace ) }}
{{ $depl := (chainSetField $depl "spec.template.spec.serviceAccountName" "default" ) }}
{{ $depl := (chainSetField $depl "spec.paused" true ) }}
{{ toYaml $depl }}`

		var replicas int32 = 3
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:            randomString(),
								Image:           randomString(),
								ImagePullPolicy: corev1.PullAlways,
							},
						},
						ServiceAccountName: randomString(),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, depl)).To(Succeed())

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())
		u := &unstructured.Unstructured{}
		u.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Deployment": u,
		}

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring("replicas: 5"))
		Expect(result).To(ContainSubstring(fmt.Sprintf("namespace: %s", cluster.Namespace)))
		Expect(result).To(ContainSubstring("paused: true"))

		modifiedDepl := appsv1.Deployment{}
		Expect(yaml.Unmarshal([]byte(result), &modifiedDepl)).To(Succeed())
		Expect(modifiedDepl.Spec.Replicas).ToNot(BeNil())
		Expect(*modifiedDepl.Spec.Replicas).To(Equal(int32(5)))
		Expect(modifiedDepl.Spec.Paused).To(BeTrue())

		policy, err := k8s_utils.GetUnstructured([]byte(result))
		Expect(err).To(BeNil())
		Expect(policy.GetNamespace()).To(Equal(namespace))
	})

	It("instantiateTemplateValues returns correct values (spec section)", func() {
		values := `valuesTemplate: |
    controller:
      name: "{{ .Cluster.metadata.name }}-test"
	  cidrs: {{ index .Cluster.spec.clusterNetwork.pods.cidrBlocks 0 }} 
	  `

		result, err := controllers.InstantiateTemplateValues(context.TODO(), testEnv.Config, testEnv.GetClient(),
			clusterSummary, randomString(), values, nil, textlogger.NewLogger(textlogger.NewConfig()))
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
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
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
			clusterSummary, randomString(), values, mgmtResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(result).To(ContainSubstring(pwd))
	})
})
