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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Drift Detection Upgrade", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
	})

	It("skipUpgrading skips clusters which are paused or not ready", func() {
		paused := true
		sveltosClusterPaused := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				Paused: paused,
			},
		}
		Expect(addTypeInformationToObject(scheme, sveltosClusterPaused)).To(Succeed())

		sveltosClusterNotReady := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				Paused: false,
			},
			Status: libsveltosv1beta1.SveltosClusterStatus{
				Ready: false,
			},
		}
		Expect(addTypeInformationToObject(scheme, sveltosClusterNotReady)).To(Succeed())

		sveltosClusterReadyAndNotPaused := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				Paused: false,
			},
			Status: libsveltosv1beta1.SveltosClusterStatus{
				Ready: true,
			},
		}
		Expect(addTypeInformationToObject(scheme, sveltosClusterReadyAndNotPaused)).To(Succeed())

		capiClusterPaused := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: clusterv1.ClusterSpec{
				Paused: &paused,
			},
		}
		Expect(addTypeInformationToObject(scheme, capiClusterPaused)).To(Succeed())

		initialized := true
		capiClusterNotPaused := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
			Spec: clusterv1.ClusterSpec{},
			Status: clusterv1.ClusterStatus{
				Initialization: clusterv1.ClusterInitializationStatus{
					ControlPlaneInitialized: &initialized,
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, capiClusterNotPaused)).To(Succeed())

		initObjects := []client.Object{
			sveltosClusterPaused,
			sveltosClusterNotReady,
			sveltosClusterReadyAndNotPaused,
			capiClusterPaused,
			capiClusterNotPaused,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		skip, err := controllers.SkipUpgrading(context.TODO(), c, sveltosClusterPaused, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeTrue())

		skip, err = controllers.SkipUpgrading(context.TODO(), c, sveltosClusterNotReady, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeTrue())

		skip, err = controllers.SkipUpgrading(context.TODO(), c, sveltosClusterReadyAndNotPaused, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeFalse())

		skip, err = controllers.SkipUpgrading(context.TODO(), c, capiClusterPaused, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeTrue())

		skip, err = controllers.SkipUpgrading(context.TODO(), c, capiClusterNotPaused, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeFalse())
	})
})
