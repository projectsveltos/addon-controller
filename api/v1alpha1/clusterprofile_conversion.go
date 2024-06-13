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

	"sigs.k8s.io/controller-runtime/pkg/conversion"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

var (
	configlog = logf.Log.WithName("conversion")
)

// ConvertTo converts v1alpha1 to the Hub version (v1beta1).
func (src *ClusterProfile) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*configv1beta1.ClusterProfile)

	configlog.V(logs.LogInfo).Info("convert ClusterProfile from v1alpha1 to v1beta1")

	dst.ObjectMeta = src.ObjectMeta

	var err error
	dst.Spec, err = convertV1Alpha1SpecToV1Beta1(&src.Spec)
	if err != nil {
		configlog.V(logs.LogInfo).Info(fmt.Sprintf("failed to convert Spec: %v", err))
		return err
	}

	jsonData, err := json.Marshal(src.Status) // Marshal the Status field
	if err != nil {
		return fmt.Errorf("error marshaling Status: %w", err)
	}

	err = json.Unmarshal(jsonData, &dst.Status) // Unmarshal to v1beta1 type
	if err != nil {
		return fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1beta1) to this v1alpha1.
func (dst *ClusterProfile) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*configv1beta1.ClusterProfile)

	configlog.V(logs.LogInfo).Info("convert ClusterProfile from v1beta1 to v1alpha1")

	dst.ObjectMeta = src.ObjectMeta

	var err error
	dst.Spec, err = convertV1Beta1SpecToV1Alpha1(&src.Spec)
	if err != nil {
		configlog.V(logs.LogInfo).Info(fmt.Sprintf("failed to convert Spec: %v", err))
		return err
	}

	labelSelector, err := src.Spec.ClusterSelector.ToSelector()
	if err != nil {
		return fmt.Errorf("failed to convert : %w", err)
	}

	dst.Spec.ClusterSelector = libsveltosv1alpha1.Selector(labelSelector.String())

	jsonData, err := json.Marshal(src.Status) // Marshal the Status field
	if err != nil {
		return fmt.Errorf("error marshaling Spec: %w", err)
	}

	err = json.Unmarshal(jsonData, &dst.Status) // Unmarshal to v1beta1 type
	if err != nil {
		return fmt.Errorf("error unmarshaling JSON: %w", err)
	}

	return nil
}
