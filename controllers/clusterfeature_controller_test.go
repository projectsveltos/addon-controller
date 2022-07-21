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
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

const selector = configv1alpha1.Selector("env=qa,zone=west")

var _ = Describe("ClusterFeature: Reconciler", func() {
	var logger logr.Logger
	var clusterFeature *configv1alpha1.ClusterFeature
	var matchingCluster *clusterv1.Cluster
	var nonMatchingCluster *clusterv1.Cluster
	var namespace string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = "reconcile" + util.RandomString(5)

		logger = klogr.New()
		matchingCluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + util.RandomString(5),
				Namespace: namespace,
				Labels: map[string]string{
					"env":  "qa",
					"zone": "west",
				},
			},
		}

		nonMatchingCluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + util.RandomString(5),
				Namespace: namespace,
				Labels: map[string]string{
					"zone": "west",
				},
			},
		}

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureName := client.ObjectKey{
			Name: clusterFeature.Name,
		}
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterFeatureName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		err = c.Get(context.TODO(), clusterFeatureName, currentClusterFeature)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentClusterFeature,
				configv1alpha1.ClusterFeatureFinalizer,
			),
		).Should(BeTrue())
	})

	It("getMatchingClusters returns matchin CAPI Cluster", func() {
		initObjects := []client.Object{
			clusterFeature,
			matchingCluster,
			nonMatchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		matches, err := controllers.GetMatchingClusters(reconciler, context.TODO(), clusterFeatureScope)
		Expect(err).To(BeNil())
		Expect(len(matches)).To(Equal(1))
		Expect(matches[0].Namespace).To(Equal(matchingCluster.Namespace))
		Expect(matches[0].Name).To(Equal(matchingCluster.Name))
	})

	It("CreateClusterSummary creates ClusterSummary with proper fields", func() {
		initObjects := []client.Object{
			clusterFeature,
			matchingCluster,
			nonMatchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		err = controllers.CreateClusterSummary(reconciler, context.TODO(),
			clusterFeatureScope, corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterFeatureSpec, clusterFeature.Spec)).To(BeTrue())
		Expect(len(clusterSummaryList.Items[0].ObjectMeta.OwnerReferences)).To(Equal(1))
		owner := clusterSummaryList.Items[0].ObjectMeta.OwnerReferences[0]
		Expect(owner.Name).To(Equal(clusterFeature.Name))
		Expect(owner.Kind).To(Equal(clusterFeature.Kind))
	})

	It("UpdateClusterSummary updates ClusterSummary with proper fields when ClusterFeature syncmode set to continuos", func() {
		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, matchingCluster.Namespace, matchingCluster.Name)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: matchingCluster.Namespace,
				ClusterName:      matchingCluster.Name,
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					SyncMode: configv1alpha1.SyncModeOneTime,
					WorkloadRoles: []corev1.ObjectReference{
						{
							Name: "c-" + util.RandomString(5),
						},
					},
				},
			},
		}

		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuos
		clusterFeature.Spec.WorkloadRoles = []corev1.ObjectReference{
			{
				Name: "b-" + util.RandomString(5),
			},
			{
				Name: "d-" + util.RandomString(5),
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			matchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		err = controllers.UpdateClusterSummary(reconciler, context.TODO(),
			clusterFeatureScope, corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterFeatureSpec, clusterFeature.Spec)).To(BeTrue())
	})

	It("UpdateClusterSummary does not update ClusterSummary when ClusterFeature syncmode set to one time", func() {
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterFeature.Spec.WorkloadRoles = []corev1.ObjectReference{
			{
				Namespace: "a-" + util.RandomString(5),
				Name:      "b-" + util.RandomString(5),
			},
			{
				Namespace: "c-" + util.RandomString(5),
				Name:      "d-" + util.RandomString(5),
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, matchingCluster.Namespace, matchingCluster.Name)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace:   matchingCluster.Namespace,
				ClusterName:        matchingCluster.Name,
				ClusterFeatureSpec: clusterFeature.Spec,
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			matchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterFeature.Spec.WorkloadRoles = []corev1.ObjectReference{
			{
				Namespace: "a-" + util.RandomString(5),
				Name:      "b-" + util.RandomString(5),
			},
		}

		Expect(c.Update(context.TODO(), clusterFeature)).To(Succeed())

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		err = controllers.UpdateClusterSummary(reconciler, context.TODO(),
			clusterFeatureScope, corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterFeatureSpec, clusterFeature.Spec)).ToNot(BeTrue())
		Expect(len(clusterSummaryList.Items[0].Spec.ClusterFeatureSpec.WorkloadRoles)).To(Equal(2))
	})

	It("DeleteClusterSummary removes ClusterSummary for non-matching cluster", func() {
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterFeature.Spec.WorkloadRoles = []corev1.ObjectReference{
			{
				Namespace: "a-" + util.RandomString(5),
				Name:      "b-" + util.RandomString(5),
			},
			{
				Namespace: "c-" + util.RandomString(5),
				Name:      "d-" + util.RandomString(5),
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, nonMatchingCluster.Namespace, nonMatchingCluster.Name)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: clusterFeature.APIVersion,
						Kind:       clusterFeature.Kind,
						Name:       clusterFeature.Name,
					},
				},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace:   nonMatchingCluster.Namespace,
				ClusterName:        nonMatchingCluster.Name,
				ClusterFeatureSpec: clusterFeature.Spec,
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			nonMatchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		err := controllers.DeleteClusterSummary(reconciler, context.TODO(), clusterSummary)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(BeZero())
	})

	It("updateClusterSummaries does not ClusterSummary for matching CAPI Cluster with no running control plane machine", func() {
		clusterFeature.Status.MatchingClusters = []corev1.ObjectReference{
			{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name,
			},
		}
		initObjects := []client.Object{
			clusterFeature,
			nonMatchingCluster,
			matchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(reconciler, context.TODO(), clusterFeatureScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(0))
	})

	It("updateClusterSummaries creates ClusterSummary for each matching CAPI Cluster", func() {
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name,
				Labels: map[string]string{
					clusterv1.ClusterLabelName:             matchingCluster.Name,
					clusterv1.MachineControlPlaneLabelName: "ok",
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		clusterFeature.Status.MatchingClusters = []corev1.ObjectReference{
			{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name,
			},
		}
		initObjects := []client.Object{
			clusterFeature,
			nonMatchingCluster,
			matchingCluster,
			cpMachine,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(reconciler, context.TODO(), clusterFeatureScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
	})

	It("updateClusterSummaries updates existing ClusterSummary for each matching CAPI Cluster", func() {
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name,
				Labels: map[string]string{
					clusterv1.ClusterLabelName:             matchingCluster.Name,
					clusterv1.MachineControlPlaneLabelName: "ok",
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		clusterFeature.Status.MatchingClusters = []corev1.ObjectReference{
			{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name,
			},
		}
		clusterFeature.Spec.WorkloadRoles = []corev1.ObjectReference{
			{
				Namespace: "x-" + util.RandomString(5),
				Name:      "y-" + util.RandomString(5),
			},
		}
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuos

		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, matchingCluster.Namespace, matchingCluster.Name)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: matchingCluster.Namespace,
				ClusterName:      matchingCluster.Name,
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					SyncMode: configv1alpha1.SyncModeContinuos,
					WorkloadRoles: []corev1.ObjectReference{
						{
							Name: "c-" + util.RandomString(5),
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			nonMatchingCluster,
			matchingCluster,
			clusterSummary,
			cpMachine,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(reconciler, context.TODO(), clusterFeatureScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterFeatureSpec, clusterFeature.Spec)).To(BeTrue())
	})

	It("getMachinesForCluster returns list of all machines for a CPI cluster", func() {
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name + util.RandomString(5),
				Labels: map[string]string{
					clusterv1.ClusterLabelName:             matchingCluster.Name,
					clusterv1.MachineControlPlaneLabelName: "ok",
				},
			},
		}
		workerMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name + util.RandomString(5),
				Labels: map[string]string{
					clusterv1.ClusterLabelName: matchingCluster.Name,
				},
			},
		}

		initObjects := []client.Object{
			workerMachine,
			cpMachine,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		cps, err := controllers.GetMachinesForCluster(reconciler, context.TODO(),
			clusterFeatureScope, corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())
		Expect(len(cps.Items)).To(Equal(2))
	})

	It("IsClusterReadyToBeConfigured returns true for a cluster with one control plane machine in running phase", func() {
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name + util.RandomString(5),
				Labels: map[string]string{
					clusterv1.ClusterLabelName:             matchingCluster.Name,
					clusterv1.MachineControlPlaneLabelName: "ok",
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		workerMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name + util.RandomString(5),
				Labels: map[string]string{
					clusterv1.ClusterLabelName: matchingCluster.Name,
				},
			},
		}
		initObjects := []client.Object{
			workerMachine,
			cpMachine,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		ready, err := controllers.IsClusterReadyToBeConfigured(reconciler, context.TODO(),
			clusterFeatureScope, corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())
		Expect(ready).To(Equal(true))
	})

	It("IsClusterReadyToBeConfigured returns false for a cluster with no control plane machine in running phase", func() {
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name + util.RandomString(5),
				Labels: map[string]string{
					clusterv1.ClusterLabelName:             matchingCluster.Name,
					clusterv1.MachineControlPlaneLabelName: "ok",
				},
			},
		}
		workerMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      matchingCluster.Name + util.RandomString(5),
				Labels: map[string]string{
					clusterv1.ClusterLabelName: matchingCluster.Name,
				},
			},
			Status: clusterv1.MachineStatus{
				Phase: "Runnning",
			},
		}
		initObjects := []client.Object{
			workerMachine,
			cpMachine,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterFeatureReconciler{
			Client: c,
			Log:    klogr.New(),
			Scheme: scheme,
		}

		clusterFeatureScope, err := scope.NewClusterFeatureScope(scope.ClusterFeatureScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ControllerName: "clusterfeature",
		})
		Expect(err).To(BeNil())

		ready, err := controllers.IsClusterReadyToBeConfigured(reconciler, context.TODO(),
			clusterFeatureScope, corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())
		Expect(ready).To(Equal(false))
	})
})

var _ = Describe("ClusterFeatureReconciler: requeue methods", func() {
	var matchingClusterFeature *configv1alpha1.ClusterFeature
	var nonMatchingClusterFeature *configv1alpha1.ClusterFeature
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = "reconcile" + util.RandomString(5)

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + util.RandomString(5),
				Namespace: namespace,
				Labels: map[string]string{
					"env":  "qa",
					"zone": "west",
				},
			},
		}

		matchingClusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}

		nonMatchingClusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: configv1alpha1.Selector("env=production"),
			},
		}
	})

	AfterEach(func() {
		ns := &corev1.Namespace{}
		Expect(testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: namespace}, ns)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), matchingClusterFeature)).To(Succeed())
		Expect(testEnv.Client.Delete(context.TODO(), nonMatchingClusterFeature)).To(Succeed())
	})

	It("RequeueClusterFeatureForCluster returns correct ClusterFeatures for a CAPI cluster", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), matchingClusterFeature)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonMatchingClusterFeature)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterFeatureList := controllers.RequeueClusterFeatureForCluster(clusterFeatureReconciler, cluster)
			result := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterFeature.Name}}
			for i := range clusterFeatureList {
				if clusterFeatureList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("RequeueClusterFeatureForMachine returns correct ClusterFeatures for a CAPI machine", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		cpMachine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + util.RandomString(5),
				Labels: map[string]string{
					clusterv1.ClusterLabelName:             cluster.Name,
					clusterv1.MachineControlPlaneLabelName: "ok",
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cpMachine)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), matchingClusterFeature)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonMatchingClusterFeature)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			clusterFeatureList := controllers.RequeueClusterFeatureForMachine(clusterFeatureReconciler, cpMachine)
			result := reconcile.Request{NamespacedName: types.NamespacedName{Name: matchingClusterFeature.Name}}
			for i := range clusterFeatureList {
				if clusterFeatureList[i] == result {
					return true
				}
			}
			return false
		}, timeout, pollingInterval).Should(BeTrue())
	})
})
