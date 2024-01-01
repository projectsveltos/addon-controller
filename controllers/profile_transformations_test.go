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
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("Profile Transformations", func() {
	var namespace string

	BeforeEach(func() {
		namespace = randomString()
	})

	It("requeueProfileForCluster returns matching Profiles", func() {
		key := randomString()
		value := randomString()

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					key: value,
				},
			},
		}

		matchingProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterProfileNamePrefix + randomString(),
				Namespace: cluster.Namespace,
			},
			Spec: configv1alpha1.Spec{
				ClusterSelector: libsveltosv1alpha1.Selector(
					fmt.Sprintf("%s=%s", key, value)),
			},
		}

		nonMatchingProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.Spec{
				ClusterSelector: libsveltosv1alpha1.Selector(
					fmt.Sprintf("%s=%s", randomString(), value)),
			},
		}

		initObjects := []client.Object{
			matchingProfile,
			nonMatchingProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ProfileReconciler{
			Client:        c,
			Scheme:        scheme,
			ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			ProfileMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		By("Setting ProfileReconciler internal structures")
		matchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: configv1alpha1.ClusterProfileKind, Name: matchingProfile.Name}
		reconciler.Profiles[matchingInfo] = matchingProfile.Spec.ClusterSelector

		nonMatchingInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: configv1alpha1.ClusterProfileKind, Name: nonMatchingProfile.Name}
		reconciler.Profiles[nonMatchingInfo] = nonMatchingProfile.Spec.ClusterSelector

		// ClusterMap contains, per ClusterName, list of ClusterProfiles matching it.
		clusterProfileSet := &libsveltosset.Set{}
		clusterProfileSet.Insert(&matchingInfo)
		clusterInfo := corev1.ObjectReference{APIVersion: cluster.APIVersion,
			Kind: cluster.Kind, Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = clusterProfileSet

		// ProfileMap contains, per Profile, list of matched Clusters.
		clusterSet1 := &libsveltosset.Set{}
		reconciler.ProfileMap[nonMatchingInfo] = clusterSet1

		clusterSet2 := &libsveltosset.Set{}
		clusterSet2.Insert(&clusterInfo)
		reconciler.ProfileMap[matchingInfo] = clusterSet2

		By("Expect only matchingProfile to be requeued")
		requests := controllers.RequeueProfileForCluster(reconciler, context.TODO(), cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing Profile ClusterSelector again to have two ClusterProfiles match")
		nonMatchingProfile.Spec.ClusterSelector = matchingProfile.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingProfile)).To(Succeed())

		reconciler.Profiles[nonMatchingInfo] = nonMatchingProfile.Spec.ClusterSelector

		clusterSet1.Insert(&clusterInfo)
		reconciler.ProfileMap[nonMatchingInfo] = clusterSet1

		clusterProfileSet.Insert(&nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = clusterProfileSet

		requests = controllers.RequeueProfileForCluster(reconciler, context.TODO(), cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: nonMatchingProfile.Name}}
		Expect(requests).To(ContainElement(expected))
	})

	It("RequeueProfileForMachine returns correct Profiles for a CAPI machine", func() {
		key := randomString()
		value := randomString()
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					key: value,
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

		profile := &configv1alpha1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.Spec{
				ClusterSelector: libsveltosv1alpha1.Selector(
					fmt.Sprintf("%s=%s", key, value)),
			},
		}

		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cpMachine)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, profile)).To(Succeed())

		// In this scenario:
		// - profile added first
		// - Cluster matching Profile added later
		// - First controlplane Machine in Cluster is ready
		// The only information Sveltos has are:
		// - Cluster's labels (stored in ClusterLabels map)
		// - Profile's selector (stored in Profiles maps)
		// RequeueProfileForMachine gets cluster from machine and using ClusterLabels
		// and Profiles maps finds the Profiles that need to be reconciled

		apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		profileReconciler := getProfileReconciler(testEnv.Client)

		clusterInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind,
			Namespace: cluster.GetNamespace(), Name: cluster.GetName()}
		profileReconciler.ClusterLabels[clusterInfo] = cluster.Labels

		apiVersion, kind = profile.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
		profileInfo := corev1.ObjectReference{APIVersion: apiVersion, Kind: kind, Name: profile.GetName()}
		profileReconciler.Profiles[profileInfo] = profile.Spec.ClusterSelector

		clusterProfileList := controllers.RequeueProfileForMachine(profileReconciler,
			context.TODO(), cpMachine)
		Expect(len(clusterProfileList)).To(Equal(1))
	})
})
