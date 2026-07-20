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

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// maxConcurrentHelmChartFetches bounds how many distinct chart repositories/registries are
	// queried at once, so peak transient memory (index.yaml parsing) stays bounded to a handful
	// of large indexes in flight rather than every distinct repo in the fleet at once.
	maxConcurrentHelmChartFetches = 5
)

// helmChartRef records one HelmChartSummary entry, on one ClusterSummary, that references a
// given helmChartKey. Multiple ClusterSummary entries (across clusters and/or profiles) can
// point at the same key; they are fanned back out to after a single upstream fetch. Profile
// and cluster identity are captured here, at collection time, rather than re-derived later,
// since deriving profileKind/profileName requires the full ClusterSummary object (its
// OwnerReferences), not just its namespace/name.
type helmChartRef struct {
	clusterSummaryNamespace string
	clusterSummaryName      string
	releaseNamespace        string
	releaseName             string
	currentVersion          string
	credentialsSecretRef    *corev1.SecretReference

	profileKind      string
	profileNamespace string
	profileName      string
	clusterType      string
	clusterNamespace string
	clusterName      string
}

// outdatedHelmChartMetricKey identifies one label combination of outdatedHelmChartGauge.
// Comparable (all string fields), so it can be used as a map key by
// reconcileOutdatedHelmChartMetric to track, across passes, which combinations are currently
// exposed as outdated.
type outdatedHelmChartMetricKey struct {
	profileKind, profileNamespace, profileName string
	clusterType, clusterNamespace, clusterName string
	chartName, releaseNamespace, releaseName   string
}

func metricKeyForRef(ref *helmChartRef, chartName string) outdatedHelmChartMetricKey {
	return outdatedHelmChartMetricKey{
		profileKind: ref.profileKind, profileNamespace: ref.profileNamespace, profileName: ref.profileName,
		clusterType: ref.clusterType, clusterNamespace: ref.clusterNamespace, clusterName: ref.clusterName,
		chartName: chartName, releaseNamespace: ref.releaseNamespace, releaseName: ref.releaseName,
	}
}

var (
	// previouslyOutdatedHelmCharts is the set of metric label combinations exposed as outdated by
	// the most recently completed checkOutdatedHelmCharts pass (or carried forward from an even
	// earlier pass, for a chart that could not be re-verified — see reconcileOutdatedHelmChartMetric).
	// Only ever read/written from checkOutdatedHelmCharts, which RunHelmChartUpdateChecker drives
	// sequentially from a single ticker loop — passes never overlap — so no lock is needed here.
	previouslyOutdatedHelmCharts = map[outdatedHelmChartMetricKey]struct{}{}
)

// outdatedHelmChartPassState accumulates, across the concurrent workers of one
// checkOutdatedHelmCharts pass, which metric label combinations were actually re-checked this
// pass and which of those came back outdated. Both are needed to reconcile the gauge correctly:
// a combination that was outdated last pass but wasn't re-checked this pass (its fetch failed)
// must be left alone, not cleared — clearing it would be reporting "confirmed fixed" when
// really nothing was learned.
type outdatedHelmChartPassState struct {
	mu                sync.Mutex
	currentlyOutdated map[outdatedHelmChartMetricKey]struct{}
	checkedThisPass   map[outdatedHelmChartMetricKey]struct{}
}

func newOutdatedHelmChartPassState() *outdatedHelmChartPassState {
	return &outdatedHelmChartPassState{
		currentlyOutdated: map[outdatedHelmChartMetricKey]struct{}{},
		checkedThisPass:   map[outdatedHelmChartMetricKey]struct{}{},
	}
}

func (s *outdatedHelmChartPassState) markChecked(key *outdatedHelmChartMetricKey, outdated bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.checkedThisPass[*key] = struct{}{}
	if outdated {
		s.currentlyOutdated[*key] = struct{}{}
	}
}

// checkOutdatedHelmCharts runs one full pass: list every ClusterSummary, evaluate every
// HelmChartSummary entry it manages, and update both HelmReleaseSummaries[].LatestVersion/
// LatestPatchVersion and the outdatedHelmChartGauge metric. Any single repository/registry
// fetch failure or timeout is logged and skipped; it never aborts the pass.
func checkOutdatedHelmCharts(ctx context.Context, c client.Client, logger logr.Logger) {
	list := &configv1beta1.ClusterSummaryList{}
	if err := c.List(ctx, list); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list ClusterSummaries, skipping this pass: %v", err))
		return
	}

	refs := map[helmChartKey][]helmChartRef{}
	for i := range list.Items {
		collectHelmChartRefs(&list.Items[i], refs, logger)
	}

	type work struct {
		key     helmChartKey
		keyRefs []helmChartRef
	}
	workCh := make(chan work, len(refs))
	for key, keyRefs := range refs {
		workCh <- work{key: key, keyRefs: keyRefs}
	}
	close(workCh)

	numWorkers := maxConcurrentHelmChartFetches
	if len(refs) < numWorkers {
		numWorkers = len(refs)
	}

	passState := newOutdatedHelmChartPassState()

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range workCh {
				processHelmChartKey(ctx, c, w.key, w.keyRefs, passState, logger)
			}
		}()
	}
	wg.Wait()

	reconcileOutdatedHelmChartMetric(passState)
	trackHelmChartCheckCompleted(logger)
}

// reconcileOutdatedHelmChartMetric updates previouslyOutdatedHelmCharts for the next pass and
// clears the gauge series for every combination confirmed, this pass, to no longer be outdated.
// Deliberately never calls outdatedHelmChartGauge.Reset(): doing so up front would make every
// still-outdated chart briefly vanish from /metrics while later workers in this same pass are
// still catching up to it, producing a false "not outdated" reading for the gap. Combinations
// found outdated this pass were already Set(1) by trackOutdatedHelmChart as they were found;
// this only needs to handle clearing the ones that are no longer outdated.
func reconcileOutdatedHelmChartMetric(passState *outdatedHelmChartPassState) {
	next := make(map[outdatedHelmChartMetricKey]struct{}, len(passState.currentlyOutdated))
	for key := range passState.currentlyOutdated {
		next[key] = struct{}{}
	}

	for key := range previouslyOutdatedHelmCharts {
		if _, stillOutdated := passState.currentlyOutdated[key]; stillOutdated {
			continue // already carried forward above
		}
		if _, checked := passState.checkedThisPass[key]; !checked {
			// Could not re-verify this pass (its chart key's fetch failed) -- leave state
			// unchanged rather than guessing, and retry on the next pass.
			next[key] = struct{}{}
			continue
		}
		clearOutdatedHelmChart(key.profileKind, key.profileNamespace, key.profileName,
			key.clusterType, key.clusterNamespace, key.clusterName,
			key.chartName, key.releaseNamespace, key.releaseName)
	}

	previouslyOutdatedHelmCharts = next
}

// collectHelmChartRefs walks cs.Status.HelmReleaseSummaries and records a helmChartRef for
// every entry this ClusterSummary is the authoritative manager for (Status == Managing, so a
// non-owning ClusterSummary in a Conflict never double-reports the same release) and that has
// a resolved chart identity. Entries with no identity are either never successfully deployed
// yet, or Flux-source-backed (setResolvedHelmChartIdentity never populates either case) — both
// are silently skipped, not an error.
func collectHelmChartRefs(cs *configv1beta1.ClusterSummary, refs map[helmChartKey][]helmChartRef, logger logr.Logger) {
	profileKind, profileNamespace, profileName := "", "", ""
	if ref, err := configv1beta1.GetProfileOwnerReference(cs); err == nil {
		profileKind = ref.Kind
		profileName = ref.Name
		if profileKind == configv1beta1.ProfileKind {
			profileNamespace = cs.Namespace
		}
	} else {
		logger.V(logs.LogVerbose).Info(fmt.Sprintf("failed to get profile owner reference for ClusterSummary %s/%s: %v",
			cs.Namespace, cs.Name, err))
	}

	for i := range cs.Status.HelmReleaseSummaries {
		rs := &cs.Status.HelmReleaseSummaries[i]

		if rs.Status != configv1beta1.HelmChartStatusManaging {
			continue
		}
		if rs.ChartName == "" || rs.RepoURL == "" || rs.ChartVersion == "" {
			continue
		}

		key := helmChartKey{repositoryURL: rs.RepoURL, repositoryName: rs.RepositoryName, chartName: rs.ChartName}
		refs[key] = append(refs[key], helmChartRef{
			clusterSummaryNamespace: cs.Namespace,
			clusterSummaryName:      cs.Name,
			releaseNamespace:        rs.ReleaseNamespace,
			releaseName:             rs.ReleaseName,
			currentVersion:          rs.ChartVersion,
			credentialsSecretRef:    rs.CredentialsSecretRef,
			profileKind:             profileKind,
			profileNamespace:        profileNamespace,
			profileName:             profileName,
			clusterType:             string(cs.Spec.ClusterType),
			clusterNamespace:        cs.Spec.ClusterNamespace,
			clusterName:             cs.Spec.ClusterName,
		})
	}
}

// processHelmChartKey fetches key's published versions once, then diffs and persists the
// result against every ClusterSummary referencing it. On a fetch failure, none of keyRefs are
// marked as checked in passState, so reconcileOutdatedHelmChartMetric leaves their previous
// metric state untouched rather than treating the failure as "confirmed no longer outdated".
func processHelmChartKey(ctx context.Context, c client.Client, key helmChartKey, keyRefs []helmChartRef,
	passState *outdatedHelmChartPassState, logger logr.Logger) {

	// First-seen credentials win when multiple refs for the same key disagree (a documented
	// limitation: this checker has no per-cluster template context to resolve which secret is
	// "correct" when they genuinely differ).
	var credentialsSecretRef *corev1.SecretReference
	for i := range keyRefs {
		if keyRefs[i].credentialsSecretRef != nil {
			credentialsSecretRef = keyRefs[i].credentialsSecretRef
			break
		}
	}

	versions, err := fetchAvailableVersions(ctx, c, key, credentialsSecretRef, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("skipping chart %q in %q: %v", key.chartName, key.repositoryURL, err))
		recordHelmChartCheckFailure()
		return
	}

	for i := range keyRefs {
		applyDiffAndPersist(ctx, c, &keyRefs[i], key, versions, passState, logger)
	}
}

// applyDiffAndPersist diffs ref's currentVersion against versions and, if the result differs
// from what is already stored on the corresponding HelmChartSummary entry, patches it — this
// also clears a previously outdated entry once it is no longer outdated (e.g. the profile was
// updated to pin a newer version). On a diff error (unparseable currentVersion), ref is left out
// of passState entirely, same as a fetch failure: nothing was learned, so its previous metric
// state is left untouched rather than cleared.
func applyDiffAndPersist(ctx context.Context, c client.Client, ref *helmChartRef, key helmChartKey, versions []string,
	passState *outdatedHelmChartPassState, logger logr.Logger) {

	latest, latestPatch, err := helmChartVersionDiff(ref.currentVersion, versions)
	if err != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("could not diff version for %q: %v", key.chartName, err))
		return
	}

	metricKey := metricKeyForRef(ref, key.chartName)

	var latestStr, latestPatchStr *string
	if latest != nil {
		s := latest.String()
		latestStr = &s
		passState.markChecked(&metricKey, true)
		trackOutdatedHelmChart(ref.profileKind, ref.profileNamespace, ref.profileName,
			ref.clusterType, ref.clusterNamespace, ref.clusterName,
			key.chartName, ref.releaseNamespace, ref.releaseName, logger)
	} else {
		passState.markChecked(&metricKey, false)
	}
	if latestPatch != nil {
		s := latestPatch.String()
		latestPatchStr = &s
	}

	if err := patchHelmChartSummaryVersions(ctx, c, ref, latestStr, latestPatchStr, logger); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to update HelmChartSummary for %s/%s: %v",
			ref.releaseNamespace, ref.releaseName, err))
	}
}

// patchHelmChartSummaryVersions updates the LatestVersion/LatestPatchVersion/LastCheckedTime
// fields on the named ClusterSummary's matching HelmChartSummary entry, mirroring the same
// Get -> mutate -> retry.RetryOnConflict(Status().Update()) pattern already used by
// updateValueHashOnHelmChartSummary. Always writes on a successful check, even when
// latest/latestPatch come back unchanged (in particular, unchanged-and-nil, the common case of
// a chart that is already up to date) -- LastCheckedTime advancing is the only signal that the
// checker is actually reaching this chart, as opposed to having stopped or never having checked
// it, so it can't be skipped just because the outdated-version verdict didn't change.
func patchHelmChartSummaryVersions(ctx context.Context, c client.Client, ref *helmChartRef,
	latest, latestPatch *string, logger logr.Logger) error {

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cs := &configv1beta1.ClusterSummary{}
		if err := c.Get(ctx,
			types.NamespacedName{Namespace: ref.clusterSummaryNamespace, Name: ref.clusterSummaryName}, cs); err != nil {
			return err
		}

		for i := range cs.Status.HelmReleaseSummaries {
			rs := &cs.Status.HelmReleaseSummaries[i]
			if rs.ReleaseName != ref.releaseName || rs.ReleaseNamespace != ref.releaseNamespace {
				continue
			}

			rs.LatestVersion = latest
			rs.LatestPatchVersion = latestPatch
			now := metav1.Now()
			rs.LastCheckedTime = &now

			return c.Status().Update(ctx, cs)
		}

		logger.V(logs.LogDebug).Info(fmt.Sprintf("release %s/%s no longer found on ClusterSummary %s/%s",
			ref.releaseNamespace, ref.releaseName, ref.clusterSummaryNamespace, ref.clusterSummaryName))
		return nil
	})
}

// RunHelmChartUpdateChecker runs checkOutdatedHelmCharts once immediately, then on every tick
// of interval, until ctx is done. Only ever started for the default (unsharded) addon-controller
// deployment: see the shardKey == "" gate in cmd/main.go.
func RunHelmChartUpdateChecker(ctx context.Context, c client.Client, interval time.Duration, logger logr.Logger) {
	checkOutdatedHelmCharts(ctx, c, logger)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("stopping helm chart update checker")
			return
		case <-ticker.C:
			checkOutdatedHelmCharts(ctx, c, logger)
		}
	}
}
