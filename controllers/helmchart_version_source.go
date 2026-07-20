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
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/getter"
	"helm.sh/helm/v4/pkg/registry"
	repo "helm.sh/helm/v4/pkg/repo/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// helmChartKey identifies a distinct upstream chart to check, deduplicated across every
// HelmChartSummary entry, on every ClusterSummary, that references it. RepositoryURL,
// RepositoryName and ChartName here are always the fully resolved (post-template) values
// captured at deploy time by setResolvedHelmChartIdentity, never a template string.
type helmChartKey struct {
	repositoryURL  string
	repositoryName string
	chartName      string
}

const (
	// fetchAvailableVersionsTimeout bounds a single chart's fetch, so one slow or misbehaving
	// repository/registry can never stall an entire checkOutdatedHelmCharts pass.
	fetchAvailableVersionsTimeout = 30 * time.Second
)

// resolveChartCredentials fetches secretRef (already resolved to a concrete namespace/name by
// setResolvedHelmChartIdentity at deploy time, so no cluster context is needed here) from the
// management cluster and extracts username/password for hostname. Returns empty strings, no
// error, when secretRef is nil (anonymous access).
func resolveChartCredentials(ctx context.Context, c client.Client, secretRef *corev1.SecretReference,
	hostname string) (username, password string, err error) {

	if secretRef == nil {
		return "", "", nil
	}

	secret := &corev1.Secret{}
	if getErr := c.Get(ctx, types.NamespacedName{Namespace: secretRef.Namespace, Name: secretRef.Name}, secret); getErr != nil {
		return "", "", getErr
	}

	return getUsernameAndPasswordFromSecret(hostname, secret)
}

// fetchAvailableVersions returns every version string currently published for key, using the
// HTTP index.yaml path or the OCI registry Tags() path depending on key.repositoryURL.
// credentialsSecretRef, if non-nil, is used to authenticate the request; on any credential
// resolution failure this falls back to an anonymous request rather than failing outright,
// since that request may still succeed for a public chart.
func fetchAvailableVersions(ctx context.Context, c client.Client, key helmChartKey,
	credentialsSecretRef *corev1.SecretReference, logger logr.Logger) ([]string, error) {

	fetchCtx, cancel := context.WithTimeout(ctx, fetchAvailableVersionsTimeout)
	defer cancel()

	bareChartName := removeRepositoryNameFromChartName(key.chartName, key.repositoryName)

	parsedURL, err := url.Parse(key.repositoryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse repository URL %q: %w", key.repositoryURL, err)
	}

	username, password, credErr := resolveChartCredentials(fetchCtx, c, credentialsSecretRef, parsedURL.Host)
	if credErr != nil {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("failed to resolve credentials for %s, trying anonymously: %v",
			key.repositoryURL, credErr))
	}

	if registry.IsOCI(key.repositoryURL) {
		return fetchOCIChartVersions(fetchCtx, key.repositoryURL, bareChartName, username, password)
	}

	return fetchHTTPIndexChartVersions(fetchCtx, bareChartName, key.repositoryURL, username, password)
}

// fetchHTTPIndexChartVersions always downloads a fresh index.yaml into a scratch directory and
// returns every published version string for chartName. Deliberately does not use
// repoAddOrUpdate/the shared repo storage cache used by the deploy path: that cache is
// download-once-per-process, which is correct for pinning a chart at deploy time but wrong for
// a periodic freshness check that must see newly published versions on every pass.
func fetchHTTPIndexChartVersions(ctx context.Context, chartName, repoURL, username, password string) ([]string, error) {
	tmpDir, err := os.MkdirTemp("", "sveltos-helm-index-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir) // best-effort cleanup of a scratch temp dir

	settings := cli.New()
	entry := &repo.Entry{Name: "chart", URL: repoURL, Username: username, Password: password}
	chartRepo, err := repo.NewChartRepository(entry, getter.All(settings))
	if err != nil {
		return nil, err
	}
	chartRepo.CachePath = tmpDir

	type downloadResult struct {
		path string
		err  error
	}
	done := make(chan downloadResult, 1)
	go func() {
		p, dErr := chartRepo.DownloadIndexFile()
		done <- downloadResult{p, dErr}
	}()

	var indexPath string
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		indexPath = r.path
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out downloading index for repository %s", repoURL)
	}

	idx, err := repo.LoadIndexFile(indexPath)
	if err != nil {
		return nil, err
	}

	versions, ok := idx.Entries[chartName]
	if !ok {
		return nil, fmt.Errorf("chart %q not found in repository index %s", chartName, repoURL)
	}

	out := make([]string, 0, len(versions))
	for i := range versions {
		if versions[i] == nil || versions[i].Metadata == nil {
			continue
		}
		out = append(out, versions[i].Version)
	}
	return out, nil
}

// fetchOCIChartVersions lists every semver-parseable tag published for the chart identified by
// repoURL/repositoryName/bareChartName. Uses Helm's own registry.Client.Tags, which already
// filters to strict-semver tags and returns them sorted descending — no hand-rolled registry
// API calls needed. Plain-HTTP/insecure-TLS OCI registries are not supported by this check
// (a known, disclosed limitation): any failure here is treated as skippable by the caller.
func fetchOCIChartVersions(ctx context.Context, repoURL, bareChartName, username, password string,
) ([]string, error) {

	ref, err := buildOCIChartRef(repoURL, bareChartName)
	if err != nil {
		return nil, err
	}

	opts := []registry.ClientOption{registry.ClientOptWriter(io.Discard)}
	if username != "" && password != "" {
		opts = append(opts, registry.ClientOptBasicAuth(username, password))
	}

	registryClient, err := registry.NewClient(opts...)
	if err != nil {
		return nil, err
	}

	type tagsResult struct {
		tags []string
		err  error
	}
	done := make(chan tagsResult, 1)
	go func() {
		t, tErr := registryClient.Tags(ref)
		done <- tagsResult{t, tErr}
	}()

	select {
	case r := <-done:
		return r.tags, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out listing tags for %s", ref)
	}
}

// buildOCIChartRef mirrors the OCI-ref construction in getHelmChartAndRepoName
// (handlers_helm.go), but is pure — no ClusterSummary/Flux resolution needed, since
// Flux-sourced charts never reach this point (setResolvedHelmChartIdentity never populates
// their identity fields). Returns a ref without the "oci://" prefix, as expected by
// registry.Client.Tags.
func buildOCIChartRef(repoURL, bareChartName string) (string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", err
	}

	// bareChartName is already stripped of any repository-name prefix by the caller
	// (removeRepositoryNameFromChartName), so it can be joined onto the repo path directly.
	u.Path = path.Join(u.Path, bareChartName)

	return strings.TrimPrefix(u.String(), "oci://"), nil
}
