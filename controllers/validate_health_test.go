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

package controllers_test

import (
	"context"
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

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosutils "github.com/projectsveltos/libsveltos/lib/utils"
)

const (
	luaFileName     = "lua_policy.lua"
	validFileName   = "valid_resource.yaml"
	invalidFileName = "invalid_resource.yaml"
)

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

		check := &configv1beta1.ValidateHealth{
			Group:   "",
			Version: "v1",
			Kind:    "Pod",
			LabelFilters: []libsveltosv1beta1.LabelFilter{
				{Key: key, Value: value, Operation: libsveltosv1beta1.OperationEqual},
			},
		}

		result, err := controllers.FetchResources(context.TODO(), testEnv.Config, check)
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
			healthy, _, err := controllers.IsHealthy(resource, string(luaPolicy), textlogger.NewLogger(textlogger.NewConfig()))
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
			healthy, _, err := controllers.IsHealthy(resource, string(luaPolicy), textlogger.NewLogger(textlogger.NewConfig()))
			Expect(err).To(BeNil())
			Expect(healthy).To(BeFalse())
		}
	}
}

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
