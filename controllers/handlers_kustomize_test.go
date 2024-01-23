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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/gdexlab/go-render/render"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
)

var _ = Describe("KustomizeRefs", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
	var clusterSummary *configv1alpha1.ClusterSummary
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

		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.Spec{
				ClusterSelector: libsveltosv1alpha1.Selector(fmt.Sprintf("%s=%s", randomString(), randomString())),
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(configv1alpha1.ClusterProfileKind,
			clusterProfile.Name, cluster.Name, false)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: cluster.Namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
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
					deployer.ReferenceKindLabel:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
					deployer.ReferenceNameLabel:      randomString(),
					deployer.ReferenceNamespaceLabel: randomString(),
					controllers.ReasonLabel:          string(configv1alpha1.FeatureKustomize),
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
					deployer.ReferenceKindLabel:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
					deployer.ReferenceNameLabel:      randomString(),
					deployer.ReferenceNamespaceLabel: randomString(),
					controllers.ReasonLabel:          string(configv1alpha1.FeatureKustomize),
				},
			},
		}

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), serviceAccount)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterRole)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, serviceAccount, clusterProfile)
		addOwnerReference(ctx, testEnv.Client, clusterRole, clusterProfile)

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureKustomize,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"ServiceAccount.v1.",
					"ConfigMaps.v1.",
					"ClusterRole.v1.rbac.authorization.k8s.io",
				},
			},
		}
		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Wait for cache to be updated
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			return err == nil &&
				currentClusterSummary.Status.FeatureSummaries != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.GenericUndeploy(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(configv1alpha1.FeatureKustomize), libsveltosv1alpha1.ClusterTypeCapi, deployer.Options{},
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// undeployKustomizeRefs finds all policies deployed because of a clusterSummary and deletes those.
		// Expect ServiceAccount and ClusterRole to be deleted. ConfigMap should remain as Labels are not set on it

		currentServiceAccount := &corev1.ServiceAccount{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(),
				types.NamespacedName{Namespace: serviceAccount.Namespace, Name: serviceAccount.Name}, currentServiceAccount)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		currentClusterRole := &rbacv1.ClusterRole{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(),
				types.NamespacedName{Name: clusterRole.Name}, currentClusterRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		currentConfigMap := &corev1.ConfigMap{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(),
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
					Artifact: &sourcev1.Artifact{
						Revision: randomString(),
					},
				},
			}
			Expect(addTypeInformationToObject(scheme, &gitRepository)).To(Succeed())
			gitRepositories = append(gitRepositories, gitRepository)
		}

		namespace := randomString()
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
				ClusterProfileSpec: configv1alpha1.Spec{
					KustomizationRefs: make([]configv1alpha1.KustomizationRef, repoNum),
				},
			},
		}

		for i := 0; i < repoNum; i++ {
			clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs[i] =
				configv1alpha1.KustomizationRef{
					Namespace: gitRepositories[i].Namespace, Name: gitRepositories[i].Name,
					Kind: sourcev1.GitRepositoryKind,
				}
		}

		initObjects := []client.Object{
			clusterSummary,
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
		config += render.AsCode(clusterSummary.Spec.ClusterProfileSpec.KustomizationRefs)
		for i := 0; i < repoNum; i++ {
			config += gitRepositories[i].Status.Artifact.Revision
		}
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.KustomizationHash(context.TODO(), c, clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
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
