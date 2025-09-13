/*
Copyright 2025. projectsveltos.io. All rights reserved.

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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint:staticcheck // SA1019: We are unable to update the dependency at this time.
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Drift Detection Upgrade", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
	})

	It("getListOfClustersWithDriftDetection returns the list of clusters with drift detection enabled", func() {
		cluster1 := corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}
		clusterSummary1 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster1.Namespace,
				ClusterName:      cluster1.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeSveltos,
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode: configv1beta1.SyncModeContinuousWithDriftDetection,
				},
			},
		}

		cluster2 := corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       clusterv1.ClusterKind,
			APIVersion: clusterv1.GroupVersion.String(),
		}
		clusterSummary2 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster2.Namespace,
				ClusterName:      cluster2.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode: configv1beta1.SyncModeContinuousWithDriftDetection,
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary1,
			clusterSummary2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusters, err := controllers.GetListOfClustersWithDriftDetection(context.TODO(), c, logger)
		Expect(err).To(BeNil())
		Expect(len(clusters)).To(Equal(2))
		Expect(clusters).To(ContainElement(cluster1))
		Expect(clusters).To(ContainElement(cluster2))

		// This is continuous, so this cluster wont be included
		clusterSummary3 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: randomString(),
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode: configv1beta1.SyncModeContinuous,
				},
			},
		}

		Expect(c.Create(context.TODO(), clusterSummary3)).To(Succeed())
		clusters, err = controllers.GetListOfClustersWithDriftDetection(context.TODO(), c, logger)
		Expect(err).To(BeNil())
		Expect(len(clusters)).To(Equal(2))
	})
})
