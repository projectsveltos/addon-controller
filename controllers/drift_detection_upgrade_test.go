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
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Drift Detection Upgrade", func() {
	sveltosKubeconfigPostfix := "-sveltos" + kubeconfigPostfix
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(0)))
	})

	It("skipUpgrading skips clusters which are paused or not ready", func() {
		paused := true
		namespace := randomString()
		sveltosClusterPaused := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				Paused: paused,
			},
		}

		sveltosClusterNotReady := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				Paused: false,
			},
		}

		sveltosClusterReadyAndNotPaused := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: libsveltosv1beta1.SveltosClusterSpec{
				Paused: false,
			},
		}

		capiClusterPaused := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: clusterv1.ClusterSpec{
				Paused: &paused,
			},
		}

		initialized := true
		capiClusterNotPaused := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: namespace,
			},
			Spec: clusterv1.ClusterSpec{},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), sveltosClusterReadyAndNotPaused)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, sveltosClusterReadyAndNotPaused)).To(Succeed())

		sveltosClusterReadyAndNotPaused.Status = libsveltosv1beta1.SveltosClusterStatus{
			Ready: true,
		}
		Expect(testEnv.Status().Update(context.TODO(), sveltosClusterReadyAndNotPaused)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), capiClusterNotPaused)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, capiClusterNotPaused)).To(Succeed())

		capiClusterNotPaused.Status = clusterv1.ClusterStatus{
			Initialization: clusterv1.ClusterInitializationStatus{
				ControlPlaneInitialized: &initialized,
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), capiClusterNotPaused)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), sveltosClusterPaused)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, sveltosClusterPaused)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), sveltosClusterNotReady)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, sveltosClusterNotReady)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), capiClusterPaused)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, capiClusterPaused)).To(Succeed())

		Expect(addTypeInformationToObject(scheme, sveltosClusterPaused)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, sveltosClusterNotReady)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, sveltosClusterReadyAndNotPaused)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, capiClusterPaused)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, capiClusterNotPaused)).To(Succeed())

		By("Create the secrets with cluster kubeconfig")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      sveltosClusterPaused.Name + sveltosKubeconfigPostfix,
			},
			Data: map[string][]byte{
				"value": testEnv.Kubeconfig,
			},
		}
		Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secret)).To(Succeed())

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      sveltosClusterNotReady.Name + sveltosKubeconfigPostfix,
			},
			Data: map[string][]byte{
				"value": testEnv.Kubeconfig,
			},
		}
		Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secret)).To(Succeed())

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      sveltosClusterReadyAndNotPaused.Name + sveltosKubeconfigPostfix,
			},
			Data: map[string][]byte{
				"value": testEnv.Kubeconfig,
			},
		}
		Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secret)).To(Succeed())

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      capiClusterPaused.Name + kubeconfigPostfix,
			},
			Data: map[string][]byte{
				"value": testEnv.Kubeconfig,
			},
		}
		Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secret)).To(Succeed())

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      capiClusterNotPaused.Name + kubeconfigPostfix,
			},
			Data: map[string][]byte{
				"value": testEnv.Kubeconfig,
			},
		}
		Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv, secret)).To(Succeed())

		skip, err := controllers.SkipUpgrading(context.TODO(), testEnv, sveltosClusterPaused, nil, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeTrue())

		skip, err = controllers.SkipUpgrading(context.TODO(), testEnv, sveltosClusterNotReady, nil, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeTrue())

		skip, err = controllers.SkipUpgrading(context.TODO(), testEnv, sveltosClusterReadyAndNotPaused, nil, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeFalse())

		skip, err = controllers.SkipUpgrading(context.TODO(), testEnv, capiClusterPaused, nil, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeTrue())

		skip, err = controllers.SkipUpgrading(context.TODO(), testEnv, capiClusterNotPaused, nil, logger)
		Expect(err).To(BeNil())
		Expect(skip).To(BeFalse())
	})
})
