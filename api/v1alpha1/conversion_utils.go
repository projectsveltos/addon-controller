/*
Copyright 2024. projectsveltos.io. All rights reserved.

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

package v1alpha1

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

func convertV1Alpha1SpecToV1Beta1(srcSpec *Spec) (configv1beta1.Spec, error) {
	dstSpec := configv1beta1.Spec{}

	dstSpec.ClusterRefs = srcSpec.ClusterRefs
	dstSpec.ContinueOnConflict = srcSpec.ContinueOnConflict
	dstSpec.DependsOn = srcSpec.DependsOn
	dstSpec.ExtraAnnotations = srcSpec.ExtraAnnotations
	dstSpec.ExtraLabels = srcSpec.ExtraLabels

	jsonData, err := json.Marshal(srcSpec.HelmCharts) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.HelmCharts: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.HelmCharts) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	jsonData, err = json.Marshal(srcSpec.KustomizationRefs) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.KustomizationRefs: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.KustomizationRefs) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	jsonData, err = json.Marshal(srcSpec.PolicyRefs) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.PolicyRefs: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.PolicyRefs) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	dstSpec.Reloader = srcSpec.Reloader
	dstSpec.SetRefs = srcSpec.SetRefs

	labelSelector, err := metav1.ParseToLabelSelector(string(srcSpec.ClusterSelector))
	if err != nil {
		return dstSpec, fmt.Errorf("error converting labels.Selector to metav1.Selector: %w", err)
	}
	dstSpec.ClusterSelector = libsveltosv1beta1.Selector{LabelSelector: *labelSelector}

	return dstSpec, nil
}

func convertV1Beta1SpecToV1Alpha1(srcSpec *configv1beta1.Spec) (Spec, error) {
	dstSpec := Spec{}

	dstSpec.ClusterRefs = srcSpec.ClusterRefs
	dstSpec.ContinueOnConflict = srcSpec.ContinueOnConflict
	dstSpec.DependsOn = srcSpec.DependsOn
	dstSpec.ExtraAnnotations = srcSpec.ExtraAnnotations
	dstSpec.ExtraLabels = srcSpec.ExtraLabels

	jsonData, err := json.Marshal(srcSpec.HelmCharts) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.HelmCharts: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.HelmCharts) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	jsonData, err = json.Marshal(srcSpec.KustomizationRefs) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.KustomizationRefs: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.KustomizationRefs) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	jsonData, err = json.Marshal(srcSpec.PolicyRefs) // Marshal the Spec field
	if err != nil {
		return dstSpec, fmt.Errorf("error marshaling Spec.PolicyRefs: %w", err)
	}
	err = json.Unmarshal(jsonData, &dstSpec.PolicyRefs) // Unmarshal to v1beta1 type
	if err != nil {
		return dstSpec, fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	dstSpec.Reloader = srcSpec.Reloader
	dstSpec.SetRefs = srcSpec.SetRefs

	return dstSpec, nil
}
