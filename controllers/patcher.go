/*
Copyright 2021 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Credit: https://github.com/fluxcd/helm-controller/blob/42fe4a39c184ee586ae59fb83fb6556f7e976219/internal/postrender/kustomize.go
*/

package controllers

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	uyaml "k8s.io/apimachinery/pkg/util/yaml"
	kustypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	kyaml "sigs.k8s.io/kustomize/kyaml/yaml"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

// Kustomize is a Helm post-render plugin that runs Kustomize.
type CustomPatchPostRenderer struct {
	// Patches is a list of patches to apply to the rendered manifests.
	Patches []configv1beta1.Patch
}

func (k *CustomPatchPostRenderer) RunUnstructured(unstructuredObjs []*unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	var renderedManifests bytes.Buffer
	for _, obj := range unstructuredObjs {
		data, err := kyaml.Marshal(obj.Object)
		if err != nil {
			return nil, err
		}
		renderedManifests.Write(data)
	}

	manifests, err := k.Run(&renderedManifests)
	if err != nil {
		return nil, err
	}

	return parseYAMLToUnstructured(manifests)
}

func (k *CustomPatchPostRenderer) Run(renderedManifests *bytes.Buffer) (modifiedManifests *bytes.Buffer, err error) {
	fs := filesys.MakeFsInMemory()
	cfg := kustypes.Kustomization{}
	cfg.APIVersion = kustypes.KustomizationVersion
	cfg.Kind = kustypes.KustomizationKind

	// Add rendered Helm output as input resource to the Kustomization.
	const input = "helm-output.yaml"
	cfg.Resources = append(cfg.Resources, input)
	if err := writeFile(fs, input, renderedManifests); err != nil {
		return nil, err
	}

	// Add patches.
	for _, m := range k.Patches {
		cfg.Patches = append(cfg.Patches, kustypes.Patch{
			Patch:  m.Patch,
			Target: adaptSelector(m.Target),
		})
	}

	// Write kustomization config to file.
	kustomization, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	if err := writeToFile(fs, "kustomization.yaml", kustomization); err != nil {
		return nil, err
	}

	resMap, err := buildKustomization(fs, ".")
	if err != nil {
		return nil, err
	}
	yaml, err := resMap.AsYaml()
	if err != nil {
		return nil, err
	}

	return bytes.NewBuffer(yaml), nil
}

func writeToFile(fs filesys.FileSystem, path string, content []byte) error {
	helmOutput, err := fs.Create(path)
	if err != nil {
		return err
	}
	if _, err = helmOutput.Write(content); err != nil {
		return err
	}
	return helmOutput.Close()
}

func writeFile(fs filesys.FileSystem, path string, content *bytes.Buffer) error {
	helmOutput, err := fs.Create(path)
	if err != nil {
		return err
	}
	if _, err = content.WriteTo(helmOutput); err != nil {
		return err
	}
	return helmOutput.Close()
}

func adaptSelector(selector *configv1beta1.PatchSelector) (output *kustypes.Selector) {
	if selector != nil {
		output = &kustypes.Selector{}
		output.Gvk.Group = selector.Group
		output.Gvk.Kind = selector.Kind
		output.Gvk.Version = selector.Version
		output.Name = selector.Name
		output.Namespace = selector.Namespace
		output.LabelSelector = selector.LabelSelector
		output.AnnotationSelector = selector.AnnotationSelector
	}
	return
}

func parseYAMLToUnstructured(yamlData *bytes.Buffer) ([]*unstructured.Unstructured, error) {
	decoder := uyaml.NewYAMLToJSONDecoder(yamlData)
	var objs []*unstructured.Unstructured
	for {
		var obj map[string]interface{}
		if err := decoder.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		objs = append(objs, &unstructured.Unstructured{Object: obj})
	}
	return objs, nil
}
