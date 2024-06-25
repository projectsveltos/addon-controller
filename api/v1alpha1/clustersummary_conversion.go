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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

// ConvertTo converts v1alpha1 to the Hub version (v1beta1).
func (src *ClusterSummary) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*configv1beta1.ClusterSummary)
	err := Convert_v1alpha1_ClusterSummary_To_v1beta1_ClusterSummary(src, dst, nil)
	if err != nil {
		return err
	}

	if src.Spec.ClusterProfileSpec.ClusterSelector == "" {
		dst.Spec.ClusterProfileSpec.ClusterSelector.LabelSelector = metav1.LabelSelector{}
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1beta1) to this v1alpha1.
func (dst *ClusterSummary) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*configv1beta1.ClusterSummary)
	err := Convert_v1beta1_ClusterSummary_To_v1alpha1_ClusterSummary(src, dst, nil)
	if err != nil {
		return err
	}

	if src.Spec.ClusterProfileSpec.ClusterSelector.MatchLabels == nil {
		dst.Spec.ClusterProfileSpec.ClusterSelector = ""
	}

	return nil
}
