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

package controllers_test

import (
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

const (
	// clusterRefsField and setRefsField name the two Spec slice fields that
	// are explicitly excluded from hash normalisation (their order is
	// author-determined). The constants are also used by
	// clusterpromotion_controller_test.go.
	clusterRefsField = "ClusterRefs"
	setRefsField     = "SetRefs"
)

var _ = Describe("getProfileSpecHash slice-field coverage", func() {
	// This test guards against getProfileSpecHash silently ignoring newly
	// added slice fields in configv1beta1.Spec. json.Marshal includes every
	// field automatically, but slice fields must also be *sorted* before
	// marshaling so the hash is stable regardless of element order.
	//
	// When a new slice field is added to configv1beta1.Spec:
	//   • Add a getSorted* helper (or reuse an existing one) in sort.go
	//   • Call it inside getProfileSpecHash in profile_utils.go
	//   • Add the field name to sortedFields below
	//
	// If its element order is always deterministic (e.g. explicit single-item
	// refs typed by the user with no meaningful reordering), add it to
	// orderDeterministic instead.
	It("accounts for every slice field in configv1beta1.Spec", func() {
		// Fields that getProfileSpecHash explicitly sorts before hashing.
		sortedFields := map[string]bool{
			"HelmCharts":           true,
			"PolicyRefs":           true,
			"KustomizationRefs":    true,
			"TemplateResourceRefs": true,
			"ValidateHealths":      true,
			"PreDeployChecks":      true,
			"PreDeleteChecks":      true,
			"PostDeleteChecks":     true,
			"Patches":              true,
			"PatchesFrom":          true,
			"DriftExclusions":      true,
			"DependsOn":            true,
		}
		// Slice fields whose element order is author-determined and therefore
		// does not need normalisation to produce a stable hash.
		orderDeterministic := map[string]bool{
			clusterRefsField: true,
			setRefsField:     true,
		}

		specType := reflect.TypeOf(configv1beta1.Spec{})
		for i := 0; i < specType.NumField(); i++ {
			f := specType.Field(i)
			if f.Type.Kind() != reflect.Slice {
				continue
			}
			Expect(sortedFields[f.Name] || orderDeterministic[f.Name]).To(BeTrue(),
				"Spec.%s is a slice field not yet handled by getProfileSpecHash.\n"+
					"Add sorting for it in getProfileSpecHash (profile_utils.go) and\n"+
					"list it in 'sortedFields' in this test, or add it to\n"+
					"'orderDeterministic' if its element order is always stable.", f.Name)
		}
	})
})
