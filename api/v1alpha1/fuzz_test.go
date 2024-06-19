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

package v1alpha1_test

import (
	"fmt"
	"testing"

	fuzz "github.com/google/gofuzz"
	"k8s.io/apimachinery/pkg/api/apitesting/fuzzer"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeserializer "k8s.io/apimachinery/pkg/runtime/serializer"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

func TestFuzzyConversion(t *testing.T) {
	t.Run("for ClusterProfile", utilconversion.FuzzTestFunc(utilconversion.FuzzTestFuncInput{
		Hub:         &configv1beta1.ClusterProfile{},
		Spoke:       &configv1alpha1.ClusterProfile{},
		FuzzerFuncs: []fuzzer.FuzzerFuncs{fuzzFuncs},
	}))

	t.Run("for Profile", utilconversion.FuzzTestFunc(utilconversion.FuzzTestFuncInput{
		Hub:         &configv1beta1.Profile{},
		Spoke:       &configv1alpha1.Profile{},
		FuzzerFuncs: []fuzzer.FuzzerFuncs{fuzzFuncs},
	}))
}

func fuzzFuncs(_ runtimeserializer.CodecFactory) []interface{} {
	return []interface{}{
		profileClusterSelectorFuzzer,
		clusterProfileClusterSelectorFuzzer,
		v1beta1ProfileClusterSelectorFuzzer,
		v1beta1ClusterProfileClusterSelectorFuzzer,
	}
}

func profileClusterSelectorFuzzer(in *configv1alpha1.Profile, _ fuzz.Continue) {
	in.Spec.ClusterSelector = libsveltosv1alpha1.Selector(
		fmt.Sprintf("%s=%s",
			randomString(), randomString(),
		))
}

func clusterProfileClusterSelectorFuzzer(in *configv1alpha1.ClusterProfile, _ fuzz.Continue) {
	in.Spec.ClusterSelector = libsveltosv1alpha1.Selector(
		fmt.Sprintf("%s=%s",
			randomString(), randomString(),
		))
}

func v1beta1ProfileClusterSelectorFuzzer(in *configv1beta1.Profile, _ fuzz.Continue) {
	in.Spec.ClusterSelector = libsveltosv1beta1.Selector{
		LabelSelector: metav1.LabelSelector{
			MatchExpressions: nil,
			MatchLabels: map[string]string{
				randomString(): randomString(),
			},
		},
	}
}

func v1beta1ClusterProfileClusterSelectorFuzzer(in *configv1beta1.ClusterProfile, _ fuzz.Continue) {
	in.Spec.ClusterSelector = libsveltosv1beta1.Selector{
		LabelSelector: metav1.LabelSelector{
			MatchExpressions: nil,
			MatchLabels: map[string]string{
				randomString(): randomString(),
			},
		},
	}
}
