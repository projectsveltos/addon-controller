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
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	tunstructured "github.com/totherme/unstructured"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
)

const (
	substituitionRuleNameSeparator = ":"
	substituitionRuleNameLength    = 2
	apiVersionSeparator            = "/"
)

// isTemplate retusn true if policy is a template, i.e, it contains
// PolicyTemplate annotation
func isTemplate(policy *unstructured.Unstructured) bool {
	if policy == nil {
		return false
	}

	annotations := policy.GetAnnotations()
	_, ok := annotations[PolicyTemplate]
	return ok
}

// instantiateTemplate instantiate a policy template.
// Finds all properties referencing a SubstitutionRule and replace it with correct
// value given current state of the system.
// Returns the instantiated policy or an error if any occurs.
func instantiateTemplate(ctx context.Context, c client.Client, config *rest.Config,
	clusterSummary *configv1alpha1.ClusterSummary, policy string, logger logr.Logger,
) (string, error) {

	re := regexp.MustCompile(`"{{(.*?)}}"`)
	matches := re.FindAllStringSubmatch(policy, -1)
	for i := range matches {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Finding value for match %s", matches[i]))
		ruleName, err := getRuleName(matches[i][1])
		if err != nil {
			return "", err
		}

		propName, err := getPropName(matches[i][1])
		if err != nil {
			return "", err
		}

		logger.V(logs.LogInfo).Info(fmt.Sprintf("ruleName: %s -- propName: %s", ruleName, propName))
		substitutionValue, err := getSubstituitionValue(ctx, c, config, clusterSummary, ruleName, propName, logger)
		if err != nil {
			return policy, err
		}

		logger.V(logs.LogInfo).Info(fmt.Sprintf("value for match %s is %s", matches[i][0], substitutionValue))
		policy = strings.ReplaceAll(policy, matches[i][0], substitutionValue)
	}

	return policy, nil
}

/******************************************************************************************/
func getSubstituitionValue(ctx context.Context, c client.Client, config *rest.Config,
	clusterSummary *configv1alpha1.ClusterSummary, ruleName, propName string,
	logger logr.Logger) (string, error) {

	substitutionObject, err := getSubstituitionObject(ctx, c, config, clusterSummary, ruleName, logger)
	if err != nil {
		return "", err
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("got substitutionRule %s", substitutionObject.GetName()))

	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(substitutionObject)
	if err != nil {
		return "", err
	}

	substitutionValue, err := propValue(ctx, c, u, propName, logger)
	if err != nil {
		return "", err
	}

	return substitutionValue, nil
}

func getSubstituitionObject(ctx context.Context, c client.Client, config *rest.Config,
	clusterSummary *configv1alpha1.ClusterSummary, ruleName string,
	logger logr.Logger) (client.Object, error) {

	// Two SubstitutionRule are implicit:
	// - Cluster refers to CAPI Cluster currently being programmed
	// - ClusterFeature refers to ClusterFeature currently being programmed into the
	// CAPI Cluster
	switch ruleName {
	case "Cluster":
		return getCluster(ctx, c, clusterSummary)
	case configv1alpha1.ClusterFeatureKind:
		return getClusterFeatureOwner(ctx, c, clusterSummary)
	default:
		substituitionRule := &configv1alpha1.SubstitutionRule{}
		err := c.Get(ctx, types.NamespacedName{Name: ruleName}, substituitionRule)
		if err != nil {
			return nil, err
		}
		return getObject(ctx, c, config, clusterSummary, substituitionRule, logger)
	}
}

func getObject(ctx context.Context, c client.Client, config *rest.Config,
	clusterSummary *configv1alpha1.ClusterSummary, substituitionRule *configv1alpha1.SubstitutionRule,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	var name, namespace string
	var err error

	logger = logger.WithValues("clusterSummary", clusterSummary.Name)
	logger = logger.WithValues("SubstitutionRule", substituitionRule.Name)
	logger.V(logs.LogInfo).Info("get object")

	// Kubernetes name can contain only lowercase alphanumeric characters, '-' or '.'
	// If name contains ":" then assume it references another SubstitutionRule
	info := strings.Split(substituitionRule.Spec.Name, substituitionRuleNameSeparator)
	if len(info) > 1 {
		if len(info) != substituitionRuleNameLength {
			return nil, fmt.Errorf("incorrect syntax %s", substituitionRule.Spec.Name)
		}
		logger.V(logs.LogInfo).Info(fmt.Sprintf("get name from SubstituionRule %s (propName %s)",
			info[0], info[1]))
		name, err = getSubstituitionValue(ctx, c, config, clusterSummary, info[0], info[1], logger)
		if err != nil {
			return nil, err
		}
		err = validatePropValue(name)
		if err != nil {
			return nil, err
		}
	} else {
		name = substituitionRule.Spec.Name
	}

	info = strings.Split(substituitionRule.Spec.Namespace, substituitionRuleNameSeparator)
	if len(info) > 1 {
		if len(info) != substituitionRuleNameLength {
			return nil, fmt.Errorf("incorrect syntax %s", substituitionRule.Spec.Namespace)
		}
		logger.V(logs.LogInfo).Info(fmt.Sprintf("get namespace from SubstituionRule %s (propName %s)",
			info[0], info[1]))
		namespace, err = getSubstituitionValue(ctx, c, config, clusterSummary, info[0], info[1], logger)
		if err != nil {
			return nil, err
		}
		err = validatePropValue(namespace)
		if err != nil {
			return nil, err
		}
	} else {
		namespace = substituitionRule.Spec.Namespace
	}

	return getResource(ctx, config,
		substituitionRule.Spec.APIVersion, substituitionRule.Spec.Kind, namespace, name,
		logger)
}

// getRuleName returns SubstitutionRule name.
// match is expected to be in the form <SubstitutionRuleName>.<propName>
func getRuleName(match string) (string, error) {
	info := strings.Split(match, substituitionRuleNameSeparator)
	if len(info) == 0 {
		return "", fmt.Errorf("substitutionRule not found in %s", match)
	}

	return strings.TrimSpace(info[0]), nil
}

// getPropName returns prop name.
// match is expected to be in the form <SubstitutionRuleName>.<propName>
func getPropName(match string) (string, error) {
	info := strings.Split(match, substituitionRuleNameSeparator)
	if len(info) != substituitionRuleNameLength {
		return "", fmt.Errorf("prop name not found in %s", match)
	}

	return strings.TrimSpace(info[1]), nil
}

func getResource(ctx context.Context, config *rest.Config,
	apiVersion, kind, namespace, name string, logger logr.Logger) (*unstructured.Unstructured, error) {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("get object kind: %s apiversion: %s namespace: %s name: %s",
		kind, apiVersion, namespace, name))
	const partInfo = 2

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, err
	}

	apiResourceList, err := clientset.Discovery().ServerResourcesForGroupVersion(gv.String())
	if err != nil {
		return nil, err
	}

	parts := strings.Split(apiVersion, apiVersionSeparator)
	if len(parts) != partInfo {
		return nil, fmt.Errorf(`apiVersion [%s] must be "group/version"`,
			apiVersion)
	}
	group, version := parts[0], parts[1]

	for i := range apiResourceList.APIResources {
		apiResource := &apiResourceList.APIResources[i]
		if apiResource.Kind == kind {
			gvr := schema.GroupVersionResource{
				Group:    group,
				Version:  version,
				Resource: apiResource.Name,
			}

			if namespace == "" {
				return dynamicClient.Resource(gvr).
					Get(ctx, name, metav1.GetOptions{})
			}

			logger.V(logs.LogInfo).Info(fmt.Sprintf("get resource %s/%s for Group:%s Version: %s",
				namespace, name, apiResource.Group, apiResource.Version))
			return dynamicClient.Resource(gvr).
				Namespace(namespace).
				Get(ctx, name, metav1.GetOptions{})
		}
	}
	return nil, fmt.Errorf("could not find resource with with version %v, "+
		"kind %v, and name %v in namespace %v",
		apiVersion, kind, name, namespace)
}

func getCluster(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary,
) (*clusterv1.Cluster, error) {

	cluster := &clusterv1.Cluster{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterSummary.Spec.ClusterNamespace, Name: clusterSummary.Spec.ClusterName},
		cluster)
	return cluster, err
}

/*
// TODO: keep working on this an replace tunstructured
func propValue(ctx context.Context, c client.Client, unstructured map[string]interface{}, propName string) (string, error) {
	propInfo := strings.Split(propName, "/")

	v, ok := unstructured[propInfo[0]]
	if !ok {
		return "", fmt.Errorf("property %s not found", propInfo[0])
	}

	if len(propInfo) == 1 {
		return fmt.Sprintf("%v", reflect.ValueOf(v)), nil
	}

	if reflect.ValueOf(v).Kind() == reflect.Slice {
		return "", fmt.Errorf("slice not supported")
	} else if reflect.ValueOf(v).Kind() == reflect.Map {
		return "", fmt.Errorf("map not supported")
	}

	if _, ok := v.(map[string]interface{}); !ok {
		return "", fmt.Errorf("incorrect formar for prop %s", propName)
	}

	return propValue(ctx, c, v.(map[string]interface{}), strings.Join(propInfo[1:], "."))
}
*/

// propValues given an unstructured map (taken from client.Object) returns value of property propName.
// If an error occurs, for instance such property does not exist, an error is returned.
// Some example of prop name:
// - /metadata/labels/<labelKey> to return value associated with label <labelKey>
// - /metadata/spec/replicas to return value associated with replicas property of a Deployment
// - /metadata/spec/<sliceField>/<sliceIndex> to return value associated with field <sliceField> at index <sliceIndex>
func propValue(ctx context.Context, c client.Client, unstructuredContent map[string]interface{},
	propName string, logger logr.Logger) (string, error) {

	tmpData, err := json.Marshal(unstructuredContent)
	if err != nil {
		return "", err
	}
	jsonData, err := tunstructured.ParseJSON(string(tmpData))
	if err != nil {
		return "", nil
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("GetByPointer: propName:%s", propName))

	result, err := jsonData.GetByPointer(propName)
	if err != nil {
		return "", err
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("GetByPointer: propName:%s got result: %v",
		propName, result.RawValue()))

	return printValue(result)
}

func printValue(result tunstructured.Data) (string, error) {
	if result.IsString() {
		return stringResult(result)
	} else if result.IsBool() {
		return boolResult(result)
	} else if result.IsList() {
		return listResult(result)
	} else if result.IsOb() {
		return obResult(result)
	}

	return fmt.Sprintf("%v", result.RawValue()), nil
}

// stringResult stringify result
func stringResult(result tunstructured.Data) (string, error) {
	stringResult, err := result.StringValue()
	if err != nil {
		return "", err
	}

	return stringResult, nil
}

// boolResult stringify result
func boolResult(result tunstructured.Data) (string, error) {
	boolResult, err := result.BoolValue()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%t", boolResult), nil
}

// listResult stringify result
func listResult(result tunstructured.Data) (string, error) {
	listResult, err := result.ListValue()
	if err != nil {
		return "", err
	}

	return listToString(listResult)
}

func listToString(list []tunstructured.Data) (string, error) {
	result := "[ "
	for i := range list {
		tmp, err := printValue(list[i])
		if err != nil {
			return "", err
		}
		result += tmp + ","
	}

	result += "]"
	return result, nil
}

// obResult stringify result
func obResult(result tunstructured.Data) (string, error) {
	obResult, err := result.ObValue()
	if err != nil {
		return "", err
	}

	out, err := yaml.Marshal(obResult)
	if err != nil {
		return "", err
	}
	return mapToString(string(out)), nil
}

func mapToString(s string) string {
	return fmt.Sprintf("{%s}", strings.ReplaceAll(s, "\n", ","))
}

// validatePropValue validates value. Value is used to dynamically
// build name and or namespace of an object.
// Kubernetes name:
// - contain no more than 253 characters
// - contain only lowercase alphanumeric characters, '-' or '.'
// - start with an alphanumeric character
// - end with an alphanumeric character
// Validates value satisfies such requirement
func validatePropValue(value string) error {
	errors := validation.IsQualifiedName(value)
	if len(errors) != 0 {
		return fmt.Errorf("value %s is not valid name. Errors: %v", value, errors)
	}

	return nil
}
