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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("Profile: Reconciler", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())

		clusterProfile = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{randomString(): randomString()},
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterProfile)).To(Succeed())
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:          c,
			Scheme:          scheme,
			ClusterMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels:   make(map[corev1.ObjectReference]map[string]string),
			Mux:             sync.Mutex{},
		}

		clusterProfileName := client.ObjectKey{
			Name: clusterProfile.Name,
		}

		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterProfileName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err = c.Get(context.TODO(), clusterProfileName, currentClusterProfile)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentClusterProfile,
				configv1beta1.ClusterProfileFinalizer,
			),
		).Should(BeTrue())
	})

	It("getClustersFromClusterSets gets cluster selected by referenced clusterSet", func() {
		clusterSet1 := &libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		clusterSet2 := &libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		clusterProfile.Spec.SetRefs = []string{
			clusterSet1.Name,
			clusterSet2.Name,
		}

		initObjects := []client.Object{
			clusterSet1,
			clusterSet2,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:          c,
			Scheme:          scheme,
			ClusterMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels:   make(map[corev1.ObjectReference]map[string]string),
			Mux:             sync.Mutex{},
		}

		clusters, err := controllers.GetClustersFromClusterSets(reconciler, context.TODO(),
			clusterProfile.Spec.SetRefs, logger)
		Expect(err).To(BeNil())
		Expect(clusters).ToNot(BeNil())
		for i := range clusterSet1.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  clusterSet1.Status.SelectedClusterRefs[i].Namespace,
				Name:       clusterSet1.Status.SelectedClusterRefs[i].Name,
				Kind:       clusterSet1.Status.SelectedClusterRefs[i].Kind,
				APIVersion: clusterSet1.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
		for i := range clusterSet2.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  clusterSet2.Status.SelectedClusterRefs[i].Namespace,
				Name:       clusterSet2.Status.SelectedClusterRefs[i].Name,
				Kind:       clusterSet2.Status.SelectedClusterRefs[i].Kind,
				APIVersion: clusterSet2.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
	})

	It("Reconciliation of deleted ClusterProfile removes finalizer only when all ClusterSummaries are gone", func() {
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:   clusterProfileNamePrefix + randomString(),
				Labels: map[string]string{controllers.ClusterProfileLabelName: clusterProfile.Name},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterType: libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		controllerutil.AddFinalizer(clusterSummary, configv1beta1.ClusterSummaryFinalizer)

		now := metav1.NewTime(time.Now())
		clusterProfile.DeletionTimestamp = &now
		controllerutil.AddFinalizer(clusterProfile, configv1beta1.ClusterProfileFinalizer)

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		addOwnerReference(ctx, c, clusterSummary, clusterProfile)

		reconciler := &controllers.ClusterProfileReconciler{
			Client:          c,
			Scheme:          scheme,
			ClusterMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels:   make(map[corev1.ObjectReference]map[string]string),
			Mux:             sync.Mutex{},
		}

		clusterProfileName := client.ObjectKey{
			Name: clusterProfile.Name,
		}

		// Reconcile ClusterProfile. ClusterSummary will be marked for deletion.
		// ClusterSummary has tough a finalizer (and there is no controller for it in this
		// test) so ClusterSummary won't be removed.
		// Since ClusterSummary won't be removed, ClusterProfile's finalizer will not be
		// removed.

		result, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterProfileName,
		})
		// Because there was one ClusterSummary and reconciliation deleted it and requested a new reconciliation
		Expect(err).To(BeNil())
		Expect(result.RequeueAfter).ToNot(BeZero())

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		err = c.Get(context.TODO(), clusterProfileName, currentClusterProfile)
		Expect(err).ToNot(HaveOccurred())

		// Remove ClusterSummary finalizer
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		controllerutil.RemoveFinalizer(currentClusterSummary, configv1beta1.ClusterSummaryFinalizer)
		Expect(c.Update(context.TODO(), currentClusterSummary)).To(Succeed())
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		// Reconcile ClusterProfile again. Since all associated ClusterSummaries are
		// gone, this reconciliation will remove finalizer and remove ClusterProfile

		_, err = reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterProfileName,
		})
		Expect(err).ToNot(HaveOccurred())

		err = c.Get(context.TODO(), clusterProfileName, currentClusterProfile)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("ClusterProfileReconciler: requeue methods", func() {
	var matchingClusterProfile *configv1beta1.ClusterProfile
	var nonMatchingClusterProfile *configv1beta1.ClusterProfile
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = randomString()

		key1 := randomString()
		value1 := randomString()
		key2 := randomString()
		value2 := randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					key1: value1,
					key2: value2,
				},
			},
		}

		matchingClusterProfile = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							key1: value1,
							key2: value2,
						},
					},
				},
			},
		}

		nonMatchingClusterProfile = &configv1beta1.ClusterProfile{
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
	})

	AfterEach(func() {
		ns := &corev1.Namespace{}
		Expect(testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: namespace}, ns)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), matchingClusterProfile)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), nonMatchingClusterProfile)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), ns)).To(Succeed())
	})

	It("RequeueClusterProfileForCluster returns correct ClusterProfiles for a CAPI cluster", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		clusterConfiguration := &configv1beta1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(cluster.Name, libsveltosv1beta1.ClusterTypeCapi),
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterConfiguration)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), matchingClusterProfile)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonMatchingClusterProfile)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, nonMatchingClusterProfile)).To(Succeed())

		clusterProfileName := client.ObjectKey{
			Name: matchingClusterProfile.Name,
		}

		clusterProfileReconciler := getClusterProfileReconciler(testEnv.Client)
		_, err := clusterProfileReconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterProfileName,
		})
		Expect(err).ToNot(HaveOccurred())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterProfileList := controllers.RequeueClusterProfileForCluster(clusterProfileReconciler,
				context.TODO(), cluster)
			result := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterProfile.Name}}
			for i := range clusterProfileList {
				if clusterProfileList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("RequeueClusterProfileForMachine returns correct ClusterProfiles for a CAPI machine", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		clusterConfiguration := &configv1beta1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(cluster.Name, libsveltosv1beta1.ClusterTypeCapi),
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

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		// Set Cluster ControlPlaneReady to true
		currentCluster := &clusterv1.Cluster{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, currentCluster)).To(Succeed())
		currentCluster.Status.ControlPlaneReady = true
		Expect(testEnv.Status().Update(context.TODO(), currentCluster)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), cpMachine)).To(Succeed())
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)
		Expect(testEnv.Client.Status().Update(context.TODO(), cpMachine)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cpMachine)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), matchingClusterProfile)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonMatchingClusterProfile)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), clusterConfiguration)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())
		Expect(addTypeInformationToObject(scheme, cpMachine)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, nonMatchingClusterProfile)).To(Succeed())

		clusterProfileName := client.ObjectKey{
			Name: matchingClusterProfile.Name,
		}

		clusterProfileReconciler := getClusterProfileReconciler(testEnv.Client)
		_, err := clusterProfileReconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterProfileName,
		})
		Expect(err).ToNot(HaveOccurred())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterProfileList := controllers.RequeueClusterProfileForMachine(clusterProfileReconciler,
				context.TODO(), cpMachine)
			result := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterProfile.Name}}
			for i := range clusterProfileList {
				if clusterProfileList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
