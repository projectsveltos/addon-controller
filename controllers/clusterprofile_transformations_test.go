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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/controllers"
)

var _ = Describe("ClusterProfileReconciler map functions", func() {
	var namespace string

	BeforeEach(func() {
		namespace = "map-function" + randomString()
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

		matchingClusterProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: configv1alpha1.Selector("env=production"),
			},
		}

		nonMatchingClusterProfile := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: configv1alpha1.Selector("env=qa"),
			},
		}

		initObjects := []client.Object{
			matchingClusterProfile,
			nonMatchingClusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[configv1alpha1.PolicyRef]*controllers.Set),
			ClusterProfileMap: make(map[configv1alpha1.PolicyRef]*controllers.Set),
			ClusterProfiles:   make(map[configv1alpha1.PolicyRef]configv1alpha1.Selector),
			Mux:               sync.Mutex{},
		}

		By("Setting ClusterProfileReconciler internal structures")
		matchingInfo := configv1alpha1.PolicyRef{Kind: configv1alpha1.ClusterProfileKind, Name: matchingClusterProfile.Name}
		reconciler.ClusterProfiles[matchingInfo] = matchingClusterProfile.Spec.ClusterSelector
		nonMatchingInfo := configv1alpha1.PolicyRef{Kind: configv1alpha1.ClusterProfileKind, Name: nonMatchingClusterProfile.Name}
		reconciler.ClusterProfiles[nonMatchingInfo] = nonMatchingClusterProfile.Spec.ClusterSelector

		// ClusterMap contains, per ClusterName, list of ClusterProfiles matching it.
		clusterProfileSet := &controllers.Set{}
		controllers.Insert(clusterProfileSet, &matchingInfo)
		clusterInfo := configv1alpha1.PolicyRef{Kind: "Cluster", Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = clusterProfileSet

		// ClusterProfileMap contains, per ClusterProfile, list of matched Clusters.
		clusterSet1 := &controllers.Set{}
		reconciler.ClusterProfileMap[nonMatchingInfo] = clusterSet1

		clusterSet2 := &controllers.Set{}
		controllers.Insert(clusterSet2, &clusterInfo)
		reconciler.ClusterProfileMap[matchingInfo] = clusterSet2

		By("Expect only matchingClusterProfile to be requeued")
		requests := controllers.RequeueClusterProfileForCluster(reconciler, cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterProfile.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterProfile ClusterSelector again to have two ClusterProfiles match")
		nonMatchingClusterProfile.Spec.ClusterSelector = matchingClusterProfile.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterProfile)).To(Succeed())

		reconciler.ClusterProfiles[nonMatchingInfo] = nonMatchingClusterProfile.Spec.ClusterSelector

		controllers.Insert(clusterSet1, &clusterInfo)
		reconciler.ClusterProfileMap[nonMatchingInfo] = clusterSet1

		controllers.Insert(clusterProfileSet, &nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = clusterProfileSet

		requests = controllers.RequeueClusterProfileForCluster(reconciler, cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterProfile.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: nonMatchingClusterProfile.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterProfile ClusterSelector again to have no ClusterProfile match")
		matchingClusterProfile.Spec.ClusterSelector = configv1alpha1.Selector("env=qa")
		Expect(c.Update(context.TODO(), matchingClusterProfile)).To(Succeed())
		nonMatchingClusterProfile.Spec.ClusterSelector = matchingClusterProfile.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterProfile)).To(Succeed())

		emptySet := &controllers.Set{}
		reconciler.ClusterProfileMap[matchingInfo] = emptySet
		reconciler.ClusterProfileMap[nonMatchingInfo] = emptySet
		reconciler.ClusterMap[clusterInfo] = emptySet

		reconciler.ClusterProfiles[matchingInfo] = matchingClusterProfile.Spec.ClusterSelector
		reconciler.ClusterProfiles[nonMatchingInfo] = nonMatchingClusterProfile.Spec.ClusterSelector

		requests = controllers.RequeueClusterProfileForCluster(reconciler, cluster)
		Expect(requests).To(HaveLen(0))
	})
})
