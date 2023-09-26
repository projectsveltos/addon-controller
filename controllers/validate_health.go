/*
Copyright 2022-23. projectsveltos.io. All rights reserved.

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
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	lua "github.com/yuin/gopher-lua"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

type healthStatus struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

// validateHealthPolicies runs all validateDeployment checks registered for the feature (Helm/Kustomize/Resources)
func validateHealthPolicies(ctx context.Context, remoteConfig *rest.Config, clusterSummary *configv1alpha1.ClusterSummary,
	featureID configv1alpha1.FeatureID, logger logr.Logger) error {

	for i := range clusterSummary.Spec.ClusterProfileSpec.ValidateHealths {
		check := &clusterSummary.Spec.ClusterProfileSpec.ValidateHealths[i]

		if check.FeatureID != featureID {
			continue
		}

		if err := validateHealthPolicy(ctx, remoteConfig, check, logger); err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to validate check: %s", err))
			return err
		}
	}

	return nil
}

func validateHealthPolicy(ctx context.Context, remoteConfig *rest.Config, check *configv1alpha1.ValidateHealth,
	logger logr.Logger) error {

	l := logger.WithValues("validation", check.Name)
	l.V(logs.LogDebug).Info("running health validation")

	list, err := fetchResources(ctx, remoteConfig, check)
	if err != nil {
		return err
	}

	if list == nil || len(list.Items) == 0 {
		return fmt.Errorf("did not fetch any resource")
	}

	for i := range list.Items {
		l = l.WithValues("resource", fmt.Sprintf("%s/%s", list.Items[i].GetNamespace(), list.Items[i].GetName()))
		l.V(logs.LogDebug).Info("examing resource's health")
		var healthy bool
		var msg string
		healthy, msg, err = isHealthy(&list.Items[i], check.Script, logger)
		if err != nil {
			return err
		}
		if !healthy {
			l.V(logs.LogInfo).Info("resource is not healthy")
			return fmt.Errorf("%s", msg)
		}
	}

	return nil
}

// fetchResources fetches resources from the managed cluster
func fetchResources(ctx context.Context, remoteConfig *rest.Config, check *configv1alpha1.ValidateHealth,
) (*unstructured.UnstructuredList, error) {

	gvk := schema.GroupVersionKind{
		Group:   check.Group,
		Version: check.Version,
		Kind:    check.Kind,
	}

	dc := discovery.NewDiscoveryClientForConfigOrDie(remoteConfig)
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	d := dynamic.NewForConfigOrDie(remoteConfig)

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}

	resourceId := schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: mapping.Resource.Resource,
	}

	options := metav1.ListOptions{}

	if len(check.LabelFilters) > 0 {
		labelFilter := ""
		for i := range check.LabelFilters {
			if labelFilter != "" {
				labelFilter += ","
			}
			f := check.LabelFilters[i]
			if f.Operation == libsveltosv1alpha1.OperationEqual {
				labelFilter += fmt.Sprintf("%s=%s", f.Key, f.Value)
			} else {
				labelFilter += fmt.Sprintf("%s!=%s", f.Key, f.Value)
			}
		}

		options.LabelSelector = labelFilter
	}

	if check.Namespace != "" {
		if options.FieldSelector != "" {
			options.FieldSelector += ","
		}
		options.FieldSelector += fmt.Sprintf("metadata.namespace=%s", check.Namespace)
	}

	list, err := d.Resource(resourceId).List(ctx, options)
	if err != nil {
		return nil, err
	}

	return list, nil
}

// isHealthy verifies whether resource is healthy according to Lua script
func isHealthy(resource *unstructured.Unstructured, script string, logger logr.Logger,
) (healthy bool, msg string, err error) {

	if script == "" {
		return true, "", nil
	}

	l := lua.NewState()
	defer l.Close()

	obj := mapToTable(resource.UnstructuredContent())

	err = l.DoString(script)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("doString failed: %v", err))
		return false, "", err
	}

	l.SetGlobal("obj", obj)

	err = l.CallByParam(lua.P{
		Fn:      l.GetGlobal("evaluate"), // name of Lua function
		NRet:    1,                       // number of returned values
		Protect: true,                    // return err or panic
	}, obj)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to evaluate health for resource: %v", err))
		return false, "", err
	}

	lv := l.Get(-1)
	tbl, ok := lv.(*lua.LTable)
	if !ok {
		logger.V(logs.LogInfo).Info(luaTableError)
		return false, "", fmt.Errorf("%s", luaTableError)
	}

	goResult := toGoValue(tbl)
	resultJson, err := json.Marshal(goResult)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to marshal result: %v", err))
		return false, "", err
	}

	var result healthStatus
	err = json.Unmarshal(resultJson, &result)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to unmarshal result: %v", err))
		return false, "", err
	}

	if result.Message != "" {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("message: %s", result.Message))
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("is healthy: %t", result.Healthy))

	if !result.Healthy {
		return false, fmt.Sprintf("resource %s/%s is not healthy: %s",
			resource.GetNamespace(), resource.GetName(), result.Message), nil
	}

	return true, "", nil
}
