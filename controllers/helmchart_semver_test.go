/*
Copyright 2025. projectsveltos.io. All rights reserved.

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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/projectsveltos/addon-controller/controllers"
)

const (
	v1_0_0      = "1.0.0"
	v1_1_0      = "1.1.0"
	v1_2_3      = "1.2.3"
	v1_2_4      = "1.2.4"
	v1_2_5      = "1.2.5"
	v1_9_9      = "1.9.9"
	v2_0_0      = "2.0.0"
	notAVersion = "not-a-version"
)

var _ = Describe("Helm chart version diff", func() {
	// An empty wantLatest or wantLatestPatch means no version is expected
	DescribeTable("helmChartVersionDiff",
		func(current string, available []string, wantLatest, wantLatestPatch string) {
			latest, latestPatch, err := controllers.HelmChartVersionDiff(current, available)
			Expect(err).To(BeNil())
			Expect(latest).To(Equal(wantLatest))
			Expect(latestPatch).To(Equal(wantLatestPatch))
		},
		Entry("newer minor and same-minor patch both found",
			v1_2_3, []string{v1_2_3, v1_2_4, "1.3.0", v2_0_0}, v2_0_0, v1_2_4),
		Entry("only a same-minor patch found",
			v1_2_3, []string{v1_2_3, v1_2_4, v1_2_5}, v1_2_5, v1_2_5),
		Entry("already the latest",
			v2_0_0, []string{v1_2_3, v1_9_9, v2_0_0}, "", ""),
		Entry("current not in available list but nothing newer",
			v2_0_0, []string{v1_9_9}, "", ""),
		Entry("unparseable entries in available list are skipped",
			v1_0_0, []string{notAVersion, v1_1_0, "also-bad"}, v1_1_0, ""),
		Entry("stable current excludes prereleases",
			v1_0_0, []string{"1.1.0-rc.1"}, "", ""),
		Entry("prerelease current includes prereleases",
			"1.0.0-rc.1", []string{"1.0.0-rc.2", v1_0_0}, v1_0_0, v1_0_0),
	)

	It("fails when the current version cannot be parsed", func() {
		_, _, err := controllers.HelmChartVersionDiff(notAVersion, []string{v1_0_0})
		Expect(err).ToNot(BeNil())
	})
})
