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
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
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
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType,
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
	if clusterType == libsveltosv1alpha1.ClusterTypeCapi {
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

func instantiateTemplateValues(ctx context.Context, config *rest.Config, c client.Client,
	clusterType libsveltosv1alpha1.ClusterType, clusterNamespace, clusterName, requestorName, values string,
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

	templateName := getTemplateName(clusterNamespace, clusterName, requestorName)
	tmpl, err := template.New(templateName).Option("missingkey=error").Funcs(sprig.FuncMap()).Parse(values)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer

	if err := tmpl.Execute(&buffer, objects); err != nil {
		return "", errors.Wrapf(err, "error executing template %q", values)
	}
	instantiatedValues := buffer.String()

	logger.V(logs.LogDebug).Info(fmt.Sprintf("Values %q", instantiatedValues))
	return instantiatedValues, nil
}

func getTemplateName(clusterNamespace, clusterName, requestorName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterNamespace, clusterName, requestorName)
}
