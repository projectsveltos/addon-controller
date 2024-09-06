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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	conversion "k8s.io/apimachinery/pkg/conversion"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

func Convert_v1alpha1_Spec_To_v1beta1_Spec(srcSpec *Spec, dstSpec *configv1beta1.Spec, scope conversion.Scope,
) error {

	if err := autoConvert_v1alpha1_Spec_To_v1beta1_Spec(srcSpec, dstSpec, nil); err != nil {
		return err
	}

	labelSelector, err := metav1.ParseToLabelSelector(string(srcSpec.ClusterSelector))
	if err != nil {
		return fmt.Errorf("error converting labels.Selector to metav1.Selector: %w", err)
	}
	dstSpec.ClusterSelector = libsveltosv1beta1.Selector{LabelSelector: *labelSelector}

	return nil
}

func Convert_v1beta1_Spec_To_v1alpha1_Spec(srcSpec *configv1beta1.Spec, dstSpec *Spec, scope conversion.Scope,
) error {

	if err := autoConvert_v1beta1_Spec_To_v1alpha1_Spec(srcSpec, dstSpec, nil); err != nil {
		return err
	}

	labelSelector, err := srcSpec.ClusterSelector.ToSelector()
	if err != nil {
		return fmt.Errorf("failed to convert : %w", err)
	}

	dstSpec.ClusterSelector = libsveltosv1alpha1.Selector(labelSelector.String())

	return nil
}

func Convert_v1beta1_HelmChart_To_v1alpha1_HelmChart(src *configv1beta1.HelmChart, dst *HelmChart, s conversion.Scope,
) error {

	if err := autoConvert_v1beta1_HelmChart_To_v1alpha1_HelmChart(src, dst, nil); err != nil {
		return err
	}

	return nil
}
