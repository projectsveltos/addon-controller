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
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gdexlab/go-render/render"
	"helm.sh/helm/v3/pkg/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

var _ = Describe("HandlersHelm", func() {
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

	It("shouldInstall returns false requested version matches installed version", func() {
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
		Expect(controllers.ShouldUpgrade(currentRelease, requestChart)).To(BeTrue())
	})
})

var _ = Describe("Hash methods", func() {
	It("HelmHash returns hash considering all referenced helm charts", func() {
		kyvernoChart := configv1alpha1.HelmChart{
			RepositoryURL:    "https://kyverno.github.io/kyverno/",
			RepositoryName:   "kyverno",
			ChartName:        "kyverno/kyverno",
			ChartVersion:     "v2.5.0",
			ReleaseName:      "kyverno-latest",
			ReleaseNamespace: "kyverno",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
		}

		nginxChart := configv1alpha1.HelmChart{
			RepositoryURL:    "https://helm.nginx.com/stable/",
			RepositoryName:   "nginx-stable",
			ChartName:        "nginx-stable/nginx-ingress",
			ChartVersion:     "0.14.0",
			ReleaseName:      "nginx-latest",
			ReleaseNamespace: "nginx",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
		}

		namespace := "reconcile" + randomString()
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
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

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		config := render.AsCode(kyvernoChart)
		config += render.AsCode(nginxChart)
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.HelmHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
