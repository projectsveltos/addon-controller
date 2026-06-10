/*
Copyright 2022-25. projectsveltos.io. All rights reserved.

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
	"sort"
	"strings"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

type SortedPolicyRefs []configv1beta1.PolicyRef

func (a SortedPolicyRefs) Len() int      { return len(a) }
func (a SortedPolicyRefs) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedPolicyRefs) Less(i, j int) bool {
	if a[i].Kind != a[j].Kind {
		return a[i].Kind < a[j].Kind
	}
	if a[i].Namespace != a[j].Namespace {
		return a[i].Namespace < a[j].Namespace
	}
	return a[i].Name < a[j].Name
}

func getSortedPolicyRefs(policyRef []configv1beta1.PolicyRef) []configv1beta1.PolicyRef {
	sortedPolicyRefs := make([]configv1beta1.PolicyRef, len(policyRef))
	copy(sortedPolicyRefs, policyRef)

	sort.Sort(SortedPolicyRefs(sortedPolicyRefs))
	return sortedPolicyRefs
}

type SortedHelmCharts []configv1beta1.HelmChart

func (a SortedHelmCharts) Len() int      { return len(a) }
func (a SortedHelmCharts) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedHelmCharts) Less(i, j int) bool {
	if a[i].RepositoryURL != a[j].RepositoryURL {
		return a[i].RepositoryURL < a[j].RepositoryURL
	}
	if a[i].ReleaseNamespace != a[j].ReleaseNamespace {
		return a[i].ReleaseNamespace < a[j].ReleaseNamespace
	}
	return a[i].ReleaseName < a[j].ReleaseName
}

func getSortedHelmCharts(helmCharts []configv1beta1.HelmChart) []configv1beta1.HelmChart {
	sortedHelmCharts := make([]configv1beta1.HelmChart, len(helmCharts))
	copy(sortedHelmCharts, helmCharts)

	sort.Sort(SortedHelmCharts(sortedHelmCharts))
	return sortedHelmCharts
}

type SortedKustomizationRefs []configv1beta1.KustomizationRef

func (a SortedKustomizationRefs) Len() int      { return len(a) }
func (a SortedKustomizationRefs) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedKustomizationRefs) Less(i, j int) bool {
	if a[i].Kind != a[j].Kind {
		return a[i].Kind < a[j].Kind
	}
	if a[i].Namespace != a[j].Namespace {
		return a[i].Namespace < a[j].Namespace
	}
	return a[i].Name < a[j].Name
}

func getSortedKustomizationRefs(kustomizationRefs []configv1beta1.KustomizationRef) []configv1beta1.KustomizationRef {
	sortedKustomizationRefs := make([]configv1beta1.KustomizationRef, len(kustomizationRefs))
	copy(sortedKustomizationRefs, kustomizationRefs)

	sort.Sort(SortedKustomizationRefs(sortedKustomizationRefs))
	return sortedKustomizationRefs
}

type SortedTemplateResourceRefs []configv1beta1.TemplateResourceRef

func (a SortedTemplateResourceRefs) Len() int      { return len(a) }
func (a SortedTemplateResourceRefs) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedTemplateResourceRefs) Less(i, j int) bool {
	return a[i].Identifier < a[j].Identifier
}

func getSortedTemplateResourceRefs(templateResourceRef []configv1beta1.TemplateResourceRef) []configv1beta1.TemplateResourceRef {
	sortedTemplateResourceRefs := make([]configv1beta1.TemplateResourceRef, len(templateResourceRef))
	copy(sortedTemplateResourceRefs, templateResourceRef)

	sort.Sort(SortedTemplateResourceRefs(sortedTemplateResourceRefs))
	return sortedTemplateResourceRefs
}

type SortedValidateHealths []libsveltosv1beta1.ValidateHealth

func (a SortedValidateHealths) Len() int      { return len(a) }
func (a SortedValidateHealths) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedValidateHealths) Less(i, j int) bool {
	return a[i].Name < a[j].Name
}

func getSortedValidateHealths(validateHealths []libsveltosv1beta1.ValidateHealth) []libsveltosv1beta1.ValidateHealth {
	sortedValidateHealths := make([]libsveltosv1beta1.ValidateHealth, len(validateHealths))
	copy(sortedValidateHealths, validateHealths)

	sort.Sort(SortedValidateHealths(sortedValidateHealths))
	return sortedValidateHealths
}

type SortedPatches []libsveltosv1beta1.Patch

func (a SortedPatches) Len() int      { return len(a) }
func (a SortedPatches) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedPatches) Less(i, j int) bool {
	return a[i].Patch < a[j].Patch
}

func getSortedPatches(patches []libsveltosv1beta1.Patch) []libsveltosv1beta1.Patch {
	sortedPatches := make([]libsveltosv1beta1.Patch, len(patches))
	copy(sortedPatches, patches)

	sort.Sort(SortedPatches(sortedPatches))
	return sortedPatches
}

// driftExclusionEntry pairs a DriftExclusion with its pre-computed sort key
// so the sort comparator can reference the slice being sorted.
type driftExclusionEntry struct {
	item libsveltosv1beta1.DriftExclusion
	key  string
}

func getSortedDriftExclusions(driftExclusions []libsveltosv1beta1.DriftExclusion) []libsveltosv1beta1.DriftExclusion {
	// Pre-compute a stable sort key for each element once. The key encodes
	// sorted Paths (so path order doesn't matter) plus Target fields as a
	// tiebreaker (so entries with identical paths sort deterministically).
	entries := make([]driftExclusionEntry, len(driftExclusions))
	for i := range driftExclusions {
		p := make([]string, len(driftExclusions[i].Paths))
		copy(p, driftExclusions[i].Paths)
		sort.Strings(p)
		key := strings.Join(p, "\x00")
		if t := driftExclusions[i].Target; t != nil {
			key += "\x01" + t.Group + "\x00" + t.Version + "\x00" + t.Kind + "\x00" + t.Namespace + "\x00" + t.Name
		}
		entries[i] = driftExclusionEntry{item: driftExclusions[i], key: key}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})

	sorted := make([]libsveltosv1beta1.DriftExclusion, len(entries))
	for i := range entries {
		sorted[i] = entries[i].item
	}
	return sorted
}

type SortedValueFroms []configv1beta1.ValueFrom

func (a SortedValueFroms) Len() int      { return len(a) }
func (a SortedValueFroms) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedValueFroms) Less(i, j int) bool {
	if a[i].Kind != a[j].Kind {
		return a[i].Kind < a[j].Kind
	}
	if a[i].Namespace != a[j].Namespace {
		return a[i].Namespace < a[j].Namespace
	}
	return a[i].Name < a[j].Name
}

func getSortedValueFroms(valueFroms []configv1beta1.ValueFrom) []configv1beta1.ValueFrom {
	sorted := make([]configv1beta1.ValueFrom, len(valueFroms))
	copy(sorted, valueFroms)
	sort.Sort(SortedValueFroms(sorted))
	return sorted
}

func getSortedKeys(m interface{}) []string {
	var keys []string
	switch v := m.(type) {
	case map[string]string:
		keys = make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
	case map[string][]byte:
		keys = make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
