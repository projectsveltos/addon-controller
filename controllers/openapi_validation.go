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

package controllers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/ghodss/yaml"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/projectsveltos/addon-controller/pkg/constraints"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

func getOpenAPIValidations(clusterNamespace, clusterName string,
	clusterType *libsveltosv1alpha1.ClusterType, logger logr.Logger) (map[string][]byte, error) {

	logger.V(logs.LogDebug).Info("collect all openapi validations")
	manager := constraints.GetManager()
	if manager == nil {
		const errMsg = "constraints manager is not ready"
		logger.V(logs.LogInfo).Info(errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	currentOpenAPIPolicies, err := manager.GetClusterOpenapiPolicies(clusterNamespace, clusterName, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(err.Error())
		return nil, fmt.Errorf("%s", err.Error())
	}

	return currentOpenAPIPolicies, nil
}

func runOpenAPIValidations(ctx context.Context, specs map[string][]byte,
	resource *unstructured.Unstructured, logger logr.Logger) error {

	logger = logger.WithValues("resource", fmt.Sprintf("%s:%s/%s", resource.GroupVersionKind().Kind,
		resource.GetNamespace(), resource.GetName()))

	logger.V(logs.LogVerbose).Info("openAPIValidations")

	for key := range specs {
		if err := openAPIValidation(ctx, specs[key], resource, logger); err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("OpenAPI validation %s failed %v", key, err))
			return fmt.Errorf("OpenAPI validation %s failed %w", key, err)
		}
	}

	return nil
}

func openAPIValidation(ctx context.Context, specs []byte,
	resource *unstructured.Unstructured, logger logr.Logger) error {

	logger.V(logs.LogVerbose).Info("openAPIValidation")
	loader := &openapi3.Loader{Context: ctx, IsExternalRefsAllowed: true}

	// Load the OpenAPI specification from the content
	doc, err := loader.LoadFromData(specs)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to loadFromData: %v", err))
		return err
	}

	if err = doc.Validate(ctx); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to validate: %v", err))
		return err
	}

	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return err
	}

	resourceYAML, err := convertUnstructuredToYAML(resource)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to convert resource to YAML: %v", err))
		return err
	}

	resourceJson, err := convertYAMLToJSON(resourceYAML)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to convert resource from YAML to JSON: %v", err))
		return err
	}

	for i := range doc.Paths {
		uri := getKubernetesURI(doc.Paths[i].Post != nil, resource)

		method := "POST"
		if doc.Paths[i].Put != nil {
			method = "PUT"
		} else if doc.Paths[i].Patch != nil {
			method = "PATCH"
		}

		httpRequest := &http.Request{
			Method: method,
			URL:    &url.URL{Path: uri},
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(bytes.NewReader(resourceJson)),
		}

		route, pathParams, err := router.FindRoute(httpRequest)
		if err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to findRoute: uri: %s", uri))
			var routeErr *routers.RouteError
			if errors.As(err, &routeErr) {
				return nil
			}
			return err
		}

		requestValidationInput := &openapi3filter.RequestValidationInput{
			Request:    httpRequest,
			PathParams: pathParams,
			Route:      route,
		}
		if err := openapi3filter.ValidateRequest(ctx, requestValidationInput); err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to ValidateRequest: %v", err))
			return err
		}
	}
	return nil
}

func convertUnstructuredToYAML(obj *unstructured.Unstructured) (string, error) {
	yamlBytes, err := yaml.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("failed to marshal to YAML: %w", err)
	}
	yamlStr := string(yamlBytes)
	return yamlStr, nil
}

func convertYAMLToJSON(yamlStr string) ([]byte, error) {
	// Convert YAML to JSON using ghodss/yaml
	jsonBytes, err := yaml.YAMLToJSON([]byte(yamlStr))
	if err != nil {
		return nil, fmt.Errorf("failed to convert YAML to JSON: %w", err)
	}

	return jsonBytes, nil
}

/*
/apis/apps/v1/namespaces/{namespace}/deployments
POST: create a Deployment

/apis/apps/v1/namespaces/{namespace}/deployments/{name}
PATCH: partially update the specified Deployment
PUT: replace the specified Deployment

/apis/apps/v1/namespaces/{namespace}/deployments/{name}/scale
PATCH: partially update scale of the specified Deployment
PUT: replace scale of the specified Deployment

/apis/apps/v1/namespaces/{namespace}/deployments/{name}/status
PATCH: partially update status of the specified Deployment
PUT: replace status of the specified Deployment
*/

func getKubernetesURI(isPost bool, obj *unstructured.Unstructured) string {
	uri := dynamic.LegacyAPIPathResolverFunc(obj.GroupVersionKind())
	group := obj.GroupVersionKind().Group
	if group != "" {
		uri += fmt.Sprintf("/%s", group)
	}
	version := obj.GroupVersionKind().Version
	uri += fmt.Sprintf("/%s", version)

	if obj.GetNamespace() != "" {
		uri += fmt.Sprintf("/namespaces/%s", obj.GetNamespace())
	}

	kind := strings.ToLower(obj.GroupVersionKind().Kind) + "s" // lower case and plural
	uri += fmt.Sprintf("/%s", kind)

	if !isPost {
		uri += fmt.Sprintf("/%s", obj.GetName())
	}

	return uri
}
