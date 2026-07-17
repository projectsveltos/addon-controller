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
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	orasremote "oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltostemplate "github.com/projectsveltos/libsveltos/lib/template"
)

const (
	urlSourceKind      = "URL"
	urlFetchTimeout    = 30 * time.Second
	defaultURLInterval = 5 * time.Minute
)

// fetchURL retrieves the raw content from rawURL.
// If secretRef is non-nil, the referenced Secret is read for optional auth
// credentials (keys: "token", "username"+"password", "caFile").
// The secretRef Name and Namespace support Go templating against the target cluster.
// When Namespace is empty it defaults to clusterNamespace.
func fetchURL(ctx context.Context, rawURL string, secretRef *corev1.SecretReference,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) ([]byte, error) {

	transport := http.DefaultTransport
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP request for %s: %w", rawURL, err)
	}

	if secretRef != nil {
		ns, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, secretRef.Namespace, clusterType)
		if err != nil {
			return nil, fmt.Errorf("failed to instantiate secret namespace for URL %s: %w", rawURL, err)
		}
		name, err := libsveltostemplate.GetReferenceResourceName(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, secretRef.Name, clusterType)
		if err != nil {
			return nil, fmt.Errorf("failed to instantiate secret name for URL %s: %w", rawURL, err)
		}
		secret, err := getSecret(ctx, getManagementClusterClient(),
			types.NamespacedName{Namespace: ns, Name: name})
		if err != nil {
			return nil, fmt.Errorf("failed to get auth secret %s/%s for URL %s: %w",
				ns, name, rawURL, err)
		}

		if token, ok := secret.Data["token"]; ok {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		} else if username, ok := secret.Data["username"]; ok {
			if password, ok := secret.Data["password"]; ok {
				req.SetBasicAuth(strings.TrimSpace(string(username)), strings.TrimSpace(string(password)))
			}
		}

		if caFile, ok := secret.Data["caFile"]; ok {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caFile) {
				return nil, fmt.Errorf("failed to parse caFile from secret %s/%s",
					ns, name)
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

// fetchContent retrieves raw manifest bytes from a remote source.
// It dispatches on the URL scheme: "oci://" routes to fetchOCI; everything
// else is handled by fetchURL.
// The secretRef Name and Namespace are treated as Go templates and instantiated
// against the target cluster, following the same convention as PolicyRef ConfigMap/Secret
// references. When Namespace is empty it defaults to clusterNamespace.
func fetchContent(ctx context.Context, rawURL string, secretRef *corev1.SecretReference,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) ([]byte, error) {

	if strings.HasPrefix(rawURL, "oci://") {
		return fetchOCI(ctx, rawURL, secretRef, clusterNamespace, clusterName, clusterType, logger)
	}
	return fetchURL(ctx, rawURL, secretRef, clusterNamespace, clusterName, clusterType, logger)
}

// fetchOCI pulls an OCI artifact and returns its YAML content.
// The artifact layers are read as gzipped tar archives; if a layer is not a
// valid tar archive it is used as-is (raw YAML blob). All .yaml/.yml/.json
// files found across all layers are concatenated and returned.
// Supported Secret keys: "token" (pre-obtained bearer token), "username"+"password" (basic auth
// exchanged for a registry token), "caFile" (PEM CA for TLS verification).
// The secretRef Name and Namespace support Go templating against the target cluster.
// When Namespace is empty it defaults to clusterNamespace.
func fetchOCI(ctx context.Context, rawURL string, secretRef *corev1.SecretReference,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	logger logr.Logger) ([]byte, error) {

	ref := strings.TrimPrefix(rawURL, "oci://")

	repo, err := orasremote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OCI reference %s: %w", rawURL, err)
	}

	authClient := &orasauth.Client{}

	if secretRef != nil {
		ns, err := libsveltostemplate.GetReferenceResourceNamespace(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, secretRef.Namespace, clusterType)
		if err != nil {
			return nil, fmt.Errorf("failed to instantiate secret namespace for OCI %s: %w", rawURL, err)
		}
		name, err := libsveltostemplate.GetReferenceResourceName(ctx, getManagementClusterClient(),
			clusterNamespace, clusterName, secretRef.Name, clusterType)
		if err != nil {
			return nil, fmt.Errorf("failed to instantiate secret name for OCI %s: %w", rawURL, err)
		}
		secret, err := getSecret(ctx, getManagementClusterClient(),
			types.NamespacedName{Namespace: ns, Name: name})
		if err != nil {
			return nil, fmt.Errorf("failed to get auth secret %s/%s for OCI %s: %w",
				ns, name, rawURL, err)
		}

		var cred orasauth.Credential
		if token, ok := secret.Data["token"]; ok {
			cred = orasauth.Credential{AccessToken: strings.TrimSpace(string(token))}
		} else if username, ok := secret.Data["username"]; ok {
			if password, ok := secret.Data["password"]; ok {
				cred = orasauth.Credential{
					Username: strings.TrimSpace(string(username)),
					Password: strings.TrimSpace(string(password)),
				}
			}
		}
		authClient.Credential = orasauth.StaticCredential(repo.Reference.Registry, cred)

		if caFile, ok := secret.Data["caFile"]; ok {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caFile) {
				return nil, fmt.Errorf("failed to parse caFile from secret %s/%s", ns, name)
			}
			authClient.Client = &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{RootCAs: pool},
				},
			}
		}
	}

	repo.Client = authClient

	logger.V(logs.LogDebug).Info(fmt.Sprintf("pulling OCI artifact %s", rawURL))

	_, manifestRC, err := repo.FetchReference(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to pull OCI artifact %s: %w", rawURL, err)
	}
	defer manifestRC.Close()

	manifestBytes, err := io.ReadAll(manifestRC)
	if err != nil {
		return nil, fmt.Errorf("failed to read OCI manifest %s: %w", rawURL, err)
	}

	var manifest struct {
		Layers []ocispec.Descriptor `json:"layers"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse OCI manifest %s: %w", rawURL, err)
	}

	var buf bytes.Buffer
	for _, layer := range manifest.Layers {
		rc, err := repo.Fetch(ctx, layer)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch OCI layer from %s: %w", rawURL, err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read OCI layer from %s: %w", rawURL, err)
		}
		content, err := extractYAMLFromLayer(raw, rawURL, logger)
		if err != nil {
			return nil, err
		}
		buf.Write(content)
	}

	return buf.Bytes(), nil
}

// extractYAMLFromLayer extracts YAML/JSON manifest bytes from a single OCI layer.
// It first attempts to parse the bytes as a tar archive and concatenates all
// .yaml/.yml/.json files found inside. If the bytes are not a valid tar archive
// (e.g. a raw YAML blob), they are returned as-is.
func extractYAMLFromLayer(raw []byte, rawURL string, logger logr.Logger) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	hdr, err := tr.Next()
	if err == io.EOF {
		// Valid tar archive with no entries.
		return []byte{}, nil
	}
	if err != nil {
		// Not a tar archive — treat the raw bytes as YAML/JSON content directly.
		logger.V(logs.LogDebug).Info(fmt.Sprintf(
			"OCI layer from %s is not a tar archive, using as raw content", rawURL))
		return raw, nil
	}

	var buf bytes.Buffer
	for {
		if hdr.Typeflag == tar.TypeReg {
			ext := strings.ToLower(filepath.Ext(hdr.Name))
			if ext == ".yaml" || ext == ".yml" || ext == ".json" {
				content, err := io.ReadAll(tr)
				if err != nil {
					return nil, fmt.Errorf("failed to read %s from OCI artifact %s: %w",
						hdr.Name, rawURL, err)
				}
				buf.Write(content)
				buf.WriteString("\n---\n")
			}
		}
		hdr, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar from OCI artifact %s: %w", rawURL, err)
		}
	}
	return buf.Bytes(), nil
}

// deployContentOfURL fetches YAML/JSON content from a remote source and deploys it
// to the destination cluster using the same pipeline as ConfigMap/Secret sources.
func deployContentOfURL(ctx context.Context, deployingToMgmtCluster bool, destConfig *rest.Config,
	destClient client.Client, ref *referencedObject, dCtx *deploymentContext,
	logger logr.Logger) ([]libsveltosv1beta1.ResourceReport, error) {

	body, err := fetchContent(ctx, ref.URL, ref.SecretRef,
		dCtx.clusterSummary.Spec.ClusterNamespace, dCtx.clusterSummary.Spec.ClusterName,
		dCtx.clusterSummary.Spec.ClusterType, logger)
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
			libsveltosv1beta1.PolicyTemplateAnnotation: annotationValueOk,
		}
	}

	data := map[string]string{"content.yaml": string(body)}
	l := logger.WithValues("url", ref.URL)
	l.V(logs.LogDebug).Info("deploying URL content")

	return deployContent(ctx, deployingToMgmtCluster, destConfig, destClient, syntheticSource,
		data, ref.Tier, ref.SkipNamespaceCreation, ref.Force, dCtx, l)
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
