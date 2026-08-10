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
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

		got, err := fetchContent(context.TODO(), url, nil, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(Equal(raw))
	})

	It("decompresses a gzip-compressed YAML document", func() {
		raw := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: http-gzip\n")
		url, cleanup := serve(gzipBytes(raw))
		defer cleanup()

		got, err := fetchContent(context.TODO(), url, nil, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
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

		got, err := fetchContent(context.TODO(), url, nil, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
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

		got, err := fetchContent(context.TODO(), url, nil, "", "", libsveltosv1beta1.ClusterTypeCapi, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(got)).To(ContainSubstring(svc))
	})
})
