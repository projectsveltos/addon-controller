/*
Copyright 2022. projectsveltos.io. All rights reserved.

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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gdexlab/go-render/render"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
)

var _ = Describe("HandlersHelm", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary

	const defaulNamespace = "default"

	BeforeEach(func() {
		clusterNamespace := randomString()

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

		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       configv1beta1.ClusterProfileKind,
						Name:       clusterProfile.Name,
						APIVersion: "config.projectsveltos.io/v1beta1",
						UID:        types.UID(randomString()),
					},
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
	})

	It("shouldInstall returns false when requested version does not match installed version", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       release.StatusDeployed.String(),
			ChartVersion: "v2.5.0",
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeFalse())
	})

	It("shouldInstall returns false when requested version matches installed version",
		func() {
			currentRelease := &controllers.ReleaseInfo{
				Status:       release.StatusDeployed.String(),
				ChartVersion: "v2.5.3",
			}
			requestChart := &configv1beta1.HelmChart{
				ChartVersion:    "v2.5.3",
				HelmChartAction: configv1beta1.HelmChartActionInstall,
			}
			Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeFalse())
		})

	It("shouldInstall returns true when there is no current installed version", func() {
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(nil, requestChart)).To(BeTrue())
	})

	It("shouldInstall returns false action is uninstall", func() {
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1beta1.HelmChartActionUninstall,
		}
		Expect(controllers.ShouldInstall(nil, requestChart)).To(BeFalse())
	})

	It("shouldUninstall returns false when there is no current release installed", func() {
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1beta1.HelmChartActionUninstall,
		}
		Expect(controllers.ShouldUninstall(nil, requestChart)).To(BeFalse())
	})

	It("shouldUninstall returns false when action is not Uninstall", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       release.StatusDeployed.String(),
			ChartVersion: "v2.5.3",
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldUninstall(currentRelease, requestChart)).To(BeFalse())
	})

	It("shouldUpgrade returns true when installed release is different than requested release", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       release.StatusDeployed.String(),
			ChartVersion: "v2.5.0",
		}
		requestChart := &configv1beta1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldUpgrade(context.TODO(), currentRelease, requestChart,
			clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))).To(BeTrue())
	})

	It("UpdateStatusForeferencedHelmReleases updates ClusterSummary.Status.HelmReleaseSummaries", func() {
		calicoChart := &configv1beta1.HelmChart{
			RepositoryURL:    "https://projectcalico.docs.tigera.io/charts",
			RepositoryName:   "projectcalico",
			ChartName:        "projectcalico/tigera-operator",
			ChartVersion:     "v3.24.1",
			ReleaseName:      "calico",
			ReleaseNamespace: "calico",
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		kyvernoSummary := configv1beta1.HelmChartSummary{
			ReleaseName:      "kyverno",
			ReleaseNamespace: "kyverno",
			Status:           configv1beta1.HelmChartStatusManaging,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{*calicoChart},
		}

		clusterSummary.Namespace = defaulNamespace
		clusterSummary.Spec.ClusterNamespace = defaulNamespace

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		// List a helm chart non referenced anymore as managed
		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				kyvernoSummary,
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		clusterSummary, conflict, err := controllers.UpdateStatusForReferencedHelmReleases(context.TODO(),
			testEnv.Client, clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())

		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			if currentClusterSummary.Status.HelmReleaseSummaries == nil {
				return false
			}
			if len(currentClusterSummary.Status.HelmReleaseSummaries) != 2 {
				return false
			}
			if currentClusterSummary.Status.HelmReleaseSummaries[0].Status != configv1beta1.HelmChartStatusManaging ||
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName != calicoChart.ReleaseName ||
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace != calicoChart.ReleaseNamespace {
				return false
			}

			// UpdateStatusForeferencedHelmReleases adds status for referenced releases and does not remove any
			// existing entry for non existing releases.
			return currentClusterSummary.Status.HelmReleaseSummaries[1].Status == kyvernoSummary.Status &&
				currentClusterSummary.Status.HelmReleaseSummaries[1].ReleaseName == kyvernoSummary.ReleaseName &&
				currentClusterSummary.Status.HelmReleaseSummaries[1].ReleaseNamespace == kyvernoSummary.ReleaseNamespace
		}, timeout, pollingInterval).Should(BeTrue())

	})

	It("updateStatusForeferencedHelmReleases is no-op in DryRun mode", func() {
		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{
				{RepositoryURL: randomString(), RepositoryName: randomString(), ChartName: randomString(), ChartVersion: randomString(),
					ReleaseName: randomString(), ReleaseNamespace: randomString()},
			},
			SyncMode: configv1beta1.SyncModeDryRun,
		}

		// List an helm chart non referenced anymore as managed
		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				{ReleaseName: randomString(), ReleaseNamespace: randomString(), Status: configv1beta1.HelmChartStatusManaging},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummary, conflict, err := controllers.UpdateStatusForReferencedHelmReleases(context.TODO(), c, clusterSummary, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())

		// Cause we are in DryRun mode, clusterSummary Status has not changed
		Expect(len(currentClusterSummary.Status.HelmReleaseSummaries)).To(Equal(1))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName).To(
			Equal(clusterSummary.Status.HelmReleaseSummaries[0].ReleaseName))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace).To(
			Equal(clusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].Status).To(
			Equal(clusterSummary.Status.HelmReleaseSummaries[0].Status))
	})

	It("UpdateStatusForNonReferencedHelmReleases updates ClusterSummary.Status.HelmReleaseSummaries", func() {
		contourChart := &configv1beta1.HelmChart{
			RepositoryURL:    "https://charts.bitnami.com/bitnami",
			RepositoryName:   "bitnami/contour",
			ChartName:        "bitnami/contour",
			ChartVersion:     "12.1.0",
			ReleaseName:      "contour-latest",
			ReleaseNamespace: "contour",
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		kyvernoSummary := configv1beta1.HelmChartSummary{
			ReleaseName:      "kyverno",
			ReleaseNamespace: "kyverno",
			Status:           configv1beta1.HelmChartStatusManaging,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			HelmCharts: []configv1beta1.HelmChart{*contourChart},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		// List a helm chart non referenced anymore as managed
		clusterSummary.Status = configv1beta1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1beta1.HelmChartSummary{
				kyvernoSummary,
				{
					ReleaseName:      contourChart.ReleaseName,
					ReleaseNamespace: contourChart.ReleaseNamespace,
					Status:           configv1beta1.HelmChartStatusManaging,
				},
			},
		}

		Expect(testEnv.Status().Update(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		Eventually(func() bool {
			currentlClusterSummary := &configv1beta1.ClusterSummary{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterSummary.Name, Namespace: clusterSummary.Namespace},
				currentlClusterSummary)
			if err != nil {
				return false
			}
			return len(currentlClusterSummary.Spec.ClusterProfileSpec.HelmCharts) == 1 &&
				len(currentlClusterSummary.Status.HelmReleaseSummaries) == 2
		}, timeout, pollingInterval).Should(BeTrue())

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), testEnv.Client)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		clusterSummary, err = controllers.UpdateStatusForNonReferencedHelmReleases(context.TODO(), testEnv.Client,
			clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			if len(currentClusterSummary.Status.HelmReleaseSummaries) == 1 &&
				currentClusterSummary.Status.HelmReleaseSummaries[0].Status == configv1beta1.HelmChartStatusManaging &&
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName == contourChart.ReleaseName &&
				currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace == contourChart.ReleaseNamespace {
				return true
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("updateChartsInClusterConfiguration updates ClusterConfiguration with deployed helm releases", func() {
		chartDeployed := []configv1beta1.Chart{
			{
				RepoURL:      "https://charts.bitnami.com/bitnami",
				ReleaseName:  "contour-latest",
				ChartVersion: "12.1.0",
				Namespace:    "projectcontour",
			},
		}

		clusterConfiguration := &configv1beta1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      controllers.GetClusterConfigurationName(clusterSummary.Spec.ClusterName, libsveltosv1beta1.ClusterTypeCapi),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Status: configv1beta1.ClusterConfigurationStatus{
				ClusterProfileResources: []configv1beta1.ClusterProfileResource{
					{
						ClusterProfileName: clusterProfile.Name},
				},
			},
		}

		initObjects := []client.Object{
			clusterConfiguration,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		Expect(controllers.UpdateChartsInClusterConfiguration(context.TODO(), c, clusterSummary,
			chartDeployed, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		currentClusterConfiguration := &configv1beta1.ClusterConfiguration{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterConfiguration.Namespace,
				Name:      clusterConfiguration.Name,
			},
			currentClusterConfiguration)).To(Succeed())

		Expect(currentClusterConfiguration.Status.ClusterProfileResources).ToNot(BeNil())
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(1))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].ClusterProfileName).To(
			Equal(clusterProfile.Name))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features).ToNot(BeNil())
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources[0].Features)).To(Equal(1))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].FeatureID).To(
			Equal(libsveltosv1beta1.FeatureHelm))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts).ToNot(BeNil())
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts)).To(Equal(1))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts[0].RepoURL).To(
			Equal(chartDeployed[0].RepoURL))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts[0].ReleaseName).To(
			Equal(chartDeployed[0].ReleaseName))
		Expect(currentClusterConfiguration.Status.ClusterProfileResources[0].Features[0].Charts[0].ChartVersion).To(
			Equal(chartDeployed[0].ChartVersion))
	})

	It("createReportForUnmanagedHelmRelease ", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			SyncMode:   configv1beta1.SyncModeDryRun,
			HelmCharts: []configv1beta1.HelmChart{*helmChart},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		report, err := controllers.CreateReportForUnmanagedHelmRelease(context.TODO(), c, clusterSummary,
			helmChart, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(report).ToNot(BeNil())
		Expect(report.Action).To(Equal(string(configv1beta1.InstallHelmAction)))
		Expect(report.ReleaseName).To(Equal(helmChart.ReleaseName))
		Expect(report.ReleaseNamespace).To(Equal(helmChart.ReleaseNamespace))
	})

	It("getInstantiatedChart returns instantiated HelmChart matching passed in chart", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Namespace = defaulNamespace
		clusterSummary.Spec.ClusterNamespace = defaulNamespace

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		instaniatedChart, err := controllers.GetInstantiatedChart(context.TODO(), clusterSummary, helmChart, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(instaniatedChart.ReleaseName).To(Equal(helmChart.ReleaseName))
		Expect(instaniatedChart.ReleaseNamespace).To(Equal(helmChart.ReleaseNamespace))
		Expect(instaniatedChart.ChartName).To(Equal(helmChart.ChartName))
		Expect(instaniatedChart.RepositoryURL).To(Equal(helmChart.RepositoryURL))
		Expect(instaniatedChart.RepositoryName).To(Equal(helmChart.RepositoryName))
		Expect(instaniatedChart.HelmChartAction).To(Equal(helmChart.HelmChartAction))
		Expect(instaniatedChart.ChartVersion).To(Equal(helmChart.ChartVersion))
	})

	It("getInstantiatedChart returns instantiated HelmChart", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), RepositoryURL: randomString(),
			RepositoryName: randomString(), HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		helmChart.ChartVersion = `{{$version := index .Cluster.metadata.labels "k8s-version" }}{{- if eq $version "1.20"}}23.4.0
{{- else if eq $version "1.22"}}24.1.0
{{- else if eq $version "1.25"}}25.0.2
{{- else }}23.4.0
{{- end}}`

		clusterSummary.Namespace = defaulNamespace
		clusterSummary.Spec.ClusterNamespace = defaulNamespace
		clusterSummary.Spec.ClusterType = libsveltosv1beta1.ClusterTypeSveltos

		cluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Labels: map[string]string{
					"k8s-version": "1.25",
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		instaniatedChart, err := controllers.GetInstantiatedChart(context.TODO(), clusterSummary, helmChart, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(instaniatedChart.ReleaseName).To(Equal(helmChart.ReleaseName))
		Expect(instaniatedChart.ReleaseNamespace).To(Equal(helmChart.ReleaseNamespace))
		Expect(instaniatedChart.ChartName).To(Equal(helmChart.ChartName))
		Expect(instaniatedChart.RepositoryURL).To(Equal(helmChart.RepositoryURL))
		Expect(instaniatedChart.RepositoryName).To(Equal(helmChart.RepositoryName))
		Expect(instaniatedChart.HelmChartAction).To(Equal(helmChart.HelmChartAction))
		Expect(instaniatedChart.ChartVersion).To(Equal("25.0.2"))
	})

	It("updateClusterReportWithHelmReports updates ClusterReports with HelmReports", func() {
		helmChart := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			SyncMode:   configv1beta1.SyncModeDryRun,
			HelmCharts: []configv1beta1.HelmChart{*helmChart},
		}

		clusterReport := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterops.GetClusterReportName(configv1beta1.ClusterProfileKind,
					clusterProfile.Name, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Spec: configv1beta1.ClusterReportSpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
			},
			Status: configv1beta1.ClusterReportStatus{
				ResourceReports: []libsveltosv1beta1.ResourceReport{
					{
						Action:   string(libsveltosv1beta1.CreateResourceAction),
						Resource: libsveltosv1beta1.Resource{Name: randomString(), Kind: randomString()}},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		releaseReports := []configv1beta1.ReleaseReport{
			{ReleaseName: helmChart.ReleaseName, ReleaseNamespace: helmChart.ReleaseNamespace, Action: string(configv1beta1.HelmChartActionInstall)},
		}
		err := controllers.UpdateClusterReportWithHelmReports(context.TODO(), c, clusterSummary, releaseReports)
		Expect(err).To(BeNil())

		currentClusterReport := &configv1beta1.ClusterReport{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Name: clusterReport.Name, Namespace: clusterReport.Namespace}, currentClusterReport)).To(Succeed())
		Expect(currentClusterReport.Status.ResourceReports).ToNot(BeNil())
		Expect(len(currentClusterReport.Status.ResourceReports)).To(Equal(1))
		Expect(reflect.DeepEqual(currentClusterReport.Status.ResourceReports, clusterReport.Status.ResourceReports)).To(BeTrue())
		Expect(currentClusterReport.Status.ReleaseReports).To(ContainElement(releaseReports[0]))
	})

	It("handleCharts in DryRun mode updates ClusterReport", func() {
		helmChartInstall := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionInstall,
		}

		helmChartUninstall := &configv1beta1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1beta1.HelmChartActionUninstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1beta1.Spec{
			SyncMode: configv1beta1.SyncModeDryRun,
			HelmCharts: []configv1beta1.HelmChart{
				*helmChartInstall, *helmChartUninstall,
			},
		}

		clusterReport := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterops.GetClusterReportName(configv1beta1.ClusterProfileKind,
					clusterProfile.Name, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Spec: configv1beta1.ClusterReportSpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterProfile)).To(Succeed())
		clusterSummary.OwnerReferences = []metav1.OwnerReference{
			{
				Kind:       configv1beta1.ClusterProfileKind,
				Name:       clusterProfile.Name,
				APIVersion: "config.projectsveltos.io/v1beta1",
				UID:        clusterProfile.UID,
			},
		}
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, clusterReport)).To(Succeed())

		kubeconfig, closer, err := clusterproxy.CreateKubeconfig(textlogger.NewLogger(textlogger.NewConfig()), testEnv.Kubeconfig)
		Expect(err).To(BeNil())
		defer closer()

		// ClusterSummary in DryRun mode. Nothing registered with chartManager with respect to the two referenced
		// helm chart. So expect action for Install will be install, and the action for Uninstall will be no action as
		// such release has never been installed.
		err = controllers.HandleCharts(context.TODO(), clusterSummary, testEnv.Client, testEnv.Client, kubeconfig,
			false, nil, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())

		var druRunError *configv1beta1.DryRunReconciliationError
		ok := errors.As(err, &druRunError)
		Expect(ok).To(BeTrue())

		currentClusterReport := &configv1beta1.ClusterReport{}
		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterReport.Name, Namespace: clusterReport.Namespace}, currentClusterReport)
			if err != nil {
				return false
			}
			return len(currentClusterReport.Status.ReleaseReports) == 2
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Name: clusterReport.Name, Namespace: clusterReport.Namespace}, currentClusterReport)).To(Succeed())

		// Verify action for helmChartInstall is Install
		found := false
		for i := range currentClusterReport.Status.ReleaseReports {
			report := &currentClusterReport.Status.ReleaseReports[i]
			if report.ReleaseName == helmChartInstall.ReleaseName {
				Expect(report.Action).To(Equal(string(configv1beta1.InstallHelmAction)))
				found = true
			}
		}
		Expect(found).To(BeTrue())

		// Verify action for helmChartUninstall is no action
		found = false
		for i := range currentClusterReport.Status.ReleaseReports {
			report := &currentClusterReport.Status.ReleaseReports[i]
			if report.ReleaseName == helmChartUninstall.ReleaseName {
				Expect(report.Action).To(Equal(string(configv1beta1.NoHelmAction)))
				found = true
			}
		}
		Expect(found).To(BeTrue())
	})
})

var _ = Describe("Hash methods", func() {
	It("HelmHash returns hash considering all referenced helm charts", func() {
		kyvernoChart := configv1beta1.HelmChart{
			RepositoryURL:    "https://kyverno.github.io/kyverno/",
			RepositoryName:   "kyverno",
			ChartName:        "kyverno/kyverno",
			ChartVersion:     "v3.0.1",
			ReleaseName:      "kyverno-latest",
			ReleaseNamespace: "kyverno",
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
		}

		nginxChart := configv1beta1.HelmChart{
			RepositoryURL:    "https://helm.nginx.com/stable/",
			RepositoryName:   "nginx-stable",
			ChartName:        "nginx-stable/nginx-ingress",
			ChartVersion:     "0.17.1",
			ReleaseName:      "nginx-latest",
			ReleaseNamespace: "nginx",
			HelmChartAction:  configv1beta1.HelmChartActionInstall,
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
					HelmCharts: []configv1beta1.HelmChart{
						kyvernoChart,
						nginxChart,
					},
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

		initObjects := []client.Object{
			clusterSummary,
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
		sort.Sort(controllers.SortedHelmCharts(clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts))
		for i := range clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			config += render.AsCode(clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.HelmCharts[i])
		}

		h = sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.HelmHash(context.TODO(), c, clusterSummaryScope.ClusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})

	It(`getHelmReferenceResourceHash returns the hash considering all referenced
	ConfigMap/Secret in the ValueFrom section`, func() {
		namespace := randomString()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string]string{
				randomString(): randomString(),
				randomString(): randomString(),
				randomString(): randomString(),
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

		helmChart := configv1beta1.HelmChart{
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
					HelmCharts: []configv1beta1.HelmChart{
						helmChart,
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

		hash, err := controllers.GetHelmReferenceResourceHash(context.TODO(), c, clusterSummary,
			&helmChart, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(expectedHash).To(Equal(hash))
	})

	It("getHelmChartValuesHash returns hash considering Values and ValuesFrom", func() {
		namespace := randomString()

		key := randomString()
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string]string{
				key: randomString(),
			},
		}

		requestedChart := configv1beta1.HelmChart{
			Values: randomString(),
			ValuesFrom: []configv1beta1.ValueFrom{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
			},
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						requestedChart,
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

		h := sha256.New()
		expectedHash := render.AsCode(requestedChart.Values)
		expectedHash += render.AsCode(configMap.Data[key])
		h.Write([]byte(expectedHash))

		initObjects := []client.Object{
			configMap, cluster,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		hash, err := controllers.GetHelmChartValuesHash(context.TODO(), c, &requestedChart,
			clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, h.Sum(nil))).To(BeTrue())
	})

	It("getCredentialsAndCAFiles returns files containing credentials and CA", func() {
		type Credentials struct {
			Username     string
			Password     string
			RefreshToken string
			AccessToken  string
		}

		credentials := Credentials{
			Username:     randomString(),
			Password:     randomString(),
			RefreshToken: randomString(),
			AccessToken:  randomString(),
		}

		credentialsBytes, err := json.Marshal(credentials)
		Expect(err).To(BeNil())

		secretCredentials := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
			Data: map[string][]byte{
				"config.json": credentialsBytes,
			},
			Type: corev1.SecretTypeDockerConfigJson,
		}

		caByte := []byte(randomString())
		secretCA := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
			Data: map[string][]byte{
				"ca.crt": caByte,
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       configv1beta1.ClusterProfileKind,
						Name:       randomString(),
						APIVersion: "config.projectsveltos.io/v1beta1",
						UID:        types.UID(randomString()),
					},
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		initObjects := []client.Object{
			secretCredentials, secretCA, cluster,
		}

		requestedChart := configv1beta1.HelmChart{
			RegistryCredentialsConfig: &configv1beta1.RegistryCredentialsConfig{
				CredentialsSecretRef: &corev1.SecretReference{
					Namespace: secretCredentials.Namespace,
					Name:      secretCredentials.Name,
				},
				CASecretRef: &corev1.SecretReference{
					Namespace: secretCA.Namespace,
					Name:      secretCA.Name,
				},
				InsecureSkipTLSVerify: false,
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		credentialsPath, caPath, err := controllers.GetCredentialsAndCAFiles(context.TODO(), c,
			clusterSummary, &requestedChart)
		Expect(err).To(BeNil())
		Expect(credentialsPath).ToNot(BeEmpty())
		verifyFileContent(credentialsPath, credentialsBytes)
		Expect(os.Remove(credentialsPath)).To(Succeed())
		Expect(caPath).ToNot(BeEmpty())
		verifyFileContent(caPath, caByte)
		Expect(os.Remove(caPath)).To(Succeed())
	})
})

func verifyFileContent(filePath string, data []byte) {
	content, err := os.ReadFile(filePath)
	Expect(err).To(BeNil())
	Expect(reflect.DeepEqual(content, data)).To(BeTrue())
}
