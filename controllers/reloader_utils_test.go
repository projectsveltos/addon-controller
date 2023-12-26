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
	"fmt"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("Reloader utils", func() {
	It("watchForRollingUpgrade returns true only for Deployment/StatefulSet/DaemonSet", func() {
		type resourceData struct {
			resource *corev1.ObjectReference
			result   bool
		}

		testData := []resourceData{
			{
				resource: &corev1.ObjectReference{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
				result:   true,
			},
			{
				resource: &corev1.ObjectReference{Kind: "StatefulSet", Namespace: randomString(), Name: randomString()},
				result:   true,
			},
			{
				resource: &corev1.ObjectReference{Kind: "DaemonSet", Namespace: randomString(), Name: randomString()},
				result:   true,
			},
			{
				resource: &corev1.ObjectReference{Kind: randomString(), Namespace: randomString(), Name: randomString()},
				result:   false,
			},
		}

		for i := range testData {
			Expect(controllers.WatchForRollingUpgrade(testData[i].resource)).To(
				Equal(testData[i].result), fmt.Sprintf("resource %s", testData[i].resource.Kind))
		}
	})

	It("createReloaderInstance creates reloader instance", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		reloaderInfo := []libsveltosv1alpha1.ReloaderInfo{
			{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
			{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
		}

		clusterProfileName := randomString()
		feature := configv1alpha1.FeatureHelm
		Expect(controllers.CreateReloaderInstance(context.TODO(), c,
			clusterProfileName, feature, reloaderInfo)).To(Succeed())

		reloaders := &libsveltosv1alpha1.ReloaderList{}
		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(1))
		Expect(len(reloaders.Items[0].Labels)).ToNot(BeNil())
		Expect(len(reloaders.Items[0].Annotations)).ToNot(BeNil())
		Expect(reflect.DeepEqual(reloaders.Items[0].Spec.ReloaderInfo, reloaderInfo)).To(BeTrue())
	})

	It("deployReloaderInstance creates/updates reloader instance", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		resources := []corev1.ObjectReference{
			{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
			{Kind: "StatefulSet", Namespace: randomString(), Name: randomString()},
			{Kind: "DaemonSet", Namespace: randomString(), Name: randomString()},
		}

		clusterProfileName := randomString()
		feature := configv1alpha1.FeatureHelm
		Expect(controllers.DeployReloaderInstance(context.TODO(), c,
			clusterProfileName, feature, resources, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

		reloaders := &libsveltosv1alpha1.ReloaderList{}
		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(1))
		Expect(len(reloaders.Items[0].Labels)).ToNot(BeNil())
		Expect(len(reloaders.Items[0].Annotations)).ToNot(BeNil())

		Expect(len(reloaders.Items[0].Spec.ReloaderInfo)).To(Equal(len(resources)))
		for i := range resources {
			Expect(reloaders.Items[0].Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1alpha1.ReloaderInfo{
					Kind:      resources[i].Kind,
					Namespace: resources[i].Namespace,
					Name:      resources[i].Name,
				}))
		}

		resources = []corev1.ObjectReference{
			{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
			{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
			{Kind: "StatefulSet", Namespace: randomString(), Name: randomString()},
			{Kind: "DaemonSet", Namespace: randomString(), Name: randomString()},
		}

		// Reloader Spec.ReloaderInfo is updated now
		Expect(controllers.DeployReloaderInstance(context.TODO(), c,
			clusterProfileName, feature, resources, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(1))
		Expect(len(reloaders.Items[0].Labels)).ToNot(BeNil())
		Expect(len(reloaders.Items[0].Annotations)).ToNot(BeNil())

		Expect(len(reloaders.Items[0].Spec.ReloaderInfo)).To(Equal(len(resources)))
		for i := range resources {
			Expect(reloaders.Items[0].Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1alpha1.ReloaderInfo{
					Kind:      resources[i].Kind,
					Namespace: resources[i].Namespace,
					Name:      resources[i].Name,
				}))
		}
	})

	It("removeReloaderInstance returns no error when Reloader instance does not exist", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		Expect(controllers.RemoveReloaderInstance(context.TODO(), c, randomString(),
			configv1alpha1.FeatureKustomize, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(BeNil())
	})

	It("removeReloaderInstance removes Reloader instance", func() {
		clusterProfileName := randomString()
		feature := configv1alpha1.FeatureKustomize

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		Expect(controllers.CreateReloaderInstance(context.TODO(), c,
			clusterProfileName, feature, nil)).To(Succeed())
		reloaders := &libsveltosv1alpha1.ReloaderList{}
		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(1))

		Expect(controllers.RemoveReloaderInstance(context.TODO(), c, clusterProfileName,
			feature, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(BeNil())

		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(0))
	})

	It("updateReloaderWithDeployedResources creates reloader instance", func() {
		resources := []corev1.ObjectReference{
			{
				Kind:      "Deployment",
				Name:      randomString(),
				Namespace: randomString(),
			},
			{
				Kind:      "DaemonSet",
				Name:      randomString(),
				Namespace: randomString(),
			},
		}

		// Creates cluster and Secret with kubeconfig to access it
		// This is needed as updateReloaderWithDeployedResources fetches the
		// Secret containing the Kubeconfig to access the cluster
		cluster := prepareCluster()

		clusterProfileOwner := &metav1.OwnerReference{
			Kind:       configv1alpha1.ClusterProfileKind,
			APIVersion: configv1alpha1.GroupVersion.String(),
			Name:       randomString(),
			UID:        types.UID(randomString()),
		}

		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
				ClusterProfileSpec: configv1alpha1.Spec{
					Reloader: true,
				},
			},
		}

		Expect(controllers.UpdateReloaderWithDeployedResources(context.TODO(), testEnv.Client, clusterProfileOwner,
			configv1alpha1.FeatureResources, resources, clusterSummary, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

		reloaders := &libsveltosv1alpha1.ReloaderList{}

		Eventually(func() bool {
			err := testEnv.Client.List(context.TODO(), reloaders)
			return err == nil && len(reloaders.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Client.List(context.TODO(), reloaders)).To(Succeed())

		for i := range resources {
			Expect(reloaders.Items[0].Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1alpha1.ReloaderInfo{
					Kind:      resources[i].Kind,
					Namespace: resources[i].Namespace,
					Name:      resources[i].Name,
				},
			))
		}

		clusterSummary.Spec.ClusterProfileSpec.Reloader = false

		Expect(controllers.UpdateReloaderWithDeployedResources(context.TODO(), testEnv.Client, clusterProfileOwner,
			configv1alpha1.FeatureResources, nil, clusterSummary, textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))))).To(Succeed())

		Eventually(func() bool {
			err := testEnv.Client.List(context.TODO(), reloaders)
			return err == nil && len(reloaders.Items) == 0
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("convertResourceReportsToObjectReference converts ResourceReports to ObjectReference", func() {
		resourceReports := []configv1alpha1.ResourceReport{
			{
				Resource: configv1alpha1.Resource{
					Kind:      "StatefulSet",
					Name:      randomString(),
					Namespace: randomString(),
				},
			},
			{
				Resource: configv1alpha1.Resource{
					Kind:      "DaemonSet",
					Name:      randomString(),
					Namespace: randomString(),
				},
			},
			{
				Resource: configv1alpha1.Resource{
					Kind:      "Deployment",
					Name:      randomString(),
					Namespace: randomString(),
				},
			},
		}

		resources := controllers.ConvertResourceReportsToObjectReference(resourceReports)
		Expect(len(resources)).To(Equal(len(resourceReports)))

		for i := range resourceReports {
			Expect(resources).To(ContainElement(corev1.ObjectReference{
				Kind:      resourceReports[i].Resource.Kind,
				Namespace: resourceReports[i].Resource.Namespace,
				Name:      resourceReports[i].Resource.Name,
			}))
		}
	})

	It("convertHelmResourcesToObjectReference converts HelmResources to ObjectReference", func() {
		resourceReports := []libsveltosv1alpha1.HelmResources{
			{
				Resources: []libsveltosv1alpha1.Resource{
					{
						Kind:      "StatefulSet",
						Name:      randomString(),
						Namespace: randomString(),
					},
					{
						Kind:      "StatefulSet",
						Name:      randomString(),
						Namespace: randomString(),
					},
				},
			},
			{
				Resources: []libsveltosv1alpha1.Resource{
					{
						Kind:      "Deployment",
						Name:      randomString(),
						Namespace: randomString(),
					},
					{
						Kind:      "DaemonSet",
						Name:      randomString(),
						Namespace: randomString(),
					},
				},
			},
		}

		resources := controllers.ConvertHelmResourcesToObjectReference(resourceReports)

		for i := range resourceReports {
			for j := range resourceReports[i].Resources {
				Expect(resources).To(ContainElement(corev1.ObjectReference{
					Kind:      resourceReports[i].Resources[j].Kind,
					Namespace: resourceReports[i].Resources[j].Namespace,
					Name:      resourceReports[i].Resources[j].Name,
				}))
			}
		}
	})
})
