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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

var _ = Describe("ClusterFeatureReconciler map functions", func() {
	var namespace string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = "map-function" + util.RandomString(5)
	})

	It("requeueClusterFeatureForCluster returns matching ClusterFeatures", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + util.RandomString(5),
				Namespace: namespace,
				Labels: map[string]string{
					"env": "production",
				},
			},
		}

		matchingClusterFeature := &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: configv1alpha1.Selector("env=production"),
			},
		}

		nonMatchingClusterFeature := &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
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
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		requests := controllers.RequeueClusterFeatureForCluster(reconciler, cluster)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal(matchingClusterFeature.Name))

		nonMatchingClusterFeature.Spec.ClusterSelector = matchingClusterFeature.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterFeature)).To(Succeed())
		requests = controllers.RequeueClusterFeatureForCluster(reconciler, cluster)
		Expect(requests).To(HaveLen(2))

		matchingClusterFeature.Spec.ClusterSelector = configv1alpha1.Selector("env=qa")
		Expect(c.Update(context.TODO(), matchingClusterFeature)).To(Succeed())
		nonMatchingClusterFeature.Spec.ClusterSelector = matchingClusterFeature.Spec.ClusterSelector
		Expect(c.Update(context.TODO(), nonMatchingClusterFeature)).To(Succeed())
		requests = controllers.RequeueClusterFeatureForCluster(reconciler, cluster)
		Expect(requests).To(HaveLen(0))
	})
})
