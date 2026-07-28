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

package crd_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/projectsveltos/addon-controller/lib/crd"
)

var _ = Describe("CRD", func() {
	It("Gets the ClusterProfile CustomResourceDefinition", func() {
		yaml := crd.GetClusterProfileCRDYAML()

		currentFile, err := os.ReadFile(crd.ClusterProfileFile)
		Expect(err).To(BeNil())

		Expect(string(yaml)).To(Equal(string(currentFile)))
	})

	It("Gets the Profile CustomResourceDefinition", func() {
		yaml := crd.GetProfileCRDYAML()

		currentFile, err := os.ReadFile(crd.ProfileFile)
		Expect(err).To(BeNil())

		Expect(string(yaml)).To(Equal(string(currentFile)))
	})

	It("Gets the ClusterSummary CustomResourceDefinition", func() {
		yaml := crd.GetClusterSummaryCRDYAML()

		currentFile, err := os.ReadFile(crd.ClusterSummaryFile)
		Expect(err).To(BeNil())

		Expect(string(yaml)).To(Equal(string(currentFile)))
	})

	It("Gets the ClusterConfiguration CustomResourceDefinition", func() {
		yaml := crd.GetClusterConfigurationCRDYAML()

		currentFile, err := os.ReadFile(crd.ClusterConfigurationFile)
		Expect(err).To(BeNil())

		Expect(string(yaml)).To(Equal(string(currentFile)))
	})

	It("Gets the ClusterReport CustomResourceDefinition", func() {
		yaml := crd.GetClusterReportCRDYAML()

		currentFile, err := os.ReadFile(crd.ClusterReportFile)
		Expect(err).To(BeNil())

		Expect(string(yaml)).To(Equal(string(currentFile)))
	})

	It("Gets the ClusterPromotion CustomResourceDefinition", func() {
		yaml := crd.GetClusterPromotionCRDYAML()

		currentFile, err := os.ReadFile(crd.ClusterPromotionFile)
		Expect(err).To(BeNil())

		Expect(string(yaml)).To(Equal(string(currentFile)))
	})
})
