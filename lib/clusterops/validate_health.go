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

package clusterops

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

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/cel"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	sveltoslua "github.com/projectsveltos/libsveltos/lib/lua"
)

type healthStatus struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

// ValidateHealthPolicies runs all validateDeployment checks registered for the feature (Helm/Kustomize/Resources)
func ValidateHealthPolicies(ctx context.Context, remoteConfig *rest.Config, validateHealths []libsveltosv1beta1.ValidateHealth,
	featureID libsveltosv1beta1.FeatureID, isDelete bool, logger logr.Logger) error {

	// If SveltosCluster is in pull mode, this will done by the agent in the managed cluster
	if remoteConfig == nil {
		return nil
	}

	for i := range validateHealths {
		check := &validateHealths[i]

		if check.FeatureID != featureID {
			continue
		}

		if err := validateHealthPolicy(ctx, remoteConfig, check, isDelete, logger); err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to validate check: %s", err))
			return err
		}
	}

	return nil
}

func validateHealthPolicy(ctx context.Context, remoteConfig *rest.Config, check *libsveltosv1beta1.ValidateHealth,
	isDelete bool, logger logr.Logger) error {

	l := logger.WithValues("validation", check.Name)
	l.V(logs.LogDebug).Info("running health validation")

	list, err := fetchResources(ctx, remoteConfig, check)
	if err != nil {
		return err
	}

	// This can happen if the CRD is not present
	if list == nil {
		return nil
	}

	if !isDelete {
		// dont fail for pre and post delete checks. Those checks are usually intended to verify
		// resources are gone
		if list == nil || len(list.Items) == 0 {
			return fmt.Errorf("validateHealth: %s did not fetch any resource", check.Name)
		}
	}

	for i := range list.Items {
		errorMsg := fmt.Sprintf("resource %s %s/%s is not healthy",
			list.Items[i].GetKind(), list.Items[i].GetNamespace(), list.Items[i].GetName())

		l = l.WithValues("resource", fmt.Sprintf("%s %s/%s",
			list.Items[i].GetKind(), list.Items[i].GetNamespace(), list.Items[i].GetName()))

		l.V(logs.LogDebug).Info("examing resource's health")
		var healthy bool
		var msg string
		healthy, msg, err = isHealthyBasedOnLua(&list.Items[i], check.Script, logger)
		if err != nil {
			return err
		}
		if !healthy {
			l.V(logs.LogInfo).Info(errorMsg)
			return fmt.Errorf("%s %q", errorMsg, msg)
		}
		healthy, err = isHealthyBasedOnCELRules(&list.Items[i], check.EvaluateCEL, logger)
		if err != nil {
			return err
		}
		if !healthy {
			l.V(logs.LogInfo).Info(errorMsg)
			return fmt.Errorf("%s", errorMsg)
		}
	}

	return nil
}

// fetchResources fetches resources from the managed cluster
func fetchResources(ctx context.Context, remoteConfig *rest.Config, check *libsveltosv1beta1.ValidateHealth,
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
		options.LabelSelector = addLabelFilters(check.LabelFilters)
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

func addLabelFilters(labelFilters []libsveltosv1beta1.LabelFilter) string {
	labelFilter := ""
	if len(labelFilters) > 0 {
		for i := range labelFilters {
			if labelFilter != "" {
				labelFilter += ","
			}
			f := labelFilters[i]
			switch f.Operation {
			case libsveltosv1beta1.OperationEqual:
				labelFilter += fmt.Sprintf("%s=%s", f.Key, f.Value)
			case libsveltosv1beta1.OperationDifferent:
				labelFilter += fmt.Sprintf("%s!=%s", f.Key, f.Value)
			case libsveltosv1beta1.OperationHas:
				// Key exists, value is not checked
				labelFilter += f.Key
			case libsveltosv1beta1.OperationDoesNotHave:
				// Key does not exist
				labelFilter += fmt.Sprintf("!%s", f.Key)
			}
		}
	}

	return labelFilter
}

// isHealthyBasedOnCELRules verifies whether resource is healthy according to CEL rules
func isHealthyBasedOnCELRules(resource *unstructured.Unstructured, rules []libsveltosv1beta1.CELRule, logger logr.Logger,
) (healthy bool, err error) {

	if len(rules) == 0 {
		return true, nil
	}

	healthy, err = cel.EvaluateRules(resource, rules, logger)
	return healthy, err
}

// isHealthyBasedOnLua verifies whether resource is healthy according to Lua script
func isHealthyBasedOnLua(resource *unstructured.Unstructured, script string, logger logr.Logger,
) (healthy bool, msg string, err error) {

	if script == "" {
		return true, "", nil
	}

	l := lua.NewState()
	defer l.Close()

	sveltoslua.LoadModulesAndRegisterMethods(l)

	obj := sveltoslua.MapToTable(resource.UnstructuredContent())

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
		logger.V(logs.LogInfo).Info(sveltoslua.LuaTableError)
		return false, "", fmt.Errorf("%s", sveltoslua.LuaTableError)
	}

	goResult := sveltoslua.ToGoValue(tbl)
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
