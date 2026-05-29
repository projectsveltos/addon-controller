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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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

const (
	defaultPrometheusPath = "api/v1/query"
	metricsHTTPTimeout    = 30 * time.Second
)

type healthStatus struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

type HealthCheckError struct {
	FeatureID   libsveltosv1beta1.FeatureID
	CheckName   string
	InternalErr error
}

func (e *HealthCheckError) Error() string {
	return fmt.Sprintf("health check '%s' for feature %s failed: %v",
		e.CheckName, e.FeatureID, e.InternalErr)
}

func (e *HealthCheckError) Unwrap() error {
	return e.InternalErr
}

// prometheusResponse is the top-level Prometheus HTTP API response.
type prometheusResponse struct {
	Status string         `json:"status"`
	Error  string         `json:"error,omitempty"`
	Data   prometheusData `json:"data"`
}

// prometheusData holds the result type and raw result from a Prometheus query.
type prometheusData struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
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
			return &HealthCheckError{
				FeatureID:   featureID,
				CheckName:   check.Name,
				InternalErr: err,
			}
		}
	}

	return nil
}

func validateHealthPolicy(ctx context.Context, remoteConfig *rest.Config, check *libsveltosv1beta1.ValidateHealth,
	isDelete bool, logger logr.Logger) error {

	l := logger.WithValues("validation", check.Name)
	l.V(logs.LogDebug).Info("running health validation")

	metricsData, err := fetchMetrics(ctx, remoteConfig, check, l)
	if err != nil {
		return err
	}

	// Metrics-only check: no resource kind specified, evaluate the script once.
	if check.Kind == "" {
		return evaluateMetricsOnly(check, metricsData, l)
	}

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
		healthy, msg, err = isHealthyBasedOnLua(&list.Items[i], check.Script, metricsData, logger)
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

// evaluateMetricsOnly runs the Lua script once with no resource argument,
// using only the metric values collected from MetricSource.
func evaluateMetricsOnly(check *libsveltosv1beta1.ValidateHealth, metricsData map[string]float64,
	logger logr.Logger) error {

	if check.Script == "" {
		return fmt.Errorf("validateHealth %s: metric-only check (no kind set) requires a Lua script", check.Name)
	}

	healthy, msg, err := isHealthyBasedOnLua(nil, check.Script, metricsData, logger)
	if err != nil {
		return err
	}
	if !healthy {
		return fmt.Errorf("metric health check failed: %s", msg)
	}
	return nil
}

// fetchMetrics queries the metric endpoint defined in check.MetricSource and
// returns a map from query name to scalar float value. Returns nil when no
// MetricSource is configured.
func fetchMetrics(ctx context.Context, remoteConfig *rest.Config,
	check *libsveltosv1beta1.ValidateHealth, logger logr.Logger) (map[string]float64, error) {

	if check.MetricSource == nil || len(check.MetricQueries) == 0 {
		return nil, nil
	}

	source := check.MetricSource

	authHeader, err := buildAuthHeader(ctx, remoteConfig, source)
	if err != nil {
		return nil, fmt.Errorf("reading metric credentials: %w", err)
	}

	path := source.Path
	if path == "" {
		path = defaultPrometheusPath
	}
	path = strings.TrimLeft(path, "/")

	endpoint := strings.TrimRight(source.URL, "/") + "/" + path

	httpClient := &http.Client{Timeout: metricsHTTPTimeout}

	results := make(map[string]float64, len(check.MetricQueries))
	for i := range check.MetricQueries {
		q := &check.MetricQueries[i]
		logger.V(logs.LogDebug).Info("querying metric", "name", q.Name, "query", q.Query)
		value, err := queryMetricEndpoint(ctx, httpClient, endpoint, q.Query, authHeader)
		if err != nil {
			return nil, fmt.Errorf("metric query %q: %w", q.Name, err)
		}
		results[q.Name] = value
	}
	return results, nil
}

// queryMetricEndpoint executes a single instant query against a
// Prometheus-compatible endpoint and returns the scalar result.
func queryMetricEndpoint(ctx context.Context, httpClient *http.Client,
	endpoint, query, authHeader string) (float64, error) {

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}

	params := req.URL.Query()
	params.Set("query", query)
	req.URL.RawQuery = params.Encode()

	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("querying Prometheus: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("metric source returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var promResp prometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return 0, fmt.Errorf("decoding Prometheus response: %w", err)
	}
	if promResp.Status != "success" {
		return 0, fmt.Errorf("metric query failed: %s", promResp.Error)
	}

	return extractScalar(promResp.Data)
}

// extractScalar pulls a single float64 from a Prometheus-compatible instant-query result.
// Accepts scalar and single-element vector result types.
func extractScalar(data prometheusData) (float64, error) {
	switch data.ResultType {
	case "scalar":
		// scalar result: [unixTime, "value"]
		var pair [2]json.RawMessage
		if err := json.Unmarshal(data.Result, &pair); err != nil {
			return 0, fmt.Errorf("parsing scalar result: %w", err)
		}
		return parsePromValue(pair[1])
	case "vector":
		var results []struct {
			Value [2]json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(data.Result, &results); err != nil {
			return 0, fmt.Errorf("parsing vector result: %w", err)
		}
		if len(results) == 0 {
			return 0, fmt.Errorf("metric query returned no data")
		}
		if len(results) > 1 {
			return 0, fmt.Errorf("metric query returned %d series; use an aggregation (e.g. sum()) to produce a single value",
				len(results))
		}
		return parsePromValue(results[0].Value[1])
	default:
		return 0, fmt.Errorf("unsupported Prometheus result type %q; use an instant query returning scalar or vector",
			data.ResultType)
	}
}

// parsePromValue converts a quoted Prometheus value string (e.g. "3.14") to float64.
func parsePromValue(raw json.RawMessage) (float64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("parsing value token: %w", err)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("converting %q to float64: %w", s, err)
	}
	return v, nil
}

// buildAuthHeader reads credentials from source.SecretRef on the managed cluster
// and returns the appropriate Authorization header value, or empty string when
// no SecretRef is set.
func buildAuthHeader(ctx context.Context, remoteConfig *rest.Config,
	source *libsveltosv1beta1.MetricSource) (string, error) {

	if source.SecretRef == nil {
		return "", nil
	}

	dynamicClient, err := dynamic.NewForConfig(remoteConfig)
	if err != nil {
		return "", fmt.Errorf("creating dynamic client: %w", err)
	}

	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	obj, err := dynamicClient.Resource(secretGVR).
		Namespace(source.SecretRef.Namespace).
		Get(ctx, source.SecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting Secret %s/%s: %w",
			source.SecretRef.Namespace, source.SecretRef.Name, err)
	}

	// Secret.data values arrive base64-encoded from the Kubernetes API.
	rawData, _, _ := unstructured.NestedStringMap(obj.Object, "data")

	if b64Token, ok := rawData["token"]; ok {
		token, err := base64.StdEncoding.DecodeString(b64Token)
		if err != nil {
			return "", fmt.Errorf("decoding token from Secret %s/%s: %w",
				source.SecretRef.Namespace, source.SecretRef.Name, err)
		}
		return "Bearer " + string(token), nil
	}

	b64User, hasUser := rawData["username"]
	b64Pass, hasPass := rawData["password"]
	if hasUser && hasPass {
		username, err := base64.StdEncoding.DecodeString(b64User)
		if err != nil {
			return "", fmt.Errorf("decoding username from Secret %s/%s: %w",
				source.SecretRef.Namespace, source.SecretRef.Name, err)
		}
		password, err := base64.StdEncoding.DecodeString(b64Pass)
		if err != nil {
			return "", fmt.Errorf("decoding password from Secret %s/%s: %w",
				source.SecretRef.Namespace, source.SecretRef.Name, err)
		}
		encoded := base64.StdEncoding.EncodeToString(
			[]byte(string(username) + ":" + string(password)))
		return "Basic " + encoded, nil
	}

	return "", fmt.Errorf("secret %s/%s must contain key \"token\" or keys \"username\" and \"password\"",
		source.SecretRef.Namespace, source.SecretRef.Name)
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

// isHealthyBasedOnLua verifies whether resource is healthy according to a Lua script.
// resource may be nil for metric-only checks; in that case evaluate is called with
// lua.LNil and obj is set to nil in the global environment.
// metricsData is injected as a global "metrics" table so scripts can access metric
// values via metrics["<name>"]. An empty or nil map results in an empty table.
func isHealthyBasedOnLua(resource *unstructured.Unstructured, script string,
	metricsData map[string]float64, logger logr.Logger,
) (healthy bool, msg string, err error) {

	if script == "" {
		return true, "", nil
	}

	l := lua.NewState()
	defer l.Close()

	sveltoslua.LoadModulesAndRegisterMethods(l)

	// Always inject a metrics table so scripts can safely reference metrics["x"]
	// even when no queries were executed.
	metricsTable := l.NewTable()
	for k, v := range metricsData {
		metricsTable.RawSetString(k, lua.LNumber(v))
	}
	l.SetGlobal("metrics", metricsTable)

	err = l.DoString(script)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("doString failed: %v", err))
		return false, "", err
	}

	var luaArg lua.LValue
	if resource != nil {
		obj := sveltoslua.MapToTable(resource.UnstructuredContent())
		l.SetGlobal("obj", obj)
		luaArg = obj
	} else {
		l.SetGlobal("obj", lua.LNil)
		luaArg = lua.LNil
	}

	err = l.CallByParam(lua.P{
		Fn:      l.GetGlobal("evaluate"), // name of Lua function
		NRet:    1,                       // number of returned values
		Protect: true,                    // return err or panic
	}, luaArg)
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
		if resource != nil {
			return false, fmt.Sprintf("resource %s/%s is not healthy: %s",
				resource.GetNamespace(), resource.GetName(), result.Message), nil
		}
		return false, result.Message, nil
	}

	return true, "", nil
}
