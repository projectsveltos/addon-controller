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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	resourceSummaryNamespace = "projectsveltos"
)

var _ = Describe("ResourceSummary Collection", func() {
	It("collectResourceSummariesFromCluster collects and processes ResourceSummaries from clusters", func() {
		cluster := prepareCluster()

		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      clusterProfileNamePrefix + randomString(),
				Labels:    map[string]string{controllers.ClusterProfileLabelName: randomString()},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterType: libsveltosv1alpha1.ClusterTypeCapi,
			},
		}
		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		// Updates clusterSummary Status to indicate Helm is deployed
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureHelm,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				Hash:      []byte(randomString()),
			},
			{
				FeatureID: configv1alpha1.FeatureResources,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				Hash:      []byte(randomString()),
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Create a ResourceSummary whose status indicates configuration drift for
		// helm resources

		// In managed cluster this is the namespace where ResourceSummaries
		// are created
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: resourceSummaryNamespace,
			},
		}
		err := testEnv.Create(context.TODO(), ns)
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		resourceSummary := getResourceSummary(nil, nil)
		resourceSummary.Labels = map[string]string{
			libsveltosv1alpha1.ClusterSummaryNameLabel:      clusterSummary.Name,
			libsveltosv1alpha1.ClusterSummaryNamespaceLabel: clusterSummary.Namespace,
		}
		Expect(testEnv.Create(context.TODO(), resourceSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, resourceSummary)).To(Succeed())

		currentResourceSummary := &libsveltosv1alpha1.ResourceSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceSummaryNamespace, Name: resourceSummary.Name},
			currentResourceSummary)).To(Succeed())
		currentResourceSummary.Status.HelmResourcesChanged = true
		Expect(testEnv.Status().Update(context.TODO(), currentResourceSummary))

		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: resourceSummaryNamespace, Name: resourceSummary.Name},
				currentResourceSummary)
			return err == nil && currentResourceSummary.Status.HelmResourcesChanged
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: resourceSummaryNamespace, Name: resourceSummary.Name},
			currentResourceSummary)).To(Succeed())
		Expect(currentResourceSummary.Status.HelmResourcesChanged).To(BeTrue())

		// Given current state:
		// - ClusterSummary.Status.FeatureSummaries marked as provisioned for helm
		// - ResourceSummary.Status indicating a configuration drift for helm
		// CollectResourceSummariesFromCluster will:
		// - reset ClusterSummary.Status.FeatureSummaries hash for helm (indicating new reconciliation is needed)
		// - reset ResourceSummary.Status
		Expect(controllers.CollectResourceSummariesFromCluster(context.TODO(), testEnv.Client, getClusterRef(cluster),
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return false
			}
			for i := range currentClusterSummary.Status.FeatureSummaries {
				if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == configv1alpha1.FeatureHelm {
					return currentClusterSummary.Status.FeatureSummaries[i].Hash == nil
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: resourceSummaryNamespace, Name: resourceSummary.Name},
				currentResourceSummary)
			return err == nil && !currentResourceSummary.Status.HelmResourcesChanged
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

func getResourceSummary(resource, helmResource *corev1.ObjectReference) *libsveltosv1alpha1.ResourceSummary {
	rs := &libsveltosv1alpha1.ResourceSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      randomString(),
			Namespace: resourceSummaryNamespace,
		},
	}

	if resource != nil {
		rs.Spec.Resources = []libsveltosv1alpha1.Resource{
			{
				Name:      resource.Name,
				Namespace: resource.Namespace,
				Kind:      resource.Kind,
				Group:     resource.GroupVersionKind().Group,
				Version:   resource.GroupVersionKind().Version,
			},
		}
	}

	if helmResource != nil {
		rs.Spec.ChartResources = []libsveltosv1alpha1.HelmResources{
			{
				ChartName:        randomString(),
				ReleaseName:      randomString(),
				ReleaseNamespace: randomString(),
				Resources: []libsveltosv1alpha1.Resource{
					{
						Name:      helmResource.Name,
						Namespace: helmResource.Namespace,
						Kind:      helmResource.Kind,
						Group:     helmResource.GroupVersionKind().Group,
						Version:   helmResource.GroupVersionKind().Version,
					},
				},
			},
		}
	}

	return rs
}
