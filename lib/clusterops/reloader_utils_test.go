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

package clusterops_test

import (
	"context"
	"fmt"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltoscrd "github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

const (
	kubeconfigPostfix        = "-kubeconfig"
	resourceSummaryNamespace = "projectsveltos"
	version                  = "v0.31.0"
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
			Expect(clusterops.WatchForRollingUpgrade(testData[i].resource)).To(
				Equal(testData[i].result), fmt.Sprintf("resource %s", testData[i].resource.Kind))
		}
	})

	It("createReloaderInstance creates reloader instance", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		reloaderInfo := []libsveltosv1beta1.ReloaderInfo{
			{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
			{Kind: "Deployment", Namespace: randomString(), Name: randomString()},
		}

		clusterProfileName := randomString()
		feature := libsveltosv1beta1.FeatureHelm
		Expect(clusterops.CreateReloaderInstance(context.TODO(), c,
			clusterProfileName, feature, reloaderInfo)).To(Succeed())

		reloaders := &libsveltosv1beta1.ReloaderList{}
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

		feature := libsveltosv1beta1.FeatureHelm
		Expect(clusterops.DeployReloaderInstance(context.TODO(), c, clusterProfileName,
			feature, resources, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		reloaders := &libsveltosv1beta1.ReloaderList{}
		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(1))
		Expect(len(reloaders.Items[0].Labels)).ToNot(BeNil())
		Expect(len(reloaders.Items[0].Annotations)).ToNot(BeNil())

		Expect(len(reloaders.Items[0].Spec.ReloaderInfo)).To(Equal(len(resources)))
		for i := range resources {
			Expect(reloaders.Items[0].Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1beta1.ReloaderInfo{
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
		Expect(clusterops.DeployReloaderInstance(context.TODO(), c, clusterProfileName,
			feature, resources, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(1))
		Expect(len(reloaders.Items[0].Labels)).ToNot(BeNil())
		Expect(len(reloaders.Items[0].Annotations)).ToNot(BeNil())

		Expect(len(reloaders.Items[0].Spec.ReloaderInfo)).To(Equal(len(resources)))
		for i := range resources {
			Expect(reloaders.Items[0].Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1beta1.ReloaderInfo{
					Kind:      resources[i].Kind,
					Namespace: resources[i].Namespace,
					Name:      resources[i].Name,
				}))
		}
	})

	It("removeReloaderInstance returns no error when Reloader instance does not exist", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		Expect(clusterops.RemoveReloaderInstance(context.TODO(), c, randomString(),
			libsveltosv1beta1.FeatureKustomize, textlogger.NewLogger(textlogger.NewConfig()))).To(BeNil())
	})

	It("removeReloaderInstance removes Reloader instance", func() {
		clusterProfileName := randomString()
		feature := libsveltosv1beta1.FeatureKustomize

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		var reloaderCRD *unstructured.Unstructured
		reloaderCRD, err := k8s_utils.GetUnstructured(libsveltoscrd.GetReloaderCRDYAML())
		Expect(err).To(BeNil())
		Expect(c.Create(context.TODO(), reloaderCRD)).To(Succeed())

		Expect(clusterops.CreateReloaderInstance(context.TODO(), c,
			clusterProfileName, feature, nil)).To(Succeed())
		reloaders := &libsveltosv1beta1.ReloaderList{}
		Expect(c.List(context.TODO(), reloaders)).To(Succeed())
		Expect(len(reloaders.Items)).To(Equal(1))

		Expect(clusterops.RemoveReloaderInstance(context.TODO(), c, clusterProfileName,
			feature, textlogger.NewLogger(textlogger.NewConfig()))).To(BeNil())

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
		prepareCluster()

		clusterProfileOwner := &corev1.ObjectReference{
			Kind:       configv1beta1.ClusterProfileKind,
			APIVersion: configv1beta1.GroupVersion.String(),
			Name:       randomString(),
			UID:        types.UID(randomString()),
		}

		Expect(clusterops.UpdateReloaderWithDeployedResources(context.TODO(), testEnv.Client, clusterProfileOwner,
			libsveltosv1beta1.FeatureResources, resources, false, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		reloaders := &libsveltosv1beta1.ReloaderList{}

		Eventually(func() bool {
			err := testEnv.List(context.TODO(), reloaders)
			return err == nil && len(reloaders.Items) == 1
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.List(context.TODO(), reloaders)).To(Succeed())

		for i := range resources {
			Expect(reloaders.Items[0].Spec.ReloaderInfo).To(ContainElement(
				libsveltosv1beta1.ReloaderInfo{
					Kind:      resources[i].Kind,
					Namespace: resources[i].Namespace,
					Name:      resources[i].Name,
				},
			))
		}

		Expect(clusterops.UpdateReloaderWithDeployedResources(context.TODO(), testEnv.Client, clusterProfileOwner,
			libsveltosv1beta1.FeatureResources, nil, true, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		Eventually(func() bool {
			err := testEnv.List(context.TODO(), reloaders)
			return err == nil && len(reloaders.Items) == 0
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("convertResourceReportsToObjectReference converts ResourceReports to ObjectReference", func() {
		resourceReports := []libsveltosv1beta1.ResourceReport{
			{
				Resource: libsveltosv1beta1.Resource{
					Kind:      "StatefulSet",
					Name:      randomString(),
					Namespace: randomString(),
				},
			},
			{
				Resource: libsveltosv1beta1.Resource{
					Kind:      "DaemonSet",
					Name:      randomString(),
					Namespace: randomString(),
				},
			},
			{
				Resource: libsveltosv1beta1.Resource{
					Kind:      "Deployment",
					Name:      randomString(),
					Namespace: randomString(),
				},
			},
		}

		resources := clusterops.ConvertResourceReportsToObjectReference(resourceReports)
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
		resourceReports := []libsveltosv1beta1.HelmResources{
			{
				Resources: []libsveltosv1beta1.ResourceSummaryResource{
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
				Resources: []libsveltosv1beta1.ResourceSummaryResource{
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

		resources := clusterops.ConvertHelmResourcesToObjectReference(resourceReports)

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

func prepareCluster() *clusterv1.Cluster {
	namespace := randomString()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
		},
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      randomString(),
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:         cluster.Name,
				clusterv1.MachineControlPlaneLabel: "ok",
			},
		},
	}

	Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
	Expect(testEnv.Create(context.TODO(), machine)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

	initialized := true
	cluster.Status = clusterv1.ClusterStatus{
		ControlPlaneReady: initialized,
	}
	Expect(testEnv.Status().Update(context.TODO(), cluster)).To(Succeed())

	machine.Status = clusterv1.MachineStatus{
		Phase: string(clusterv1.MachinePhaseRunning),
	}
	Expect(testEnv.Status().Update(context.TODO(), machine)).To(Succeed())

	// Create a secret with cluster kubeconfig

	By("Create the secret with cluster kubeconfig")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name + kubeconfigPostfix,
		},
		Data: map[string][]byte{
			"value": testEnv.Kubeconfig,
		},
	}
	Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

	By("Create the ConfigMap with drift-detection version")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: resourceSummaryNamespace,
			Name:      "drift-detection-version",
		},
		Data: map[string]string{
			"version": version,
		},
	}
	err := testEnv.Create(context.TODO(), cm)
	if err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}
	Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())

	Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

	return cluster
}
