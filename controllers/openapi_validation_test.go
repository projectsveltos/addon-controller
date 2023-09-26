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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2/klogr"

	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosutils "github.com/projectsveltos/libsveltos/lib/utils"
)

const (
	validFileName   = "valid_resource.yaml"
	invalidFileName = "invalid_resource.yaml"
	openAPIFileName = "openapi_policy.yaml"
)

var (
	deplReplicaSpec = `openapi: 3.0.0
info:
  title: Kubernetes Replica Validation
  version: 1.0.0

paths:
  /apis/apps/v1/namespaces/{namespace}/deployments:
    post:
      parameters:
        - in: path
          name: namespace
          required: true
          schema:
            type: string
            minimum: 1
          description: The namespace of the resource
      summary: Create/Update a new deployment
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Deployment'
      responses:
        '200':
          description: OK

components:
  schemas:
    Deployment:
      type: object
      properties:
        metadata:
          type: object
          properties:
            name:
              type: string
        spec:
          type: object
          properties:
            replicas:
              type: integer
              minimum: 3`

	nameSpec = `openapi: 3.0.0
info:
  title: Kubernetes Resource Validation
  version: 1.0.0
paths:
  '/apis/apps/v1/namespaces/{namespace}/deployments/{deployment}':
    put:
      summary: Create a Kubernetes Resource
      parameters:
        - name: namespace
          in: path
          required: true
          schema:
            type: string
        - name: deployment
          in: path
          required: true
          schema:
            $ref: '#/components/schemas/NameSchema'
      responses:
        '200':
          description: Successful operation
components:
  schemas:
    NameSchema:
      type: string
      maxLength: 10
`

	appLabel = `openapi: 3.0.0
info:
  title: Kubernetes Deployment Label Validation
  version: 1.0.0

components:
  schemas:
    Deployment:
      type: object
      properties:
        metadata:
          type: object
          properties:
            labels:
              type: object
              additionalProperties:
                type: string
      required:
        - metadata
    DeploymentWithAppLabel:
      allOf:
        - $ref: '#/components/schemas/Deployment'
        - properties:
            metadata:
              properties:
                labels:
                  type: object
                  additionalProperties:
                    type: string
                  required:
                    - app
              required:
                - labels                    
      required:
        - metadata

paths:
  /apis/apps/v1/namespaces/{namespace}/deployments/{deployment}:
    patch:
      parameters:
        - in: path
          name: namespace
          required: true
          schema:
            type: string
            minimum: 1
          description: The namespace of the resource
        - in: path
          name: deployment
          required: true
          schema:
            type: string
            minimum: 1
          description: The name of the resource
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/DeploymentWithAppLabel'
      responses:
        '200':
          description: Valid Deployment with "app" label provided
        '400':
          description: Invalid Deployment, missing or incorrect "app" label
`

	deplNameSpecificNamespace = `openapi: 3.0.0
info:
  title: Kubernetes Replica Validation
  version: 1.0.0

paths:
  /apis/apps/v1/namespaces/production/deployments/{deployment}:
    put:
      parameters:
        - in: path
          name: deployment
          required: true
          schema:
            type: string
            minimum: 1
          description: The name of the resource
      summary: Create/Update a new deployment
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Deployment'
      responses:
        '200':
          description: OK

components:
  schemas:
    Deployment:
      type: object
      properties:
        metadata:
          type: object
          properties:
            name:
              type: string
              maxLength: 10`
)

var _ = Describe("OpenAPI validations", func() {
	It("openAPIValidations returns error when one validation fails", func() {
		specs := map[string][]byte{randomString(): []byte(deplReplicaSpec)}
		resource := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
  namespace: default
  labels:
    app: sample-app
spec:
  replicas: %d
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

		validDeployment := fmt.Sprintf(resource, 3)
		err := controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(validDeployment)), klogr.New())
		Expect(err).To(BeNil())

		invalidDeployment := fmt.Sprintf(resource, 1)
		err = controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(invalidDeployment)), klogr.New())
		Expect(err).ToNot(BeNil())
	})

	It("openAPIValidations returns error when nameSpec validation fails", func() {
		specs := map[string][]byte{randomString(): []byte(nameSpec)}
		deployment := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
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

		name := "foo"
		validResource := fmt.Sprintf(deployment, name, randomString())
		By(fmt.Sprintf("using deployment with valid name %s", name))
		err := controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(validResource)), klogr.New())
		Expect(err).To(BeNil())

		name = "bar" + randomString()
		invalidResource := fmt.Sprintf(deployment, name, randomString())
		By(fmt.Sprintf("using deployment with invalid name %s", name))
		err = controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(invalidResource)), klogr.New())
		Expect(err).ToNot(BeNil())
	})

	It("openAPIValidations returns error when appLabel validation fails", func() {
		specs := map[string][]byte{randomString(): []byte(nameSpec), randomString(): []byte(appLabel)}

		deployment := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    %s: %s
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

		name := "nginx"
		validResource := fmt.Sprintf(deployment, name, randomString(), "app", randomString())
		By("using resource with valid name and required label")
		err := controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(validResource)), klogr.New())
		Expect(err).To(BeNil())

		invalidResource := fmt.Sprintf(deployment, name, randomString(), randomString(), randomString())
		By("using resource with valid name but missing required label")
		err = controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(invalidResource)), klogr.New())
		Expect(err).ToNot(BeNil())

		// define a deployment with no labels
		deployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
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

		invalidResource = fmt.Sprintf(deployment, name, randomString())
		By("using resource with valid name but no labels")
		err = controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(invalidResource)), klogr.New())
		Expect(err).ToNot(BeNil())
	})

	It("openAPIValidations validates only resources in specified namespace", func() {
		specs := map[string][]byte{randomString(): []byte(deplNameSpecificNamespace)}

		resource := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: depl-with-invalid-name
  namespace: %s
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

		validResource := fmt.Sprintf(resource, randomString())
		By("using resource with invalid name but not in production namespace")
		err := controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(validResource)), klogr.New())
		Expect(err).To(BeNil())

		invalidResource := fmt.Sprintf(resource, "production")
		By("using resource with invalid name in production namespace")
		err = controllers.RunOpenapiValidations(context.TODO(), specs, getUnstructured([]byte(invalidResource)), klogr.New())
		Expect(err).ToNot(BeNil())
	})
})

func getUnstructured(section []byte) *unstructured.Unstructured {
	policy, err := libsveltosutils.GetUnstructured(section)
	Expect(err).To(BeNil())

	return policy
}

var _ = Describe("OpenAPI AddonCompliance", func() {

	It("Verify all openapi policies", func() {
		const openAPIDir = "./compliance_policies/openapi_policies"

		dirs, err := os.ReadDir(openAPIDir)
		Expect(err).To(BeNil())

		for i := range dirs {
			if dirs[i].IsDir() {
				verifyOpenAPIPolicies(filepath.Join(openAPIDir, dirs[i].Name()))
			}
		}
	})
})

func verifyOpenAPIPolicies(dirName string) {
	By(fmt.Sprintf("Verifying openAPI policies %s", dirName))

	dirs, err := os.ReadDir(dirName)
	Expect(err).To(BeNil())

	fileCount := 0

	for i := range dirs {
		if dirs[i].IsDir() {
			verifyOpenAPIPolicies(fmt.Sprintf("%s/%s", dirName, dirs[i].Name()))
		} else {
			fileCount++
		}
	}

	if fileCount > 0 {
		verifyOpenAPIPolicy(dirName)
	}
}

func verifyOpenAPIPolicy(dirName string) {
	files, err := os.ReadDir(dirName)
	Expect(err).To(BeNil())

	for i := range files {
		if files[i].IsDir() {
			verifyOpenAPIPolicies(filepath.Join(dirName, files[i].Name()))
			continue
		}
	}

	By(fmt.Sprintf("Validating openapi policies in dir: %s", dirName))
	fileName := filepath.Join(dirName, openAPIFileName)
	openAPIPolicy, err := os.ReadFile(fileName)
	Expect(err).To(BeNil())

	validResource := getResource(dirName, validFileName)
	if validResource == nil {
		By(fmt.Sprintf("%s file not present", validFileName))
	} else {
		By("Verifying valid resource")
		spec := map[string][]byte{randomString(): openAPIPolicy}
		err = controllers.RunOpenapiValidations(context.TODO(), spec, validResource, klogr.New())
		Expect(err).To(BeNil())
	}

	invalidResource := getResource(dirName, invalidFileName)
	if invalidResource == nil {
		By(fmt.Sprintf("%s file not present", invalidFileName))
	} else {
		By("Verifying non-matching content")
		spec := map[string][]byte{randomString(): openAPIPolicy}
		err = controllers.RunOpenapiValidations(context.TODO(), spec, invalidResource, klogr.New())
		Expect(err).ToNot(BeNil())
	}
}

var _ = Describe("Lua AddonCompliance", func() {

	It("Verify all lua policies", func() {
		const luaDir = "./compliance_policies/lua_policies"

		dirs, err := os.ReadDir(luaDir)
		Expect(err).To(BeNil())

		for i := range dirs {
			if dirs[i].IsDir() {
				verifyLuaPolicies(filepath.Join(luaDir, dirs[i].Name()))
			}
		}
	})
})

func getResource(dirName, fileName string) *unstructured.Unstructured {
	resourceFileName := filepath.Join(dirName, fileName)

	_, err := os.Stat(resourceFileName)
	if os.IsNotExist(err) {
		return nil
	}
	Expect(err).To(BeNil())

	content, err := os.ReadFile(resourceFileName)
	Expect(err).To(BeNil())

	u, err := libsveltosutils.GetUnstructured(content)
	Expect(err).To(BeNil())

	return u
}
