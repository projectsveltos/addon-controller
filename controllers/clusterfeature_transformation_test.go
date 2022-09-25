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

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

var _ = Describe("ClusterFeatureReconciler map functions", func() {
	var namespace string

	BeforeEach(func() {
		namespace = "map-function" + randomString()
	})

	It("requeueClusterFeatureForCluster returns matching ClusterFeatures", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		matchingClusterFeature := &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: configv1alpha1.Selector("env=production"),
			},
		}

		nonMatchingClusterFeature := &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: configv1alpha1.Selector("env=qa"),
			},
		}

		initObjects := []client.Object{
			matchingClusterFeature,
			nonMatchingClusterFeature,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[configv1alpha1.PolicyRef]*controllers.Set),
			ClusterFeatureMap: make(map[configv1alpha1.PolicyRef]*controllers.Set),
			ClusterFeatures:   make(map[configv1alpha1.PolicyRef]configv1alpha1.Selector),
			Mux:               sync.Mutex{},
		}

		By("Setting ClusterFeatureReconciler internal structures")
		matchingInfo := configv1alpha1.PolicyRef{Kind: configv1alpha1.ClusterFeatureKind, Name: matchingClusterFeature.Name}
		reconciler.ClusterFeatures[matchingInfo] = matchingClusterFeature.Spec.ClusterSelector
		nonMatchingInfo := configv1alpha1.PolicyRef{Kind: configv1alpha1.ClusterFeatureKind, Name: nonMatchingClusterFeature.Name}
		reconciler.ClusterFeatures[nonMatchingInfo] = nonMatchingClusterFeature.Spec.ClusterSelector

		// ClusterMap contains, per ClusterName, list of ClusterFeatures matching it.
		clusterFeatureSet := &controllers.Set{}
		controllers.Insert(clusterFeatureSet, &matchingInfo)
		clusterInfo := configv1alpha1.PolicyRef{Kind: "Cluster", Namespace: cluster.Namespace, Name: cluster.Name}
		reconciler.ClusterMap[clusterInfo] = clusterFeatureSet

		// ClusterFeatureMap contains, per ClusterFeature, list of matched Clusters.
		clusterSet1 := &controllers.Set{}
		reconciler.ClusterFeatureMap[nonMatchingInfo] = clusterSet1

		clusterSet2 := &controllers.Set{}
		controllers.Insert(clusterSet2, &clusterInfo)
		reconciler.ClusterFeatureMap[matchingInfo] = clusterSet2

		By("Expect only matchingClusterFeature to be requeued")
		requests := controllers.RequeueClusterFeatureForCluster(reconciler, cluster)
		expected := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterFeature.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterFeature ClusterSelector again to have two ClusterFeatures match")
		nonMatchingClusterFeature.Spec.ClusterSelector = matchingClusterFeature.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterFeature)).To(Succeed())

		reconciler.ClusterFeatures[nonMatchingInfo] = nonMatchingClusterFeature.Spec.ClusterSelector

		controllers.Insert(clusterSet1, &clusterInfo)
		reconciler.ClusterFeatureMap[nonMatchingInfo] = clusterSet1

		controllers.Insert(clusterFeatureSet, &nonMatchingInfo)
		reconciler.ClusterMap[clusterInfo] = clusterFeatureSet

		requests = controllers.RequeueClusterFeatureForCluster(reconciler, cluster)
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterFeature.Name}}
		Expect(requests).To(ContainElement(expected))
		expected = reconcile.Request{NamespacedName: types.NamespacedName{Name: nonMatchingClusterFeature.Name}}
		Expect(requests).To(ContainElement(expected))

		By("Changing clusterFeature ClusterSelector again to have no ClusterFeature match")
		matchingClusterFeature.Spec.ClusterSelector = configv1alpha1.Selector("env=qa")
		Expect(c.Update(context.TODO(), matchingClusterFeature)).To(Succeed())
		nonMatchingClusterFeature.Spec.ClusterSelector = matchingClusterFeature.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterFeature)).To(Succeed())

		emptySet := &controllers.Set{}
		reconciler.ClusterFeatureMap[matchingInfo] = emptySet
		reconciler.ClusterFeatureMap[nonMatchingInfo] = emptySet
		reconciler.ClusterMap[clusterInfo] = emptySet

		reconciler.ClusterFeatures[matchingInfo] = matchingClusterFeature.Spec.ClusterSelector
		reconciler.ClusterFeatures[nonMatchingInfo] = nonMatchingClusterFeature.Spec.ClusterSelector

		requests = controllers.RequeueClusterFeatureForCluster(reconciler, cluster)
		Expect(requests).To(HaveLen(0))
	})
})
