/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/go-containerregistry/pkg/registry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	orasremote "oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"

	"github.com/go-logr/logr"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// buildTar creates an uncompressed tar archive containing the given files.
func buildTar(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Size:     int64(len(content)),
		}
		Expect(tw.WriteHeader(hdr)).To(Succeed())
		_, err := tw.Write([]byte(content))
		Expect(err).ToNot(HaveOccurred())
	}
	Expect(tw.Close()).To(Succeed())
	return buf.Bytes()
}

// gzipBytes gzip-compresses raw, mirroring the layout produced by tools such
// as `flux push artifact`.
func gzipBytes(raw []byte) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write(raw)
	Expect(err).ToNot(HaveOccurred())
	Expect(gw.Close()).To(Succeed())
	return buf.Bytes()
}

var _ = Describe("extractYAMLFromLayer", func() {
	var logger logr.Logger
	const rawURL = "oci://example.com/unit/space/slug:head"

	BeforeEach(func() {
		logger = logr.Discard()
	})

	It("returns raw bytes unchanged when the layer is not a tar archive", func() {
		raw := []byte("apiVersion: v1\nkind: ConfigMap\n")
		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(raw))
	})

	It("extracts .yaml and .yml files from a tar archive", func() {
		cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\n"
		dep := "apiVersion: apps/v1\nkind: Deployment\n"
		raw := buildTar(map[string]string{
			"cm.yaml":    cm,
			"deploy.yml": dep,
			"README.txt": "should be ignored",
		})

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		s := string(got)
		Expect(s).To(ContainSubstring(cm))
		Expect(s).To(ContainSubstring(dep))
		Expect(s).NotTo(ContainSubstring("README"))
	})

	It("extracts .json files from a tar archive", func() {
		json := `{"apiVersion":"v1","kind":"ConfigMap"}`
		raw := buildTar(map[string]string{"resource.json": json})

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(got)).To(ContainSubstring(json))
	})

	It("returns empty bytes when the tar contains no YAML or JSON files", func() {
		raw := buildTar(map[string]string{
			"README.md": "no manifests here",
			"notes.txt": "nothing",
		})

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("returns empty bytes for an empty tar archive", func() {
		raw := buildTar(map[string]string{})

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("extracts .yaml files from a gzip-compressed tar archive", func() {
		svc := "apiVersion: v1\nkind: Service\nmetadata:\n  name: gzipped\n"
		raw := gzipBytes(buildTar(map[string]string{
			"svc.yaml": svc,
			"notes.md": "not a manifest",
		}))

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		s := string(got)
		Expect(s).To(ContainSubstring(svc))
		Expect(s).NotTo(ContainSubstring("notes"))
	})

	It("returns the decompressed bytes unchanged when a gzip layer is not a tar archive", func() {
		raw := gzipBytes([]byte("apiVersion: v1\nkind: ConfigMap\n"))

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal([]byte("apiVersion: v1\nkind: ConfigMap\n")))
	})

	It("returns raw bytes unchanged when the gzip header is present but the stream is invalid", func() {
		raw := append([]byte{0x1f, 0x8b}, []byte("not actually gzip")...)

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(raw))
	})

	It("skips macOS AppleDouble sidecar entries", func() {
		ns := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: sidecar-test\n"
		raw := buildTar(map[string]string{
			"ns.yaml":   ns,
			"._ns.yaml": "\x00\x05\x16\x07\x00\x02\x00\x00AppleDouble binary junk",
		})

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		s := string(got)
		Expect(s).To(ContainSubstring(ns))
		Expect(s).NotTo(ContainSubstring("AppleDouble"))
	})

	It("skips PAX header entries", func() {
		crb := "apiVersion: v1\nkind: ClusterRoleBinding\nmetadata:\n  name: pax-test\n"
		raw := buildTar(map[string]string{
			"crb.yaml":            crb,
			"PaxHeader/crb2.yaml": "not a real manifest",
		})

		got, err := extractYAMLFromLayer(raw, rawURL, logger)
		Expect(err).ToNot(HaveOccurred())
		s := string(got)
		Expect(s).To(ContainSubstring(crb))
		Expect(s).NotTo(ContainSubstring("not a real manifest"))
	})
})

var _ = Describe("fetchContent (http/https)", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = logr.Discard()
	})

	// serve starts an httptest server returning body for every request, and
	// returns its URL alongside a cleanup func.
	serve := func(body []byte) (string, func()) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, err := w.Write(body)
			Expect(err).ToNot(HaveOccurred())
		}))
		return server.URL, server.Close
	}

	It("returns a raw YAML document unchanged", func() {
		raw := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: http-raw\n")
		url, cleanup := serve(raw)
		defer cleanup()

		got, err := fetchContent(context.TODO(), url, remoteFetchOptions{}, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(raw))
	})

	It("decompresses a gzip-compressed YAML document", func() {
		raw := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: http-gzip\n")
		url, cleanup := serve(gzipBytes(raw))
		defer cleanup()

		got, err := fetchContent(context.TODO(), url, remoteFetchOptions{}, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(raw))
	})

	It("extracts .yaml files from a gzip-compressed tar archive (tar.gz)", func() {
		dep := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: http-targz\n"
		archive := gzipBytes(buildTar(map[string]string{
			"deploy.yaml": dep,
			"CHANGELOG":   "irrelevant changelog entry",
		}))
		url, cleanup := serve(archive)
		defer cleanup()

		got, err := fetchContent(context.TODO(), url, remoteFetchOptions{}, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		s := string(got)
		Expect(s).To(ContainSubstring(dep))
		Expect(s).NotTo(ContainSubstring("changelog"))
	})

	It("extracts .yaml files from an uncompressed tar archive", func() {
		svc := "apiVersion: v1\nkind: Service\nmetadata:\n  name: http-tar\n"
		archive := buildTar(map[string]string{"endpoints.yaml": svc})
		url, cleanup := serve(archive)
		defer cleanup()

		got, err := fetchContent(context.TODO(), url, remoteFetchOptions{}, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(got)).To(ContainSubstring(svc))
	})
})

var _ = Describe("pullOCIArtifactLayers (plain HTTP)", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = logr.Discard()
	})

	// servePlainOCIRegistry starts an in-process, plain-HTTP OCI registry
	// (github.com/google/go-containerregistry/pkg/registry) seeded with a
	// single-layer artifact tagged tag, and returns the "oci://" reference to
	// pull it back alongside a cleanup func.
	servePlainOCIRegistry := func(tag string, layerContent []byte) (string, func()) {
		server := httptest.NewServer(registry.New())

		store := memory.New()
		layerDesc := content.NewDescriptorFromBytes("application/vnd.projectsveltos.yaml", layerContent)
		Expect(store.Push(context.TODO(), layerDesc, bytes.NewReader(layerContent))).To(Succeed())

		manifestDesc, err := oras.PackManifest(context.TODO(), store, oras.PackManifestVersion1_1,
			"application/vnd.projectsveltos.manifest", oras.PackManifestOptions{Layers: []ocispec.Descriptor{layerDesc}})
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Tag(context.TODO(), manifestDesc, tag)).To(Succeed())

		host := strings.TrimPrefix(server.URL, "http://")
		repoRef := fmt.Sprintf("%s/unit/plainhttp:%s", host, tag)

		remoteRepo, err := orasremote.NewRepository(repoRef)
		Expect(err).ToNot(HaveOccurred())
		remoteRepo.PlainHTTP = true

		_, err = oras.Copy(context.TODO(), store, tag, remoteRepo, tag, oras.CopyOptions{})
		Expect(err).ToNot(HaveOccurred())

		return "oci://" + repoRef, server.Close
	}

	It("pulls layers when PlainHTTP is set", func() {
		layerContent := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: oci-plain-http\n")
		ociURL, cleanup := servePlainOCIRegistry("v1", layerContent)
		defer cleanup()

		got, err := fetchOCI(context.TODO(), ociURL, remoteFetchOptions{plainHTTP: true},
			"", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(layerContent))
	})

	It("fails without PlainHTTP against a plain-HTTP registry", func() {
		layerContent := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: oci-plain-http\n")
		ociURL, cleanup := servePlainOCIRegistry("v1", layerContent)
		defer cleanup()

		_, err := fetchOCI(context.TODO(), ociURL, remoteFetchOptions{},
			"", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("pullOCIArtifactLayers (self-signed TLS)", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = logr.Discard()
	})

	// serveSelfSignedOCIRegistry starts an in-process OCI registry behind a
	// self-signed TLS certificate (httptest.NewTLSServer) seeded with a
	// single-layer artifact tagged tag, and returns the "oci://" reference to
	// pull it back alongside a cleanup func. Seeding itself skips certificate
	// verification, same as a caFile-less client with InsecureSkipTLSVerify set
	// would need to, since the server's cert is not signed by a known CA.
	serveSelfSignedOCIRegistry := func(tag string, layerContent []byte) (string, func()) {
		server := httptest.NewTLSServer(registry.New())

		store := memory.New()
		layerDesc := content.NewDescriptorFromBytes("application/vnd.projectsveltos.yaml", layerContent)
		Expect(store.Push(context.TODO(), layerDesc, bytes.NewReader(layerContent))).To(Succeed())

		manifestDesc, err := oras.PackManifest(context.TODO(), store, oras.PackManifestVersion1_1,
			"application/vnd.projectsveltos.manifest", oras.PackManifestOptions{Layers: []ocispec.Descriptor{layerDesc}})
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Tag(context.TODO(), manifestDesc, tag)).To(Succeed())

		host := strings.TrimPrefix(server.URL, "https://")
		repoRef := fmt.Sprintf("%s/unit/selfsigned:%s", host, tag)

		remoteRepo, err := orasremote.NewRepository(repoRef)
		Expect(err).ToNot(HaveOccurred())
		remoteRepo.Client = &orasauth.Client{
			Client: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only, seeding a self-signed test registry
				},
			},
		}

		_, err = oras.Copy(context.TODO(), store, tag, remoteRepo, tag, oras.CopyOptions{})
		Expect(err).ToNot(HaveOccurred())

		return "oci://" + repoRef, server.Close
	}

	It("pulls layers when InsecureSkipTLSVerify is set and no caFile is provided", func() {
		layerContent := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: oci-self-signed\n")
		ociURL, cleanup := serveSelfSignedOCIRegistry("v1", layerContent)
		defer cleanup()

		got, err := fetchOCI(context.TODO(), ociURL, remoteFetchOptions{insecureSkipTLSVerify: true},
			"", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(layerContent))
	})

	It("fails without InsecureSkipTLSVerify against a self-signed TLS registry", func() {
		layerContent := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: oci-self-signed\n")
		ociURL, cleanup := serveSelfSignedOCIRegistry("v1", layerContent)
		defer cleanup()

		_, err := fetchOCI(context.TODO(), ociURL, remoteFetchOptions{},
			"", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).To(HaveOccurred())
	})
})
