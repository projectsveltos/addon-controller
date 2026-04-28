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

package driftdetection_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	driftdetection "github.com/projectsveltos/addon-controller/pkg/drift-detection"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

var _ = Describe("Validate drift-detection-manager YAML", func() {
	It("Deployment name validation", func() {
		driftDetectionManagerYAML := string(driftdetection.GetDriftDetectionManagerYAML())
		Expect(len(driftDetectionManagerYAML)).ToNot(BeZero())

		elements, err := deployer.CustomSplit(driftDetectionManagerYAML)
		Expect(err).To(BeNil())
		for i := range elements {
			policy, err := k8s_utils.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			if policy.GetKind() == "Deployment" {
				// drift-detection upgrade logic (isDriftDetectionManagerDeployedInCluster)
				// assumes this is the name
				Expect(policy.GetName()).To(Equal("drift-detection-manager"))
				Expect(policy.GetNamespace()).To(Equal("projectsveltos"))
			}
		}
	})
})
