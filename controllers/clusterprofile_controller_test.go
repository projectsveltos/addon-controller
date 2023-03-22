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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/controllers"
	"github.com/projectsveltos/sveltos-manager/pkg/scope"
)

const (
	selector    = libsveltosv1alpha1.Selector("env=qa,zone=west")
	clusterKind = "Cluster"
)

var _ = Describe("ClusterProfile: Reconciler", func() {
	var logger logr.Logger
	var clusterProfile *configv1alpha1.ClusterProfile
	var matchingCluster *clusterv1.Cluster
	var nonMatchingCluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

		logger = klogr.New()
		matchingCluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env":  "qa",
					"zone": "west",
				},
			},
		}

		nonMatchingCluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"zone": "west",
				},
			},
		}

		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: selector,
			},
		}
	})

	It("getMatchingCluster considers both ClusterSelector and ClusterRefs", func() {
		initObjects := []client.Object{
			matchingCluster,
			nonMatchingCluster,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		// Only clusterSelector is, so only matchingCluster is a match
		matching, err := controllers.GetMatchingClusters(reconciler, context.TODO(), clusterProfileScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(len(matching)).To(Equal(1))

		clusterProfile.Spec.ClusterRefs = []corev1.ObjectReference{
			{
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
				Name:       nonMatchingCluster.Name,
				Namespace:  nonMatchingCluster.Namespace,
			},
		}

		// Both clusterSelector (matchingCluster is a match) and ClusterRefs (nonMatchingCluster is referenced) are set
		// So two clusters are now matching
		matching, err = controllers.GetMatchingClusters(reconciler, context.TODO(), clusterProfileScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(len(matching)).To(Equal(2))
	})

	It("Adds finalizer", func() {
		initObjects := []client.Object{
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileName := client.ObjectKey{
			Name: clusterProfile.Name,
		}
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterProfileName,
		})
		Expect(err).ToNot(HaveOccurred())

		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		err = c.Get(context.TODO(), clusterProfileName, currentClusterProfile)
		Expect(err).ToNot(HaveOccurred())
		Expect(
			controllerutil.ContainsFinalizer(
				currentClusterProfile,
				configv1alpha1.ClusterProfileFinalizer,
			),
		).Should(BeTrue())
	})

	It("UpdateClusterConfiguration idempotently adds ClusterProfile as OwnerReference and in Status.ClusterProfileResources", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		clusterConfiguration := &configv1alpha1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(matchingCluster.Name, libsveltosv1alpha1.ClusterTypeCapi),
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			ns,
			clusterConfiguration,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		clusterRef := corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name,
			Kind: clusterKind, APIVersion: clusterv1.GroupVersion.String()}
		Expect(controllers.UpdateClusterConfiguration(reconciler, context.TODO(), clusterProfileScope, &clusterRef)).To(Succeed())

		currentClusterConfiguration := &configv1alpha1.ClusterConfiguration{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterConfiguration.Namespace, Name: clusterConfiguration.Name}, currentClusterConfiguration)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(currentClusterConfiguration.OwnerReferences[0].Name).To(Equal(clusterProfile.Name))

		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(1))

		Expect(controllers.UpdateClusterConfiguration(reconciler, context.TODO(), clusterProfileScope, &clusterRef)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(1))
	})

	It("CleanClusterConfiguration idempotently removes ClusterProfile as OwnerReference and from Status.ClusterProfileResources", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			ns,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		// Preprare clusterConfiguration with Status section. OwnerReference
		clusterConfiguration := &configv1alpha1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(matchingCluster.Name, libsveltosv1alpha1.ClusterTypeCapi),
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       currentClusterProfile.Kind,
						Name:       currentClusterProfile.Name,
						APIVersion: currentClusterProfile.APIVersion,
						UID:        currentClusterProfile.UID,
					},
					{ // Add a second fake Owner, so that when removing ClusterProfile as owner,
						// ClusterConfiguration is not deleted
						Kind:       currentClusterProfile.Kind,
						Name:       randomString(),
						APIVersion: currentClusterProfile.APIVersion,
						UID:        types.UID(randomString()),
					},
				},
			},
			Status: configv1alpha1.ClusterConfigurationStatus{
				ClusterProfileResources: []configv1alpha1.ClusterProfileResource{
					{ClusterProfileName: clusterProfile.Name},
				},
			},
		}

		Expect(c.Create(context.TODO(), clusterConfiguration)).To(Succeed())

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		Expect(controllers.CleanClusterConfiguration(reconciler, context.TODO(), currentClusterProfile, clusterConfiguration)).To(Succeed())

		currentClusterConfiguration := &configv1alpha1.ClusterConfiguration{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterConfiguration.Namespace,
				Name:      clusterConfiguration.Name,
			},
			currentClusterConfiguration)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(0))

		Expect(controllers.CleanClusterConfiguration(reconciler, context.TODO(), currentClusterProfile, clusterConfiguration)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(0))
	})

	It("Reconciliation of deleted ClusterProfile removes finalizer only when all ClusterSummaries are gone", func() {
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:   clusterProfileNamePrefix + randomString(),
				Labels: map[string]string{controllers.ClusterProfileLabelName: clusterProfile.Name},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterType: libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		controllerutil.AddFinalizer(clusterSummary, configv1alpha1.ClusterSummaryFinalizer)

		now := metav1.NewTime(time.Now())
		clusterProfile.DeletionTimestamp = &now
		controllerutil.AddFinalizer(clusterProfile, configv1alpha1.ClusterProfileFinalizer)

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		addOwnerReference(ctx, c, clusterSummary, clusterProfile)

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileName := client.ObjectKey{
			Name: clusterProfile.Name,
		}

		// Reconcile ClusterProfile. ClusterSummary will be marked for deletion.
		// ClusterSummary has tough a finalizer (and there is no controller for it in this
		// test) so ClusterSummary won't be removed.
		// Since ClusterSummary won't be removed, ClusterProfile's finalizer will not be
		// removed.

		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{
			NamespacedName: clusterProfileName,
		})
		// Because there was one ClusterSummary and reconciliation deleted it and returned an error
		Expect(err).To(HaveOccurred())

		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		err = c.Get(context.TODO(), clusterProfileName, currentClusterProfile)
		Expect(err).ToNot(HaveOccurred())

		// Remove ClusterSummary finalizer
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		controllerutil.RemoveFinalizer(currentClusterSummary, configv1alpha1.ClusterSummaryFinalizer)
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

	It("CreateClusterSummary creates ClusterSummary with proper fields", func() {
		initObjects := []client.Object{
			clusterProfile,
			matchingCluster,
			nonMatchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		err = controllers.CreateClusterSummary(reconciler, context.TODO(),
			clusterProfileScope,
			&corev1.ObjectReference{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       clusterKind,
			})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterProfileSpec, clusterProfile.Spec)).To(BeTrue())
		Expect(len(clusterSummaryList.Items[0].ObjectMeta.OwnerReferences)).To(Equal(1))
		owner := clusterSummaryList.Items[0].ObjectMeta.OwnerReferences[0]
		Expect(owner.Name).To(Equal(clusterProfile.Name))
		Expect(owner.Kind).To(Equal(clusterProfile.Kind))
	})

	It("UpdateClusterSummary updates ClusterSummary with proper fields when ClusterProfile syncmode set to continuous", func() {
		sveltosCluster := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
				Labels:    matchingCluster.Labels,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(sveltosCluster.Name, sveltosCluster.Name, false)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: sveltosCluster.Namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: sveltosCluster.Namespace,
				ClusterName:      sveltosCluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeSveltos,
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					SyncMode: configv1alpha1.SyncModeOneTime,
					PolicyRefs: []libsveltosv1alpha1.PolicyRef{
						{
							Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
							Namespace: "c-" + randomString(),
							Name:      "c-" + randomString(),
						},
					},
				},
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, sveltosCluster.Name, libsveltosv1alpha1.ClusterTypeSveltos)

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
				Namespace: "b-" + randomString(),
				Name:      "b-" + randomString(),
			},
			{
				Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
				Namespace: "d-" + randomString(),
				Name:      "d-" + randomString(),
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			sveltosCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		err = controllers.UpdateClusterSummary(reconciler, context.TODO(),
			clusterProfileScope, &corev1.ObjectReference{
				Namespace: sveltosCluster.Namespace, Name: sveltosCluster.Name,
				Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String()})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(sveltosCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(sveltosCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterProfileSpec, clusterProfile.Spec)).To(BeTrue())
	})

	It("UpdateClusterSummary does not update ClusterSummary when ClusterProfile syncmode set to one time", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: "a-" + randomString(),
				Name:      "b-" + randomString(),
			},
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: "c-" + randomString(),
				Name:      "d-" + randomString(),
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterProfile.Name, matchingCluster.Name, false)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: matchingCluster.Namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace:   matchingCluster.Namespace,
				ClusterName:        matchingCluster.Name,
				ClusterProfileSpec: clusterProfile.Spec,
				ClusterType:        libsveltosv1alpha1.ClusterTypeCapi,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, matchingCluster.Name, libsveltosv1alpha1.ClusterTypeCapi)

		initObjects := []client.Object{
			clusterProfile,
			matchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: "a-" + randomString(),
				Name:      "b-" + randomString(),
			},
		}

		Expect(c.Update(context.TODO(), clusterProfile)).To(Succeed())

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		err = controllers.UpdateClusterSummary(reconciler, context.TODO(),
			clusterProfileScope, &corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterProfileSpec, clusterProfile.Spec)).ToNot(BeTrue())
		Expect(len(clusterSummaryList.Items[0].Spec.ClusterProfileSpec.PolicyRefs)).To(Equal(2))
	})

	It("DeleteClusterSummary removes ClusterSummary for non-matching cluster", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeOneTime
		clusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: "a-" + randomString(),
				Name:      "b-" + randomString(),
			},
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: "c-" + randomString(),
				Name:      "d-" + randomString(),
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterProfile.Name, nonMatchingCluster.Name, false)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: nonMatchingCluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: clusterProfile.APIVersion,
						Kind:       clusterProfile.Kind,
						Name:       clusterProfile.Name,
					},
				},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace:   nonMatchingCluster.Namespace,
				ClusterName:        nonMatchingCluster.Name,
				ClusterProfileSpec: clusterProfile.Spec,
				ClusterType:        libsveltosv1alpha1.ClusterTypeCapi,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, matchingCluster.Name, libsveltosv1alpha1.ClusterTypeCapi)

		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		err := controllers.DeleteClusterSummary(reconciler, context.TODO(), clusterSummary)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(BeZero())
	})

	It("updateClusterSummaries does not ClusterSummary for matching CAPI Cluster with no running control plane machine", func() {
		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}
		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(reconciler, context.TODO(), clusterProfileScope)
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
					clusterv1.ClusterNameLabel:         matchingCluster.Name,
					clusterv1.MachineControlPlaneLabel: "ok",
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}
		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
			cpMachine,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(reconciler, context.TODO(), clusterProfileScope)
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
					clusterv1.ClusterNameLabel:         matchingCluster.Name,
					clusterv1.MachineControlPlaneLabel: "ok",
				},
			},
		}
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)

		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}
		clusterProfile.Spec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{
				Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
				Namespace: "x-" + randomString(),
				Name:      "y-" + randomString(),
			},
		}
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		clusterSummaryName := controllers.GetClusterSummaryName(clusterProfile.Name, matchingCluster.Name, false)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: matchingCluster.Namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: matchingCluster.Namespace,
				ClusterName:      matchingCluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					SyncMode: configv1alpha1.SyncModeContinuous,
					PolicyRefs: []libsveltosv1alpha1.PolicyRef{
						{
							Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
							Namespace: "c-" + randomString(),
							Name:      "c-" + randomString(),
						},
					},
				},
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, matchingCluster.Name, libsveltosv1alpha1.ClusterTypeCapi)

		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
			clusterSummary,
			cpMachine,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(reconciler, context.TODO(), clusterProfileScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterProfileSpec, clusterProfile.Spec)).To(BeTrue())
	})

	It("updateClusterReports creates ClusterReport for matching cluster in DryRun mode", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeDryRun
		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}
		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.UpdateClusterReports(reconciler, context.TODO(), clusterProfileScope)).To(Succeed())

		// ClusterReport for matching cluster is created
		currentClusterReportList := &configv1alpha1.ClusterReportList{}
		listOptions := []client.ListOption{
			client.MatchingLabels{
				controllers.ClusterProfileLabelName: clusterProfile.Name,
			},
		}
		err = c.List(context.TODO(), currentClusterReportList, listOptions...)
		Expect(err).To(BeNil())
		// No other ClusterReports are created
		Expect(len(currentClusterReportList.Items)).To(Equal(1))
	})

	It("updateClusterReports does not create ClusterReport for matching cluster in non dryRun mode", func() {
		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}
		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		clusterProfileScope, err := scope.NewClusterProfileScope(scope.ClusterProfileScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterProfile: clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.UpdateClusterReports(reconciler, context.TODO(), clusterProfileScope)).To(Succeed())

		// No ClusterReports are created
		currentClusterReportList := &configv1alpha1.ClusterReportList{}
		Expect(c.List(context.TODO(), currentClusterReportList)).To(Succeed())
		Expect(len(currentClusterReportList.Items)).To(Equal(0))
	})

	It("cleanClusterSummaries removes all CluterReports created for a ClusterProfile instance", func() {
		clusterReport1 := &configv1alpha1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      randomString(),
				Labels: map[string]string{
					controllers.ClusterProfileLabelName: clusterProfile.Name,
				},
			},
		}

		clusterReport2 := &configv1alpha1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      randomString(),
				Labels: map[string]string{
					controllers.ClusterProfileLabelName: clusterProfile.Name + randomString(),
				},
			},
		}

		initObjects := []client.Object{
			clusterReport1,
			clusterReport2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            c,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		Expect(controllers.CleanClusterReports(reconciler, context.TODO(), clusterProfile)).To(Succeed())
		// ClusterReport1 is gone
		currentClusterReport := &configv1alpha1.ClusterReport{}
		err := c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterReport1.Namespace, Name: clusterReport1.Name}, currentClusterReport)
		Expect(err).ToNot(BeNil())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		// ClusterReport2 is still present
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterReport2.Namespace, Name: clusterReport2.Name}, currentClusterReport)
		Expect(err).To(BeNil())
	})

	It("updateClusterSummarySyncMode updates ClusterSummary SyncMode", func() {
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  randomString(),
				Name:       clusterProfileNamePrefix + randomString(),
				Labels:     map[string]string{controllers.ClusterProfileLabelName: clusterProfile.Name},
				Finalizers: []string{configv1alpha1.ClusterSummaryFinalizer},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterProfileSpec: configv1alpha1.ClusterProfileSpec{
					SyncMode: configv1alpha1.SyncModeDryRun,
				},
				ClusterType: libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		// Make sure to have clustersummary marked as deleted.
		// ClusterProfile will update SyncMode for ClusterSummary representing CAPI Clusters
		// not matching anymore. So deleted ClusterSummaries.
		now := metav1.NewTime(time.Now())
		clusterSummary.DeletionTimestamp = &now

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummary.Namespace,
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterSummary)).To(Succeed())

		clusterProfile.Spec.SyncMode = configv1alpha1.SyncModeContinuous

		reconciler := &controllers.ClusterProfileReconciler{
			Client:            testEnv,
			Scheme:            scheme,
			ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterProfiles:   make(map[corev1.ObjectReference]libsveltosv1alpha1.Selector),
			ClusterLabels:     make(map[corev1.ObjectReference]map[string]string),
			Mux:               sync.Mutex{},
		}

		Expect(controllers.UpdateClusterSummarySyncMode(reconciler, context.TODO(),
			clusterProfile, clusterSummary)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
			if err != nil {
				return false
			}
			return currentClusterSummary.Spec.ClusterProfileSpec.SyncMode == clusterProfile.Spec.SyncMode
		}, timeout, pollingInterval).Should(BeTrue())
	})
})

var _ = Describe("ClusterProfileReconciler: requeue methods", func() {
	var matchingClusterProfile *configv1alpha1.ClusterProfile
	var nonMatchingClusterProfile *configv1alpha1.ClusterProfile
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = "reconcile" + randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"env":  "qa",
					"zone": "west",
				},
			},
		}

		matchingClusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: selector,
			},
		}

		nonMatchingClusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: libsveltosv1alpha1.Selector("env=production"),
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

		clusterConfiguration := &configv1alpha1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(cluster.Name, libsveltosv1alpha1.ClusterTypeCapi),
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
				cluster)
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

		clusterConfiguration := &configv1alpha1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(cluster.Name, libsveltosv1alpha1.ClusterTypeCapi),
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
		cpMachine.Status.SetTypedPhase(clusterv1.MachinePhaseRunning)
		Expect(addTypeInformationToObject(scheme, cpMachine)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterConfiguration)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cpMachine)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), matchingClusterProfile)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), nonMatchingClusterProfile)).To(Succeed())
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
				cpMachine)
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
