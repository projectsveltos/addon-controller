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

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/funcmap"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

type currentClusterObjects struct {
	Cluster                map[string]interface{}
	KubeadmControlPlane    map[string]interface{}
	InfrastructureProvider map[string]interface{}
	MgmtResources          map[string]map[string]interface{}
}

func fetchResource(ctx context.Context, config *rest.Config, namespace, name, apiVersion, kind string,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse apiversion %v", err))
		return nil, err
	}
	gvk := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    kind,
	}
	var dr dynamic.ResourceInterface
	dr, err = k8s_utils.GetDynamicResourceInterface(config, gvk, namespace)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch %s: %v", kind, err))
		return nil, err
	}
	var resource *unstructured.Unstructured
	resource, err = dr.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch %s %v", kind, err))
		return nil, err
	}

	return resource, nil
}

func fetchInfrastructureProvider(ctx context.Context, config *rest.Config, cluster *clusterv1.Cluster,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	var provider *unstructured.Unstructured
	var err error
	if cluster.Spec.InfrastructureRef != nil {
		provider, err = fetchResource(ctx, config, cluster.Namespace, cluster.Spec.InfrastructureRef.Name,
			cluster.Spec.InfrastructureRef.APIVersion, cluster.Spec.InfrastructureRef.Kind, logger)
	}

	return provider, err
}

func fetchKubeadmControlPlane(ctx context.Context, config *rest.Config, cluster *clusterv1.Cluster,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	var kubeadmControlPlane *unstructured.Unstructured
	var err error
	if cluster.Spec.ControlPlaneRef != nil {
		kubeadmControlPlane, err = fetchResource(ctx, config, cluster.Namespace, cluster.Spec.ControlPlaneRef.Name,
			cluster.Spec.ControlPlaneRef.APIVersion, cluster.Spec.ControlPlaneRef.Kind, logger)
	}

	return kubeadmControlPlane, err
}

// fecthClusterObjects fetches resources representing a cluster.
// All fetched objects are in the management cluster.
// Currently limited to Cluster and Infrastructure Provider
func fecthClusterObjects(ctx context.Context, config *rest.Config, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) (*currentClusterObjects, error) {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Fetch cluster %s: %s/%s",
		clusterType, clusterNamespace, clusterName))

	genericCluster, err := clusterproxy.GetCluster(ctx, c, clusterNamespace, clusterName, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch cluster %v", err))
		return nil, err
	}

	var cluster *clusterv1.Cluster
	var unstructuredCluster map[string]interface{}
	var provider *unstructured.Unstructured
	var kubeadmControlPlane *unstructured.Unstructured
	if clusterType == libsveltosv1beta1.ClusterTypeCapi {
		cluster = genericCluster.(*clusterv1.Cluster)
		provider, err = fetchInfrastructureProvider(ctx, config, cluster, logger)
		if err != nil {
			return nil, err
		}

		kubeadmControlPlane, err = fetchKubeadmControlPlane(ctx, config, cluster, logger)
		if err != nil {
			return nil, err
		}
		unstructuredCluster, err = runtime.DefaultUnstructuredConverter.ToUnstructured(genericCluster)
		if err != nil {
			return nil, err
		}
	} else {
		unstructuredCluster, err = runtime.DefaultUnstructuredConverter.ToUnstructured(genericCluster)
		if err != nil {
			return nil, err
		}
	}

	result := &currentClusterObjects{
		Cluster: unstructuredCluster,
	}
	if provider != nil {
		result.InfrastructureProvider = provider.UnstructuredContent()
	}
	if kubeadmControlPlane != nil {
		result.KubeadmControlPlane = kubeadmControlPlane.UnstructuredContent()
	}
	return result, nil
}

// instantiateGenericField is a helper function to instantiate a single field, regardless of type.
func instantiateGenericField(ctx context.Context, config *rest.Config, c client.Client,
	field interface{}, clusterSummary *configv1beta1.ClusterSummary, objects *currentClusterObjects,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) (interface{}, error) {

	// Marshal the field's value to a YAML string.
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	err := encoder.Encode(field)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal field: %w", err)
	}

	// Instantiate the YAML string with the template engine.
	instantiatedString, err := instantiateTemplateValues(ctx, config, c,
		clusterSummary, "chart-name", buf.String(), objects, mgmtResources, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate field: %w", err)
	}

	// Unmarshal the instantiated string back into a new variable of the same type as the original field.
	result := reflect.New(reflect.TypeOf(field)).Interface()
	err = yaml.Unmarshal([]byte(instantiatedString), result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal instantiated field: %w", err)
	}

	return result, nil
}

// instantiateStructFields is a recursive helper function to instantiate all string fields in a struct.
func instantiateStructFields(ctx context.Context, config *rest.Config, c client.Client, s interface{},
	clusterSummary *configv1beta1.ClusterSummary, objects *currentClusterObjects,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) error {

	value := reflect.ValueOf(s)

	if value.Kind() == reflect.Ptr {
		value = value.Elem()
	}

	if value.Kind() != reflect.Struct {
		return nil
	}

	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)

		if field.CanSet() {
			// Use the generic instantiation helper for all fields that can be set.
			instantiatedValue, err := instantiateGenericField(ctx, config, c, field.Interface(), clusterSummary,
				objects, mgmtResources, logger)
			if err != nil {
				return fmt.Errorf("failed to instantiate field '%s': %w", value.Type().Field(i).Name, err)
			}

			// Set the instantiated value back to the field.
			field.Set(reflect.ValueOf(instantiatedValue).Elem())
		}
	}
	return nil
}

func instantiateTemplateValues(ctx context.Context, config *rest.Config, c client.Client, //nolint: funlen // adding few closures
	clusterSummary *configv1beta1.ClusterSummary, requestorName, values string, objects *currentClusterObjects,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) (string, error) {

	if mgmtResources != nil {
		objects.MgmtResources = make(map[string]map[string]interface{})
		for k := range mgmtResources {
			logger.V(logs.LogDebug).Info(fmt.Sprintf("using mgmt resource %s %s/%s with identifier %s",
				mgmtResources[k].GetKind(), mgmtResources[k].GetNamespace(), mgmtResources[k].GetName(), k))
			objects.MgmtResources[k] = mgmtResources[k].UnstructuredContent()
		}
	}

	funcMap := funcmap.SveltosFuncMap(funcmap.HasTextTemplateAnnotation(clusterSummary.Annotations))
	funcMap["getResource"] = func(id string) map[string]interface{} {
		u, ok := objects.MgmtResources[id]
		if !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("resource %s does not exist", id))
			return nil
		}

		uObject := resetFields(u)

		return uObject.Object
	}
	funcMap["copy"] = func(id string) string {
		u, ok := objects.MgmtResources[id]
		if !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("resource %s does not exist", id))
			return ""
		}

		uObject := resetFields(u)

		data, err := yaml.Marshal(uObject.UnstructuredContent())
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		return strings.TrimSuffix(string(data), "\n")
	}
	funcMap["getField"] = func(id string, fields string) interface{} {
		u, ok := objects.MgmtResources[id]
		if !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("resource %s does not exist", id))
			return ""
		}

		v, isPresent, err := unstructured.NestedFieldCopy(u, strings.Split(fields, ".")...)
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		if !isPresent {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("field %s does not exist", fields))
			return ""
		}

		return v
	}
	funcMap["removeField"] = func(id, fields string) string {
		u, ok := objects.MgmtResources[id]
		if !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("resource %s does not exist", id))
			return ""
		}

		unstructured.RemoveNestedField(u, strings.Split(fields, ".")...)
		uObject := resetFields(u)

		data, err := yaml.Marshal(uObject.UnstructuredContent())
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		return strings.TrimSuffix(string(data), "\n")
	}
	funcMap["setField"] = func(id, fields string, value any) string {
		u, ok := objects.MgmtResources[id]
		if !ok {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("resource %s does not exist", id))
			return ""
		}

		err := unstructured.SetNestedField(u, value, strings.Split(fields, ".")...)
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		uObject := resetFields(u)

		data, err := yaml.Marshal(uObject.UnstructuredContent())
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return ""
		}

		return strings.TrimSuffix(string(data), "\n")
	}
	funcMap["chainRemoveField"] = func(u map[string]interface{}, fields string) map[string]interface{} {
		unstructured.RemoveNestedField(u, strings.Split(fields, ".")...)

		return u
	}
	funcMap["chainSetField"] = func(u map[string]interface{}, fields string, value any) map[string]interface{} {
		err := unstructured.SetNestedField(u, value, strings.Split(fields, ".")...)
		if err != nil {
			// Swallow errors inside of a template.
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed with err %v", err))
			return nil
		}

		return u
	}

	templateName := getTemplateName(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, requestorName)
	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(funcMap).Parse(values)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer

	if err := tmpl.Execute(&buffer, objects); err != nil {
		return "", fmt.Errorf("error executing template %q: %w", values, err)
	}
	instantiatedValues := buffer.String()

	logger.V(logs.LogDebug).Info(fmt.Sprintf("Values %q", instantiatedValues))
	return instantiatedValues, nil
}

func getTemplateName(clusterNamespace, clusterName, requestorName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterNamespace, clusterName, requestorName)
}

func resetFields(u map[string]interface{}) unstructured.Unstructured {
	var uObject unstructured.Unstructured
	uObject.SetUnstructuredContent(u)
	uObject.SetManagedFields(nil)
	uObject.SetResourceVersion("")
	uObject.SetUID("")

	return uObject
}
