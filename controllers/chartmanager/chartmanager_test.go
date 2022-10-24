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

package chartmanager_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/controllers/chartmanager"
)

const (
	upstreamClusterNamePrefix = "chart-manager"
)

var _ = Describe("Chart manager", func() {
	var clusterSummary *configv1alpha1.ClusterSummary
	var c client.Client
	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = setupScheme()

		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: randomString(),
				ClusterName:      upstreamClusterNamePrefix + randomString(),
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					HelmCharts: []configv1alpha1.HelmChart{
						{
							RepositoryURL:    "https://kyverno.github.io/kyverno/",
							RepositoryName:   "kyverno",
							ChartName:        "kyverno/kyverno",
							ChartVersion:     "v2.5.0",
							ReleaseName:      "kyverno-latest",
							ReleaseNamespace: "kyverno",
							HelmChartAction:  configv1alpha1.HelmChartActionInstall,
						},
						{
							RepositoryURL:    "https://helm.nginx.com/stable/",
							RepositoryName:   "nginx-stable",
							ChartName:        "nginx-stable/nginx-ingress",
							ChartVersion:     "0.14.0",
							ReleaseName:      "nginx-latest",
							ReleaseNamespace: "nginx",
							HelmChartAction:  configv1alpha1.HelmChartActionInstall,
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

	})

	AfterEach(func() {
		removeSubscriptions(c, clusterSummary)
	})

	It("registerClusterSummaryForCharts registers clusterSummary for all referenced helm charts",
		func() {
			manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
			Expect(err).To(BeNil())

			manager.RegisterClusterSummaryForCharts(clusterSummary)

			for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
				chart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
				By(fmt.Sprintf("Verifying ClusterSummary %s manages helm release %s/%s",
					clusterSummary.Name, chart.ReleaseNamespace, chart.ReleaseName))
				Expect(manager.CanManageChart(clusterSummary, chart)).To(BeTrue())
			}
		})

	It("canManageChart return true only for the first registered ClusterSummary", func() {
		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		tmpClusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Name + randomString(),
			},
			Spec: clusterSummary.Spec,
		}

		manager.RegisterClusterSummaryForCharts(tmpClusterSummary)
		defer removeSubscriptions(c, tmpClusterSummary)

		for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			chart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			By(fmt.Sprintf("Verifying ClusterSummary %s does not manage helm release %s/%s",
				tmpClusterSummary.Name, chart.ReleaseNamespace, chart.ReleaseName))
			Expect(manager.CanManageChart(tmpClusterSummary, chart)).To(BeFalse())
		}
	})

	It("removeStaleRegistrations removes registration for helm charts not referenced anymore", func() {
		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			chart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			By(fmt.Sprintf("Verifying ClusterSummary %s manages helm release %s/%s",
				clusterSummary.Name, chart.ReleaseNamespace, chart.ReleaseName))
			Expect(manager.CanManageChart(clusterSummary, chart)).To(BeTrue())
		}

		clusterSummary.Spec.ClusterProfileSpec.HelmCharts = nil
		manager.RemoveStaleRegistrations(clusterSummary)

		for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			chart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			By(fmt.Sprintf("Verifying ClusterSummary %s does not manage helm release %s/%s",
				clusterSummary.Name, chart.ReleaseNamespace, chart.ReleaseName))
			Expect(manager.CanManageChart(clusterSummary, chart)).To(BeTrue())
		}
	})

	It("getManagedHelmReleases returns the managed helm releases only", func() {
		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		tmpClusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Name + randomString(),
			},
			Spec: clusterSummary.Spec,
		}

		prometheusChart := configv1alpha1.HelmChart{
			RepositoryURL:    "https://prometheus-community.github.io/helm-charts",
			RepositoryName:   "prometheus-community",
			ChartName:        "prometheus-community/kube-prometheus-stack",
			ChartVersion:     "40.0.0",
			ReleaseName:      "prometheus-latest",
			ReleaseNamespace: "prometheus",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
		}

		tmpClusterSummary.Spec.ClusterProfileSpec.HelmCharts = append(tmpClusterSummary.Spec.ClusterProfileSpec.HelmCharts,
			prometheusChart)

		manager.RegisterClusterSummaryForCharts(tmpClusterSummary)
		defer removeSubscriptions(c, tmpClusterSummary)

		managedReleases := manager.GetManagedHelmReleases(clusterSummary)
		Expect(len(managedReleases)).To(Equal(2))

		managedReleases = manager.GetManagedHelmReleases(tmpClusterSummary)
		Expect(len(managedReleases)).To(Equal(1))

		Expect(manager.CanManageChart(tmpClusterSummary, &prometheusChart)).To(BeTrue())

		for i := range clusterSummary.Spec.ClusterProfileSpec.HelmCharts {
			chart := &clusterSummary.Spec.ClusterProfileSpec.HelmCharts[i]
			By(fmt.Sprintf("Verifying ClusterSummary %s does not manage helm release %s/%s",
				tmpClusterSummary.Name, chart.ReleaseNamespace, chart.ReleaseName))
			Expect(manager.CanManageChart(tmpClusterSummary, chart)).To(BeFalse())
		}
	})

	It("isClusterSummaryAlreadyRegistered returns true if a clusterSummary key is already present", func() {
		key := randomString()
		keys := []string{randomString(), randomString(), key, randomString()}
		Expect(chartmanager.IsClusterSummaryAlreadyRegistered(keys, key+randomString())).To(BeFalse())
		Expect(chartmanager.IsClusterSummaryAlreadyRegistered(keys, key)).To(BeTrue())
	})

	It("getManagerForChart returns the name of the ClusterSummary managing an helm chart", func() {
		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())

		manager.RegisterClusterSummaryForCharts(clusterSummary)

		tmpClusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Name + randomString(),
			},
			Spec: clusterSummary.Spec,
		}

		prometheusChart := configv1alpha1.HelmChart{
			RepositoryURL:    "https://prometheus-community.github.io/helm-charts",
			RepositoryName:   "prometheus-community",
			ChartName:        "prometheus-community/kube-prometheus-stack",
			ChartVersion:     "40.0.0",
			ReleaseName:      "prometheus-latest",
			ReleaseNamespace: "prometheus",
			HelmChartAction:  configv1alpha1.HelmChartActionInstall,
		}

		tmpClusterSummary.Spec.ClusterProfileSpec.HelmCharts = append(tmpClusterSummary.Spec.ClusterProfileSpec.HelmCharts,
			prometheusChart)
		manager.RegisterClusterSummaryForCharts(tmpClusterSummary)
		defer removeSubscriptions(c, tmpClusterSummary)

		csName, err := manager.GetManagerForChart(
			clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, &prometheusChart)
		Expect(err).To(BeNil())
		Expect(csName).To(Equal(tmpClusterSummary.Name))
	})

	It("getRegisteredClusterSummaries returns currently registered ClusterSummaries filtering by CAPI Cluster",
		func() {
			manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
			Expect(err).To(BeNil())

			manager.RegisterClusterSummaryForCharts(clusterSummary)

			tmpClusterSummary1 := &configv1alpha1.ClusterSummary{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterSummary.Name + randomString(),
				},
				Spec: clusterSummary.Spec,
			}
			manager.RegisterClusterSummaryForCharts(tmpClusterSummary1)
			defer removeSubscriptions(c, tmpClusterSummary1)

			tmpClusterSummary2 := &configv1alpha1.ClusterSummary{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterSummary.Name + randomString(),
				},
				Spec: clusterSummary.Spec,
			}
			tmpClusterSummary2.Spec.ClusterNamespace = clusterSummary.Spec.ClusterNamespace + randomString()
			manager.RegisterClusterSummaryForCharts(tmpClusterSummary2)
			defer removeSubscriptions(c, tmpClusterSummary2)

			registered := manager.GetRegisteredClusterSummaries(
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName)
			Expect(len(registered)).To(Equal(2))
			Expect(registered).To(ContainElement(clusterSummary.Name))
			Expect(registered).To(ContainElement(tmpClusterSummary1.Name))
		})

	It("rebuildRegistrations rebuilds helm chart registrations", func() {
		Expect(len(clusterSummary.Spec.ClusterProfileSpec.HelmCharts)).Should(BeNumerically(">=", 2))

		// Mark clusterSummary as manager for one release
		clusterSummary.Status = configv1alpha1.ClusterSummaryStatus{
			HelmReleaseSummaries: []configv1alpha1.HelmChartSummary{
				{
					ReleaseName:      clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0].ReleaseName,
					ReleaseNamespace: clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0].ReleaseNamespace,
					Status:           configv1alpha1.HelChartStatusManaging,
				},
				{
					ReleaseName:      clusterSummary.Spec.ClusterProfileSpec.HelmCharts[1].ReleaseName,
					ReleaseNamespace: clusterSummary.Spec.ClusterProfileSpec.HelmCharts[1].ReleaseNamespace,
					Status:           configv1alpha1.HelChartStatusConflict,
				},
			},
		}
		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		// Mark tmpClusterSummary as manager for other release
		tmpClusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Name + randomString(),
			},
			Spec: clusterSummary.Spec,
			Status: configv1alpha1.ClusterSummaryStatus{
				HelmReleaseSummaries: []configv1alpha1.HelmChartSummary{
					{
						ReleaseName:      clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0].ReleaseName,
						ReleaseNamespace: clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0].ReleaseNamespace,
						Status:           configv1alpha1.HelChartStatusConflict,
					},
					{
						ReleaseName:      clusterSummary.Spec.ClusterProfileSpec.HelmCharts[1].ReleaseName,
						ReleaseNamespace: clusterSummary.Spec.ClusterProfileSpec.HelmCharts[1].ReleaseNamespace,
						Status:           configv1alpha1.HelChartStatusManaging,
					},
				},
			},
		}
		Expect(c.Create(context.TODO(), tmpClusterSummary)).To(Succeed())
		defer removeSubscriptions(c, tmpClusterSummary)

		manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
		Expect(err).To(BeNil())

		err = chartmanager.RebuildRegistrations(manager, context.TODO(), c)
		Expect(err).To(BeNil())

		Expect(manager.CanManageChart(clusterSummary,
			&clusterSummary.Spec.ClusterProfileSpec.HelmCharts[0])).To(BeTrue())
		Expect(manager.CanManageChart(tmpClusterSummary,
			&tmpClusterSummary.Spec.ClusterProfileSpec.HelmCharts[0])).To(BeFalse())

		Expect(manager.CanManageChart(clusterSummary,
			&clusterSummary.Spec.ClusterProfileSpec.HelmCharts[1])).To(BeFalse())
		Expect(manager.CanManageChart(tmpClusterSummary,
			&tmpClusterSummary.Spec.ClusterProfileSpec.HelmCharts[1])).To(BeTrue())
	})
})

func removeSubscriptions(c client.Client, clusterSummary *configv1alpha1.ClusterSummary) {
	manager, err := chartmanager.GetChartManagerInstance(context.TODO(), c)
	Expect(err).To(BeNil())

	clusterSummary.Spec.ClusterProfileSpec.HelmCharts = nil
	manager.RemoveStaleRegistrations(clusterSummary)
}

func setupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	Expect(configv1alpha1.AddToScheme(s)).To(Succeed())
	Expect(clusterv1.AddToScheme(s)).To(Succeed())
	Expect(clientgoscheme.AddToScheme(s)).To(Succeed())
	return s
}
