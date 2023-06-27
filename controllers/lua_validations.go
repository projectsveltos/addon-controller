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
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	lua "github.com/yuin/gopher-lua"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/projectsveltos/addon-controller/pkg/compliances"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

type complianceStatus struct {
	Valid   bool   `json:"valid"`
	Message string `json:"message"`
}

func getLuaValidations(clusterNamespace, clusterName string,
	clusterType *libsveltosv1alpha1.ClusterType, logger logr.Logger) (map[string][]byte, error) {

	logger.V(logs.LogDebug).Info("collect all lua validations")
	manager := compliances.GetManager()
	if manager == nil {
		const errMsg = "compliances manager is not ready"
		logger.V(logs.LogInfo).Info(errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	currentLuaPolicies, err := manager.GetClusterLuaPolicies(clusterNamespace, clusterName, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(err.Error())
		return nil, fmt.Errorf("%s", err.Error())
	}

	return currentLuaPolicies, nil
}

func runLuaValidations(ctx context.Context, scripts map[string][]byte,
	resources []*unstructured.Unstructured, logger logr.Logger) error {

	logger.V(logs.LogVerbose).Info("luaValidations")

	for key := range scripts {
		if err := luaValidation(ctx, scripts[key], resources, logger); err != nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("Lua validation %s failed %v", key, err))
			return fmt.Errorf("Lua validation %s failed %w", key, err)
		}
	}

	return nil
}

func luaValidation(ctx context.Context, script []byte,
	resources []*unstructured.Unstructured, logger logr.Logger) error {

	logger.V(logs.LogVerbose).Info("luaValidation")

	luaScript := string(script)

	if luaScript == "" {
		return nil
	}

	// Create a new Lua state
	l := lua.NewState()
	defer l.Close()

	// Load the Lua script
	if err := l.DoString(luaScript); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("doString failed: %v", err))
		return err
	}

	// Create an argument table
	argTable := l.NewTable()
	for _, resource := range resources {
		obj := mapToTable(resource.UnstructuredContent())
		argTable.Append(obj)
	}

	l.SetGlobal("resources", argTable)

	if err := l.CallByParam(lua.P{
		Fn:      l.GetGlobal("evaluate"), // name of Lua function
		NRet:    1,                       // number of returned values
		Protect: true,                    // return err or panic
	}, argTable); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to call evaluate function: %s", err.Error()))
		return err
	}

	lv := l.Get(-1)
	tbl, ok := lv.(*lua.LTable)
	if !ok {
		logger.V(logs.LogInfo).Info(luaTableError)
		return fmt.Errorf("%s", luaTableError)
	}

	goResult := toGoValue(tbl)
	resultJson, err := json.Marshal(goResult)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to marshal result: %v", err))
		return err
	}

	var result complianceStatus
	err = json.Unmarshal(resultJson, &result)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to marshal result: %v", err))
		return err
	}

	if result.Message != "" {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("message: %s", result.Message))
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("is a valid: %t", result.Valid))

	if !result.Valid {
		return fmt.Errorf("lua compliant validation failed: %s", result.Message)
	}

	return nil
}
