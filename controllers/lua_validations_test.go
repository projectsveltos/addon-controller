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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2/textlogger"

	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/libsveltos/lib/utils"
	libsveltosutils "github.com/projectsveltos/libsveltos/lib/utils"
)

const (
	luaFileName     = "lua_policy.lua"
	validFileName   = "valid_resource.yaml"
	invalidFileName = "invalid_resource.yaml"
)

const (
	deploymentName = `function evaluate()
   local hs = {}
   hs.valid = true
   hs.message = ""
   local deployments = {}
   local pattern = "^prod"
   
   -- Separate deployments and services from the resources
   for _, resource in ipairs(resources) do
     if resource.kind == "Deployment" then
       table.insert(deployments, resource)
     end
   end
   -- Check for each deployment if there is a matching service
   for _, deployment in ipairs(deployments) do
     local deploymentInfo = deployment.metadata.namespace .. "/" .. deployment.metadata.name
     
     if not string.match(deployment.metadata.name, pattern) then
       hs.message = "Name not compliant for deployment: " .. deploymentInfo
       hs.valid = false
       break
     end
   end
   return hs
 end`

	deploymentAndService = `function compareMaps(map1, map2)
  -- Check if the number of keys in both maps is the same
  local count1 = 0
  for _ in pairs(map1) do count1 = count1 + 1 end

  local count2 = 0
  for _ in pairs(map2) do count2 = count2 + 1 end

  if count1 ~= count2 then
    return false
  end

  -- Check if the keys and values match in both maps
  for key, value1 in pairs(map1) do
    local value2 = map2[key]
    if value2 == nil or value1 ~= value2 then
      return false
    end
  end

  return true
end

function evaluate()
    local hs = {}
    hs.valid = true
    hs.message = ""

    local deployments = {}
    local services = {}

    -- Separate deployments and services from the resources
    for _, resource in ipairs(resources) do
        local kind = resource.kind
        if kind == "Deployment" then
            table.insert(deployments, resource)
        elseif kind == "Service" then
            table.insert(services, resource)
        end
    end

    -- Check for each deployment if there is a matching service
    for _, deployment in ipairs(deployments) do
        local deploymentName = deployment.metadata.name
        local matchingService = false

        for _, service in ipairs(services) do
            if service.metadata.namespace == deployment.metadata.namespace then
			    local selector = service.spec.selector
                if selector and compareMaps(selector, deployment.spec.selector.matchLabels) then
                    matchingService = true
                    break
                end
            end
        end

        if not matchingService then
            hs.valid = false
            hs.message = "No matching service found for deployment: " .. deploymentName
            break
        end
    end

    return hs
end`

	deployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
  labels:
    app: sample-app
spec:
  replicas: 3
  selector:
    matchLabels:
      app: sample-app
  template:
    metadata:
      labels:
        app: sample-app
    spec:
      containers:
        - name: sample-container
          image: nginx:latest
          ports:
            - containerPort: 80`
)

var _ = Describe("Lua validations", func() {
	It("luaValidation returns no error when resources satisfy a given lua validation. Single resource.", func() {
		u, err := utils.GetUnstructured([]byte(fmt.Sprintf(deployment, "prod-"+randomString())))
		Expect(err).To(BeNil())

		Expect(controllers.LuaValidation(context.TODO(), []byte(deploymentName),
			[]*unstructured.Unstructured{u}, textlogger.NewLogger(textlogger.NewConfig()))).To(BeNil())
	})

	It("luaValidation returns an error when resources do not satisfy a given lua validation. Single resource.", func() {
		u, err := utils.GetUnstructured([]byte(fmt.Sprintf(deployment, randomString())))
		Expect(err).To(BeNil())

		Expect(controllers.LuaValidation(context.TODO(), []byte(deploymentName),
			[]*unstructured.Unstructured{u}, textlogger.NewLogger(textlogger.NewConfig()))).ToNot(BeNil())
	})
	It("luaValidation returns no error when resources do not satisfy a given lua validation. Multiple resource.", func() {
		service := `apiVersion: v1
kind: Service
metadata:
  name: my-service
  namespace: default
spec:
  selector:
    app: my-app
  ports:
    - protocol: TCP
      port: 80
      targetPort: 80`

		deployment := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deployment
  namespace: default
spec:
  replicas: 3
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
        - name: my-container
          image: nginx:latest
          ports:
            - containerPort: 80
`
		deplUnstructured, err := utils.GetUnstructured([]byte(deployment))
		Expect(err).To(BeNil())
		serviceUnstructured, err := utils.GetUnstructured([]byte(service))
		Expect(err).To(BeNil())

		Expect(controllers.LuaValidation(context.TODO(), []byte(deploymentAndService),
			[]*unstructured.Unstructured{deplUnstructured, serviceUnstructured}, textlogger.NewLogger(textlogger.NewConfig()))).To(BeNil())
	})
	It("luaValidation returns no error when resources do not satisfy a given lua validation. Multiple resource.", func() {
		pod := `apiVersion: v1
kind: Pod
metadata:
  name: my-pod
spec:
  containers:
  - name: my-container
    image: nginx:latest
    ports:
    - containerPort: 80
`
		deplUnstructured, err := utils.GetUnstructured([]byte(fmt.Sprintf(deployment, randomString())))
		Expect(err).To(BeNil())
		podUnstructured, err := utils.GetUnstructured([]byte(pod))
		Expect(err).To(BeNil())

		Expect(controllers.LuaValidation(context.TODO(), []byte(deploymentAndService),
			[]*unstructured.Unstructured{deplUnstructured, podUnstructured}, textlogger.NewLogger(textlogger.NewConfig()))).ToNot(BeNil())
	})
})

var _ = Describe("Lua AddonCompliance", func() {

	It("Verify all lua policies", func() {
		const openAPIDir = "./compliance_policies/lua_policies"

		dirs, err := os.ReadDir(openAPIDir)
		Expect(err).To(BeNil())

		for i := range dirs {
			if dirs[i].IsDir() {
				verifyLuaPolicies(filepath.Join(openAPIDir, dirs[i].Name()))
			}
		}
	})
})

func verifyLuaPolicies(dirName string) {
	By(fmt.Sprintf("Verifying lua policies %s", dirName))

	dirs, err := os.ReadDir(dirName)
	Expect(err).To(BeNil())

	fileCount := 0

	for i := range dirs {
		if dirs[i].IsDir() {
			verifyLuaPolicies(fmt.Sprintf("%s/%s", dirName, dirs[i].Name()))
		} else {
			fileCount++
		}
	}

	if fileCount > 0 {
		verifyLuaPolicy(dirName)
	}
}

func verifyLuaPolicy(dirName string) {
	files, err := os.ReadDir(dirName)
	Expect(err).To(BeNil())

	for i := range files {
		if files[i].IsDir() {
			verifyLuaPolicies(filepath.Join(dirName, files[i].Name()))
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
		spec := map[string][]byte{randomString(): luaPolicy}
		err = controllers.RunLuaValidations(context.TODO(), spec, validResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
	}

	invalidResources := getResources(dirName, invalidFileName)
	if len(invalidResources) == 0 {
		By(fmt.Sprintf("%s file not present", invalidFileName))
	} else {
		By("Verifying non-matching content")
		spec := map[string][]byte{randomString(): luaPolicy}
		err = controllers.RunLuaValidations(context.TODO(), spec, invalidResources, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())
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
