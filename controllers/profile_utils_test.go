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
	"fmt"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/internal/test/helpers/external"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	clusterKind = "Cluster"
)

var _ = Describe("Profile: Reconciler", func() {
	var logger logr.Logger
	var clusterProfile *configv1beta1.ClusterProfile
	var matchingCluster *clusterv1.Cluster
	var nonMatchingCluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = "profile-utils-" + randomString()

		key1 := randomString()
		value1 := randomString()
		key2 := randomString()
		value2 := randomString()

		logger = textlogger.NewLogger(textlogger.NewConfig())
		initialized := true
		matchingCluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					key1: value1,
					key2: value2,
				},
			},
			Status: clusterv1.ClusterStatus{
				Initialization: clusterv1.ClusterInitializationStatus{
					ControlPlaneInitialized: &initialized,
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, matchingCluster)).To(Succeed())

		nonMatchingCluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					key2: value2,
				},
			},
			Status: clusterv1.ClusterStatus{
				Initialization: clusterv1.ClusterInitializationStatus{
					ControlPlaneInitialized: &initialized,
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, nonMatchingCluster)).To(Succeed())

		clusterProfile = &configv1beta1.ClusterProfile{
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
		Expect(addTypeInformationToObject(scheme, clusterProfile)).To(Succeed())
	})

	It("getMatchingCluster considers both ClusterSelector and ClusterRefs", func() {
		clusterCRD := external.TestClusterCRD.DeepCopy()

		initObjects := []client.Object{
			clusterCRD,
			matchingCluster,
			nonMatchingCluster,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		// Only clusterSelector is, so only matchingCluster is a match
		matching, err := controllers.GetMatchingClusters(context.TODO(), c, "", profileScope.GetSelector(),
			profileScope.GetSpec().ClusterRefs, textlogger.NewLogger(textlogger.NewConfig()))
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
		matching, err = controllers.GetMatchingClusters(context.TODO(), c, "", profileScope.GetSelector(),
			profileScope.GetSpec().ClusterRefs, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(len(matching)).To(Equal(2))
	})

	It("UpdateClusterConfiguration idempotently adds ClusterProfile as OwnerReference and in Status.ClusterProfileResources", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		clusterConfiguration := &configv1beta1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(matchingCluster.Name, libsveltosv1beta1.ClusterTypeCapi),
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			ns,
			clusterConfiguration,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterRef := corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name,
			Kind: clusterKind, APIVersion: clusterv1.GroupVersion.String()}
		Expect(controllers.UpdateClusterConfigurationWithProfile(context.TODO(), c, clusterProfile, &clusterRef)).To(Succeed())

		currentClusterConfiguration := &configv1beta1.ClusterConfiguration{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterConfiguration.Namespace, Name: clusterConfiguration.Name}, currentClusterConfiguration)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(currentClusterConfiguration.OwnerReferences[0].Name).To(Equal(clusterProfile.Name))

		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(1))

		Expect(controllers.UpdateClusterConfigurationWithProfile(context.TODO(), c, clusterProfile, &clusterRef)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(1))
	})

	It("CleanClusterConfiguration idempotently removes ClusterProfile as OwnerReference and from Status.ClusterProfileResources", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		clusterConfiguration := &configv1beta1.ClusterConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      controllers.GetClusterConfigurationName(matchingCluster.Name, libsveltosv1beta1.ClusterTypeCapi),
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			ns,
			clusterConfiguration,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

		currentClusterConfiguration := &configv1beta1.ClusterConfiguration{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterConfiguration.Namespace,
				Name:      clusterConfiguration.Name,
			},
			currentClusterConfiguration)).To(Succeed())

		// Preprare clusterConfiguration with Status section. OwnerReference
		clusterConfiguration.OwnerReferences = []metav1.OwnerReference{
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
		}

		Expect(c.Update(context.TODO(), clusterConfiguration)).To(Succeed())

		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterConfiguration.Namespace,
				Name:      clusterConfiguration.Name,
			},
			currentClusterConfiguration)).To(Succeed())

		currentClusterConfiguration.Status =
			configv1beta1.ClusterConfigurationStatus{
				ClusterProfileResources: []configv1beta1.ClusterProfileResource{
					{
						ClusterProfileName: clusterProfile.Name,
					},
				},
			}

		Expect(c.Status().Update(context.TODO(), currentClusterConfiguration)).To(Succeed())

		Expect(controllers.CleanClusterConfiguration(context.TODO(), c, currentClusterProfile,
			currentClusterConfiguration)).To(Succeed())

		Expect(c.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterConfiguration.Namespace,
				Name:      clusterConfiguration.Name,
			},
			currentClusterConfiguration)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(0))

		Expect(controllers.CleanClusterConfiguration(context.TODO(), c, currentClusterProfile,
			currentClusterConfiguration)).To(Succeed())

		Expect(len(currentClusterConfiguration.OwnerReferences)).To(Equal(1))
		Expect(len(currentClusterConfiguration.Status.ClusterProfileResources)).To(Equal(0))
	})

	It("CreateClusterSummary creates ClusterSummary with proper fields", func() {
		initObjects := []client.Object{
			clusterProfile,
			matchingCluster,
			nonMatchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.CreateClusterSummary(context.TODO(), c, clusterProfileScope,
			&corev1.ObjectReference{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       clusterKind,
			})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
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
		sveltosCluster := &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
				Labels:    matchingCluster.Labels,
			},
		}

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			sveltosCluster.Name, sveltosCluster.Name, false)
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: sveltosCluster.Namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: sveltosCluster.Namespace,
				ClusterName:      sveltosCluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeSveltos,
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode: configv1beta1.SyncModeOneTime,
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
				},
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, sveltosCluster.Name, libsveltosv1beta1.ClusterTypeSveltos)

		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
			{
				Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			sveltosCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummary(context.TODO(), c,
			clusterProfileScope, &corev1.ObjectReference{
				Namespace: sveltosCluster.Namespace, Name: sveltosCluster.Name,
				Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String()})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(sveltosCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(sveltosCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterProfileSpec, clusterProfile.Spec)).To(BeTrue())
	})

	It("UpdateClusterSummary does not update ClusterSummary when ClusterProfile syncmode set to one time", func() {
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeOneTime
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, matchingCluster.Name, false)
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: matchingCluster.Namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace:   matchingCluster.Namespace,
				ClusterName:        matchingCluster.Name,
				ClusterProfileSpec: clusterProfile.Spec,
				ClusterType:        libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, matchingCluster.Name, libsveltosv1beta1.ClusterTypeCapi)

		initObjects := []client.Object{
			clusterProfile,
			matchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		Expect(c.Update(context.TODO(), clusterProfile)).To(Succeed())

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummary(context.TODO(), c,
			clusterProfileScope, &corev1.ObjectReference{Namespace: matchingCluster.Namespace, Name: matchingCluster.Name})
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterProfileSpec, clusterProfile.Spec)).ToNot(BeTrue())
		Expect(len(clusterSummaryList.Items[0].Spec.ClusterProfileSpec.PolicyRefs)).To(Equal(2))
	})

	It("cleanClusterSummaries removes ClusterSummary for non-matching cluster", func() {
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeOneTime
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
			{

				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, nonMatchingCluster.Name, false)
		clusterSummary := &configv1beta1.ClusterSummary{
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
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace:   nonMatchingCluster.Namespace,
				ClusterName:        nonMatchingCluster.Name,
				ClusterProfileSpec: clusterProfile.Spec,
				ClusterType:        libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, matchingCluster.Name,
			libsveltosv1beta1.ClusterTypeCapi)

		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.CleanClusterSummaries(context.TODO(), c, profileScope)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("clusterSummaries still present"))

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(BeZero())
	})

	It("updateClusterSummarySyncMode updates ClusterSummary SyncMode", func() {
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      clusterProfileNamePrefix + randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfile.Name,
				},
				Finalizers: []string{configv1beta1.ClusterSummaryFinalizer},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode: configv1beta1.SyncModeDryRun,
				},
				ClusterType: libsveltosv1beta1.ClusterTypeCapi,
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

		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous

		Expect(controllers.UpdateClusterSummarySyncMode(context.TODO(), testEnv.Client, clusterSummary,
			clusterProfile.Spec.SyncMode)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			currentClusterSummary := &configv1beta1.ClusterSummary{}
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)
			if err != nil {
				return false
			}
			return currentClusterSummary.Spec.ClusterProfileSpec.SyncMode == clusterProfile.Spec.SyncMode
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("updateClusterSummaries does not create ClusterSummary for matching CAPI Cluster not ready", func() {
		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}

		initialized := false
		matchingCluster.Status.Initialization.ControlPlaneInitialized = &initialized

		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(context.TODO(), c, clusterProfileScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(0))
	})

	It("updateClusterSummaries does not create ClusterSummary for matching Paused Cluster", func() {
		maxUpdate := int32(3)
		clusterProfile.Spec.MaxUpdate = &intstr.IntOrString{Type: intstr.Int, IntVal: maxUpdate}
		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}

		initialized := true
		matchingCluster.Status.Initialization.ControlPlaneInitialized = &initialized
		paused := true
		matchingCluster.Spec.Paused = &paused

		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(context.TODO(), c, clusterProfileScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(0))
	})

	It("updateClusterSummaries creates ClusterSummary for each matching CAPI Cluster", func() {
		initialized := true
		matchingCluster.Status.Initialization.ControlPlaneInitialized = &initialized
		nonMatchingCluster.Status.Conditions = matchingCluster.Status.Conditions

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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(context.TODO(), c, clusterProfileScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
	})

	It("updateClusterSummaries updates existing ClusterSummary for each matching CAPI Cluster", func() {
		initialized := true
		matchingCluster.Status.Initialization.ControlPlaneInitialized = &initialized

		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{
				Namespace:  matchingCluster.Namespace,
				Name:       matchingCluster.Name,
				Kind:       clusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			},
		}
		clusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				Namespace: "x-" + randomString(),
				Name:      "y-" + randomString(),
			},
		}
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, matchingCluster.Name, false)
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: matchingCluster.Namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: matchingCluster.Namespace,
				ClusterName:      matchingCluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					SyncMode: configv1beta1.SyncModeContinuous,
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
				},
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, matchingCluster.Name, libsveltosv1beta1.ClusterTypeCapi)

		initObjects := []client.Object{
			clusterProfile,
			nonMatchingCluster,
			matchingCluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		err = controllers.UpdateClusterSummaries(context.TODO(), c, clusterProfileScope)
		Expect(err).To(BeNil())

		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaryList)).To(BeNil())
		Expect(len(clusterSummaryList.Items)).To(Equal(1))
		Expect(clusterSummaryList.Items[0].Spec.ClusterName).To(Equal(matchingCluster.Name))
		Expect(clusterSummaryList.Items[0].Spec.ClusterNamespace).To(Equal(matchingCluster.Namespace))
		Expect(reflect.DeepEqual(clusterSummaryList.Items[0].Spec.ClusterProfileSpec, clusterProfile.Spec)).To(BeTrue())
	})

	It("updateClusterReports creates ClusterReport for matching cluster in DryRun mode", func() {
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeDryRun
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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.UpdateClusterReports(context.TODO(), c, clusterProfileScope)).To(Succeed())

		// ClusterReport for matching cluster is created
		currentClusterReportList := &configv1beta1.ClusterReportList{}
		listOptions := []client.ListOption{
			client.MatchingLabels{
				clusterops.ClusterProfileLabelName: clusterProfile.Name,
			},
		}
		err = c.List(context.TODO(), currentClusterReportList, listOptions...)
		Expect(err).To(BeNil())
		// No other ClusterReports are created
		Expect(len(currentClusterReportList.Items)).To(Equal(1))
	})

	It("updateClusterReports does not create ClusterReport for matching cluster in non dryRun mode", func() {
		clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.UpdateClusterReports(context.TODO(), c, clusterProfileScope)).To(Succeed())

		// No ClusterReports are created
		currentClusterReportList := &configv1beta1.ClusterReportList{}
		Expect(c.List(context.TODO(), currentClusterReportList)).To(Succeed())
		Expect(len(currentClusterReportList.Items)).To(Equal(0))
	})

	It("cleanClusterReports removes all ClusterReports created for a ClusterProfile instance", func() {
		clusterReport1 := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfile.Name,
				},
			},
		}

		clusterReport2 := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: matchingCluster.Namespace,
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfile.Name + randomString(),
				},
			},
		}

		initObjects := []client.Object{
			clusterReport1,
			clusterReport2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.CleanClusterReports(context.TODO(), c, clusterProfileScope)).To(Succeed())
		// ClusterReport1 is gone
		currentClusterReport := &configv1beta1.ClusterReport{}
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterReport1.Namespace, Name: clusterReport1.Name}, currentClusterReport)
		Expect(err).ToNot(BeNil())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		// ClusterReport2 is still present
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterReport2.Namespace, Name: clusterReport2.Name}, currentClusterReport)
		Expect(err).To(BeNil())
	})

	It("cleanClusterReports removes all ClusterReports instances created for a Profile instance", func() {
		profile := configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}
		Expect(addTypeInformationToObject(scheme, &profile)).To(Succeed())

		clusterReport1 := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: profile.Namespace,
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ProfileLabelName: profile.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Name:       profile.Name,
						Kind:       configv1beta1.ProfileKind,
						APIVersion: configv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		clusterReport2 := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ProfileLabelName: profile.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Name:       profile.Name,
						Kind:       configv1beta1.ProfileKind,
						APIVersion: configv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		clusterReport3 := &configv1beta1.ClusterReport{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: profile.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Name:       profile.Name,
						Kind:       configv1beta1.ClusterProfileKind,
						APIVersion: configv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterReport1,
			clusterReport2,
			clusterReport3,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        &profile,
			ControllerName: "profile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.CleanClusterReports(context.TODO(), c, profileScope)).To(Succeed())
		// ClusterReport1 is gone
		currentClusterReport := &configv1beta1.ClusterReport{}
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterReport1.Namespace, Name: clusterReport1.Name}, currentClusterReport)
		Expect(err).ToNot(BeNil())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		// ClusterReport2 is still present
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterReport2.Namespace, Name: clusterReport2.Name}, currentClusterReport)
		Expect(err).To(BeNil())

		// ClusterReport3 is still present
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterReport3.Namespace, Name: clusterReport3.Name}, currentClusterReport)
		Expect(err).To(BeNil())
	})

	It("cleanClusterSummaries removes all ClusterSummary instances created for a Profile instance", func() {
		profile := configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}
		Expect(addTypeInformationToObject(scheme, &profile)).To(Succeed())

		clusterSummary1 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: profile.Namespace,
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ProfileLabelName: profile.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Name:       profile.Name,
						Kind:       configv1beta1.ProfileKind,
						APIVersion: configv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		// clusterSummary2 is created by a Profile in a different namespace with same name as Profile
		clusterSummary2 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ProfileLabelName: profile.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Name:       profile.Name,
						Kind:       configv1beta1.ProfileKind,
						APIVersion: configv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		// clusterSummary3 is created by a ClusterProfile with same name as Profile
		clusterSummary3 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: profile.Name,
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						Name:       profile.Name,
						Kind:       configv1beta1.ClusterProfileKind,
						APIVersion: configv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary1,
			clusterSummary2,
			clusterSummary3,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        &profile,
			ControllerName: "profile",
		})
		Expect(err).To(BeNil())

		err = controllers.CleanClusterSummaries(context.TODO(), c, profileScope)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("clusterSummaries still present"))

		// ClusterSummary1 is gone
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary1.Namespace, Name: clusterSummary1.Name},
			currentClusterSummary)
		Expect(err).ToNot(BeNil())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		// ClusterSummary2 is still present as in different namespace
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary2.Namespace, Name: clusterSummary2.Name},
			currentClusterSummary)
		Expect(err).To(BeNil())

		// ClusterSummary3 is still present as created by ClusterProfile
		err = c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary3.Namespace, Name: clusterSummary3.Name},
			currentClusterSummary)
		Expect(err).To(BeNil())
	})

	It("getMaxUpdate returns max value of clusters that can be updated (fixed)", func() {
		const maxUpdate = int32(10)
		clusterProfile.Spec.MaxUpdate = &intstr.IntOrString{Type: intstr.Int, IntVal: maxUpdate}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.GetMaxUpdate(clusterProfileScope)).To(Equal(maxUpdate))
	})

	It("getMaxUpdate returns max value of clusters that can be updated (percentage)", func() {
		const maxUpdate = 50
		clusterProfile.Spec.MaxUpdate = &intstr.IntOrString{Type: intstr.String, StrVal: fmt.Sprintf("%d%%", maxUpdate)}
		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{Namespace: randomString(), Name: randomString(), Kind: libsveltosv1beta1.SveltosClusterKind},
			{Namespace: randomString(), Name: randomString(), Kind: libsveltosv1beta1.SveltosClusterKind},
			{Namespace: randomString(), Name: randomString(), Kind: libsveltosv1beta1.SveltosClusterKind},
			{Namespace: randomString(), Name: randomString(), Kind: libsveltosv1beta1.SveltosClusterKind},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.GetMaxUpdate(clusterProfileScope)).To(Equal(int32(2)))
	})

	It("getMaxUpdate returns max value of clusters that can be updated (percentage)", func() {
		const maxUpdate = 30
		clusterProfile.Spec.MaxUpdate = &intstr.IntOrString{Type: intstr.String, StrVal: fmt.Sprintf("%d%%", maxUpdate)}
		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			{Namespace: randomString(), Name: randomString(), Kind: libsveltosv1beta1.SveltosClusterKind},
			{Namespace: randomString(), Name: randomString(), Kind: libsveltosv1beta1.SveltosClusterKind},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		Expect(controllers.GetMaxUpdate(clusterProfileScope)).To(Equal(int32(1)))
	})

	It("reviseUpdatedAndUpdatingClusters removes non matching clusters from ClusterProfile Updated/Updating Clusters",
		func() {
			cluster1 := types.NamespacedName{Namespace: randomString(), Name: randomString()}
			cluster2 := types.NamespacedName{Namespace: randomString(), Name: randomString()}
			clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
				{
					Namespace: cluster1.Namespace, Name: cluster1.Name,
					Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
				},
				{
					Namespace: cluster2.Namespace, Name: cluster2.Name,
					Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
				},
				{
					Namespace: randomString(), Name: randomString(),
					Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
				},
			}
			clusterProfile.Status.UpdatedClusters = configv1beta1.Clusters{
				Hash: []byte(randomString()),
				Clusters: []corev1.ObjectReference{
					{
						Namespace: cluster1.Namespace, Name: cluster1.Name,
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			}
			clusterProfile.Status.UpdatingClusters = configv1beta1.Clusters{
				Hash: []byte(randomString()),
				Clusters: []corev1.ObjectReference{
					{
						Namespace: cluster2.Namespace, Name: cluster2.Name,
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: randomString(), Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			}

			c := fake.NewClientBuilder().WithScheme(scheme).Build()

			clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
				Client:         c,
				Logger:         logger,
				Profile:        clusterProfile,
				ControllerName: "clusterprofile",
			})
			Expect(err).To(BeNil())
			controllers.ReviseUpdatedAndUpdatingClusters(clusterProfileScope)

			Expect(len(clusterProfile.Status.UpdatedClusters.Clusters)).To(Equal(1))
			Expect(clusterProfile.Status.UpdatedClusters.Clusters).To(ContainElement(corev1.ObjectReference{
				Namespace: cluster1.Namespace, Name: cluster1.Name,
				Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
			}))

			Expect(len(clusterProfile.Status.UpdatingClusters.Clusters)).To(Equal(1))
			Expect(clusterProfile.Status.UpdatingClusters.Clusters).To(ContainElement(corev1.ObjectReference{
				Namespace: cluster2.Namespace, Name: cluster2.Name,
				Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
			}))
		})

	It("getUpdatedAndUpdatingClusters returns list of clusters already updated and being updated", func() {
		cluster1 := types.NamespacedName{Namespace: randomString(), Name: randomString()}
		cluster2 := types.NamespacedName{Namespace: randomString(), Name: randomString()}

		clusterProfile.Status.UpdatedClusters = configv1beta1.Clusters{
			Hash: []byte(randomString()),
			Clusters: []corev1.ObjectReference{
				{
					Namespace: cluster1.Namespace, Name: cluster1.Name,
					Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
				},
			},
		}
		clusterProfile.Status.UpdatingClusters = configv1beta1.Clusters{
			Hash: []byte(randomString()),
			Clusters: []corev1.ObjectReference{
				{
					Namespace: cluster2.Namespace, Name: cluster2.Name,
					Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
				},
			},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		// Not all Features are marked as provisioned
		updated, updating := controllers.GetUpdatedAndUpdatingClusters(clusterProfileScope)
		Expect(updated.Len()).To(Equal(1))
		Expect(updated.Has(&clusterProfile.Status.UpdatedClusters.Clusters[0])).To(BeTrue())

		Expect(updating.Len()).To(Equal(1))
		Expect(updating.Has(&clusterProfile.Status.UpdatingClusters.Clusters[0])).To(BeTrue())
	})

	It("updateClusterSummaries respects MaxUpdate field", func() {
		cluster1 := corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}
		sveltosCluster1 := libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster1.Namespace,
				Name:      cluster1.Name,
			},
			Status: libsveltosv1beta1.SveltosClusterStatus{
				Ready: true,
			},
		}

		cluster2 := corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}
		sveltosCluster2 := libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster2.Namespace,
				Name:      cluster2.Name,
			},
			Status: libsveltosv1beta1.SveltosClusterStatus{
				Ready: true,
			},
		}

		clusterProfile.Status.MatchingClusterRefs = []corev1.ObjectReference{
			cluster1, cluster2,
		}

		clusterProfile.Spec.MaxUpdate = &intstr.IntOrString{Type: intstr.Int, IntVal: 1}

		initObjects := []client.Object{
			clusterProfile,
			&sveltosCluster1,
			&sveltosCluster2,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterProfileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        clusterProfile,
			ControllerName: "clusterprofile",
		})
		Expect(err).To(BeNil())

		// Reports an error that not all clusters are being updated due to MaxUpdate policy
		Expect(controllers.UpdateClusterSummaries(context.TODO(), c, clusterProfileScope)).ToNot(BeNil())

		// Since MaxUpdate is set to 1 expect only one clusterSummary is created
		clusterSummaries := &configv1beta1.ClusterSummaryList{}
		Expect(c.List(context.TODO(), clusterSummaries)).To(Succeed())
		Expect(len(clusterSummaries.Items)).To(Equal(1))

		// Reset MaxUpdate to 2
		clusterProfile.Spec.MaxUpdate = &intstr.IntOrString{Type: intstr.Int, IntVal: 2}
		Expect(c.Update(context.TODO(), clusterProfile)).To(Succeed())

		Expect(controllers.UpdateClusterSummaries(context.TODO(), c, clusterProfileScope)).To(BeNil())

		// Since MaxUpdate is set to 2 expect two clusterSummaries are created
		Expect(c.List(context.TODO(), clusterSummaries)).To(Succeed())
		Expect(len(clusterSummaries.Items)).To(Equal(2))
	})
})
