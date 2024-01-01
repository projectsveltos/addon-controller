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
	"errors"
	"fmt"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gdexlab/go-render/render"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/controllers/chartmanager"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
)

var _ = Describe("HandlersHelm", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
	var clusterSummary *configv1alpha1.ClusterSummary

	BeforeEach(func() {
		clusterNamespace := randomString()

		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.Spec{
				ClusterSelector: libsveltosv1alpha1.Selector(fmt.Sprintf("%s=%s", randomString(), randomString())),
			},
		}

		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       configv1alpha1.ClusterProfileKind,
						Name:       clusterProfile.Name,
						APIVersion: "config.projectsveltos.io/v1alpha1",
					},
				},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: clusterNamespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}
	})

	It("shouldInstall returns false when requested version does not match installed version", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       release.StatusDeployed.String(),
			ChartVersion: "v2.5.0",
		}
		requestChart := &configv1alpha1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1alpha1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeFalse())
	})

	It("shouldInstall returns false when requested version matches installed version",
		func() {
			currentRelease := &controllers.ReleaseInfo{
				Status:       release.StatusDeployed.String(),
				ChartVersion: "v2.5.3",
			}
			requestChart := &configv1alpha1.HelmChart{
				ChartVersion:    "v2.5.3",
				HelmChartAction: configv1alpha1.HelmChartActionInstall,
			}
			Expect(controllers.ShouldInstall(currentRelease, requestChart)).To(BeFalse())
		})

	It("shouldInstall returns true when there is no current installed version", func() {
		requestChart := &configv1alpha1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1alpha1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldInstall(nil, requestChart)).To(BeTrue())
	})

	It("shouldInstall returns false action is uninstall", func() {
		requestChart := &configv1alpha1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1alpha1.HelmChartActionUninstall,
		}
		Expect(controllers.ShouldInstall(nil, requestChart)).To(BeFalse())
	})

	It("shouldUninstall returns false when there is no current release installed", func() {
		requestChart := &configv1alpha1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1alpha1.HelmChartActionUninstall,
		}
		Expect(controllers.ShouldUninstall(nil, requestChart)).To(BeFalse())
	})

	It("shouldUninstall returns false when action is not Uninstall", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       release.StatusDeployed.String(),
			ChartVersion: "v2.5.3",
		}
		requestChart := &configv1alpha1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1alpha1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldUninstall(currentRelease, requestChart)).To(BeFalse())
	})

	It("shouldUpgrade returns true when installed release is different than requested release", func() {
		currentRelease := &controllers.ReleaseInfo{
			Status:       release.StatusDeployed.String(),
			ChartVersion: "v2.5.0",
		}
		requestChart := &configv1alpha1.HelmChart{
			ChartVersion:    "v2.5.3",
			HelmChartAction: configv1alpha1.HelmChartActionInstall,
		}
		Expect(controllers.ShouldUpgrade(currentRelease, requestChart, clusterSummary)).To(BeTrue())
	})

	It("UpdateStatusForeferencedHelmReleases updates ClusterSummary.Status.HelmReleaseSummaries", func() {
		calicoChart := &configv1alpha1.HelmChart{
			RepositoryURL:    "https://projectcalico.docs.tigera.io/charts",
			RepositoryName:   "projectcalico",
			ChartName:        "projectcalico/tigera-operator",
			ChartVersion:     "v3.24.1",
			ReleaseName:      "calico",
			ReleaseNamespace: "calico",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
		}

		kyvernoSummary := configv1alpha1.HelmChartSummary{
			ReleaseName:      "kyverno",
			ReleaseNamespace: "kyverno",
			Status:           configv1alpha1.HelChartStatusManaging,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1alpha1.Spec{
			HelmCharts: []configv1alpha1.HelmChart{*calicoChart},
		}

		// List a helm chart non referenced anymore as managed
		clusterSummary.Status = configv1alpha1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1alpha1.HelmChartSummary{
				kyvernoSummary,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		conflict, err := controllers.UpdateStatusForeferencedHelmReleases(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		Expect(currentClusterSummary.Status.HelmReleaseSummaries).ToNot(BeNil())
		Expect(len(currentClusterSummary.Status.HelmReleaseSummaries)).To(Equal(2))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].Status).To(Equal(configv1alpha1.HelChartStatusManaging))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName).To(Equal(calicoChart.ReleaseName))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace).To(Equal(calicoChart.ReleaseNamespace))

		// UpdateStatusForeferencedHelmReleases adds status for referenced releases and does not remove any
		// existing entry for non existing releases.
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[1].Status).To(Equal(kyvernoSummary.Status))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[1].ReleaseName).To(Equal(kyvernoSummary.ReleaseName))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[1].ReleaseNamespace).To(Equal(kyvernoSummary.ReleaseNamespace))
	})

	It("updateStatusForeferencedHelmReleases is no-op in DryRun mode", func() {
		clusterSummary.Spec.ClusterProfileSpec = configv1alpha1.Spec{
			HelmCharts: []configv1alpha1.HelmChart{
				{RepositoryURL: randomString(), RepositoryName: randomString(), ChartName: randomString(), ChartVersion: randomString(),
					ReleaseName: randomString(), ReleaseNamespace: randomString()},
			},
			SyncMode: configv1alpha1.SyncModeDryRun,
		}

		// List an helm chart non referenced anymore as managed
		clusterSummary.Status = configv1alpha1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1alpha1.HelmChartSummary{
				{ReleaseName: randomString(), ReleaseNamespace: randomString(), Status: configv1alpha1.HelChartStatusManaging},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		conflict, err := controllers.UpdateStatusForeferencedHelmReleases(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(conflict).To(BeFalse())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
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
		contourChart := &configv1alpha1.HelmChart{
			RepositoryURL:    "https://charts.bitnami.com/bitnami",
			RepositoryName:   "bitnami/contour",
			ChartName:        "bitnami/contour",
			ChartVersion:     "12.1.0",
			ReleaseName:      "contour-latest",
			ReleaseNamespace: "contour",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
		}

		kyvernoSummary := configv1alpha1.HelmChartSummary{
			ReleaseName:      "kyverno",
			ReleaseNamespace: "kyverno",
			Status:           configv1alpha1.HelChartStatusManaging,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1alpha1.Spec{
			HelmCharts: []configv1alpha1.HelmChart{*contourChart},
		}
		// List a helm chart non referenced anymore as managed
		clusterSummary.Status = configv1alpha1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1alpha1.HelmChartSummary{
				kyvernoSummary,
				{
					ReleaseName:      contourChart.ReleaseName,
					ReleaseNamespace: contourChart.ReleaseNamespace,
					Status:           configv1alpha1.HelChartStatusManaging,
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		err = controllers.UpdateStatusForNonReferencedHelmReleases(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		Expect(currentClusterSummary.Status.HelmReleaseSummaries).ToNot(BeNil())
		Expect(len(currentClusterSummary.Status.HelmReleaseSummaries)).To(Equal(1))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].Status).To(Equal(configv1alpha1.HelChartStatusManaging))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseName).To(Equal(contourChart.ReleaseName))
		Expect(currentClusterSummary.Status.HelmReleaseSummaries[0].ReleaseNamespace).To(Equal(contourChart.ReleaseNamespace))
	})

	It("updateChartsInClusterConfiguration updates ClusterConfiguration with deployed helm releases", func() {
		chartDeployed := []configv1alpha1.Chart{
			{
				RepoURL:      "https://charts.bitnami.com/bitnami",
				ReleaseName:  "contour-latest",
				ChartVersion: "12.1.0",
				Namespace:    "projectcontour",
			},
		}

		clusterConfiguration := &configv1alpha1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      controllers.GetClusterConfigurationName(clusterSummary.Spec.ClusterName, libsveltosv1alpha1.ClusterTypeCapi),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Status: configv1alpha1.ClusterConfigurationStatus{
				ClusterProfileResources: []configv1alpha1.ClusterProfileResource{
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
			chartDeployed, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

		currentClusterConfiguration := &configv1alpha1.ClusterConfiguration{}
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
			Equal(configv1alpha1.FeatureHelm))
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
		helmChart := &configv1alpha1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1alpha1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1alpha1.Spec{
			SyncMode:   configv1alpha1.SyncModeDryRun,
			HelmCharts: []configv1alpha1.HelmChart{*helmChart},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		report, err := controllers.CreateReportForUnmanagedHelmRelease(context.TODO(), c, clusterSummary,
			helmChart, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))
		Expect(err).To(BeNil())
		Expect(report).ToNot(BeNil())
		Expect(report.Action).To(Equal(string(configv1alpha1.InstallHelmAction)))
		Expect(report.ReleaseName).To(Equal(helmChart.ReleaseName))
		Expect(report.ReleaseNamespace).To(Equal(helmChart.ReleaseNamespace))
	})

	It("updateClusterReportWithHelmReports updates ClusterReports with HelmReports", func() {
		helmChart := &configv1alpha1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1alpha1.HelmChartActionInstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1alpha1.Spec{
			SyncMode:   configv1alpha1.SyncModeDryRun,
			HelmCharts: []configv1alpha1.HelmChart{*helmChart},
		}

		clusterReport := &configv1alpha1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: controllers.GetClusterReportName(configv1alpha1.ClusterProfileKind,
					clusterProfile.Name, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Spec: configv1alpha1.ClusterReportSpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
			},
			Status: configv1alpha1.ClusterReportStatus{
				ResourceReports: []configv1alpha1.ResourceReport{
					{
						Action:   string(configv1alpha1.CreateResourceAction),
						Resource: configv1alpha1.Resource{Name: randomString(), Kind: randomString()}},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterReport,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		releaseReports := []configv1alpha1.ReleaseReport{
			{ReleaseName: helmChart.ReleaseName, ReleaseNamespace: helmChart.ReleaseNamespace, Action: string(configv1alpha1.HelmChartActionInstall)},
		}
		err := controllers.UpdateClusterReportWithHelmReports(context.TODO(), c, clusterSummary, releaseReports)
		Expect(err).To(BeNil())

		currentClusterReport := &configv1alpha1.ClusterReport{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Name: clusterReport.Name, Namespace: clusterReport.Namespace}, currentClusterReport)).To(Succeed())
		Expect(currentClusterReport.Status.ResourceReports).ToNot(BeNil())
		Expect(len(currentClusterReport.Status.ResourceReports)).To(Equal(1))
		Expect(reflect.DeepEqual(currentClusterReport.Status.ResourceReports, clusterReport.Status.ResourceReports)).To(BeTrue())
		Expect(currentClusterReport.Status.ReleaseReports).To(ContainElement(releaseReports[0]))
	})

	It("handleCharts in DryRun mode updates ClusterReport", func() {
		helmChartInstall := &configv1alpha1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1alpha1.HelmChartActionInstall,
		}

		helmChartUninstall := &configv1alpha1.HelmChart{
			ReleaseName: randomString(), ReleaseNamespace: randomString(),
			ChartName: randomString(), ChartVersion: randomString(),
			RepositoryURL: randomString(), RepositoryName: randomString(),
			HelmChartAction: configv1alpha1.HelmChartActionUninstall,
		}

		clusterSummary.Spec.ClusterProfileSpec = configv1alpha1.Spec{
			SyncMode: configv1alpha1.SyncModeDryRun,
			HelmCharts: []configv1alpha1.HelmChart{
				*helmChartInstall, *helmChartUninstall,
			},
		}

		clusterReport := &configv1alpha1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Name: controllers.GetClusterReportName(configv1alpha1.ClusterProfileKind,
					clusterProfile.Name, clusterSummary.Spec.ClusterName, clusterSummary.Spec.ClusterType),
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
			Spec: configv1alpha1.ClusterReportSpec{
				ClusterNamespace: clusterSummary.Spec.ClusterNamespace,
				ClusterName:      clusterSummary.Spec.ClusterName,
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Namespace,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), clusterProfile)).To(Succeed())
		clusterSummary.OwnerReferences = []metav1.OwnerReference{
			{
				Kind:       configv1alpha1.ClusterProfileKind,
				Name:       clusterProfile.Name,
				APIVersion: "config.projectsveltos.io/v1alpha1",
				UID:        clusterProfile.UID,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterReport)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, clusterReport)).To(Succeed())

		kubeconfig, err := clusterproxy.CreateKubeconfig(textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))), testEnv.Kubeconfig)
		Expect(err).To(BeNil())

		// ClusterSummary in DryRun mode. Nothing registered with chartManager with respect to the two referenced
		// helm chart. So expect action for Install will be install, and the action for Uninstall will be no action as
		// such release has never been installed.
		err = controllers.HandleCharts(context.TODO(), clusterSummary, testEnv.Client, testEnv.Client, kubeconfig,
			textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))
		Expect(err).ToNot(BeNil())

		var druRunError *configv1alpha1.DryRunReconciliationError
		ok := errors.As(err, &druRunError)
		Expect(ok).To(BeTrue())

		currentClusterReport := &configv1alpha1.ClusterReport{}
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
				Expect(report.Action).To(Equal(string(configv1alpha1.InstallHelmAction)))
				found = true
			}
		}
		Expect(found).To(BeTrue())

		// Verify action for helmChartUninstall is no action
		found = false
		for i := range currentClusterReport.Status.ReleaseReports {
			report := &currentClusterReport.Status.ReleaseReports[i]
			if report.ReleaseName == helmChartUninstall.ReleaseName {
				Expect(report.Action).To(Equal(string(configv1alpha1.NoHelmAction)))
				found = true
			}
		}
		Expect(found).To(BeTrue())
	})
})

var _ = Describe("Hash methods", func() {
	It("HelmHash returns hash considering all referenced helm charts", func() {
		kyvernoChart := configv1alpha1.HelmChart{
			RepositoryURL:    "https://kyverno.github.io/kyverno/",
			RepositoryName:   "kyverno",
			ChartName:        "kyverno/kyverno",
			ChartVersion:     "v3.0.1",
			ReleaseName:      "kyverno-latest",
			ReleaseNamespace: "kyverno",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
		}

		nginxChart := configv1alpha1.HelmChart{
			RepositoryURL:    "https://helm.nginx.com/stable/",
			RepositoryName:   "nginx-stable",
			ChartName:        "nginx-stable/nginx-ingress",
			ChartVersion:     "0.17.1",
			ReleaseName:      "nginx-latest",
			ReleaseNamespace: "nginx",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
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
					HelmCharts: []configv1alpha1.HelmChart{
						kyvernoChart,
						nginxChart,
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		config := fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode)
		config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Reloader)
		config += render.AsCode(kyvernoChart)
		config += render.AsCode(nginxChart)
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.HelmHash(context.TODO(), c, clusterSummaryScope,
			textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
