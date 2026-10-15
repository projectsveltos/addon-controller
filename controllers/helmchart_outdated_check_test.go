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

// Package-internal (not controllers_test): collectHelmChartRefs is pure and dependency-free
// (no client, no envtest), and asserting on its output requires reading the unexported
// helmChartKey/helmChartRef fields it produces, so this is tested directly rather than through
// the Ginkgo/envtest suite used by the rest of this package's tests.
package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

const (
	testReleaseName      = "r1"
	testReleaseNamespace = "ns1"
	testChartName        = "nginx"
	testChartVersion     = "1.0.0"
	testRepoURL          = "https://example.com/charts"
	testRepositoryName   = "bitnami"
	testBitnamiRepoURL   = "https://charts.bitnami.com/bitnami"
	testNamespace        = "ns"
	testClusterNamespace = "cluster-ns"
	testClusterName      = "cluster"
	testClusterType      = "Capi"
	testProfileName      = "profile"
)

func TestCollectHelmChartRefs_SkipsConflictEntries(t *testing.T) {
	cs := &configv1beta1.ClusterSummary{
		Status: configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				{
					ReleaseName:      testReleaseName,
					ReleaseNamespace: testReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusConflict,
					ChartName:        testChartName,
					RepoURL:          testRepoURL,
					ChartVersion:     testChartVersion,
				},
			},
		},
	}

	refs := map[helmChartKey][]helmChartRef{}
	collectHelmChartRefs(cs, refs, logr.Discard())

	if len(refs) != 0 {
		t.Fatalf("expected no refs for a Conflict-status entry, got %d keys", len(refs))
	}
}

func TestCollectHelmChartRefs_SkipsUnresolvedIdentity(t *testing.T) {
	tests := []struct {
		name string
		rs   configv1beta1.HelmChartSummary
	}{
		{
			name: "never deployed (no ChartName/RepoURL/ChartVersion)",
			rs: configv1beta1.HelmChartSummary{
				ReleaseName: testReleaseName, ReleaseNamespace: testReleaseNamespace, Status: configv1beta1.HelmChartStatusManaging,
			},
		},
		{
			name: "flux-sourced (ChartName/RepoURL never populated)",
			rs: configv1beta1.HelmChartSummary{
				ReleaseName: testReleaseName, ReleaseNamespace: testReleaseNamespace, Status: configv1beta1.HelmChartStatusManaging,
				ChartVersion: testChartVersion,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := &configv1beta1.ClusterSummary{
				Status: configv1beta1.ClusterSummaryStatus{HelmReleaseSummaries: []configv1beta1.HelmChartSummary{tt.rs}},
			}

			refs := map[helmChartKey][]helmChartRef{}
			collectHelmChartRefs(cs, refs, logr.Discard())

			if len(refs) != 0 {
				t.Fatalf("expected no refs, got %d keys", len(refs))
			}
		})
	}
}

func TestCollectHelmChartRefs_DedupsAcrossClusterSummaries(t *testing.T) {
	makeClusterSummary := func(name string) *configv1beta1.ClusterSummary {
		return &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: testClusterNamespace, ClusterName: name, ClusterType: testClusterType,
			},
			Status: configv1beta1.ClusterSummaryStatus{
				HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
					{
						ReleaseName:      testChartName,
						ReleaseNamespace: "web",
						Status:           configv1beta1.HelmChartStatusManaging,
						ChartName:        testChartName,
						RepositoryName:   testRepositoryName,
						RepoURL:          testBitnamiRepoURL,
						ChartVersion:     testChartVersion,
					},
				},
			},
		}
	}

	refs := map[helmChartKey][]helmChartRef{}
	collectHelmChartRefs(makeClusterSummary("cluster-a"), refs, logr.Discard())
	collectHelmChartRefs(makeClusterSummary("cluster-b"), refs, logr.Discard())

	if len(refs) != 1 {
		t.Fatalf("expected exactly one deduped chart key, got %d", len(refs))
	}

	for key, keyRefs := range refs {
		if key.chartName != testChartName || key.repositoryName != testRepositoryName || key.repositoryURL != testBitnamiRepoURL {
			t.Errorf("unexpected key: %+v", key)
		}
		if len(keyRefs) != 2 {
			t.Fatalf("expected 2 refs for the deduped key, got %d", len(keyRefs))
		}
		if keyRefs[0].clusterSummaryName == keyRefs[1].clusterSummaryName {
			t.Errorf("expected refs from two distinct ClusterSummaries, got the same name twice: %q",
				keyRefs[0].clusterSummaryName)
		}
	}
}

func TestSetResolvedHelmChartIdentity_ClearsStaleOutdatedVersionInfoOnVersionChange(t *testing.T) {
	newLatest := v1_1_0
	lastChecked := metav1.Now()

	clusterSummary := &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name: controllerNameClusterSummary, Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: configv1beta1.ClusterProfileKind, APIVersion: configv1beta1.GroupVersion.String(), Name: testProfileName},
			},
		},
		Spec: configv1beta1.ClusterSummarySpec{
			ClusterNamespace: testClusterNamespace, ClusterName: testClusterName, ClusterType: testClusterType,
		},
	}

	requestedChart := &configv1beta1.HelmChart{
		ChartName: testChartName, RepositoryName: testRepositoryName, RepositoryURL: testBitnamiRepoURL,
		ReleaseName: testReleaseName, ReleaseNamespace: testReleaseNamespace,
	}

	rs := &configv1beta1.HelmChartSummary{
		ReleaseName: testReleaseName, ReleaseNamespace: testReleaseNamespace, Status: configv1beta1.HelmChartStatusManaging,
		ChartName: testChartName, RepositoryName: testRepositoryName, RepoURL: testBitnamiRepoURL, ChartVersion: testChartVersion,
		LatestVersion: &newLatest, LastCheckedTime: &lastChecked,
	}

	currentRelease := &releaseInfo{ChartVersion: newLatest} // upgrading to the version previously flagged as latest

	setResolvedHelmChartIdentity(context.TODO(), nil, clusterSummary, rs, requestedChart, currentRelease, logr.Discard())

	if rs.ChartVersion != newLatest {
		t.Errorf("expected ChartVersion to be updated to %q, got %q", newLatest, rs.ChartVersion)
	}
	if rs.LatestVersion != nil {
		t.Errorf("expected LatestVersion to be cleared, got %v", *rs.LatestVersion)
	}
	if rs.LatestPatchVersion != nil {
		t.Errorf("expected LatestPatchVersion to be cleared, got %v", *rs.LatestPatchVersion)
	}
	if rs.LastCheckedTime != nil {
		t.Errorf("expected LastCheckedTime to be cleared, got %v", *rs.LastCheckedTime)
	}
}

func TestSetResolvedHelmChartIdentity_KeepsOutdatedVersionInfoWhenVersionUnchanged(t *testing.T) {
	newLatest := v1_1_0
	lastChecked := metav1.Now()

	clusterSummary := &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name: controllerNameClusterSummary, Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: configv1beta1.ClusterProfileKind, APIVersion: configv1beta1.GroupVersion.String(), Name: testProfileName},
			},
		},
		Spec: configv1beta1.ClusterSummarySpec{
			ClusterNamespace: testClusterNamespace, ClusterName: testClusterName, ClusterType: testClusterType,
		},
	}

	requestedChart := &configv1beta1.HelmChart{
		ChartName: testChartName, RepositoryName: testRepositoryName, RepositoryURL: testBitnamiRepoURL,
		ReleaseName: testReleaseName, ReleaseNamespace: testReleaseNamespace,
	}

	rs := &configv1beta1.HelmChartSummary{
		ReleaseName: testReleaseName, ReleaseNamespace: testReleaseNamespace, Status: configv1beta1.HelmChartStatusManaging,
		ChartName: testChartName, RepositoryName: testRepositoryName, RepoURL: testBitnamiRepoURL, ChartVersion: testChartVersion,
		LatestVersion: &newLatest, LastCheckedTime: &lastChecked,
	}

	// Same deployed version as before (e.g. a values-only reconcile) -- nothing to invalidate.
	currentRelease := &releaseInfo{ChartVersion: testChartVersion}

	setResolvedHelmChartIdentity(context.TODO(), nil, clusterSummary, rs, requestedChart, currentRelease, logr.Discard())

	if rs.LatestVersion == nil || *rs.LatestVersion != newLatest {
		t.Errorf("expected LatestVersion to be left untouched at %q, got %v", newLatest, rs.LatestVersion)
	}
	if rs.LastCheckedTime == nil {
		t.Errorf("expected LastCheckedTime to be left untouched, got nil")
	}
}

// outdatedChartKey is the outdated_helm_chart series of the release clusterSummaryManagingChart manages.
func outdatedChartKey() outdatedHelmChartMetricKey {
	return outdatedHelmChartMetricKey{
		profileKind: configv1beta1.ClusterProfileKind, profileName: testProfileName,
		clusterType: testClusterType, clusterNamespace: testClusterNamespace, clusterName: testClusterName,
		chartName: testChartName, releaseNamespace: testReleaseNamespace, releaseName: testReleaseName,
	}
}

// clusterSummaryManagingChart returns the ClusterSummary of the ClusterProfile testProfileName that
// manages version testChartVersion of testChartName, from repoURL.
func clusterSummaryManagingChart(repoURL string) *configv1beta1.ClusterSummary {
	return &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testProfileName + "-" + testClusterName,
			Namespace: testClusterNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: configv1beta1.GroupVersion.String(), Kind: configv1beta1.ClusterProfileKind, Name: testProfileName},
			},
		},
		Spec: configv1beta1.ClusterSummarySpec{
			ClusterNamespace: testClusterNamespace, ClusterName: testClusterName, ClusterType: testClusterType,
		},
		Status: configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				{
					ReleaseName:      testReleaseName,
					ReleaseNamespace: testReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusManaging,
					ChartName:        testChartName,
					RepoURL:          repoURL,
					ChartVersion:     testChartVersion,
				},
			},
		},
	}
}

// markOutdatedByPreviousPass leaves the checker as a previous pass does when it finds key outdated,
// and restores the checker when t ends.
func markOutdatedByPreviousPass(t *testing.T, key *outdatedHelmChartMetricKey) {
	t.Helper()

	saved := previouslyOutdatedHelmCharts
	t.Cleanup(func() {
		previouslyOutdatedHelmCharts = saved
		clearOutdatedHelmChart(key.profileKind, key.profileNamespace, key.profileName, key.clusterType,
			key.clusterNamespace, key.clusterName, key.chartName, key.releaseNamespace, key.releaseName)
	})

	previouslyOutdatedHelmCharts = map[outdatedHelmChartMetricKey]struct{}{*key: {}}
	trackOutdatedHelmChart(key.profileKind, key.profileNamespace, key.profileName, key.clusterType,
		key.clusterNamespace, key.clusterName, key.chartName, key.releaseNamespace, key.releaseName, logr.Discard())
}

// runCheckerPass runs one checkOutdatedHelmCharts pass against a fake client holding objs.
func runCheckerPass(t *testing.T, objs ...client.Object) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := configv1beta1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() = %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(objs...).Build()

	checkOutdatedHelmCharts(context.Background(), c, logr.Discard())
}

// outdatedHelmChartState returns whether the checker still tracks key as outdated, and whether its
// outdated_helm_chart series is still exposed.
func outdatedHelmChartState(t *testing.T, key *outdatedHelmChartMetricKey) (tracked, exposed bool) {
	t.Helper()

	_, tracked = previouslyOutdatedHelmCharts[*key]

	want := map[string]string{
		metricProfileKindLabel: key.profileKind, metricProfileNamespaceLabel: key.profileNamespace,
		metricProfileNameLabel: key.profileName, metricClusterTypeLabel: key.clusterType,
		metricClusterNamespaceLabel: key.clusterNamespace, metricClusterNameLabel: key.clusterName,
		metricChartNameLabel: key.chartName, metricReleaseNamespaceLabel: key.releaseNamespace,
		metricReleaseNameLabel: key.releaseName,
	}

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() = %v", err)
	}
	for _, family := range families {
		if family.GetName() != metricNamespace+"_outdated_helm_chart" {
			continue
		}
		for _, metric := range family.GetMetric() {
			matched := 0
			for _, label := range metric.GetLabel() {
				if value, ok := want[label.GetName()]; ok && value == label.GetValue() {
					matched++
				}
			}
			if matched == len(want) {
				return tracked, true
			}
		}
	}

	return tracked, false
}

// A deleted ClusterSummary is never checked again, so its series must not be carried forward the
// way the series of a chart that could not be fetched is.
func TestCheckOutdatedHelmCharts_ClearsChartOfDeletedClusterSummary(t *testing.T) {
	key := outdatedChartKey()
	markOutdatedByPreviousPass(t, &key)

	runCheckerPass(t)

	if tracked, exposed := outdatedHelmChartState(t, &key); tracked || exposed {
		t.Errorf("chart of a deleted ClusterSummary: tracked = %v, exposed = %v, want false, false", tracked, exposed)
	}
}

func TestCheckOutdatedHelmCharts_ClearsChartRemovedFromProfile(t *testing.T) {
	key := outdatedChartKey()
	markOutdatedByPreviousPass(t, &key)

	clusterSummary := clusterSummaryManagingChart(testRepoURL)
	clusterSummary.Status.HelmReleaseSummaries = nil

	runCheckerPass(t, clusterSummary)

	if tracked, exposed := outdatedHelmChartState(t, &key); tracked || exposed {
		t.Errorf("chart removed from its profile: tracked = %v, exposed = %v, want false, false", tracked, exposed)
	}
}

// A failed fetch says nothing about the chart, so its series stays until a pass can check it.
func TestCheckOutdatedHelmCharts_KeepsChartItCouldNotCheck(t *testing.T) {
	key := outdatedChartKey()
	markOutdatedByPreviousPass(t, &key)

	// Nothing listens on port 1, so fetching the index fails right away.
	runCheckerPass(t, clusterSummaryManagingChart("http://127.0.0.1:1/charts"))

	if tracked, exposed := outdatedHelmChartState(t, &key); !tracked || !exposed {
		t.Errorf("chart that could not be checked: tracked = %v, exposed = %v, want true, true", tracked, exposed)
	}
}

func TestCheckOutdatedHelmCharts_ClearsChartThatIsUpToDate(t *testing.T) {
	key := outdatedChartKey()
	markOutdatedByPreviousPass(t, &key)

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

	runCheckerPass(t, clusterSummaryManagingChart(server.URL))

	if tracked, exposed := outdatedHelmChartState(t, &key); tracked || exposed {
		t.Errorf("chart that is up to date: tracked = %v, exposed = %v, want false, false", tracked, exposed)
	}
}
