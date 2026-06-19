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

package controllers_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
)

const (
	testHTTPRepoURL  = "https://charts.example.com"
	testOCIRepoURL   = "oci://registry.example.com/charts/myapp"
	testKeyringName  = "my-keyring"
	testPEMBlockType = "PUBLIC KEY"
)

var _ = Describe("SourceIntegrity", func() {

	Describe("needsCosignVerification", func() {
		It("returns false for an HTTP chart", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL:         testHTTPRepoURL,
				SignatureVerification: &configv1beta1.CosignVerification{Provider: configv1beta1.CosignProviderPublicKey},
			}
			Expect(controllers.NeedsCosignVerification(chart)).To(BeFalse())
		})

		It("returns false for an OCI chart without SignatureVerification", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL: testOCIRepoURL,
			}
			Expect(controllers.NeedsCosignVerification(chart)).To(BeFalse())
		})

		It("returns false for a Flux OCI source even with SignatureVerification", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL:         "ocirepository://default/my-oci-source/charts",
				SignatureVerification: &configv1beta1.CosignVerification{Provider: configv1beta1.CosignProviderPublicKey},
			}
			Expect(controllers.NeedsCosignVerification(chart)).To(BeFalse())
		})

		It("returns true for an OCI chart with SignatureVerification", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL:         testOCIRepoURL,
				SignatureVerification: &configv1beta1.CosignVerification{Provider: configv1beta1.CosignProviderPublicKey},
			}
			Expect(controllers.NeedsCosignVerification(chart)).To(BeTrue())
		})
	})

	Describe("needsProvenanceVerification", func() {
		It("returns false for an OCI chart", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL:          testOCIRepoURL,
				ProvenanceVerification: &configv1beta1.ProvenanceVerification{KeyringSecretRef: corev1.SecretReference{Name: testKeyringName}},
			}
			Expect(controllers.NeedsProvenanceVerification(chart)).To(BeFalse())
		})

		It("returns false for an HTTP chart without ProvenanceVerification", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL: testHTTPRepoURL,
			}
			Expect(controllers.NeedsProvenanceVerification(chart)).To(BeFalse())
		})

		It("returns false for a Flux git source even with ProvenanceVerification", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL:          "gitrepository://default/my-repo/charts",
				ProvenanceVerification: &configv1beta1.ProvenanceVerification{KeyringSecretRef: corev1.SecretReference{Name: testKeyringName}},
			}
			Expect(controllers.NeedsProvenanceVerification(chart)).To(BeFalse())
		})

		It("returns true for an HTTP chart with ProvenanceVerification", func() {
			chart := &configv1beta1.HelmChart{
				RepositoryURL:          testHTTPRepoURL,
				ProvenanceVerification: &configv1beta1.ProvenanceVerification{KeyringSecretRef: corev1.SecretReference{Name: testKeyringName}},
			}
			Expect(controllers.NeedsProvenanceVerification(chart)).To(BeTrue())
		})
	})

	Describe("loadCosignPublicKey", func() {
		It("returns an error when data is empty", func() {
			_, err := controllers.LoadCosignPublicKey([]byte{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no PEM block found"))
		})

		It("returns an error when PEM block has invalid DER bytes", func() {
			block := &pem.Block{Type: testPEMBlockType, Bytes: []byte("not-valid-der")}
			_, err := controllers.LoadCosignPublicKey(pem.EncodeToMemory(block))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to parse PKIX public key"))
		})

		It("returns a verifier for a valid ECDSA P-256 public key", func() {
			priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			Expect(err).NotTo(HaveOccurred())
			der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
			Expect(err).NotTo(HaveOccurred())
			pemData := pem.EncodeToMemory(&pem.Block{Type: testPEMBlockType, Bytes: der})

			verifier, err := controllers.LoadCosignPublicKey(pemData)
			Expect(err).NotTo(HaveOccurred())
			Expect(verifier).NotTo(BeNil())
		})
	})

	Describe("createFileWithKeyring", func() {
		var namespace string
		var clusterSummary *configv1beta1.ClusterSummary

		BeforeEach(func() {
			namespace = randomString()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			Expect(testEnv.Client.Create(context.Background(), ns)).To(Succeed())

			clusterSummary = &configv1beta1.ClusterSummary{
				ObjectMeta: metav1.ObjectMeta{
					Name:      randomString(),
					Namespace: namespace,
				},
			}
		})

		It("returns empty path when ProvenanceVerification is nil", func() {
			chart := &configv1beta1.HelmChart{}
			path, err := controllers.CreateFileWithKeyring(context.Background(), clusterSummary, chart)
			Expect(err).NotTo(HaveOccurred())
			Expect(path).To(BeEmpty())
		})

		It("returns an error when the keyring Secret does not exist", func() {
			chart := &configv1beta1.HelmChart{
				ProvenanceVerification: &configv1beta1.ProvenanceVerification{
					KeyringSecretRef: corev1.SecretReference{Name: "nonexistent-secret"},
				},
			}
			_, err := controllers.CreateFileWithKeyring(context.Background(), clusterSummary, chart)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nonexistent-secret"))
		})

		It("returns an error when the Secret has no keyring.gpg key", func() {
			secretName := randomString()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"wrong-key": []byte("data")},
			}
			Expect(testEnv.Client.Create(context.Background(), secret)).To(Succeed())

			// Wait for the informer cache to pick up the newly created Secret
			// before calling createFileWithKeyring, which reads via the cached client.
			Eventually(func() error {
				return testEnv.Client.Get(context.Background(),
					types.NamespacedName{Namespace: namespace, Name: secretName},
					&corev1.Secret{})
			}, timeout, pollingInterval).Should(Succeed())

			chart := &configv1beta1.HelmChart{
				ProvenanceVerification: &configv1beta1.ProvenanceVerification{
					KeyringSecretRef: corev1.SecretReference{Name: secretName},
				},
			}
			_, err := controllers.CreateFileWithKeyring(context.Background(), clusterSummary, chart)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("missing key \"keyring.gpg\""))
		})

		It("creates a temporary file containing the keyring data", func() {
			keyringData := []byte("fake-gpg-keyring-data")
			secretName := randomString()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"keyring.gpg": keyringData},
			}
			Expect(testEnv.Client.Create(context.Background(), secret)).To(Succeed())

			chart := &configv1beta1.HelmChart{
				ProvenanceVerification: &configv1beta1.ProvenanceVerification{
					KeyringSecretRef: corev1.SecretReference{Name: secretName},
				},
			}
			path, err := controllers.CreateFileWithKeyring(context.Background(), clusterSummary, chart)
			Expect(err).NotTo(HaveOccurred())
			Expect(path).NotTo(BeEmpty())
			defer os.Remove(path)

			contents, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(contents).To(Equal(keyringData))
		})
	})
})
