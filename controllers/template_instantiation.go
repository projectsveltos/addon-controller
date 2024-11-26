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

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	"github.com/projectsveltos/libsveltos/lib/funcmap"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"
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
	dr, err = utils.GetDynamicResourceInterface(config, gvk, namespace)
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

func instantiateTemplateValues(ctx context.Context, config *rest.Config, c client.Client, //nolint: funlen // adding few closures
	clusterType libsveltosv1beta1.ClusterType, clusterNamespace, clusterName, requestorName, values string,
	mgmtResources map[string]*unstructured.Unstructured, logger logr.Logger) (string, error) {

	objects, err := fecthClusterObjects(ctx, config, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return "", err
	}

	if mgmtResources != nil {
		objects.MgmtResources = make(map[string]map[string]interface{})
		for k := range mgmtResources {
			logger.V(logs.LogDebug).Info(fmt.Sprintf("using mgmt resource %s %s/%s with identifier %s",
				mgmtResources[k].GetKind(), mgmtResources[k].GetNamespace(), mgmtResources[k].GetName(), k))
			objects.MgmtResources[k] = mgmtResources[k].UnstructuredContent()
		}
	}

	funcMap := funcmap.SveltosFuncMap()
	funcMap["getResource"] = func(id string) map[string]interface{} {
		return objects.MgmtResources[id]
	}
	funcMap["copy"] = func(id string) string {
		u, ok := objects.MgmtResources[id]
		if !ok {
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
			// Swallow errors inside of a template.
			return ""
		}

		v, isPresent, err := unstructured.NestedFieldCopy(u, strings.Split(fields, ".")...)
		if err != nil || !isPresent {
			return ""
		}

		return v
	}
	funcMap["removeField"] = func(id, fields string) string {
		u, ok := objects.MgmtResources[id]
		if !ok {
			// Swallow errors inside of a template.
			return ""
		}

		unstructured.RemoveNestedField(u, strings.Split(fields, ".")...)
		uObject := resetFields(u)

		data, err := yaml.Marshal(uObject)
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
			// Swallow errors inside of a template.
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

	templateName := getTemplateName(clusterNamespace, clusterName, requestorName)
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
