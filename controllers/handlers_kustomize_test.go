/*
Copyright 2023. projectsveltos.io. All rights reserved.

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
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
)

var _ = Describe("KustomizeRefs", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

		clusterProfile = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
		}

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, cluster.Name, false)
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: cluster.Namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		prepareForDeployment(clusterProfile, clusterSummary, cluster)

		// Get ClusterSummary so OwnerReference is set
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, clusterSummary)).To(Succeed())
	})

	AfterEach(func() {
		deleteResources(namespace, clusterProfile, clusterSummary)
	})

	It("undeployKustomizeRefs removes all ClusterRole and Role created by a ClusterSummary", func() {
		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				Labels: map[string]string{
					deployer.ReferenceKindLabel:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					deployer.ReferenceNameLabel:      randomString(),
					deployer.ReferenceNamespaceLabel: randomString(),
					deployer.ReasonLabel:             string(libsveltosv1beta1.FeatureKustomize),
				},
			},
		}

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}

		clusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					deployer.ReferenceKindLabel:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					deployer.ReferenceNameLabel:      randomString(),
					deployer.ReferenceNamespaceLabel: randomString(),
					deployer.ReasonLabel:             string(libsveltosv1beta1.FeatureKustomize),
				},
			},
		}

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), serviceAccount)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterRole)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterRole)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, serviceAccount, clusterProfile)
		addOwnerReference(ctx, testEnv.Client, clusterRole, clusterProfile)

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureKustomize,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}
		currentClusterSummary.Status.DeployedGVKs = []libsveltosv1beta1.FeatureDeploymentInfo{
			{
				FeatureID: libsveltosv1beta1.FeatureKustomize,
				DeployedGroupVersionKind: []string{
					"ServiceAccount.v1.",
					"ConfigMaps.v1.",
					"ClusterRole.v1.rbac.authorization.k8s.io",
				},
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Wait for cache to be updated
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			return err == nil &&
				currentClusterSummary.Status.FeatureSummaries != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.GenericUndeploy(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(libsveltosv1beta1.FeatureKustomize), libsveltosv1beta1.ClusterTypeCapi, deployer.Options{},
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// undeployKustomizeRefs finds all policies deployed because of a clusterSummary and deletes those.
		// Expect ServiceAccount and ClusterRole to be deleted. ConfigMap should remain as Labels are not set on it

		currentServiceAccount := &corev1.ServiceAccount{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: serviceAccount.Namespace, Name: serviceAccount.Name}, currentServiceAccount)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		currentClusterRole := &rbacv1.ClusterRole{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRole.Name}, currentClusterRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		currentConfigMap := &corev1.ConfigMap{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("extractTarGz extracts tar.gz", func() {
		defer os.RemoveAll("testdata")
		createTarGz("testdata/test.tar.gz")

		// Create a temporary directory to use as the destination for the extracted files
		dest, err := os.MkdirTemp("", "test")
		Expect(err).To(BeNil())
		defer os.RemoveAll(dest)

		// Extract the test tarball to the destination
		err = controllers.ExtractTarGz("testdata/test.tar.gz", dest)
		Expect(err).To(BeNil())

		// Check that the extracted files match the expected contents
		expectedContents := map[string]string{
			"test.txt":         "This is a test file.",
			"testdir/test.txt": "This is another test file.",
		}
		for path, expectedContents := range expectedContents {
			filePath := filepath.Join(dest, path)
			var contents []byte
			contents, err = os.ReadFile(filePath)
			Expect(err).To(BeNil())
			Expect(string(contents)).To(Equal(expectedContents))
		}

		// Check that no additional files were extracted
		extraFilePath := filepath.Join(dest, "testdir", "extra.txt")
		_, err = os.Stat(extraFilePath)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})

var _ = Describe("Hash methods", func() {
	It("kustomizationHash returns hash considering all referenced resources", func() {
		gitRepositories := make([]sourcev1.GitRepository, 0)

		repoNum := 3
		for i := 0; i < repoNum; i++ {
			gitRepository := sourcev1.GitRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      randomString(),
					Namespace: randomString(),
				},
				Status: sourcev1.GitRepositoryStatus{
					Artifact: &meta.Artifact{
						Revision: randomString(),
					},
				},
			}
			Expect(addTypeInformationToObject(scheme, &gitRepository)).To(Succeed())
			gitRepositories = append(gitRepositories, gitRepository)
		}

		namespace := randomString()
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					KustomizationRefs: make([]configv1beta1.KustomizationRef, repoNum),
					Patches: []libsveltosv1beta1.Patch{
						{
							Patch: `- op: add
  path: /metadata/labels/environment
  value: production`,
						},
					},

					Tier: 100,
				},
			},
		}

		for i := 0; i < repoNum; i++ {
			clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i] =
				configv1beta1.KustomizationRef{
					Namespace: gitRepositories[i].Namespace, Name: gitRepositories[i].Name,
					Kind: sourcev1.GitRepositoryKind,
				}
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			cluster,
		}
		for i := 0; i < repoNum; i++ {
			initObjects = append(initObjects, &gitRepositories[i])
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		config := fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode)
		config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Reloader)
		config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Tier)
		config += fmt.Sprintf("%t", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.ContinueOnConflict)
		config += render.AsCode(clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Patches)
		h := sha256.New()
		h.Write([]byte(config))
		tmpHash := h.Sum(nil)

		config = string(tmpHash)

		sort.Sort(controllers.SortedKustomizationRefs(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs))
		config += render.AsCode(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs)
		for i := 0; i < repoNum; i++ {
			config += getRevision(&clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i], gitRepositories)
		}

		h = sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.KustomizationHash(context.TODO(), c, clusterSummaryScope.ClusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})

	It(`getKustomizeReferenceResourceHash returns the hash considering all referenced
	ConfigMap/Secret in the ValueFrom section`, func() {
		namespace := randomString()
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string]string{
				"cluster-name": "{{ .Cluster.metadata.namespace }}-{{ .Cluster.metadata.name }}",
				randomString(): randomString(),
			},
		}

		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
		},
			Data: map[string][]byte{
				randomString(): []byte(randomString()),
				randomString(): []byte(randomString()),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		var expectedHash string
		expectedHash += controllers.GetStringDataSectionHash(configMap.Data)
		expectedHash += controllers.GetByteDataSectionHash(secret.Data)

		kustomizationRef := configv1beta1.KustomizationRef{
			ValuesFrom: []configv1beta1.ValueFrom{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: namespace,
					Name:      configMap.Name,
				},
				{
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
					Namespace: namespace,
					Name:      secret.Name,
				},
			},
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					KustomizationRefs: []configv1beta1.KustomizationRef{
						kustomizationRef,
					},
				},
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		initObjects := []client.Object{
			configMap,
			secret,
			clusterSummary,
			cluster,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		hash, err := controllers.GetKustomizeReferenceResourceHash(context.TODO(), c, clusterSummary,
			&kustomizationRef, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(expectedHash).To(Equal(hash))
	})

	It("instantiateKustomizeSubstituteValues instantiates substitute values", func() {
		namespace := randomString()
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		region := "us-west1"
		k8sVersion := "v1.29.0"
		cidrBlock := "192.168.10.0/24"
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Name:      clusterSummary.Spec.ClusterName,
				Labels: map[string]string{
					"region": region,
				},
			},
			Spec: clusterv1.ClusterSpec{
				Topology: &clusterv1.Topology{
					Version: k8sVersion,
				},
				ClusterNetwork: &clusterv1.ClusterNetwork{
					Pods: &clusterv1.NetworkRanges{
						CIDRBlocks: []string{cidrBlock},
					},
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cluster)
		Expect(err).To(BeNil())

		var uCluster unstructured.Unstructured
		uCluster.SetUnstructuredContent(content)

		mgmtResources := map[string]*unstructured.Unstructured{
			"Cluster": &uCluster,
		}

		values := map[string]string{
			`region`:  `{{ index .Cluster.metadata.labels "region" }}`,
			`version`: `{{ .Cluster.spec.topology.version }}`,
			`cidrs`: `{{ range $cidr := .Cluster.spec.clusterNetwork.pods.cidrBlocks }}
            - cidr: {{ $cidr }}
              encapsulation: VXLAN
          {{ end }}`,
		}

		result, err := controllers.InstantiateKustomizeSubstituteValues(context.TODO(), clusterSummary,
			mgmtResources, values, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		v, ok := result["region"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(region))

		v, ok = result["version"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(k8sVersion))

		v, ok = result["cidrs"]
		Expect(ok).To(BeTrue())
		Expect(v).To(ContainSubstring(cidrBlock))
	})

	It("getKustomizeSubstituteValuesFrom collects substitute values from referenced ConfigMap/Secrets", func() {
		namespace := randomString()
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string]string{
				randomString(): randomString(),
				randomString(): randomString(),
			},
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string][]byte{
				randomString(): []byte(randomString()),
				randomString(): []byte(randomString()),
			},
			Type: libsveltosv1beta1.ClusterProfileSecretType,
		}

		kustomizationRef := &configv1beta1.KustomizationRef{
			ValuesFrom: []configv1beta1.ValueFrom{
				{
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				},
				{
					Namespace: secret.Namespace,
					Name:      secret.Name,
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
				},
			},
			Values: map[string]string{
				randomString(): randomString(),
			},
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					KustomizationRefs: []configv1beta1.KustomizationRef{
						*kustomizationRef,
					},
				},
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		initObjects := []client.Object{
			configMap,
			secret,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		// This will get alll substitute values considering only ValuesFrom
		templatedValues, nonTemplatedValues, err := controllers.GetKustomizeSubstituteValuesFrom(context.TODO(), c, clusterSummary, kustomizationRef,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		for k := range configMap.Data {
			v, ok := templatedValues[k]
			if !ok {
				v, ok = nonTemplatedValues[k]
			}
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal(configMap.Data[k]))
		}

		for k := range secret.Data {
			v, ok := templatedValues[k]
			if !ok {
				v, ok = nonTemplatedValues[k]
			}
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal(string(secret.Data[k])))
		}

		// This will get alll substitute values considering both Values and ValuesFrom
		templatedValues, nonTemplatedValues, err = controllers.GetKustomizeSubstituteValues(context.TODO(), c, clusterSummary, kustomizationRef,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		for k := range configMap.Data {
			v, ok := templatedValues[k]
			if !ok {
				v, ok = nonTemplatedValues[k]
			}
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal(configMap.Data[k]))
		}

		for k := range secret.Data {
			v, ok := templatedValues[k]
			if !ok {
				v, ok = nonTemplatedValues[k]
			}
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal(string(secret.Data[k])))
		}

		for k := range kustomizationRef.Values {
			v, ok := templatedValues[k]
			if !ok {
				v, ok = nonTemplatedValues[k]
			}
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal(kustomizationRef.Values[k]))
		}
	})

	It("instantiateResourceWithSubstituteValues instantiates a Kubernetes resorces with substitte values", func() {
		deployment := `apiVersion: apps/v1
		kind: Deployment
		metadata:
		  name: nginx-deployment
		  namespace: {{ .ClusterNamespace }}
		  labels:
		    region: {{ default "west" .Region }}
		spec:
		  replicas: 2
		  selector:
			matchLabels:
			  app: nginx-{{ .Version }}
		  template:
			metadata:
			  labels:
			  app: nginx-{{ .Version }}
			spec:
			  containers:
			  - name: nginx
				image: nginx:{{ .Version }}
				ports:
				- containerPort: 80`

		clusterNamespace := randomString()
		version := "v1.2.0"
		substituteValues := map[string]string{
			"ClusterNamespace": clusterNamespace,
			"Version":          version,
			"Region":           "",
		}
		data, err := controllers.InstantiateResourceWithSubstituteValues(randomString(), []byte(deployment), substituteValues,
			false, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		Expect(string(data)).To(ContainSubstring(fmt.Sprintf("namespace: %s", clusterNamespace)))
		Expect(string(data)).To(ContainSubstring(fmt.Sprintf("app: nginx-%s", version)))
		Expect(string(data)).To(ContainSubstring("region: west"))
	})
})

func createTarGz(dest string) {
	// Create the test directory and some test files.
	err := os.MkdirAll("testdata/testdir", 0755)
	Expect(err).To(BeNil())
	err = os.WriteFile("testdata/test.txt", []byte("This is a test file."), 0600)
	Expect(err).To(BeNil())
	err = os.WriteFile("testdata/testdir/test.txt", []byte("This is another test file."), 0600)
	Expect(err).To(BeNil())

	// Create the testdata/test.tar.gz file.
	file, err := os.Create(dest)
	Expect(err).To(BeNil())
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	err = filepath.Walk("testdata/", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = path[len("testdata")+1:]
		err = tarWriter.WriteHeader(header)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tarWriter, file)
		return err
	})
	Expect(err).To(BeNil())
}

func getRevision(kustomizationRefs *configv1beta1.KustomizationRef, gitRepositories []sourcev1.GitRepository) string {
	for i := range gitRepositories {
		if kustomizationRefs.Namespace == gitRepositories[i].Namespace &&
			kustomizationRefs.Name == gitRepositories[i].Name {

			return gitRepositories[i].Status.Artifact.Revision
		}
	}
	return ""
}
