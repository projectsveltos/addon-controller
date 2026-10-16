/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

// Package healthmonitoring joins the health reports of one cluster with the ClusterSummaries
// that own the HealthChecks behind them. It has no client and no locks: callers (ui-backend,
// sveltosctl) build the inputs from their own caches or from Lists and pass them in, so the
// cluster list and the per-cluster detail are computed by the same code.
package healthmonitoring

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// State is the health state of a profile or of a cluster.
type State string

const (
	// StateNotEnabled means no profile has health monitoring enabled.
	StateNotEnabled State = "NotEnabled"

	// StateNoData means monitoring is enabled but no report has been received yet.
	StateNoData State = "NoData"

	StateHealthy     State = "Healthy"
	StateSuspended   State = "Suspended"
	StateProgressing State = "Progressing"
	StateDegraded    State = "Degraded"
)

// SummaryInput is what the join needs from one ClusterSummary.
type SummaryInput struct {
	// Summary is the ClusterSummary.
	Summary corev1.ObjectReference

	// Profile is the ClusterProfile or Profile owning the ClusterSummary.
	Profile corev1.ObjectReference

	// Enabled is true when the profile has health monitoring enabled.
	Enabled bool

	// ListedChecks are the HealthCheck names the profile lists in healthMonitoring.healthChecks.
	ListedChecks []string

	// GeneratedCheck is the name of the HealthCheck generated for the ClusterSummary,
	// empty if it has not been created yet.
	GeneratedCheck string
}

// ReportInput is the compact copy of one HealthCheckReport of the cluster.
type ReportInput struct {
	HealthCheckName string
	Resources       []ResourceInput
}

// ResourceInput is the status of one resource in a report.
type ResourceInput struct {
	ObjectRef corev1.ObjectReference
	Status    libsveltosv1beta1.HealthStatus
	Message   string
}

// ClusterHealth is the result of Evaluate for one cluster.
type ClusterHealth struct {
	State State

	// Profiles has one entry per ClusterSummary with health monitoring enabled.
	Profiles []ProfileHealth

	// Resources are the resources not Healthy, one entry per resource, worst status first.
	Resources []UnhealthyResource
}

// ProfileHealth is the health state of one profile on the cluster.
type ProfileHealth struct {
	Profile corev1.ObjectReference
	Summary corev1.ObjectReference
	State   State
}

// UnhealthyResource is a resource reported not Healthy.
type UnhealthyResource struct {
	ObjectRef corev1.ObjectReference
	Status    libsveltosv1beta1.HealthStatus
	Message   string

	// Profiles are the profiles owning the HealthCheck that reported the resource.
	// Empty when no profile owns it.
	Profiles []corev1.ObjectReference

	// HealthCheckName is the HealthCheck that reported the resource. Set only when Profiles is empty.
	HealthCheckName string
}

// Evaluate runs the join for ONE cluster: pass only the ClusterSummaries and the reports of that cluster.
//   - A summary owns its generated HealthCheck and the ones its profile lists.
//   - A report is attributed to every enabled summary owning its HealthCheck. A report
//     nobody owns is "unclaimed" and its resources carry the HealthCheck name instead of profiles.
//   - A profile has no data until a report of one of its checks exists. A report with
//     no unhealthy resources makes it Healthy.
//   - The cluster state is the worst state among the profiles and the listed resources.
func Evaluate(summaries []SummaryInput, reports []ReportInput) ClusterHealth {
	enabled := enabledSummaries(summaries)
	if len(enabled) == 0 {
		return ClusterHealth{State: StateNotEnabled}
	}

	profiles := make([]ProfileHealth, len(enabled))
	for i := range enabled {
		profiles[i] = ProfileHealth{Profile: enabled[i].Profile, Summary: enabled[i].Summary, State: StateNoData}
	}

	owners := ownersByCheck(enabled)
	collector := newResourceCollector()
	for i := range reports {
		owning := owners[reports[i].HealthCheckName]
		reportState := StateHealthy
		for j := range reports[i].Resources {
			resource := &reports[i].Resources[j]
			reportState = WorstState(reportState, StateOf(resource.Status))
			if resource.Status != libsveltosv1beta1.HealthStatusHealthy {
				collector.add(resource, owningProfiles(profiles, owning), reports[i].HealthCheckName)
			}
		}
		for _, index := range owning {
			profiles[index].State = WorstState(profiles[index].State, reportState)
		}
	}

	resources := collector.sorted()
	state := StateNoData
	for i := range profiles {
		state = WorstState(state, profiles[i].State)
	}
	for i := range resources {
		state = WorstState(state, StateOf(resources[i].Status))
	}

	return ClusterHealth{State: state, Profiles: profiles, Resources: resources}
}

// StateOf converts the status of a resource into a State.
func StateOf(status libsveltosv1beta1.HealthStatus) State {
	switch status {
	case libsveltosv1beta1.HealthStatusHealthy:
		return StateHealthy
	case libsveltosv1beta1.HealthStatusSuspended:
		return StateSuspended
	case libsveltosv1beta1.HealthStatusProgressing:
		return StateProgressing
	case libsveltosv1beta1.HealthStatusDegraded:
		return StateDegraded
	}
	return StateNoData
}

// WorstState returns the worse of two states. From best to worst: NotEnabled, NoData, Healthy,
// Suspended, Progressing, Degraded.
func WorstState(a, b State) State {
	if severity(b) > severity(a) {
		return b
	}
	return a
}

var (
	// statesBestToWorst is the order used to compare states.
	statesBestToWorst = []State{
		StateNotEnabled, StateNoData, StateHealthy, StateSuspended, StateProgressing, StateDegraded,
	}
)

func severity(state State) int {
	for i := range statesBestToWorst {
		if statesBestToWorst[i] == state {
			return i
		}
	}
	return 0
}

// GeneratedCheckOwner returns the ClusterSummary a HealthCheck has been generated for. It
// reads only the metadata, so a metadata only cache is enough. The returned bool is false
// when the object does not carry the labels and annotations of a generated check.
func GeneratedCheckOwner(obj metav1.Object) (*corev1.ObjectReference, bool) {
	if _, ok := obj.GetLabels()[configv1beta1.HealthMonitoringClusterSummaryUIDLabel]; !ok {
		return nil, false
	}

	annotations := obj.GetAnnotations()
	namespace := annotations[configv1beta1.HealthMonitoringClusterSummaryNamespaceAnnotation]
	name := annotations[configv1beta1.HealthMonitoringClusterSummaryNameAnnotation]
	if namespace == "" || name == "" {
		return nil, false
	}

	return &corev1.ObjectReference{
		APIVersion: configv1beta1.GroupVersion.String(),
		Kind:       configv1beta1.ClusterSummaryKind,
		Namespace:  namespace,
		Name:       name,
	}, true
}

// NewSummaryInput builds the input for a ClusterSummary. generatedCheck is the name of the HealthCheck
// generated for it, empty if unknown. Listed HealthChecks are honored for a ClusterProfile only: a
// Profile gets the default checks only (see HealthMonitoring.HealthChecks).
func NewSummaryInput(clusterSummary *configv1beta1.ClusterSummary, generatedCheck string) (SummaryInput, error) {
	profileRef, err := configv1beta1.GetProfileRef(clusterSummary)
	if err != nil {
		return SummaryInput{}, err
	}
	if profileRef.Kind == configv1beta1.ProfileKind {
		profileRef.Namespace = clusterSummary.Namespace
	}

	input := SummaryInput{
		Summary: corev1.ObjectReference{
			APIVersion: configv1beta1.GroupVersion.String(),
			Kind:       configv1beta1.ClusterSummaryKind,
			Namespace:  clusterSummary.Namespace,
			Name:       clusterSummary.Name,
		},
		Profile:        *profileRef,
		GeneratedCheck: generatedCheck,
	}

	healthMonitoring := clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring
	if healthMonitoring == nil || !healthMonitoring.Enabled {
		return input, nil
	}

	input.Enabled = true
	if profileRef.Kind == configv1beta1.ClusterProfileKind {
		input.ListedChecks = append([]string{}, healthMonitoring.HealthChecks...)
	}

	return input, nil
}

// NewReportInput builds the input for a HealthCheckReport.
func NewReportInput(report *libsveltosv1beta1.HealthCheckReport) ReportInput {
	resources := make([]ResourceInput, len(report.Spec.ResourceStatuses))
	for i := range report.Spec.ResourceStatuses {
		resources[i] = ResourceInput{
			ObjectRef: report.Spec.ResourceStatuses[i].ObjectRef,
			Status:    report.Spec.ResourceStatuses[i].HealthStatus,
			Message:   report.Spec.ResourceStatuses[i].Message,
		}
	}

	return ReportInput{HealthCheckName: report.Spec.HealthCheckName, Resources: resources}
}

func enabledSummaries(summaries []SummaryInput) []SummaryInput {
	enabled := make([]SummaryInput, 0, len(summaries))
	for i := range summaries {
		if summaries[i].Enabled {
			enabled = append(enabled, summaries[i])
		}
	}
	return enabled
}

// ownersByCheck maps a HealthCheck name to the indexes (in summaries) of the summaries owning it.
func ownersByCheck(summaries []SummaryInput) map[string][]int {
	owners := make(map[string][]int)
	for i := range summaries {
		names := summaries[i].ListedChecks
		if summaries[i].GeneratedCheck != "" {
			names = append([]string{summaries[i].GeneratedCheck}, names...)
		}
		for _, name := range uniqueNames(names) {
			owners[name] = append(owners[name], i)
		}
	}
	return owners
}

func uniqueNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	return unique
}

func owningProfiles(profiles []ProfileHealth, indexes []int) []corev1.ObjectReference {
	refs := make([]corev1.ObjectReference, 0, len(indexes))
	for _, index := range indexes {
		refs = append(refs, profiles[index].Profile)
	}
	return refs
}

// resourceKey identifies a resource: the same resource reported by several checks is listed once.
type resourceKey struct {
	apiVersion string
	kind       string
	namespace  string
	name       string
}

type resourceCollector struct {
	byKey map[resourceKey]*UnhealthyResource
	order []resourceKey
}

func newResourceCollector() *resourceCollector {
	return &resourceCollector{byKey: make(map[resourceKey]*UnhealthyResource)}
}

// add records an unhealthy resource. If the resource is already known, the profiles are merged
// and the worst status, with its message, is kept.
func (c *resourceCollector) add(resource *ResourceInput, profiles []corev1.ObjectReference, healthCheckName string) {
	key := resourceKey{
		apiVersion: resource.ObjectRef.APIVersion,
		kind:       resource.ObjectRef.Kind,
		namespace:  resource.ObjectRef.Namespace,
		name:       resource.ObjectRef.Name,
	}

	existing, ok := c.byKey[key]
	if !ok {
		entry := &UnhealthyResource{
			ObjectRef: resource.ObjectRef,
			Status:    resource.Status,
			Message:   resource.Message,
			Profiles:  profiles,
		}
		if len(profiles) == 0 {
			entry.HealthCheckName = healthCheckName
		}
		c.byKey[key] = entry
		c.order = append(c.order, key)
		return
	}

	if severity(StateOf(resource.Status)) > severity(StateOf(existing.Status)) {
		existing.Status = resource.Status
		existing.Message = resource.Message
	}
	for i := range profiles {
		if !containsRef(existing.Profiles, &profiles[i]) {
			existing.Profiles = append(existing.Profiles, profiles[i])
		}
	}
	if len(existing.Profiles) > 0 {
		existing.HealthCheckName = ""
	}
}

// sorted returns the collected resources, worst status first, then by kind, namespace and name.
func (c *resourceCollector) sorted() []UnhealthyResource {
	resources := make([]UnhealthyResource, 0, len(c.order))
	for _, key := range c.order {
		resources = append(resources, *c.byKey[key])
	}

	sort.SliceStable(resources, func(i, j int) bool {
		si, sj := severity(StateOf(resources[i].Status)), severity(StateOf(resources[j].Status))
		if si != sj {
			return si > sj
		}
		a, b := resources[i].ObjectRef, resources[j].ObjectRef
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	return resources
}

func containsRef(refs []corev1.ObjectReference, ref *corev1.ObjectReference) bool {
	for i := range refs {
		if refs[i].Kind == ref.Kind && refs[i].Namespace == ref.Namespace && refs[i].Name == ref.Name {
			return true
		}
	}
	return false
}
