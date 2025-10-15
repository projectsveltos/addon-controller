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
	"bytes"
	"context"
	"fmt"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	ClusterSelectorField = "ClusterSelector"
)

var _ = Describe("ClusterPromotionController", func() {
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
	})

	It("ClusterPromotion exposes all ClusterProfile.Spec fields", func() {
		// With the exception of ClusterSelector,ClusterRefs and SetRefs
		// ClusterPromotion exposes all other fields defined in ClusterProfile.Spec

		// 1. Get the reflect.Type for the source struct
		profileSpecType := reflect.TypeOf(configv1beta1.Spec{})

		// 2. Get the reflect.Type for the embedded struct within ClusterPromotionSpec
		clusterPromotionProfileSpecField := reflect.TypeOf(configv1beta1.ProfileSpec{})

		for i := 0; i < profileSpecType.NumField(); i++ {
			sourceField := profileSpecType.Field(i)

			// Those fields are only present in ClusterProfile.Spec
			if sourceField.Name == ClusterSelectorField || sourceField.Name == "ClusterRefs" ||
				sourceField.Name == "SetRefs" {

				_, found := clusterPromotionProfileSpecField.FieldByName(sourceField.Name)
				if found {
					By(fmt.Sprintf("field %s must not be present in ClusterPromotion Spec", sourceField.Name))
				}
				Expect(found).To(BeFalse())
				continue
			}

			// Those fields are deprecated in ClusterProfile.Spec
			if sourceField.Name == "ExtraLabels" || sourceField.Name == "ExtraAnnotations" {
				_, found := clusterPromotionProfileSpecField.FieldByName(sourceField.Name)
				if found {
					By(fmt.Sprintf("field %s must not be present in ClusterPromotion Spec", sourceField.Name))
				}
				Expect(found).To(BeFalse())
				continue
			}

			targetField, found := clusterPromotionProfileSpecField.FieldByName(sourceField.Name)

			if !found {
				By(fmt.Sprintf("field %s is missing in ClusterPromotion Spec", sourceField.Name))
			}
			Expect(found).To(BeTrue())

			// Verify that the data type of the field matches
			if sourceField.Type != targetField.Type {
				By(fmt.Sprintf("Field %s.%s has a type mismatch: Expected %v, Got %v.",
					profileSpecType.Name(), sourceField.Name, sourceField.Type, targetField.Type))
			}
			Expect(sourceField.Type).To(Equal(targetField.Type))
		}
	})

	It("Calculate Stages  hash", func() {
		profileSpec := &configv1beta1.ProfileSpec{}

		stage1 := configv1beta1.Stage{
			Name: randomString(),
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{randomString(): randomString()},
				},
			},
		}

		stage2 := configv1beta1.Stage{
			Name: randomString(),
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{randomString(): randomString()},
				},
			},
		}

		promotionSpec := &configv1beta1.ClusterPromotionSpec{
			ProfileSpec: *profileSpec,
			Stages:      []configv1beta1.Stage{stage1, stage2},
		}

		reconciler := controllers.ClusterPromotionReconciler{}

		hash, err := controllers.GetStagesHash(&reconciler, promotionSpec)
		Expect(err).To(BeNil())
		Expect(hash).ToNot(BeNil())

		// order of stages does matter
		promotionSpec = &configv1beta1.ClusterPromotionSpec{
			ProfileSpec: *profileSpec,
			Stages:      []configv1beta1.Stage{stage2, stage1},
		}

		newHash, err := controllers.GetStagesHash(&reconciler, promotionSpec)
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, newHash)).To(BeFalse())
	})

	It("Calculate Stages hash: trigger changes are not included", func() {
		profileSpec := &configv1beta1.ProfileSpec{}

		notApproved := false
		stage1 := configv1beta1.Stage{
			Name: randomString(),
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{randomString(): randomString()},
				},
			},
			Trigger: &configv1beta1.Trigger{
				Manual: &configv1beta1.ManualTrigger{
					AutomaticReset: true,
					Approved:       &notApproved,
				},
			},
		}

		stage2 := configv1beta1.Stage{
			Name: randomString(),
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{randomString(): randomString()},
				},
			},
			Trigger: &configv1beta1.Trigger{
				Manual: &configv1beta1.ManualTrigger{
					AutomaticReset: true,
					Approved:       &notApproved,
				},
			},
		}

		promotionSpec := &configv1beta1.ClusterPromotionSpec{
			ProfileSpec: *profileSpec,
			Stages:      []configv1beta1.Stage{stage1, stage2},
		}

		reconciler := controllers.ClusterPromotionReconciler{}

		hash, err := controllers.GetStagesHash(&reconciler, promotionSpec)
		Expect(err).To(BeNil())
		Expect(hash).ToNot(BeNil())

		// change stage1 trigger
		approved := true
		stage1.Trigger.Manual.Approved = &approved

		promotionSpec = &configv1beta1.ClusterPromotionSpec{
			ProfileSpec: *profileSpec,
			Stages:      []configv1beta1.Stage{stage1, stage2},
		}

		newHash, err := controllers.GetStagesHash(&reconciler, promotionSpec)
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, newHash)).To(BeTrue())
	})

	It("Calculate Stages hash: non trigger changes are included", func() {
		profileSpec := &configv1beta1.ProfileSpec{}

		notApproved := false
		stage1 := configv1beta1.Stage{
			Name: randomString(),
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{randomString(): randomString()},
				},
			},
			Trigger: &configv1beta1.Trigger{
				Manual: &configv1beta1.ManualTrigger{
					AutomaticReset: true,
					Approved:       &notApproved,
				},
			},
		}

		stage2 := configv1beta1.Stage{
			Name: randomString(),
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{randomString(): randomString()},
				},
			},
			Trigger: &configv1beta1.Trigger{
				Manual: &configv1beta1.ManualTrigger{
					AutomaticReset: true,
					Approved:       &notApproved,
				},
			},
		}

		promotionSpec := &configv1beta1.ClusterPromotionSpec{
			ProfileSpec: *profileSpec,
			Stages:      []configv1beta1.Stage{stage1, stage2},
		}

		reconciler := controllers.ClusterPromotionReconciler{}

		hash, err := controllers.GetStagesHash(&reconciler, promotionSpec)
		Expect(err).To(BeNil())
		Expect(hash).ToNot(BeNil())

		// change stage1 trigger
		stage1.ClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{randomString(): randomString()},
			},
		}

		promotionSpec = &configv1beta1.ClusterPromotionSpec{
			ProfileSpec: *profileSpec,
			Stages:      []configv1beta1.Stage{stage1, stage2},
		}

		newHash, err := controllers.GetStagesHash(&reconciler, promotionSpec)
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, newHash)).To(BeFalse())
	})

	It("Calculate ProfileSpec hash", func() {
		hc1 := configv1beta1.HelmChart{
			RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
			ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
		}

		hc2 := configv1beta1.HelmChart{
			RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
			ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
		}

		pr1 := configv1beta1.PolicyRef{
			Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		pr2 := configv1beta1.PolicyRef{
			Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.SecretReferencedResourceKind),
		}

		kr1 := configv1beta1.KustomizationRef{
			Namespace: randomString(), Name: randomString(), Kind: sourcev1.GitRepositoryKind,
		}

		kr2 := configv1beta1.KustomizationRef{
			Namespace: randomString(), Name: randomString(), Kind: sourcev1.GitRepositoryKind,
		}

		dependency1 := randomString()
		dependency2 := randomString()

		profileSpec := &configv1beta1.ProfileSpec{
			DependsOn:         []string{dependency1, dependency2},
			HelmCharts:        []configv1beta1.HelmChart{hc1, hc2},
			PolicyRefs:        []configv1beta1.PolicyRef{pr1, pr2},
			KustomizationRefs: []configv1beta1.KustomizationRef{kr1, kr2},
		}

		reconciler := controllers.ClusterPromotionReconciler{}

		hash, err := controllers.GetProfileSpecHash(&reconciler, profileSpec)
		Expect(err).To(BeNil())
		Expect(hash).ToNot(BeNil())

		// order does not matter
		profileSpec = &configv1beta1.ProfileSpec{
			DependsOn:         []string{dependency2, dependency1},
			HelmCharts:        []configv1beta1.HelmChart{hc2, hc1},
			PolicyRefs:        []configv1beta1.PolicyRef{pr2, pr1},
			KustomizationRefs: []configv1beta1.KustomizationRef{kr2, kr1},
		}

		newHash, err := controllers.GetProfileSpecHash(&reconciler, profileSpec)
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, newHash)).To(BeTrue())
	})

	It("profileSpecChanged detects ProfileSpec changes", func() {
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterPromotionSpec{
				ProfileSpec: configv1beta1.ProfileSpec{
					HelmCharts: []configv1beta1.HelmChart{
						{
							RepositoryURL: randomString(), ChartName: randomString(),
							ChartVersion: randomString(), ReleaseName: randomString(),
							ReleaseNamespace: randomString(), RepositoryName: randomString(),
						},
					},
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: randomString(), Name: randomString(),
							Kind: string(libsveltosv1beta1.SecretReferencedResourceKind),
						},
					},
				},
			},
			Status: configv1beta1.ClusterPromotionStatus{
				ProfileSpecHash: []byte(randomString()),
			},
		}

		reconciler := controllers.ClusterPromotionReconciler{}
		promotionScope := scope.ClusterPromotionScope{
			ClusterPromotion: clusterPromotion,
		}

		changed, profileSpecHash, err := controllers.ProfileSpecChanged(&reconciler, &promotionScope)
		Expect(err).To(BeNil())
		Expect(changed).To(BeTrue())

		currentHash, err := controllers.GetProfileSpecHash(&reconciler, &clusterPromotion.Spec.ProfileSpec)
		Expect(err).To(BeNil())
		Expect(bytes.Equal(currentHash, profileSpecHash)).To(BeTrue())
		clusterPromotion.Status.ProfileSpecHash = currentHash

		changed, profileSpecHash, err = controllers.ProfileSpecChanged(&reconciler, &promotionScope)
		Expect(err).To(BeNil())
		Expect(changed).To(BeFalse())
		Expect(bytes.Equal(currentHash, profileSpecHash)).To(BeTrue())
	})

	It("stagesChanged detects Stages changes", func() {
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterPromotionSpec{
				Stages: []configv1beta1.Stage{
					{
						Name: randomString(),
						ClusterSelector: libsveltosv1beta1.Selector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{randomString(): randomString()},
							},
						},
					},
					{
						Name: randomString(),
						ClusterSelector: libsveltosv1beta1.Selector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{randomString(): randomString()},
							},
						},
					},
				},
			},
			Status: configv1beta1.ClusterPromotionStatus{
				StagesHash: []byte(randomString()),
			},
		}

		reconciler := controllers.ClusterPromotionReconciler{}
		promotionScope := scope.ClusterPromotionScope{
			ClusterPromotion: clusterPromotion,
		}

		changed, stagesHash, err := controllers.StagesChanged(&reconciler, &promotionScope)
		Expect(err).To(BeNil())
		Expect(changed).To(BeTrue())

		currentHash, err := controllers.GetStagesHash(&reconciler, &clusterPromotion.Spec)
		Expect(err).To(BeNil())
		Expect(bytes.Equal(currentHash, stagesHash)).To(BeTrue())
		clusterPromotion.Status.StagesHash = currentHash

		changed, stagesHash, err = controllers.StagesChanged(&reconciler, &promotionScope)
		Expect(err).To(BeNil())
		Expect(bytes.Equal(currentHash, stagesHash)).To(BeTrue())
		Expect(changed).To(BeFalse())
	})

	It("reconcileStageProfile creates/updates ClusterProfile", func() {
		hc1 := configv1beta1.HelmChart{
			RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
			ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
		}

		pr1 := configv1beta1.PolicyRef{
			Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
		}

		pr2 := configv1beta1.PolicyRef{
			Namespace: randomString(), Name: randomString(), Kind: string(libsveltosv1beta1.SecretReferencedResourceKind),
		}

		kr1 := configv1beta1.KustomizationRef{
			Namespace: randomString(), Name: randomString(), Kind: sourcev1.GitRepositoryKind,
		}

		check := libsveltosv1beta1.ValidateHealth{
			Group:   "",
			Version: "v1",
			Kind:    "Pod",
			LabelFilters: []libsveltosv1beta1.LabelFilter{
				{Key: randomString(), Value: randomString(), Operation: libsveltosv1beta1.OperationEqual},
			},
			Name:      randomString(),
			FeatureID: libsveltosv1beta1.FeatureHelm,
		}

		const tier = 90
		var maxConsecutiveFailures = uint(10)

		profileSpec := &configv1beta1.ProfileSpec{
			DependsOn:              []string{randomString(), randomString()},
			HelmCharts:             []configv1beta1.HelmChart{hc1},
			PolicyRefs:             []configv1beta1.PolicyRef{pr1, pr2},
			KustomizationRefs:      []configv1beta1.KustomizationRef{kr1},
			ValidateHealths:        []libsveltosv1beta1.ValidateHealth{check},
			Tier:                   tier,
			SyncMode:               configv1beta1.SyncModeContinuousWithDriftDetection,
			ContinueOnConflict:     true,
			ContinueOnError:        true,
			MaxConsecutiveFailures: &maxConsecutiveFailures,
		}

		reconciler := controllers.ClusterPromotionReconciler{
			Client: testEnv.Client,
			Config: testEnv.Config,
			Scheme: testEnv.Scheme(),
		}

		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterPromotionSpec{
				ProfileSpec: *profileSpec,
				Stages: []configv1beta1.Stage{
					{
						Name: randomString(),
						ClusterSelector: libsveltosv1beta1.Selector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									randomString(): randomString(),
									randomString(): randomString(),
								},
							},
						},
					},
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), clusterPromotion)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterPromotion)).To(Succeed())

		promotionScope := scope.ClusterPromotionScope{
			ClusterPromotion: clusterPromotion,
			Logger:           logger,
		}

		stage := &clusterPromotion.Spec.Stages[0]
		Expect(controllers.ReconcileStageProfile(&reconciler, context.TODO(), &promotionScope,
			*stage)).To(Succeed())

		clusterProfileName := controllers.GetClusterProfileName(promotionScope.Name(), stage.Name)
		Eventually(func() error {
			currentClusterProfile := &configv1beta1.ClusterProfile{}
			return testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterProfileName},
				currentClusterProfile)
		}, timeout, pollingInterval).Should(BeNil())

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterProfileName},
			currentClusterProfile)).To(Succeed())

		verifyProfileSpecFields(&currentClusterProfile.Spec, &clusterPromotion.Spec.ProfileSpec,
			&stage.ClusterSelector)

		clusterPromotion.Spec.ProfileSpec.HelmCharts = append(currentClusterProfile.Spec.HelmCharts,
			configv1beta1.HelmChart{
				RepositoryURL: randomString(), ChartName: randomString(), ChartVersion: randomString(),
				ReleaseName: randomString(), ReleaseNamespace: randomString(), RepositoryName: randomString(),
			})

		Expect(controllers.ReconcileStageProfile(&reconciler, context.TODO(), &promotionScope,
			*stage)).To(Succeed())
		Eventually(func() bool {
			currentClusterProfile := &configv1beta1.ClusterProfile{}
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterProfileName},
				currentClusterProfile)
			if err != nil {
				return false
			}
			return reflect.DeepEqual(currentClusterProfile.Spec.HelmCharts,
				clusterPromotion.Spec.ProfileSpec.HelmCharts)
		}, timeout, pollingInterval).Should(BeFalse())
	})

	It("stage's methods update ClusterPromotions Status with stage statuses", func() {
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		stageName1 := randomString()
		controllers.AddStageStatus(clusterPromotion, stageName1)
		Expect(len(clusterPromotion.Status.Stages)).To(Equal(1))
		Expect(clusterPromotion.Status.Stages[0].Name).To(Equal(stageName1))
		Expect(clusterPromotion.Status.Stages[0].LastUpdateReconciledTime).ToNot(BeNil())
		Expect(clusterPromotion.Status.Stages[0].LastStatusCheckTime).To(BeNil())
		Expect(clusterPromotion.Status.Stages[0].LastSuccessfulAppliedTime).To(BeNil())

		failureMessage := randomString()
		controllers.UpdateStageStatus(clusterPromotion, stageName1, false, &failureMessage)
		Expect(len(clusterPromotion.Status.Stages)).To(Equal(1))
		Expect(clusterPromotion.Status.Stages[0].Name).To(Equal(stageName1))
		Expect(clusterPromotion.Status.Stages[0].LastUpdateReconciledTime).ToNot(BeNil())
		Expect(clusterPromotion.Status.Stages[0].LastStatusCheckTime).ToNot(BeNil())
		Expect(clusterPromotion.Status.Stages[0].LastSuccessfulAppliedTime).To(BeNil())
		Expect(clusterPromotion.Status.Stages[0].FailureMessage).ToNot(BeNil())
		Expect(*clusterPromotion.Status.Stages[0].FailureMessage).To(Equal(failureMessage))

		controllers.UpdateStageStatus(clusterPromotion, stageName1, true, nil)
		Expect(len(clusterPromotion.Status.Stages)).To(Equal(1))
		Expect(clusterPromotion.Status.Stages[0].Name).To(Equal(stageName1))
		Expect(clusterPromotion.Status.Stages[0].LastUpdateReconciledTime).ToNot(BeNil())
		Expect(clusterPromotion.Status.Stages[0].LastStatusCheckTime).ToNot(BeNil())
		Expect(clusterPromotion.Status.Stages[0].LastSuccessfulAppliedTime).ToNot(BeNil())
		Expect(clusterPromotion.Status.Stages[0].FailureMessage).To(BeNil())

		stageName2 := randomString()
		controllers.AddStageStatus(clusterPromotion, stageName2)
		Expect(len(clusterPromotion.Status.Stages)).To(Equal(2))
		Expect(clusterPromotion.Status.Stages[0].Name).To(Equal(stageName1))
		Expect(clusterPromotion.Status.Stages[1].Name).To(Equal(stageName2))
		Expect(clusterPromotion.Status.Stages[1].LastUpdateReconciledTime).ToNot(BeNil())
		Expect(clusterPromotion.Status.Stages[1].LastStatusCheckTime).To(BeNil())
		Expect(clusterPromotion.Status.Stages[1].LastSuccessfulAppliedTime).To(BeNil())

		controllers.ResetStageStatuses(clusterPromotion)
		Expect(len(clusterPromotion.Status.Stages)).To(Equal(0))
	})

	It("checkCurrentStageDeployment verifies that all clusters matching a stage are provisined", func() {
		stageName := randomString()

		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: configv1beta1.ClusterPromotionStatus{
				CurrentStageName: stageName,
			},
		}

		// ClusterProfile matching a clusterPromotion stage
		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: controllers.GetClusterProfileName(clusterPromotion.Name, stageName),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{randomString(): randomString()},
					},
				},
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Namespace: randomString(), Name: randomString(),
						Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					},
				},
				HelmCharts: []configv1beta1.HelmChart{
					{
						RepositoryURL: randomString(), ChartName: randomString(),
						ChartVersion: randomString(), ReleaseName: randomString(),
						ReleaseNamespace: randomString(), RepositoryName: randomString(),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterProfile)).To(Succeed())

		// Create two ClusterSummary instance for the above ClusterProfile
		clusterSummary1 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfile.Name,
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: clusterProfile.Spec.PolicyRefs,
					HelmCharts: clusterProfile.Spec.HelmCharts,
				},
			},
			Status: configv1beta1.ClusterSummaryStatus{
				FeatureSummaries: []configv1beta1.FeatureSummary{
					{
						FeatureID: libsveltosv1beta1.FeatureResources,
						Status:    libsveltosv1beta1.FeatureStatusProvisioned,
						Hash:      []byte(randomString()),
					},
					{
						FeatureID: libsveltosv1beta1.FeatureHelm,
						Status:    libsveltosv1beta1.FeatureStatusProvisioned,
						Hash:      []byte(randomString()),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterSummary1)).To(Succeed())

		clusterSummary2 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfile.Name,
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: clusterProfile.Spec.PolicyRefs,
				},
			},
			Status: configv1beta1.ClusterSummaryStatus{
				FeatureSummaries: []configv1beta1.FeatureSummary{
					{
						FeatureID: libsveltosv1beta1.FeatureHelm,
						Status:    libsveltosv1beta1.FeatureStatusProvisioning,
						Hash:      []byte(randomString())},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterSummary2)).To(Succeed())

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary1,
			clusterSummary2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := controllers.ClusterPromotionReconciler{Client: c}
		promotionScope := scope.ClusterPromotionScope{
			ClusterPromotion: clusterPromotion,
		}

		deployed, msg, err := controllers.CheckCurrentStageDeployment(&reconciler, context.TODO(), &promotionScope, logger)
		Expect(err).To(BeNil())
		Expect(deployed).To(BeFalse())
		Expect(msg).To(Equal(fmt.Sprintf("ClusterSummary HelmCharts %s is not in sync.", clusterSummary2.Name)))

		clusterSummary2.Spec.ClusterProfileSpec = configv1beta1.Spec{
			PolicyRefs: clusterProfile.Spec.PolicyRefs,
			HelmCharts: clusterProfile.Spec.HelmCharts,
		}

		Expect(c.Update(context.TODO(), clusterSummary2)).To(Succeed())
		deployed, msg, err = controllers.CheckCurrentStageDeployment(&reconciler, context.TODO(), &promotionScope, logger)
		Expect(err).To(BeNil())
		Expect(deployed).To(BeFalse())
		Expect(msg).To(Equal(fmt.Sprintf("ClusterSummary %s is not yet provisioned.", clusterSummary2.Name)))

		clusterSummary2.Status = configv1beta1.ClusterSummaryStatus{
			FeatureSummaries: []configv1beta1.FeatureSummary{
				{
					FeatureID: libsveltosv1beta1.FeatureResources,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
					Hash:      []byte(randomString()),
				},
				{
					FeatureID: libsveltosv1beta1.FeatureHelm,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
					Hash:      []byte(randomString()),
				},
			},
		}
		Expect(c.Status().Update(context.TODO(), clusterSummary2)).To(Succeed())
		deployed, msg, err = controllers.CheckCurrentStageDeployment(&reconciler, context.TODO(), &promotionScope, logger)
		Expect(err).To(BeNil())
		Expect(deployed).To(BeTrue())
		Expect(msg).To(Equal("All matching clusters are successfully deployed."))
	})

	It("getNextStage returns the next stage to provision", func() {
		stage1 := randomString()
		stage2 := randomString()
		stage3 := randomString()

		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterPromotionSpec{
				Stages: []configv1beta1.Stage{
					{Name: stage1},
					{Name: stage2},
					{Name: stage3},
				},
			},
			Status: configv1beta1.ClusterPromotionStatus{
				CurrentStageName: stage1,
			},
		}

		reconciler := controllers.ClusterPromotionReconciler{}

		nextStage := controllers.GetNextStage(&reconciler, clusterPromotion, stage1)
		Expect(nextStage).ToNot(BeNil())
		Expect(nextStage.Name).To(Equal(stage2))

		clusterPromotion.Status.CurrentStageName = stage2
		nextStage = controllers.GetNextStage(&reconciler, clusterPromotion, stage2)
		Expect(nextStage).ToNot(BeNil())
		Expect(nextStage.Name).To(Equal(stage3))

		clusterPromotion.Status.CurrentStageName = stage3
		nextStage = controllers.GetNextStage(&reconciler, clusterPromotion, stage3)
		Expect(nextStage).To(BeNil())
	})

	It("canAutoAdvance returns true when advancing to next stage is allowed", func() {
		oneHour, err := time.ParseDuration("1h")
		Expect(err).To(BeNil())

		stage1 := randomString()
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterPromotionSpec{
				Stages: []configv1beta1.Stage{
					{
						Name: stage1,
						Trigger: &configv1beta1.Trigger{
							Auto: &configv1beta1.AutoTrigger{
								Delay: &metav1.Duration{Duration: oneHour},
							},
						},
					},
				},
			},
			Status: configv1beta1.ClusterPromotionStatus{
				CurrentStageName: stage1,
				Stages: []configv1beta1.StageStatus{
					{
						Name:                      stage1,
						LastSuccessfulAppliedTime: &metav1.Time{Time: time.Now()},
					},
				},
			},
		}

		// ClusterProfile matching a clusterPromotion stage
		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: controllers.GetClusterProfileName(clusterPromotion.Name, stage1),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{randomString(): randomString()},
					},
				},
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Namespace: randomString(), Name: randomString(),
						Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					},
				},
				HelmCharts: []configv1beta1.HelmChart{
					{
						RepositoryURL: randomString(), ChartName: randomString(),
						ChartVersion: randomString(), ReleaseName: randomString(),
						ReleaseNamespace: randomString(), RepositoryName: randomString(),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterProfile)).To(Succeed())

		// Create two ClusterSummary instance for the above ClusterProfile
		clusterSummary1 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfile.Name,
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: clusterProfile.Spec.PolicyRefs,
					HelmCharts: clusterProfile.Spec.HelmCharts,
				},
			},
			Status: configv1beta1.ClusterSummaryStatus{
				FeatureSummaries: []configv1beta1.FeatureSummary{
					{
						FeatureID: libsveltosv1beta1.FeatureResources,
						Status:    libsveltosv1beta1.FeatureStatusProvisioned,
						Hash:      []byte(randomString()),
					},
					{
						FeatureID: libsveltosv1beta1.FeatureHelm,
						Status:    libsveltosv1beta1.FeatureStatusProvisioned,
						Hash:      []byte(randomString()),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterSummary1)).To(Succeed())

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary1,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := controllers.ClusterPromotionReconciler{Client: c}

		canAdvance, err := controllers.CanAutoAdvance(&reconciler, context.TODO(), clusterPromotion,
			stage1, clusterPromotion.Spec.Stages[0].Trigger.Auto, logger)
		Expect(err).To(BeNil())
		Expect(canAdvance).To(BeFalse()) // delay is set to one hour

		twoHoursAgo := time.Now().Add(-2 * time.Hour)
		clusterPromotion.Status.Stages = []configv1beta1.StageStatus{
			{
				Name:                      stage1,
				LastSuccessfulAppliedTime: &metav1.Time{Time: twoHoursAgo},
			},
		}

		canAdvance, err = controllers.CanAutoAdvance(&reconciler, context.TODO(), clusterPromotion,
			stage1, clusterPromotion.Spec.Stages[0].Trigger.Auto, logger)
		Expect(err).To(BeNil())
		Expect(canAdvance).To(BeTrue()) // delay is set to one hour and LastSuccessfulAppliedTime was set to 2 hours back
	})

	It("canManulAdvance returns true when advancing to next stage is allowed", func() {
		stage1 := randomString()
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterPromotionSpec{
				Stages: []configv1beta1.Stage{
					{
						Name: stage1,
						Trigger: &configv1beta1.Trigger{
							Manual: &configv1beta1.ManualTrigger{},
						},
					},
				},
			},
			Status: configv1beta1.ClusterPromotionStatus{
				CurrentStageName: stage1,
				Stages: []configv1beta1.StageStatus{
					{
						Name:                      stage1,
						LastSuccessfulAppliedTime: &metav1.Time{Time: time.Now()},
					},
				},
			},
		}

		// ClusterProfile matching a clusterPromotion stage
		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: controllers.GetClusterProfileName(clusterPromotion.Name, stage1),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{randomString(): randomString()},
					},
				},
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Namespace: randomString(), Name: randomString(),
						Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					},
				},
				HelmCharts: []configv1beta1.HelmChart{
					{
						RepositoryURL: randomString(), ChartName: randomString(),
						ChartVersion: randomString(), ReleaseName: randomString(),
						ReleaseNamespace: randomString(), RepositoryName: randomString(),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterProfile)).To(Succeed())

		// Create two ClusterSummary instance for the above ClusterProfile
		clusterSummary1 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
				Labels: map[string]string{
					clusterops.ClusterProfileLabelName: clusterProfile.Name,
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: clusterProfile.Spec.PolicyRefs,
					HelmCharts: clusterProfile.Spec.HelmCharts,
				},
			},
			Status: configv1beta1.ClusterSummaryStatus{
				FeatureSummaries: []configv1beta1.FeatureSummary{
					{
						FeatureID: libsveltosv1beta1.FeatureResources,
						Status:    libsveltosv1beta1.FeatureStatusProvisioned,
						Hash:      []byte(randomString()),
					},
					{
						FeatureID: libsveltosv1beta1.FeatureHelm,
						Status:    libsveltosv1beta1.FeatureStatusProvisioned,
						Hash:      []byte(randomString()),
					},
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterSummary1)).To(Succeed())

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary1,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := controllers.ClusterPromotionReconciler{Client: c}

		canAdvance := controllers.CanManualAdvance(&reconciler,
			stage1, clusterPromotion.Spec.Stages[0].Trigger.Manual, logger)
		Expect(canAdvance).To(BeFalse()) // Approved is not set in ManualTrigger

		approved := true

		clusterPromotion.Spec = configv1beta1.ClusterPromotionSpec{
			Stages: []configv1beta1.Stage{
				{
					Name: stage1,
					Trigger: &configv1beta1.Trigger{
						Manual: &configv1beta1.ManualTrigger{
							Approved: &approved,
						},
					},
				},
			},
		}

		canAdvance = controllers.CanManualAdvance(&reconciler,
			stage1, clusterPromotion.Spec.Stages[0].Trigger.Manual, logger)
		Expect(canAdvance).To(BeTrue()) // Approved is set to true in ManualTrigger
	})

	It("cleanClusterProfiles deletes all ClusterProfile instances created by a ClusterPromotion", func() {
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}

		clusterProfile1 := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   controllers.GetClusterProfileName(clusterPromotion.Name, randomString()),
				Labels: controllers.GetClusterPromotionLabels(clusterPromotion)},
		}
		clusterProfile2 := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   controllers.GetClusterProfileName(clusterPromotion.Name, randomString()),
				Labels: controllers.GetClusterPromotionLabels(clusterPromotion)},
		}
		clusterProfile3 := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:   controllers.GetClusterProfileName(clusterPromotion.Name, randomString()),
				Labels: controllers.GetClusterPromotionLabels(clusterPromotion)},
		}

		initObjects := []client.Object{
			clusterProfile1,
			clusterProfile2,
			clusterProfile3,
			clusterPromotion,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := controllers.ClusterPromotionReconciler{Client: c}
		Expect(controllers.CleanClusterProfiles(&reconciler, context.TODO(), clusterPromotion)).To(Succeed())

		listOptions := []client.ListOption{
			client.MatchingLabels(controllers.GetClusterPromotionLabels(clusterPromotion)),
		}
		clusterProfiles := &configv1beta1.ClusterProfileList{}
		Expect(c.List(ctx, clusterProfiles, listOptions...)).To(Succeed())
		Expect(len(clusterProfiles.Items)).To(BeZero())
	})

	Context("When the current time is inside the promotion window", func() {
		// Window: Open 10:00 AM daily, Close 11:00 AM daily
		openCron := "0 10 * * *"
		closeCron := "0 11 * * *"
		var window *configv1beta1.TimeWindow

		It("should return true when anchored 5 minutes after opening", func() {
			window = &configv1beta1.TimeWindow{From: openCron, To: closeCron}

			// Set 'now' to be exactly 10:05 AM in the local time zone
			anchorTime := time.Date(2025, time.October, 7, 10, 5, 0, 0, time.Local)

			reconciler := &controllers.ClusterPromotionReconciler{}
			isOpen, nextTime, err := controllers.IsPromotionWindowOpen(reconciler, window, anchorTime, logger)

			Expect(err).NotTo(HaveOccurred())
			Expect(isOpen).To(BeTrue())

			// Next scheduled event should be the window close time: 11:00 AM
			expectedCloseTime := time.Date(2025, time.October, 7, 11, 0, 0, 0, time.Local)
			Expect(*nextTime).To(BeTemporally("==", expectedCloseTime))
		})
	})

	Context("When the current time is outside the promotion window", func() {
		// Use the same window: Open 10:00 AM, Close 11:00 AM
		openCron := "0 20 * * *"
		closeCron := "0 22 * * *"
		var window *configv1beta1.TimeWindow

		It("should return false and point to the next open time", func() {
			window = &configv1beta1.TimeWindow{From: openCron, To: closeCron}

			// Set 'now' to be exactly 19:30 PM (before the 10:00 PM opening)
			anchorTime := time.Date(2025, time.October, 7, 19, 30, 0, 0, time.Local)

			reconciler := &controllers.ClusterPromotionReconciler{}
			isOpen, nextTime, err := controllers.IsPromotionWindowOpen(reconciler, window, anchorTime, logger)

			Expect(err).NotTo(HaveOccurred())
			Expect(isOpen).To(BeFalse())

			// Next scheduled event should be the window open time: 10:00 AM
			expectedOpenTime := time.Date(2025, time.October, 7, 20, 0, 0, 0, time.Local)
			Expect(*nextTime).To(BeTemporally("==", expectedOpenTime))
		})
	})

	Context("When the current time is well before the next promotion window", func() {
		openCron := "0 10 * * 6"  // Opens Saturday at 10:00 AM (Hour 10, Day of Week 6 = Saturday)
		closeCron := "0 22 * * 0" // Closes Sunday at 10:00 PM (Hour 22, Day of Week 0 = Sunday)
		var window *configv1beta1.TimeWindow

		It("should return false and point to the next open time", func() {
			window = &configv1beta1.TimeWindow{From: openCron, To: closeCron}

			// October, 7th, 2025 is a Tuesday
			anchorTime := time.Date(2025, time.October, 7, 17, 30, 0, 0, time.Local)

			reconciler := &controllers.ClusterPromotionReconciler{}
			isOpen, nextTime, err := controllers.IsPromotionWindowOpen(reconciler, window, anchorTime, logger)

			Expect(err).NotTo(HaveOccurred())
			Expect(isOpen).To(BeFalse())

			// Next scheduled event should be the window open time: 10:00 AM
			expectedOpenTime := time.Date(2025, time.October, 11, 10, 0, 0, 0, time.Local)
			Expect(*nextTime).To(BeTemporally("==", expectedOpenTime))
		})
	})
})

func verifyProfileSpecFields(
	actualSpec *configv1beta1.Spec,
	expectedProfileSpec *configv1beta1.ProfileSpec,
	stageSelector *libsveltosv1beta1.Selector,
) {

	actualVal := reflect.ValueOf(*actualSpec)
	expectedVal := reflect.ValueOf(*expectedProfileSpec)

	// Ensure the types are structs before proceeding
	if actualVal.Kind() != reflect.Struct || expectedVal.Kind() != reflect.Struct {
		Fail("Inputs must be struct types for comparison.")
		return
	}

	// Iterate over the fields of the expected ProfileSpec (the template)
	for i := 0; i < expectedVal.NumField(); i++ {
		expectedField := expectedVal.Field(i)
		fieldName := expectedVal.Type().Field(i).Name

		// Find the field in the actual ClusterProfile Spec by name
		actualField := actualVal.FieldByName(fieldName)

		// Skip the ClusterSelector field, as it is derived from the stage, not ProfileSpec
		if fieldName == ClusterSelectorField {
			// Special case: Verify the actual ClusterSelector matches the STAGE selector
			Expect(actualField.Interface()).To(
				Equal(stageSelector),
				"Expected ClusterSelector to match the stage selector",
			)
			continue
		}

		// Skip invalid fields in actualSpec (i.e., fields that don't exist)
		if !actualField.IsValid() {
			continue
		}

		// Handle different types of comparison
		expectedInterface := expectedField.Interface()
		actualInterface := actualField.Interface()

		// If the field type is a slice, use DeepEqual for a more accurate comparison
		if reflect.TypeOf(expectedInterface).Kind() == reflect.Slice {
			Expect(reflect.DeepEqual(actualInterface, expectedInterface)).To(
				BeTrue(),
				"Field '%s' value mismatch. Expected: %v, Actual: %v", fieldName, expectedInterface, actualInterface,
			)
		} else {
			Expect(actualInterface).To(
				Equal(expectedInterface),
				"Field '%s' value mismatch. Expected: %v, Actual: %v", fieldName, expectedInterface, actualInterface,
			)
		}
	}
}
