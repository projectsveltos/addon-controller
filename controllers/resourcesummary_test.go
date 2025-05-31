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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("ResourceSummary Deployer", func() {
	It("deployDebuggingConfigurationCRD deploys DebuggingConfiguration CRD", func() {
		Expect(controllers.DeployDebuggingConfigurationCRD(context.TODO(), testEnv.Config,
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			classifierCRD := &apiextensionsv1.CustomResourceDefinition{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: "debuggingconfigurations.lib.projectsveltos.io"}, classifierCRD)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deployResourceSummaryCRD deploys ResourceSummary CRD", func() {
		Expect(controllers.DeployResourceSummaryCRD(context.TODO(), testEnv.Config,
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			classifierCRD := &apiextensionsv1.CustomResourceDefinition{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: "resourcesummaries.lib.projectsveltos.io"}, classifierCRD)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deployResourceSummaryInstance updates ResourceSummary instance", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		resources := []libsveltosv1beta1.Resource{
			{
				Name:      randomString(),
				Namespace: randomString(),
				Group:     randomString(),
				Kind:      randomString(),
				Version:   randomString(),
			},
		}
		namespace := randomString()
		name := randomString()

		clusterSummaryNamespace := randomString()
		clusterSummaryName := randomString()
		annotations := map[string]string{
			libsveltosv1beta1.ClusterSummaryNameAnnotation:      clusterSummaryName,
			libsveltosv1beta1.ClusterSummaryNamespaceAnnotation: clusterSummaryNamespace,
		}

		Expect(controllers.DeployResourceSummaryInstance(ctx, c, resources, nil, nil,
			namespace, name, nil, annotations, nil, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		currentResourceSummary := &libsveltosv1beta1.ResourceSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			},
			currentResourceSummary)).To(Succeed())

		Expect(currentResourceSummary.Annotations).ToNot(BeNil())
		v, ok := currentResourceSummary.Annotations[libsveltosv1beta1.ClusterSummaryNameAnnotation]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(clusterSummaryName))

		v, ok = currentResourceSummary.Annotations[libsveltosv1beta1.ClusterSummaryNamespaceAnnotation]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(clusterSummaryNamespace))

		Expect(reflect.DeepEqual(currentResourceSummary.Spec.Resources, resources)).To(BeTrue())
	})

	It("deployDriftDetectionManagerInCluster deploys CRDs in cluster", func() {
		cluster := prepareCluster()
		clusterSummaryName := randomString()

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

		// Just verify result is success (testEnv is used to simulate both management and workload cluster and because
		// classifier is expected in the management cluster, above line is required
		Expect(controllers.DeployDriftDetectionManagerInCluster(context.TODO(), testEnv.Client, cluster.Namespace, cluster.Name,
			clusterSummaryName, libsveltosv1beta1.ClusterTypeCapi, false, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			resourceSummaryCRD := &apiextensionsv1.CustomResourceDefinition{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: "resourcesummaries.lib.projectsveltos.io"}, resourceSummaryCRD)
		}, timeout, pollingInterval).Should(BeNil())
	})

	It("deploy/remove DriftDetectionManager resources to/from management cluster", func() {
		clusterNamespace := randomString()
		clusterName := randomString()
		clusterType := libsveltosv1beta1.ClusterTypeSveltos

		Expect(controllers.DeployDriftDetectionManagerInManagementCluster(context.TODO(), testEnv.Config,
			clusterNamespace, clusterName, "", clusterType, nil,
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		expectedLabels := controllers.GetDriftDetectionManagerLabels(clusterNamespace, clusterName, clusterType)

		listOptions := []client.ListOption{
			client.InNamespace(controllers.GetDriftDetectionNamespaceInMgmtCluster()),
		}
		Eventually(func() bool {
			deployments := &appsv1.DeploymentList{}
			err := testEnv.List(context.TODO(), deployments, listOptions...)
			if err != nil {
				return false
			}

			if len(deployments.Items) == 0 {
				return false
			}

			for i := range deployments.Items {
				d := &deployments.Items[i]
				if verifyLabels(d.Labels, expectedLabels) {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.RemoveDriftDetectionManagerFromManagementCluster(context.TODO(), clusterNamespace, clusterName,
			clusterType, textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// Verify resources are gone
		Eventually(func() bool {
			deployments := &appsv1.DeploymentList{}
			err := testEnv.List(context.TODO(), deployments, listOptions...)
			if err != nil {
				return false
			}
			for i := range deployments.Items {
				d := &deployments.Items[i]
				if verifyLabels(d.Labels, expectedLabels) {
					return false
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("transformDriftExclusionsToPatches transforms DriftExclusions to Patches", func() {
		driftExclusions := []configv1beta1.DriftExclusion{
			{
				Paths: []string{"spec/replicas"},
			},
			{
				Paths: []string{"spec/template/spec/containers[*]image"},
				Target: &libsveltosv1beta1.PatchSelector{
					Kind:    "Deployment",
					Group:   "apps",
					Version: "v1",
				},
			},
		}

		patches := controllers.TransformDriftExclusionsToPatches(driftExclusions)
		Expect(len(patches)).To(Equal(len(driftExclusions)))

		expectedPatch := libsveltosv1beta1.Patch{
			Patch: fmt.Sprintf(`- op: remove
  path: %s`, driftExclusions[0].Paths[0]),
		}

		Expect(patches).To(ContainElement(expectedPatch))

		expectedPatch = libsveltosv1beta1.Patch{
			Patch: fmt.Sprintf(`- op: remove
  path: %s`, driftExclusions[1].Paths[0]),
			Target: driftExclusions[1].Target,
		}
		Expect(patches).To(ContainElement(expectedPatch))
	})

	It("transformDriftExclusionsToPatches expands DriftExclusions paths to multiple to Patches", func() {
		driftExclusions := []configv1beta1.DriftExclusion{
			{
				Paths: []string{"spec/replicas", "metadata/labels"},
				Target: &libsveltosv1beta1.PatchSelector{
					Kind:    "Deployment",
					Group:   "apps",
					Version: "v1",
				},
			},
			{
				Paths: []string{"metadata/annotations", "spec.securityContext"},
				Target: &libsveltosv1beta1.PatchSelector{
					Kind:    "Pod",
					Group:   "",
					Version: "v1",
				},
			},
		}

		patches := controllers.TransformDriftExclusionsToPatches(driftExclusions)
		Expect(len(patches)).To(Equal(2 * len(driftExclusions))) // each Paths has two entries

		expectedPatch := libsveltosv1beta1.Patch{
			Patch: fmt.Sprintf(`- op: remove
  path: %s`, driftExclusions[0].Paths[0]),
			Target: driftExclusions[0].Target,
		}

		Expect(patches).To(ContainElement(expectedPatch))

		expectedPatch = libsveltosv1beta1.Patch{
			Patch: fmt.Sprintf(`- op: remove
  path: %s`, driftExclusions[0].Paths[1]),
			Target: driftExclusions[0].Target,
		}

		Expect(patches).To(ContainElement(expectedPatch))

		expectedPatch = libsveltosv1beta1.Patch{
			Patch: fmt.Sprintf(`- op: remove
  path: %s`, driftExclusions[1].Paths[0]),
			Target: driftExclusions[1].Target,
		}
		Expect(patches).To(ContainElement(expectedPatch))

		expectedPatch = libsveltosv1beta1.Patch{
			Patch: fmt.Sprintf(`- op: remove
  path: %s`, driftExclusions[1].Paths[1]),
			Target: driftExclusions[1].Target,
		}
		Expect(patches).To(ContainElement(expectedPatch))
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

	cluster.Status = clusterv1.ClusterStatus{
		InfrastructureReady: true,
		ControlPlaneReady:   true,
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
	Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
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
	err := testEnv.Client.Create(context.TODO(), cm)
	if err != nil {
		Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
	}
	Expect(waitForObject(context.TODO(), testEnv.Client, cm)).To(Succeed())

	Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

	return cluster
}

// verifyLabels verifies that all labels in expectedLabels are also present
// in currentLabels with same value
func verifyLabels(currentLabels, expectedLabels map[string]string) bool {
	if currentLabels == nil {
		return false
	}

	for k := range expectedLabels {
		v, ok := currentLabels[k]
		if !ok {
			return false
		}
		if v != expectedLabels[k] {
			return false
		}
	}

	return true
}
