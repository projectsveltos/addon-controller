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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	urlSourceKind      = "URL"
	urlFetchTimeout    = 30 * time.Second
	defaultURLInterval = 5 * time.Minute
)

// fetchURL retrieves the raw content from rawURL.
// If secretRef is non-nil, the Secret named secretRef.Name in secretNamespace is read
// for optional auth credentials (keys: "token", "username"+"password", "caFile").
func fetchURL(ctx context.Context, rawURL string, secretRef *corev1.LocalObjectReference,
	secretNamespace string, logger logr.Logger) ([]byte, error) {

	transport := http.DefaultTransport
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP request for %s: %w", rawURL, err)
	}

	if secretRef != nil {
		secret, err := getSecret(ctx, getManagementClusterClient(),
			types.NamespacedName{Namespace: secretNamespace, Name: secretRef.Name})
		if err != nil {
			return nil, fmt.Errorf("failed to get auth secret %s/%s for URL %s: %w",
				secretNamespace, secretRef.Name, rawURL, err)
		}

		if token, ok := secret.Data["token"]; ok {
			req.Header.Set("Authorization", "Bearer "+string(token))
		} else if username, ok := secret.Data["username"]; ok {
			if password, ok := secret.Data["password"]; ok {
				req.SetBasicAuth(string(username), string(password))
			}
		}

		if caFile, ok := secret.Data["caFile"]; ok {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caFile) {
				return nil, fmt.Errorf("failed to parse caFile from secret %s/%s",
					secretNamespace, secretRef.Name)
			}
			transport = &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool},
			}
		}
	}

	httpClient := &http.Client{
		Timeout:   urlFetchTimeout,
		Transport: transport,
	}

	logger.V(logs.LogDebug).Info(fmt.Sprintf("fetching URL %s", rawURL))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d fetching %s", resp.StatusCode, rawURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body from %s: %w", rawURL, err)
	}

	return body, nil
}

// deployContentOfURL fetches YAML/JSON content from a remote URL and deploys it
// to the destination cluster using the same pipeline as ConfigMap/Secret sources.
func deployContentOfURL(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, ref *referencedObject, dCtx *deploymentContext,
	logger logr.Logger) ([]libsveltosv1beta1.ResourceReport, error) {

	secretNamespace := dCtx.clusterSummary.Namespace
	body, err := fetchURL(ctx, ref.URL, ref.SecretRef, secretNamespace, logger)
	if err != nil {
		if ref.Optional {
			logger.V(logs.LogInfo).Info(fmt.Sprintf(
				"optional URL source %s could not be fetched, ignoring: %v", ref.URL, err))
			return nil, nil
		}
		return nil, err
	}

	// Build a synthetic source object so that deployContent can read annotations
	// the same way it does for ConfigMap/Secret references.
	syntheticSource := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: ref.URL,
		},
	}
	if ref.IsTemplate {
		syntheticSource.Annotations = map[string]string{
			libsveltosv1beta1.PolicyTemplateAnnotation: "ok",
		}
	}

	data := map[string]string{"content.yaml": string(body)}
	l := logger.WithValues("url", ref.URL)
	l.V(logs.LogDebug).Info("deploying URL content")

	return deployContent(ctx, deployingToMgmtCluster, destConfig, destClient, syntheticSource,
		data, ref.Tier, ref.SkipNamespaceCreation, dCtx, l)
}

// minURLInterval returns the shortest polling interval across all URL-based PolicyRefs,
// using defaultURLInterval for any entry that does not specify an explicit interval.
// Returns 0 if no URL-based PolicyRefs are present.
func minURLInterval(refs []configv1beta1.PolicyRef) time.Duration {
	result := time.Duration(0)
	for i := range refs {
		if refs[i].RemoteURL == nil {
			continue
		}
		interval := defaultURLInterval
		if refs[i].RemoteURL.Interval != nil && refs[i].RemoteURL.Interval.Duration > 0 {
			interval = refs[i].RemoteURL.Interval.Duration
		}
		if result == 0 || interval < result {
			result = interval
		}
	}
	return result
}
