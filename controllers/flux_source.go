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

package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fluxcd/pkg/http/fetch"
	"github.com/fluxcd/pkg/tar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	gitRepositoryScheme = "gitrepository"
	ociRepositoryScheme = "ocirepository"
	bucketScheme        = "bucket"
)

func prepareFileSystemWithFluxSource(source sourcev1.Source, logger logr.Logger) (string, error) {
	if source.GetArtifact() == nil {
		msg := "Source is not ready, artifact not found"
		logger.V(logs.LogInfo).Info(msg)
		return "", fmt.Errorf("prepareFileSystemWithFluxSource: %s", msg)
	}

	// Update status with the reconciliation progress.
	// revision := source.GetArtifact().Revision

	// Sanitize revision string by replacing invalid filesystem characters
	// Git artifact revisions contain characters like '/', ':', '@' which are not valid in directory names
	sanitizedRevision := strings.NewReplacer("/", "-", ":", "-", "@", "-").Replace(source.GetArtifact().Revision)

	// Create tmp dir.
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("kustomization-%s", sanitizedRevision))
	if err != nil {
		err = fmt.Errorf("prepareFileSystemWithFluxSource: tmp dir error: %w", err)
		return "", err
	}

	artifactFetcher := fetch.New(
		fetch.WithRetries(1),
		fetch.WithMaxDownloadSize(tar.UnlimitedUntarSize),
		fetch.WithUntar(tar.WithMaxUntarSize(tar.UnlimitedUntarSize)),
		fetch.WithHostnameOverwrite(os.Getenv("SOURCE_CONTROLLER_LOCALHOST")))

	// Download artifact and extract files to the tmp dir.
	err = artifactFetcher.Fetch(source.GetArtifact().URL, source.GetArtifact().Digest, tmpDir)
	if err != nil {
		return "", err
	}

	return tmpDir, nil
}

func getSource(ctx context.Context, c client.Client, namespace, sourceName, sourceKind string,
) (client.Object, error) {

	namespacedName := types.NamespacedName{
		Namespace: namespace,
		Name:      sourceName,
	}

	switch sourceKind {
	case sourcev1.GitRepositoryKind:
		var repository sourcev1.GitRepository
		err := c.Get(ctx, namespacedName, &repository)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		return &repository, nil
	case sourcev1b2.OCIRepositoryKind:
		var repository sourcev1b2.OCIRepository
		err := c.Get(ctx, namespacedName, &repository)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		return &repository, nil
	case sourcev1b2.BucketKind:
		var bucket sourcev1b2.Bucket
		err := c.Get(ctx, namespacedName, &bucket)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		return &bucket, nil
	default:
		return nil, fmt.Errorf("source `%s` kind '%s' not supported",
			sourceName, sourceKind)
	}
}

func isReferencingFluxSource(hc *configv1beta1.HelmChart) bool {
	lowerRepoURL := strings.ToLower(hc.RepositoryURL)
	return strings.HasPrefix(lowerRepoURL, fmt.Sprintf("%s://", gitRepositoryScheme)) ||
		strings.HasPrefix(lowerRepoURL, fmt.Sprintf("%s://", ociRepositoryScheme)) ||
		strings.HasPrefix(lowerRepoURL, fmt.Sprintf("%s://", bucketScheme))
}

func getReferencedFluxSourceFromURL(hc *configv1beta1.HelmChart) (*corev1.ObjectReference, string, error) {
	const repoURLParts = 2
	parts := strings.SplitN(hc.RepositoryURL, "://", repoURLParts)
	if len(parts) != repoURLParts {
		return nil, "", fmt.Errorf(
			"incorrect format: %q. Expected format sourceKind://sourceNamespace/sourceName/sourcePath",
			hc.RepositoryURL,
		)
	}

	sourceKind := parts[0]
	remainingParts := strings.Split(parts[1], "/")
	if len(remainingParts) < 3 { //nolint: mnd // expected namespace, name and path
		return nil, "", fmt.Errorf(
			"incorrect format: %q. Expected format sourceKind://sourceNamespace/sourceName/sourcePath",
			hc.RepositoryURL,
		)
	}

	sourceNamespace := remainingParts[0]
	sourceName := remainingParts[1]
	sourcePath := strings.Join(remainingParts[2:], "/")

	sourceRef := &corev1.ObjectReference{
		Namespace: sourceNamespace,
		Name:      sourceName,
	}

	switch strings.ToLower(sourceKind) {
	case strings.ToLower(sourcev1.GitRepositoryKind):
		sourceRef.Kind = sourcev1.GitRepositoryKind
		sourceRef.APIVersion = sourcev1.GroupVersion.String()
	case strings.ToLower(sourcev1b2.OCIRepositoryKind):
		sourceRef.Kind = sourcev1b2.OCIRepositoryKind
		sourceRef.APIVersion = sourcev1b2.GroupVersion.String()
	case strings.ToLower(sourcev1b2.BucketKind):
		sourceRef.Kind = sourcev1b2.BucketKind
		sourceRef.APIVersion = sourcev1b2.GroupVersion.String()
	}
	return sourceRef, sourcePath, nil
}
