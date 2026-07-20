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
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
