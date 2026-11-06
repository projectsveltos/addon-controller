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

package fv_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	crdPreflightChartName      = "crdtest"
	crdPreflightRepoName       = "crd-preflight"
	crdPreflightRepoResource   = "crd-preflight-repo"
	crdPreflightRepoNamespace  = "default"
	crdPreflightRepoURL        = "http://" + crdPreflightRepoResource + "." + crdPreflightRepoNamespace + ".svc.cluster.local:80"
	crdPreflightReleaseName    = "crdtest"
	crdPreflightReleaseNS      = "crdtest"
	crdPreflightGroup          = "crdpreflight.fv.projectsveltos.io"
	crdPreflightFirstCRDName   = "aaas." + crdPreflightGroup
	crdPreflightSecondCRDName  = "zzzs." + crdPreflightGroup
	crdPreflightObjectName     = "important"
	crdPreflightObjectNS       = "default"
	crdPreflightOlderVersion   = "0.1.0"
	crdPreflightNewerVersion   = "0.2.0"
	crdPreflightRefusedMessage = "No CRD has been modified"
	crdPreflightV1             = "v1"
	crdPreflightV2             = "v2"
	crdPreflightVolumeName     = "charts"
)

var _ = Describe("Helm CRDs preflight", func() {
	const (
		namePrefix = "helm-crds-preflight-"
	)

	// The chart has two CRDs. In 0.2.0:
	// - the first one gets a new schema field (spec.b)
	// - the second one gets a new storage version (v2), v1 is still served.
	// Going back to 0.1.0 must not downgrade any of them: the API server refuses the second one
	// (v2 is still a stored version), and no CRD must be touched when that happens.
	It("Does not downgrade any CRD when one of them can not be applied", Label("FV", "EXTENDED"), func() {
		Byf("Getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		// Helm never removes CRDs. Start from, and leave behind, a cluster without them.
		deleteCRDPreflightCRDs(workloadClient)
		DeferCleanup(func() { deleteCRDPreflightCRDs(workloadClient) })

		Byf("Serving the chart versions %s and %s from the management cluster",
			crdPreflightOlderVersion, crdPreflightNewerVersion)
		deployCRDPreflightChartRepo()
		DeferCleanup(deleteCRDPreflightChartRepo)

		Byf("Create a ClusterProfile matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
		clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{newCRDPreflightChart(crdPreflightOlderVersion)}
		Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

		verifyClusterProfileMatches(clusterProfile)

		clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName, clusterProfile.Name,
			&clusterProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Verifying chart %s CRDs have been installed", crdPreflightOlderVersion)
		Eventually(func() []string {
			return getCRDPreflightFirstCRDProperties(workloadClient)
		}, timeout, pollingInterval).Should(ConsistOf("a"))
		Expect(getCRDPreflightSecondCRDVersions(workloadClient)).To(ConsistOf(crdPreflightV1))

		Byf("Changing ClusterProfile %s to require chart %s", clusterProfile.Name, crdPreflightNewerVersion)
		updateCRDPreflightChartVersion(clusterProfile.Name, crdPreflightNewerVersion)

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
		clusterSummary = verifyClusterSummary(clusterops.ClusterProfileLabelName, currentClusterProfile.Name,
			&currentClusterProfile.Spec, kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

		Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
		verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

		Byf("Verifying chart %s CRDs have been upgraded", crdPreflightNewerVersion)
		Eventually(func() []string {
			return getCRDPreflightFirstCRDProperties(workloadClient)
		}, timeout, pollingInterval).Should(ConsistOf("a", "b"))
		Eventually(func() []string {
			return getCRDPreflightSecondCRDVersions(workloadClient)
		}, timeout, pollingInterval).Should(ConsistOf(crdPreflightV1, crdPreflightV2))

		Byf("Creating an object that uses the field added in chart %s", crdPreflightNewerVersion)
		createCRDPreflightObject(workloadClient)

		Byf("Changing ClusterProfile %s back to chart %s", clusterProfile.Name, crdPreflightOlderVersion)
		updateCRDPreflightChartVersion(clusterProfile.Name, crdPreflightOlderVersion)

		Byf("Verifying the upgrade is refused before any CRD is modified")
		Eventually(func() bool {
			return isCRDPreflightRefused(clusterSummary.Namespace, clusterSummary.Name)
		}, timeout, pollingInterval).Should(BeTrue())

		Byf("Verifying the CRDs of chart %s have not been applied", crdPreflightNewerVersion)
		Consistently(func() []string {
			return getCRDPreflightFirstCRDProperties(workloadClient)
		}, 30*time.Second, pollingInterval).Should(ConsistOf("a", "b"))
		Expect(getCRDPreflightSecondCRDVersions(workloadClient)).To(ConsistOf(crdPreflightV1, crdPreflightV2))

		Byf("Verifying the field added in chart %s can still be read", crdPreflightNewerVersion)
		Expect(getCRDPreflightObjectField(workloadClient, "b")).To(Equal("important data"))

		deleteClusterProfile(clusterProfile)
	})
})

func newCRDPreflightChart(version string) configv1beta1.HelmChart {
	return configv1beta1.HelmChart{
		RepositoryURL:    crdPreflightRepoURL,
		RepositoryName:   crdPreflightRepoName,
		ChartName:        crdPreflightRepoName + "/" + crdPreflightChartName,
		ChartVersion:     version,
		ReleaseName:      crdPreflightReleaseName,
		ReleaseNamespace: crdPreflightReleaseNS,
		HelmChartAction:  configv1beta1.HelmChartActionInstall,
		Options: &configv1beta1.HelmOptions{
			InstallOptions: configv1beta1.HelmInstallOptions{
				CreateNamespace: ptr.To(true),
			},
			UpgradeOptions: configv1beta1.HelmUpgradeOptions{
				UpgradeCRDs: true,
			},
		},
	}
}

func updateCRDPreflightChartVersion(clusterProfileName, version string) {
	currentClusterProfile := &configv1beta1.ClusterProfile{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfileName}, currentClusterProfile)).To(Succeed())
		currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{newCRDPreflightChart(version)}
		return k8sClient.Update(context.TODO(), currentClusterProfile)
	})
	Expect(err).To(BeNil())
}

// isCRDPreflightRefused returns true when the Helm feature of the ClusterSummary failed because
// the CRDs could not be applied and none was modified.
func isCRDPreflightRefused(clusterSummaryNamespace, clusterSummaryName string) bool {
	currentClusterSummary := &configv1beta1.ClusterSummary{}
	err := k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName},
		currentClusterSummary)
	if err != nil {
		return false
	}

	for i := range currentClusterSummary.Status.FeatureSummaries {
		fs := currentClusterSummary.Status.FeatureSummaries[i]
		if fs.FeatureID == libsveltosv1beta1.FeatureHelm && fs.FailureMessage != nil &&
			strings.Contains(*fs.FailureMessage, crdPreflightRefusedMessage) {

			return true
		}
	}

	return false
}

// crdPreflightManifest returns a CRD of the test chart with the given versions (the last one is the
// storage version) and the given string properties under spec.
func crdPreflightManifest(plural, kind string, versions, properties []string) string {
	manifest := fmt.Sprintf(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: %s.%s
spec:
  group: %s
  scope: Namespaced
  names:
    kind: %s
    plural: %s
  versions:
`, plural, crdPreflightGroup, crdPreflightGroup, kind, plural)

	for i, version := range versions {
		manifest += fmt.Sprintf(`  - name: %s
    served: true
    storage: %t
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
`, version, i == len(versions)-1)
		for _, property := range properties {
			manifest += fmt.Sprintf("              %s: {type: string}\n", property)
		}
	}

	return manifest
}

// buildCRDPreflightChart returns the chart archive.
func buildCRDPreflightChart(version string, newer bool) []byte {
	firstProperties := []string{"a"}
	secondVersions := []string{crdPreflightV1}
	if newer {
		firstProperties = []string{"a", "b"}
		secondVersions = []string{crdPreflightV1, crdPreflightV2}
	}

	files := map[string]string{
		crdPreflightChartName + "/Chart.yaml": fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n",
			crdPreflightChartName, version),
		crdPreflightChartName + "/crds/a-aaas.yaml": crdPreflightManifest("aaas", "Aaa", []string{crdPreflightV1}, firstProperties),
		crdPreflightChartName + "/crds/z-zzzs.yaml": crdPreflightManifest("zzzs", "Zzz", secondVersions, []string{"a"}),
		crdPreflightChartName + "/templates/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n" +
			"  name: crdtest\ndata:\n  version: \"" + version + "\"\n",
	}

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		Expect(tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))})).To(Succeed())
		_, err := tarWriter.Write([]byte(content))
		Expect(err).To(BeNil())
	}
	Expect(tarWriter.Close()).To(Succeed())
	Expect(gzipWriter.Close()).To(Succeed())

	return buffer.Bytes()
}

// deployCRDPreflightChartRepo serves the two versions of the test chart from an nginx pod
// in the management cluster.
func deployCRDPreflightChartRepo() {
	olderChart := buildCRDPreflightChart(crdPreflightOlderVersion, false)
	newerChart := buildCRDPreflightChart(crdPreflightNewerVersion, true)

	entry := func(version string, chartArchive []byte) string {
		digest := sha256.Sum256(chartArchive)
		return fmt.Sprintf(`  - apiVersion: v2
    name: %s
    version: %s
    urls:
    - %s-%s.tgz
    digest: %s
    created: "2026-01-01T00:00:00Z"
`, crdPreflightChartName, version, crdPreflightChartName, version, hex.EncodeToString(digest[:]))
	}
	index := "apiVersion: v1\nentries:\n  " + crdPreflightChartName + ":\n" +
		entry(crdPreflightOlderVersion, olderChart) + entry(crdPreflightNewerVersion, newerChart) +
		"generated: \"2026-01-01T00:00:00Z\"\n"

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: crdPreflightRepoResource, Namespace: crdPreflightRepoNamespace},
		Data:       map[string]string{"index.yaml": index},
		BinaryData: map[string][]byte{
			crdPreflightChartName + "-" + crdPreflightOlderVersion + ".tgz": olderChart,
			crdPreflightChartName + "-" + crdPreflightNewerVersion + ".tgz": newerChart,
		},
	}
	Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

	labels := map[string]string{"app": crdPreflightRepoResource}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: crdPreflightRepoResource, Namespace: crdPreflightRepoNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         "nginx",
						Image:        "nginx:alpine",
						Ports:        []corev1.ContainerPort{{ContainerPort: 80}},
						VolumeMounts: []corev1.VolumeMount{{Name: crdPreflightVolumeName, MountPath: "/usr/share/nginx/html"}},
					}},
					Volumes: []corev1.Volume{{
						Name: crdPreflightVolumeName,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: crdPreflightRepoResource},
							},
						},
					}},
				},
			},
		},
	}
	Expect(k8sClient.Create(context.TODO(), deployment)).To(Succeed())

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: crdPreflightRepoResource, Namespace: crdPreflightRepoNamespace},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(80)}},
		},
	}
	Expect(k8sClient.Create(context.TODO(), service)).To(Succeed())

	Eventually(func() int32 {
		currentDeployment := &appsv1.Deployment{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: crdPreflightRepoNamespace, Name: crdPreflightRepoResource},
			currentDeployment)
		if err != nil {
			return 0
		}
		return currentDeployment.Status.AvailableReplicas
	}, timeout, pollingInterval).Should(Equal(int32(1)))
}

func deleteCRDPreflightChartRepo() {
	objects := []client.Object{
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: crdPreflightRepoResource, Namespace: crdPreflightRepoNamespace}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: crdPreflightRepoResource, Namespace: crdPreflightRepoNamespace}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: crdPreflightRepoResource, Namespace: crdPreflightRepoNamespace}},
	}
	for _, object := range objects {
		Expect(client.IgnoreNotFound(k8sClient.Delete(context.TODO(), object))).To(Succeed())
	}
}

// getCRDPreflightFirstCRDProperties returns the names of the spec properties of the first CRD,
// nil when the CRD cannot be read.
func getCRDPreflightFirstCRDProperties(workloadClient client.Client) []string {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: crdPreflightFirstCRDName}, crd); err != nil {
		return nil
	}
	if len(crd.Spec.Versions) == 0 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		return nil
	}

	specSchema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	properties := make([]string, 0, len(specSchema.Properties))
	for name := range specSchema.Properties {
		properties = append(properties, name)
	}

	return properties
}

// getCRDPreflightSecondCRDVersions returns the names of the versions of the second CRD,
// nil when the CRD cannot be read.
func getCRDPreflightSecondCRDVersions(workloadClient client.Client) []string {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: crdPreflightSecondCRDName}, crd); err != nil {
		return nil
	}

	versions := make([]string, 0, len(crd.Spec.Versions))
	for i := range crd.Spec.Versions {
		versions = append(versions, crd.Spec.Versions[i].Name)
	}

	return versions
}

func crdPreflightObjectGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: crdPreflightGroup, Version: crdPreflightV1, Kind: "Aaa"}
}

func createCRDPreflightObject(workloadClient client.Client) {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(crdPreflightObjectGVK())
	object.SetName(crdPreflightObjectName)
	object.SetNamespace(crdPreflightObjectNS)
	Expect(unstructured.SetNestedField(object.Object, "kept", "spec", "a")).To(Succeed())
	Expect(unstructured.SetNestedField(object.Object, "important data", "spec", "b")).To(Succeed())
	Expect(workloadClient.Create(context.TODO(), object)).To(Succeed())
}

// getCRDPreflightObjectField returns the field of the object spec as read from the API server,
// an empty string when the field is not there.
func getCRDPreflightObjectField(workloadClient client.Client, field string) string {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(crdPreflightObjectGVK())
	Expect(workloadClient.Get(context.TODO(),
		types.NamespacedName{Namespace: crdPreflightObjectNS, Name: crdPreflightObjectName}, object)).To(Succeed())

	value, _, err := unstructured.NestedString(object.Object, "spec", field)
	Expect(err).To(BeNil())

	return value
}

// deleteCRDPreflightCRDs removes the CRDs of the test chart, and the objects using them, and waits
// for the CRDs to be gone. Helm does not remove CRDs when a release is uninstalled.
func deleteCRDPreflightCRDs(workloadClient client.Client) {
	for _, name := range []string{crdPreflightFirstCRDName, crdPreflightSecondCRDName} {
		crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(client.IgnoreNotFound(workloadClient.Delete(context.TODO(), crd))).To(Succeed())
	}

	Eventually(func() bool {
		for _, name := range []string{crdPreflightFirstCRDName, crdPreflightSecondCRDName} {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			err := workloadClient.Get(context.TODO(), types.NamespacedName{Name: name}, crd)
			if err == nil || !apierrors.IsNotFound(err) {
				return false
			}
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())
}
