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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
)

// buildTar creates an uncompressed tar archive containing the given files.
// extractYAMLFromLayer receives already-uncompressed layer bytes, so no gzip
// wrapper is needed in tests.
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
})
