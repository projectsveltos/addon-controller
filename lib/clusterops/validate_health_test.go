/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

package clusterops_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosutils "github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

const (
	luaFileName     = "lua_policy.lua"
	validFileName   = "valid_resource.yaml"
	invalidFileName = "invalid_resource.yaml"

	jobManifestDataKey = "job.yaml"
)

const jobManifest = `apiVersion: batch/v1
kind: Job
metadata:
  name: probe
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: probe
        image: busybox
`

var _ = Describe("Lua Health Policies", func() {

	It("fetchResources returns resources", func() {
		namespace := randomString()
		key := randomString()
		value := randomString()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		pod1 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: ns.Name,
				Labels: map[string]string{
					key: value,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  randomString(),
						Image: randomString(),
					},
				},
			},
		}
		Expect(testEnv.Create(context.TODO(), pod1)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, pod1)).To(Succeed())

		pod2 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: ns.Name,
				Labels: map[string]string{
					key: randomString(),
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  randomString(),
						Image: randomString(),
					},
				},
			},
		}
		Expect(testEnv.Create(context.TODO(), pod2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, pod2)).To(Succeed())

		check := &libsveltosv1beta1.ValidateHealth{
			Group:   "",
			Version: "v1",
			Kind:    "Pod",
			LabelFilters: []libsveltosv1beta1.LabelFilter{
				{Key: key, Value: value, Operation: libsveltosv1beta1.OperationEqual},
			},
		}

		result, err := clusterops.FetchResources(context.TODO(), testEnv.Config, check)
		Expect(err).To(BeNil())
		Expect(len(result.Items)).To(Equal(1))
		Expect(result.Items[0].GetNamespace()).To(Equal(namespace))
		Expect(result.Items[0].GetName()).To(Equal(pod1.Name))
	})

	It("Verify all lua policies", func() {
		const luaDir = "./health_policies"

		dirs, err := os.ReadDir(luaDir)
		Expect(err).To(BeNil())

		for i := range dirs {
			if dirs[i].IsDir() {
				verifyHealthLuaPolicies(filepath.Join(luaDir, dirs[i].Name()))
			}
		}
	})
})

func verifyHealthLuaPolicies(dirName string) {
	By(fmt.Sprintf("Verifying lua policies %s", dirName))

	dirs, err := os.ReadDir(dirName)
	Expect(err).To(BeNil())

	fileCount := 0

	for i := range dirs {
		if dirs[i].IsDir() {
			verifyHealthLuaPolicies(fmt.Sprintf("%s/%s", dirName, dirs[i].Name()))
		} else {
			fileCount++
		}
	}

	if fileCount > 0 {
		verifyHealthLuaPolicy(dirName)
	}
}

func verifyHealthLuaPolicy(dirName string) {
	files, err := os.ReadDir(dirName)
	Expect(err).To(BeNil())

	for i := range files {
		if files[i].IsDir() {
			verifyHealthLuaPolicies(filepath.Join(dirName, files[i].Name()))
			continue
		}
	}

	By(fmt.Sprintf("Validating lua policies in dir: %s", dirName))
	fileName := filepath.Join(dirName, luaFileName)
	luaPolicy, err := os.ReadFile(fileName)
	Expect(err).To(BeNil())

	validResources := getResources(dirName, validFileName)
	if len(validResources) == 0 {
		By(fmt.Sprintf("%s file not present", validFileName))
	} else {
		By("Verifying valid resource")
		for i := range validResources {
			resource := validResources[i]
			healthy, _, err := clusterops.IsHealthy(resource, string(luaPolicy), nil, textlogger.NewLogger(textlogger.NewConfig()))
			Expect(err).To(BeNil())
			Expect(healthy).To(BeTrue())
		}
	}

	invalidResources := getResources(dirName, invalidFileName)
	if len(invalidResources) == 0 {
		By(fmt.Sprintf("%s file not present", invalidFileName))
	} else {
		By("Verifying non-matching content")
		for i := range invalidResources {
			resource := invalidResources[i]
			healthy, _, err := clusterops.IsHealthy(resource, string(luaPolicy), nil, textlogger.NewLogger(textlogger.NewConfig()))
			Expect(err).To(BeNil())
			Expect(healthy).To(BeFalse())
		}
	}
}

var _ = Describe("Metric health checks", func() {

	logger := textlogger.NewLogger(textlogger.NewConfig())

	Context("parsePromValue", func() {
		It("parses an integer value string", func() {
			v, err := clusterops.ParsePromValue(json.RawMessage(`"42"`))
			Expect(err).To(BeNil())
			Expect(v).To(Equal(float64(42)))
		})

		It("parses a float value string", func() {
			v, err := clusterops.ParsePromValue(json.RawMessage(`"3.14"`))
			Expect(err).To(BeNil())
			Expect(v).To(BeNumerically("~", 3.14, 0.001))
		})

		It("returns error for non-string JSON", func() {
			_, err := clusterops.ParsePromValue(json.RawMessage(`123`))
			Expect(err).NotTo(BeNil())
		})

		It("returns error for non-numeric string", func() {
			_, err := clusterops.ParsePromValue(json.RawMessage(`"abc"`))
			Expect(err).NotTo(BeNil())
		})
	})

	Context("extractScalar", func() {
		It("extracts value from scalar result type", func() {
			data := clusterops.PrometheusData{
				ResultType: "scalar",
				Result:     json.RawMessage(`[1234567890.123, "7.5"]`),
			}
			v, err := clusterops.ExtractScalar(data)
			Expect(err).To(BeNil())
			Expect(v).To(Equal(7.5))
		})

		It("extracts value from single-element vector", func() {
			data := clusterops.PrometheusData{
				ResultType: testResultTypeVector,
				Result:     json.RawMessage(`[{"metric":{"job":"app"},"value":[1234567890.123,"2.0"]}]`),
			}
			v, err := clusterops.ExtractScalar(data)
			Expect(err).To(BeNil())
			Expect(v).To(Equal(2.0))
		})

		It("returns error for empty vector", func() {
			data := clusterops.PrometheusData{
				ResultType: testResultTypeVector,
				Result:     json.RawMessage(`[]`),
			}
			_, err := clusterops.ExtractScalar(data)
			Expect(err).NotTo(BeNil())
		})

		It("returns error for multi-element vector", func() {
			data := clusterops.PrometheusData{
				ResultType: testResultTypeVector,
				Result: json.RawMessage(
					`[{"metric":{},"value":[1,"1"]},{"metric":{},"value":[2,"2"]}]`),
			}
			_, err := clusterops.ExtractScalar(data)
			Expect(err).NotTo(BeNil())
		})

		It("returns error for unsupported result type", func() {
			data := clusterops.PrometheusData{
				ResultType: "matrix",
				Result:     json.RawMessage(`[]`),
			}
			_, err := clusterops.ExtractScalar(data)
			Expect(err).NotTo(BeNil())
		})
	})

	Context("isHealthyBasedOnLua with metrics", func() {
		const errorRateScript = `
function evaluate()
  if metrics["errorRate"] > 0.05 then
    return {healthy=false, message="error rate too high"}
  end
  return {healthy=true, message=""}
end`

		It("evaluates metrics-only check as healthy", func() {
			healthy, _, err := clusterops.IsHealthy(nil, errorRateScript,
				map[string]float64{testErrorRateKey: 0.01}, logger)
			Expect(err).To(BeNil())
			Expect(healthy).To(BeTrue())
		})

		It("evaluates metrics-only check as unhealthy when threshold exceeded", func() {
			healthy, msg, err := clusterops.IsHealthy(nil, errorRateScript,
				map[string]float64{testErrorRateKey: 0.10}, logger)
			Expect(err).To(BeNil())
			Expect(healthy).To(BeFalse())
			Expect(msg).NotTo(BeEmpty())
		})

		It("makes metrics available alongside resource obj", func() {
			obj := &unstructured.Unstructured{}
			obj.SetKind("Deployment")
			obj.SetNamespace("default")
			obj.SetName("my-app")
			obj.Object["spec"] = map[string]interface{}{"replicas": int64(2)}
			obj.Object["status"] = map[string]interface{}{"availableReplicas": int64(2)}

			script := `
function evaluate(obj)
  if obj.status.availableReplicas ~= obj.spec.replicas then
    return {healthy=false, message="replicas not ready"}
  end
  if metrics["p99Ms"] > 500 then
    return {healthy=false, message="latency too high"}
  end
  return {healthy=true, message=""}
end`
			healthy, _, err := clusterops.IsHealthy(obj, script,
				map[string]float64{"p99Ms": 200}, logger)
			Expect(err).To(BeNil())
			Expect(healthy).To(BeTrue())
		})

		It("empty metrics table does not cause script error", func() {
			script := `
function evaluate()
  return {healthy=true, message=""}
end`
			healthy, _, err := clusterops.IsHealthy(nil, script, nil, logger)
			Expect(err).To(BeNil())
			Expect(healthy).To(BeTrue())
		})
	})
})

var _ = Describe("FetchJobManifest", func() {
	logger := textlogger.NewLogger(textlogger.NewConfig())

	var clusterNamespace string
	var clusterName string
	var clusterType libsveltosv1beta1.ClusterType
	var sveltosCluster *libsveltosv1beta1.SveltosCluster

	BeforeEach(func() {
		clusterNamespace = randomString()
		clusterName = randomString()
		clusterType = libsveltosv1beta1.ClusterTypeSveltos

		sveltosCluster = &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: clusterName},
		}
	})

	It("decodes a Job manifest from a referenced ConfigMap", func() {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: randomString()},
			Data:       map[string]string{jobManifestDataKey: jobManifest},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sveltosCluster, configMap).Build()

		jobRef := &libsveltosv1beta1.PolicyRef{
			Namespace: configMap.Namespace,
			Name:      configMap.Name,
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		job, err := clusterops.FetchJobManifest(context.TODO(), c, clusterNamespace, clusterName, clusterType, jobRef, logger)
		Expect(err).To(BeNil())
		Expect(job.Name).To(Equal("probe"))
		Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
	})

	It("decodes a Job manifest from a referenced Secret", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: randomString()},
			Data:       map[string][]byte{jobManifestDataKey: []byte(jobManifest)},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sveltosCluster, secret).Build()

		jobRef := &libsveltosv1beta1.PolicyRef{
			Namespace: secret.Namespace,
			Name:      secret.Name,
			Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
		}

		job, err := clusterops.FetchJobManifest(context.TODO(), c, clusterNamespace, clusterName, clusterType, jobRef, logger)
		Expect(err).To(BeNil())
		Expect(job.Name).To(Equal("probe"))
	})

	It("fails when the ConfigMap does not contain a Job manifest", func() {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: randomString()},
			Data: map[string]string{"deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: foo
`},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sveltosCluster, configMap).Build()

		jobRef := &libsveltosv1beta1.PolicyRef{
			Namespace: configMap.Namespace,
			Name:      configMap.Name,
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		_, err := clusterops.FetchJobManifest(context.TODO(), c, clusterNamespace, clusterName, clusterType, jobRef, logger)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring("does not contain a Job manifest"))
	})

	It("fails when the ConfigMap contains more than one entry", func() {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: randomString()},
			Data: map[string]string{
				jobManifestDataKey: jobManifest,
				"other.yaml":       jobManifest,
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sveltosCluster, configMap).Build()

		jobRef := &libsveltosv1beta1.PolicyRef{
			Namespace: configMap.Namespace,
			Name:      configMap.Name,
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		_, err := clusterops.FetchJobManifest(context.TODO(), c, clusterNamespace, clusterName, clusterType, jobRef, logger)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring("exactly one Job manifest"))
	})

	It("fails when the manifest has no containers", func() {
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: randomString()},
			Data: map[string]string{jobManifestDataKey: `apiVersion: batch/v1
kind: Job
metadata:
  name: probe
`},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sveltosCluster, configMap).Build()

		jobRef := &libsveltosv1beta1.PolicyRef{
			Namespace: configMap.Namespace,
			Name:      configMap.Name,
			Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		_, err := clusterops.FetchJobManifest(context.TODO(), c, clusterNamespace, clusterName, clusterType, jobRef, logger)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring("no containers"))
	})
})

func getResources(dirName, fileName string) []*unstructured.Unstructured {
	resourceFileName := filepath.Join(dirName, fileName)

	_, err := os.Stat(resourceFileName)
	if os.IsNotExist(err) {
		return nil
	}
	Expect(err).To(BeNil())

	content, err := os.ReadFile(resourceFileName)
	Expect(err).To(BeNil())

	separator := "---"

	resources := make([]*unstructured.Unstructured, 0)
	elements := strings.Split(string(content), separator)
	for i := range elements {
		if elements[i] == "" {
			continue
		}

		u, err := libsveltosutils.GetUnstructured([]byte(elements[i]))
		Expect(err).To(BeNil())

		resources = append(resources, u)
	}

	return resources
}
