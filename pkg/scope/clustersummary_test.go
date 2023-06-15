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

package scope_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/pkg/scope"
)

const (
	clusterSummaryNamePrefix = "scope-"
	failedToDeploy           = "failed to deploy"
	apiserverNotReachable    = "apiserver not reachable"
)

var _ = Describe("ClusterSummaryScope", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
	var clusterSummary *configv1alpha1.ClusterSummary
	var c client.Client

	BeforeEach(func() {
		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
		}

		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryNamePrefix + randomString(),
			},
		}

		scheme := setupScheme()
		initObjects := []client.Object{clusterProfile, clusterSummary}
		c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

	})

	It("Return nil,error if ClusterSummary is not specified", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterProfile: clusterProfile,
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		params := scope.ClusterSummaryScopeParams{
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).To(HaveOccurred())
		Expect(scope).To(BeNil())
	})

	It("Name returns ClusterSummary Name", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.Name()).To(Equal(clusterSummary.Name))
	})

	It("SetFeatureStatus updates ClusterSummary Status FeatureSummary", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		hash := []byte(randomString())
		scope.SetFeatureStatus(configv1alpha1.FeatureResources, configv1alpha1.FeatureStatusProvisioned, hash)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureResources))
		Expect(clusterSummary.Status.FeatureSummaries[0].Hash).To(Equal(hash))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusProvisioned))
	})

	It("SetFailureMessage updates ClusterSummary Status FeatureSummary when not nil", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioned, Hash: []byte(randomString())},
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		found := false
		failureMessage := failedToDeploy
		scope.SetFailureMessage(configv1alpha1.FeatureResources, &failureMessage)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		for i := range clusterSummary.Status.FeatureSummaries {
			fs := clusterSummary.Status.FeatureSummaries[i]
			if fs.FeatureID == configv1alpha1.FeatureResources {
				found = true
				Expect(fs.FailureMessage).ToNot(BeNil())
				Expect(*fs.FailureMessage).To(Equal(failureMessage))
			}
		}
		Expect(found).To(Equal(true))
	})

	It("SetFailureMessage updates ClusterSummary Status FeatureSummary when nil", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		failureMessage := failedToDeploy
		scope.SetFailureMessage(configv1alpha1.FeatureHelm, &failureMessage)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureHelm))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(Equal(failureMessage))
	})

	It("SetFeatureStatus updates ClusterSummary Status FeatureSummary when not nil", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusProvisioned, Hash: []byte(randomString())},
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		found := false
		hash := []byte(randomString())
		scope.SetFeatureStatus(configv1alpha1.FeatureResources, configv1alpha1.FeatureStatusProvisioning, hash)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(2))
		for i := range clusterSummary.Status.FeatureSummaries {
			fs := clusterSummary.Status.FeatureSummaries[i]
			if fs.FeatureID == configv1alpha1.FeatureResources {
				found = true
				Expect(fs.Status).To(Equal(configv1alpha1.FeatureStatusProvisioning))
			}
		}
		Expect(found).To(Equal(true))
	})

	It("SetFeatureStatus overriddes ClusterSummary Status FeatureSummary when not nil", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioned, Hash: []byte(randomString())},
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		hash := []byte(randomString())
		scope.SetFeatureStatus(configv1alpha1.FeatureResources, configv1alpha1.FeatureStatusProvisioning, hash)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusProvisioning))
		Expect(clusterSummary.Status.FeatureSummaries[0].Hash).To(Equal(hash))
	})

	It("SetFeatureStatus updates ClusterSummary Status FeatureSummary when nil", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		hash := []byte(randomString())
		scope.SetFeatureStatus(configv1alpha1.FeatureHelm, configv1alpha1.FeatureStatusProvisioning, hash)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureHelm))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusProvisioning))
	})

	It("SetFailureReason updates ClusterSummary Status FeatureSummary when not nil", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioned, Hash: []byte(randomString())},
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		found := false
		failureReason := apiserverNotReachable
		scope.SetFailureReason(configv1alpha1.FeatureHelm, &failureReason)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(2))
		for i := range clusterSummary.Status.FeatureSummaries {
			fs := clusterSummary.Status.FeatureSummaries[i]
			if fs.FeatureID == configv1alpha1.FeatureHelm {
				found = true
				Expect(fs.FailureReason).ToNot(BeNil())
				Expect(*fs.FailureReason).To(Equal(failureReason))
			}
		}
		Expect(found).To(Equal(true))
	})

	It("SetFailureReason updates ClusterSummary Status FeatureSummary when nil", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		failureReason := apiserverNotReachable
		scope.SetFailureReason(configv1alpha1.FeatureResources, &failureReason)
		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureResources))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureReason).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].FailureReason).To(Equal(failureReason))
	})

	It("Close updates ClusterSummary", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureHelm, Status: configv1alpha1.FeatureStatusProvisioned, Hash: []byte(randomString())},
		}
		Expect(scope.Close(context.TODO())).To(Succeed())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		Expect(currentClusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(currentClusterSummary.Status.FeatureSummaries)).To(Equal(1))
	})

	It("SetLastAppliedTime updates featureSummary with time (entry not existing yet)", func() {
		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		now := metav1.NewTime(time.Now())
		scope.SetLastAppliedTime(configv1alpha1.FeatureHelm, &now)

		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureHelm))
		Expect(clusterSummary.Status.FeatureSummaries[0].LastAppliedTime).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].LastAppliedTime).To(Equal(now))
	})

	It("SetLastAppliedTime updates featureSummary with time (entry existing)", func() {
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureResources, Status: configv1alpha1.FeatureStatusProvisioned},
		}

		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		now := metav1.NewTime(time.Now())
		scope.SetLastAppliedTime(configv1alpha1.FeatureResources, &now)

		Expect(clusterSummary.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureResources))
		Expect(clusterSummary.Status.FeatureSummaries[0].LastAppliedTime).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].LastAppliedTime).To(Equal(now))
	})

	It("IsContinuousSync returns true when mode is Continuous", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous

		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsContinuousSync()).To(BeTrue())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(scope.IsContinuousSync()).To(BeFalse())
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeOneTime
		Expect(scope.IsContinuousSync()).To(BeFalse())
	})

	It("IsOneTimeSync returns true when mode is OneTime", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeOneTime

		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsOneTimeSync()).To(BeTrue())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(scope.IsOneTimeSync()).To(BeFalse())
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(scope.IsOneTimeSync()).To(BeFalse())
	})

	It("IsDryRunSync returns true when mode is DryRun", func() {
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun

		params := scope.ClusterSummaryScopeParams{
			Client:         c,
			ClusterProfile: clusterProfile,
			ClusterSummary: clusterSummary,
			Logger:         klogr.New(),
		}

		scope, err := scope.NewClusterSummaryScope(params)
		Expect(err).ToNot(HaveOccurred())
		Expect(scope).ToNot(BeNil())

		Expect(scope.IsDryRunSync()).To(BeTrue())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeContinuous
		Expect(scope.IsDryRunSync()).To(BeFalse())
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeOneTime
		Expect(scope.IsDryRunSync()).To(BeFalse())
	})
})
