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
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
)

const (
	outdatedCheckReleaseName      = "r1"
	outdatedCheckReleaseNamespace = "ns1"
	outdatedCheckChartName        = "nginx"
	outdatedCheckChartVersion     = "1.0.0"
	outdatedCheckRepoURL          = "https://example.com/charts"
	outdatedCheckRepositoryName   = "bitnami"
	outdatedCheckBitnamiRepoURL   = "https://charts.bitnami.com/bitnami"
	outdatedCheckNamespace        = "ns"
	outdatedCheckClusterNamespace = "cluster-ns"
	outdatedCheckClusterName      = "cluster"
	outdatedCheckClusterType      = "Capi"
	outdatedCheckProfileName      = "profile"
)

// newClusterSummaryWithHelmRelease returns a ClusterSummary of the ClusterProfile outdatedCheckProfileName,
// with a single Helm release
func newClusterSummaryWithHelmRelease(release *configv1beta1.HelmChartSummary) *configv1beta1.ClusterSummary {
	return &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      outdatedCheckProfileName + "-" + outdatedCheckClusterName,
			Namespace: outdatedCheckClusterNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: configv1beta1.GroupVersion.String(), Kind: configv1beta1.ClusterProfileKind,
					Name: outdatedCheckProfileName,
				},
			},
		},
		Spec: configv1beta1.ClusterSummarySpec{
			ClusterNamespace: outdatedCheckClusterNamespace, ClusterName: outdatedCheckClusterName,
			ClusterType: outdatedCheckClusterType,
		},
		Status: configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{*release},
		},
	}
}

var _ = Describe("Outdated Helm chart check", func() {
	Context("collecting the charts referenced by the ClusterSummaries", func() {
		It("skips the releases the ClusterSummary does not manage", func() {
			clusterSummary := newClusterSummaryWithHelmRelease(&configv1beta1.HelmChartSummary{
				ReleaseName:      outdatedCheckReleaseName,
				ReleaseNamespace: outdatedCheckReleaseNamespace,
				Status:           configv1beta1.HelmChartStatusConflict,
				ChartName:        outdatedCheckChartName,
				RepoURL:          outdatedCheckRepoURL,
				ChartVersion:     outdatedCheckChartVersion,
			})

			Expect(controllers.CollectHelmChartRefs(clusterSummary)).To(BeEmpty())
		})

		DescribeTable("skips the releases whose chart identity is not resolved",
			func(release *configv1beta1.HelmChartSummary) {
				clusterSummary := newClusterSummaryWithHelmRelease(release)

				Expect(controllers.CollectHelmChartRefs(clusterSummary)).To(BeEmpty())
			},
			Entry("never deployed (no ChartName, RepoURL and ChartVersion)", &configv1beta1.HelmChartSummary{
				ReleaseName: outdatedCheckReleaseName, ReleaseNamespace: outdatedCheckReleaseNamespace,
				Status: configv1beta1.HelmChartStatusManaging,
			}),
			Entry("sourced from flux (ChartName and RepoURL never populated)", &configv1beta1.HelmChartSummary{
				ReleaseName: outdatedCheckReleaseName, ReleaseNamespace: outdatedCheckReleaseNamespace,
				Status: configv1beta1.HelmChartStatusManaging, ChartVersion: outdatedCheckChartVersion,
			}),
		)

		It("groups the ClusterSummaries referencing the same chart", func() {
			newClusterSummary := func(clusterName string) *configv1beta1.ClusterSummary {
				clusterSummary := newClusterSummaryWithHelmRelease(&configv1beta1.HelmChartSummary{
					ReleaseName:      outdatedCheckChartName,
					ReleaseNamespace: "web",
					Status:           configv1beta1.HelmChartStatusManaging,
					ChartName:        outdatedCheckChartName,
					RepositoryName:   outdatedCheckRepositoryName,
					RepoURL:          outdatedCheckBitnamiRepoURL,
					ChartVersion:     outdatedCheckChartVersion,
				})
				clusterSummary.Name = clusterName
				clusterSummary.Namespace = outdatedCheckNamespace
				clusterSummary.Spec.ClusterName = clusterName
				return clusterSummary
			}

			refs := controllers.CollectHelmChartRefs(newClusterSummary("cluster-a"), newClusterSummary("cluster-b"))

			chartKey := controllers.CollectedHelmChartKey{
				ChartName:      outdatedCheckChartName,
				RepositoryName: outdatedCheckRepositoryName,
				RepositoryURL:  outdatedCheckBitnamiRepoURL,
			}
			Expect(refs).To(HaveLen(1))
			Expect(refs).To(HaveKey(chartKey))
			Expect(refs[chartKey]).To(ConsistOf("cluster-a", "cluster-b"))
		})
	})

	Context("resolving the identity of a deployed chart", func() {
		var (
			clusterSummary *configv1beta1.ClusterSummary
			requestedChart *configv1beta1.HelmChart
			release        *configv1beta1.HelmChartSummary
			latestVersion  string
		)

		BeforeEach(func() {
			latestVersion = v1_1_0
			lastChecked := metav1.Now()

			clusterSummary = newClusterSummaryWithHelmRelease(&configv1beta1.HelmChartSummary{})
			clusterSummary.Namespace = outdatedCheckNamespace

			requestedChart = &configv1beta1.HelmChart{
				ChartName: outdatedCheckChartName, RepositoryName: outdatedCheckRepositoryName,
				RepositoryURL: outdatedCheckBitnamiRepoURL, ReleaseName: outdatedCheckReleaseName,
				ReleaseNamespace: outdatedCheckReleaseNamespace,
			}

			// The release is outdated: a newer version was found
			release = &configv1beta1.HelmChartSummary{
				ReleaseName: outdatedCheckReleaseName, ReleaseNamespace: outdatedCheckReleaseNamespace,
				Status:    configv1beta1.HelmChartStatusManaging,
				ChartName: outdatedCheckChartName, RepositoryName: outdatedCheckRepositoryName,
				RepoURL: outdatedCheckBitnamiRepoURL, ChartVersion: outdatedCheckChartVersion,
				LatestVersion: &latestVersion, LastCheckedTime: &lastChecked,
			}
		})

		It("clears the stale outdated version information when the deployed version changes", func() {
			// Upgrading to the version previously flagged as the latest one
			controllers.SetResolvedHelmChartIdentity(clusterSummary, release, requestedChart, v1_1_0)

			Expect(release.ChartVersion).To(Equal(v1_1_0))
			Expect(release.LatestVersion).To(BeNil())
			Expect(release.LatestPatchVersion).To(BeNil())
			Expect(release.LastCheckedTime).To(BeNil())
		})

		It("keeps the outdated version information when the deployed version is unchanged", func() {
			// For instance a reconciliation changing the values only: nothing to invalidate
			controllers.SetResolvedHelmChartIdentity(clusterSummary, release, requestedChart, outdatedCheckChartVersion)

			Expect(release.LatestVersion).ToNot(BeNil())
			Expect(*release.LatestVersion).To(Equal(v1_1_0))
			Expect(release.LastCheckedTime).ToNot(BeNil())
		})
	})

	// The series of a chart found outdated by a previous pass, after a whole pass against a fake client
	Context("checking the charts after one found outdated", func() {
		var chart controllers.OutdatedHelmChart

		BeforeEach(func() {
			chart = controllers.OutdatedHelmChart{
				ProfileKind: configv1beta1.ClusterProfileKind, ProfileName: outdatedCheckProfileName,
				ClusterType: outdatedCheckClusterType, ClusterNamespace: outdatedCheckClusterNamespace,
				ClusterName: outdatedCheckClusterName, ChartName: outdatedCheckChartName,
				ReleaseNamespace: outdatedCheckReleaseNamespace, ReleaseName: outdatedCheckReleaseName,
			}
			DeferCleanup(controllers.SetOutdatedByPreviousPass(&chart))
		})

		// runPass runs one pass of the check against a fake client holding objs
		runPass := func(objs ...client.Object) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(objs...).Build()
			controllers.CheckOutdatedHelmCharts(context.TODO(), c)
		}

		// newClusterSummaryManagingChart returns the ClusterSummary managing the chart, deployed from repoURL
		newClusterSummaryManagingChart := func(repoURL string) *configv1beta1.ClusterSummary {
			return newClusterSummaryWithHelmRelease(&configv1beta1.HelmChartSummary{
				ReleaseName:      outdatedCheckReleaseName,
				ReleaseNamespace: outdatedCheckReleaseNamespace,
				Status:           configv1beta1.HelmChartStatusManaging,
				ChartName:        outdatedCheckChartName,
				RepoURL:          repoURL,
				ChartVersion:     outdatedCheckChartVersion,
			})
		}

		expectTrackedAndExposed := func(want bool) {
			Expect(controllers.IsOutdatedHelmChartTracked(&chart)).To(Equal(want))

			exposed, err := controllers.IsOutdatedHelmChartExposed(&chart)
			Expect(err).To(BeNil())
			Expect(exposed).To(Equal(want))
		}

		It("clears the series of a deleted ClusterSummary: it is never checked again", func() {
			expectTrackedAndExposed(true)

			runPass()

			expectTrackedAndExposed(false)
		})

		It("clears the series of a chart removed from the profile", func() {
			clusterSummary := newClusterSummaryManagingChart(outdatedCheckRepoURL)
			clusterSummary.Status.HelmReleaseSummaries = nil

			runPass(clusterSummary)

			expectTrackedAndExposed(false)
		})

		It("keeps the series of a chart which could not be checked: nothing was learned about it", func() {
			// The server is closed: fetching the index fails right away
			server := httptest.NewServer(http.NotFoundHandler())
			server.Close()

			runPass(newClusterSummaryManagingChart(server.URL))

			expectTrackedAndExposed(true)
		})

		It("clears the series of a chart which is up to date", func() {
			index := []byte(`apiVersion: v1
entries:
  nginx:
  - apiVersion: v2
    name: nginx
    version: 1.0.0
    urls:
    - nginx-1.0.0.tgz
`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/index.yaml" {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(index)
			}))
			defer server.Close()

			runPass(newClusterSummaryManagingChart(server.URL))

			expectTrackedAndExposed(false)
		})
	})
})
