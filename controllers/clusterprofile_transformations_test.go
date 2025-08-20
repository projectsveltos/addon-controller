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
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("ClusterProfileReconciler map functions", func() {
	var namespace string

	BeforeEach(func() {
		namespace = randomString()
	})

	It("requeueClusterProfileForCluster returns matching ClusterProfiles", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		matchingClusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "production",
						},
					},
				},
			},
		}

		nonMatchingClusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "qa",
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			matchingClusterProfile,
			nonMatchingClusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:          c,
			Scheme:          scheme,
			ClusterMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels:   make(map[corev1.ObjectReference]map[string]string),
			Mux:             sync.Mutex{},
		}

		By("Setting ClusterProfileReconciler internal structures")
		matchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: configv1beta1.ClusterProfileKind, Name: matchingClusterProfile.Name}
		reconciler.ClusterProfiles[matchingInfo] = matchingClusterProfile.Spec.ClusterSelector
		nonMatchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: configv1beta1.ClusterProfileKind, Name: nonMatchingClusterProfile.Name}
		reconciler.ClusterProfiles[nonMatchingInfo] = nonMatchingClusterProfile.Spec.ClusterSelector

		// ClusterMap contains, per ClusterName, list of ClusterProfiles matching it.
		clusterProfileSet := &libsveltosset.Set{}
		clusterProfileSet.Insert(&matchingInfo)
		clusterInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion, Kind: cluster.Kind, Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = clusterProfileSet

		By("Expect only matchingClusterProfile to be requeued")
		requests := controllers.RequeueClusterProfileForCluster(reconciler, context.TODO(), cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterProfile.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterProfile ClusterSelector again to have two ClusterProfiles match")
		nonMatchingClusterProfile.Spec.ClusterSelector = matchingClusterProfile.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterProfile)).To(Succeed())

		reconciler.ClusterProfiles[nonMatchingInfo] = nonMatchingClusterProfile.Spec.ClusterSelector

		clusterProfileSet.Insert(&nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = clusterProfileSet

		requests = controllers.RequeueClusterProfileForCluster(reconciler, context.TODO(), cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterProfile.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: nonMatchingClusterProfile.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterProfile ClusterSelector again to have no ClusterProfile match")
		matchingClusterProfile.Spec.ClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"env": "qa",
				},
			},
		}
		Expect(c.Update(context.TODO(), matchingClusterProfile)).To(Succeed())
		nonMatchingClusterProfile.Spec.ClusterSelector = matchingClusterProfile.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterProfile)).To(Succeed())

		emptySet := &libsveltosset.Set{}
		reconciler.ClusterMap[clusterInfo] = emptySet

		reconciler.ClusterProfiles[matchingInfo] = matchingClusterProfile.Spec.ClusterSelector
		reconciler.ClusterProfiles[nonMatchingInfo] = nonMatchingClusterProfile.Spec.ClusterSelector

		requests = controllers.RequeueClusterProfileForCluster(reconciler, context.TODO(), cluster)
		Expect(requests).To(HaveLen(0))
	})

	It("RequeueClusterProfileForMachine returns correct ClusterProfiles for a CAPI machine", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + randomString(),
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         cluster.Name,
					clusterv1.MachineControlPlaneLabel: "ok",
				},
			},
		}

		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"env": "production",
						},
					},
				},
			},
		}

		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cpMachine)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, clusterProfile)).To(Succeed())

		// In this scenario:
		// - ClusterProfile added first
		// - Cluster matching ClusterProfile added later
		// - First controlplane Machine in Cluster is ready
		// The only information Sveltos has are:
		// - Cluster's labels (stored in ClusterLabels map)
		// - ClusterProfile's selector (stored in ClusterProfiles maps)
		// RequeueClusterProfileForMachine gets cluster from machine and using ClusterLabels
		// and ClusterProfiles maps finds the ClusterProfiles that need to be reconciled

		apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		clusterProfileReconciler := getClusterProfileReconciler(testEnv.Client)

		clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
			Namespace: cluster.GetNamespace(), Name: cluster.GetName()}
		clusterProfileReconciler.ClusterLabels[clusterInfo] = cluster.Labels

		apiVersion, kind = clusterProfile.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		clusterProfileInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Name: clusterProfile.GetName()}
		clusterProfileReconciler.ClusterProfiles[clusterProfileInfo] = clusterProfile.Spec.ClusterSelector

		clusterProfileList := controllers.RequeueClusterProfileForMachine(clusterProfileReconciler,
			context.TODO(), cpMachine)
		Expect(len(clusterProfileList)).To(Equal(1))
	})
})
