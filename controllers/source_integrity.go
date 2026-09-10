/*
Copyright 2022-26. projectsveltos.io. All rights reserved.

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
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/sigstore/cosign/v3/cmd/cosign/cli/fulcio"
	coptions "github.com/sigstore/cosign/v3/cmd/cosign/cli/options"
	"github.com/sigstore/cosign/v3/pkg/cosign"
	sigsig "github.com/sigstore/sigstore/pkg/signature"
	"helm.sh/helm/v4/pkg/registry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// needsCosignVerification reports whether the chart requires Cosign signature verification.
// Only applies to non-Flux OCI charts that have SignatureVerification configured.
func needsCosignVerification(requestedChart *configv1beta1.HelmChart) bool {
	return !isReferencingFluxSource(requestedChart) &&
		registry.IsOCI(requestedChart.RepositoryURL) &&
		requestedChart.SignatureVerification != nil
}

// needsProvenanceVerification reports whether the chart requires Helm GPG .prov verification.
// Only applies to non-Flux HTTP charts that have ProvenanceVerification configured.
func needsProvenanceVerification(requestedChart *configv1beta1.HelmChart) bool {
	return !isReferencingFluxSource(requestedChart) &&
		!registry.IsOCI(requestedChart.RepositoryURL) &&
		requestedChart.ProvenanceVerification != nil
}

// verifyCosignSignature verifies the Cosign signature for an OCI Helm chart before deployment.
// ociChartRef is the chart reference as returned by getHelmChartAndRepoName, e.g.
// "oci://registry.example.com/charts/myapp". The version tag is appended from requestedChart.ChartVersion.
func verifyCosignSignature(ctx context.Context, requestedChart *configv1beta1.HelmChart,
	ociChartRef string, secretNamespace string, logger logr.Logger) error {

	sv := requestedChart.SignatureVerification

	// Strip "oci://" prefix so name.ParseReference can handle the reference.
	ref := strings.TrimPrefix(ociChartRef, "oci://")
	if requestedChart.ChartVersion != "" {
		ref += ":" + requestedChart.ChartVersion
	}

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("cosign: failed to parse OCI reference %q: %w", ref, err)
	}

	// Set up a keychain-aware registry transport so cosign can authenticate
	// against registries that require credentials.
	ro := coptions.RegistryOptions{}
	registryClientOpts, err := ro.ClientOpts(ctx)
	if err != nil {
		return fmt.Errorf("cosign: failed to build registry client options: %w", err)
	}

	checkOpts := &cosign.CheckOpts{
		RegistryClientOpts: registryClientOpts,
	}

	switch sv.Provider {
	case configv1beta1.CosignProviderPublicKey:
		if sv.SecretRef == nil {
			return fmt.Errorf("cosign: signatureVerification.secretRef is required when provider is PublicKey")
		}
		ns := sv.SecretRef.Namespace
		if ns == "" {
			ns = secretNamespace
		}
		secret := &corev1.Secret{}
		if err := getManagementClusterClient().Get(ctx,
			types.NamespacedName{Namespace: ns, Name: sv.SecretRef.Name},
			secret); err != nil {
			return fmt.Errorf("cosign: failed to get public key secret %s/%s: %w",
				ns, sv.SecretRef.Name, err)
		}
		pubKeyPEM, ok := secret.Data["cosign.pub"]
		if !ok {
			return fmt.Errorf("cosign: secret %s/%s missing key \"cosign.pub\"",
				ns, sv.SecretRef.Name)
		}
		verifier, err := loadCosignPublicKey(pubKeyPEM)
		if err != nil {
			return fmt.Errorf("cosign: failed to load public key from secret %s/%s: %w",
				ns, sv.SecretRef.Name, err)
		}
		checkOpts.SigVerifier = verifier
		checkOpts.IgnoreTlog = true
		checkOpts.IgnoreSCT = true

	case configv1beta1.CosignProviderKeyless:
		// Keyless signatures from CI systems are stored as OCI 1.1 referrers;
		// enable the referrers API so cosign can find them.
		checkOpts.ExperimentalOCI11 = true
		if err := applyKeylessOpts(ctx, sv, checkOpts); err != nil {
			return err
		}

	default:
		return fmt.Errorf("cosign: unknown provider %q", sv.Provider)
	}

	logger.V(logs.LogInfo).Info(fmt.Sprintf("verifying cosign signature for %s", ref))

	// Newer cosign versions (v3.2+) store static-key signatures as Sigstore Bundle v0.3
	// (a DSSE envelope OCI referrer with artifactType application/vnd.dev.sigstore.bundle.v0.3+json).
	// VerifyImageSignatures cannot find these because it only scans the legacy .sig tag and
	// application/vnd.dev.cosign.artifact.sig.v1+json referrers. Try the bundle format path
	// first and fall back to the legacy path if no bundle-format signature is found.
	if sv.Provider == configv1beta1.CosignProviderPublicKey {
		bundleOpts := *checkOpts
		bundleOpts.NewBundleFormat = true
		if _, _, err := cosign.VerifyImageAttestations(ctx, imgRef, &bundleOpts); err == nil {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("cosign signature verified for %s", ref))
			return nil
		}
	}

	if _, _, err := cosign.VerifyImageSignatures(ctx, imgRef, checkOpts); err != nil {
		return fmt.Errorf("cosign signature verification failed for %s: %w", ref, err)
	}
	logger.V(logs.LogInfo).Info(fmt.Sprintf("cosign signature verified for %s", ref))
	return nil
}

// applyKeylessOpts populates checkOpts for keyless (Sigstore) verification:
// certificate identity matchers, Fulcio root/intermediate CAs, and Rekor public keys.
func applyKeylessOpts(ctx context.Context, sv *configv1beta1.CosignVerification,
	checkOpts *cosign.CheckOpts) error {

	if len(sv.MatchOIDCIdentity) == 0 {
		return fmt.Errorf("cosign: signatureVerification.matchOIDCIdentity is required when provider is Keyless")
	}
	identities := make([]cosign.Identity, len(sv.MatchOIDCIdentity))
	for i, m := range sv.MatchOIDCIdentity {
		identities[i] = cosign.Identity{
			IssuerRegExp:  m.Issuer,
			SubjectRegExp: m.Subject,
		}
	}
	checkOpts.Identities = identities

	roots, err := fulcio.GetRoots()
	if err != nil {
		return fmt.Errorf("cosign: failed to fetch Fulcio root certificates: %w", err)
	}
	checkOpts.RootCerts = roots

	intermediates, err := fulcio.GetIntermediates()
	if err != nil {
		return fmt.Errorf("cosign: failed to fetch Fulcio intermediate certificates: %w", err)
	}
	checkOpts.IntermediateCerts = intermediates

	rekorPubs, err := cosign.GetRekorPubs(ctx)
	if err != nil {
		return fmt.Errorf("cosign: failed to fetch Rekor public keys: %w", err)
	}
	checkOpts.RekorPubKeys = rekorPubs

	ctLogPubs, err := cosign.GetCTLogPubs(ctx)
	if err != nil {
		return fmt.Errorf("cosign: failed to fetch CT log public keys: %w", err)
	}
	checkOpts.CTLogPubKeys = ctLogPubs

	return nil
}

// loadCosignPublicKey parses a PEM-encoded public key and returns a signature verifier.
func loadCosignPublicKey(pemData []byte) (sigsig.Verifier, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in cosign.pub")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PKIX public key: %w", err)
	}
	return sigsig.LoadVerifier(pub, crypto.SHA256)
}

// createFileWithKeyring reads the GPG keyring from the Secret referenced by
// ProvenanceVerification.KeyringSecretRef and writes it to a temporary file.
// The caller is responsible for removing the file when done.
func createFileWithKeyring(ctx context.Context, clusterSummary *configv1beta1.ClusterSummary,
	requestedChart *configv1beta1.HelmChart) (string, error) {

	if requestedChart.ProvenanceVerification == nil {
		return "", nil
	}

	secretRef := requestedChart.ProvenanceVerification.KeyringSecretRef
	ns := secretRef.Namespace
	if ns == "" {
		ns = clusterSummary.Namespace
	}
	secret := &corev1.Secret{}
	if err := getManagementClusterClient().Get(ctx,
		types.NamespacedName{Namespace: ns, Name: secretRef.Name},
		secret); err != nil {
		return "", fmt.Errorf("failed to get keyring secret %s/%s: %w",
			ns, secretRef.Name, err)
	}

	keyring, ok := secret.Data["keyring.gpg"]
	if !ok {
		return "", fmt.Errorf("secret %s/%s missing key \"keyring.gpg\"",
			ns, secretRef.Name)
	}

	return createTemporaryFile("keyring-*.gpg", keyring)
}
