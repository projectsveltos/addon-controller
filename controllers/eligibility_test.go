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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	license "github.com/projectsveltos/libsveltos/lib/licenses"
)

var _ = Describe("grantsPullModeEligibility", func() {
	logger := textlogger.NewLogger(textlogger.NewConfig())

	BeforeEach(func() {
		controllers.NewLicenseManager()
	})

	It("denies when Features is non-empty and does not include PullMode, even with a qualifying Plan", func() {
		result := &license.LicenseVerificationResult{
			IsValid: true,
			Payload: &license.LicensePayload{
				Features:    []license.Features{license.FeatureMCP},
				Plan:        license.PlanEnterprisePlus,
				MaxClusters: 0,
			},
		}
		Expect(controllers.GrantsPullModeEligibility(result, randomString(), randomString(), logger)).To(BeFalse())
	})

	It("denies when Features includes PullMode but Plan does not qualify", func() {
		result := &license.LicenseVerificationResult{
			IsValid: true,
			Payload: &license.LicensePayload{
				Features:    []license.Features{license.FeaturePullMode},
				MaxClusters: 0,
			},
		}
		Expect(controllers.GrantsPullModeEligibility(result, randomString(), randomString(), logger)).To(BeFalse())
	})

	It("grants unlimited clusters when Features includes PullMode, Plan qualifies, and MaxClusters is 0", func() {
		result := &license.LicenseVerificationResult{
			IsValid: true,
			Payload: &license.LicensePayload{
				Features:    []license.Features{license.FeaturePullMode},
				Plan:        license.PlanEnterprisePlus,
				MaxClusters: 0,
			},
		}
		Expect(controllers.GrantsPullModeEligibility(result, randomString(), randomString(), logger)).To(BeTrue())
	})

	It("grants when Features is empty (backward compatible), Plan qualifies, and MaxClusters is 0", func() {
		result := &license.LicenseVerificationResult{
			IsValid: true,
			Payload: &license.LicensePayload{Plan: license.PlanEnterprise, MaxClusters: 0},
		}
		Expect(controllers.GrantsPullModeEligibility(result, randomString(), randomString(), logger)).To(BeTrue())
	})

	It("only admits the first MaxClusters registered clusters when MaxClusters is set", func() {
		clusterNamespace := randomString()
		firstCluster := randomString()
		secondCluster := randomString()

		controllers.GetLicenseManager().AddCluster(&libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: firstCluster},
		})
		controllers.GetLicenseManager().AddCluster(&libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: secondCluster},
		})

		result := &license.LicenseVerificationResult{
			IsValid: true,
			Payload: &license.LicensePayload{
				Features:    []license.Features{license.FeaturePullMode},
				Plan:        license.PlanEnterprisePlus,
				MaxClusters: 1,
			},
		}
		Expect(controllers.GrantsPullModeEligibility(result, clusterNamespace, firstCluster, logger)).To(BeTrue())
		Expect(controllers.GrantsPullModeEligibility(result, clusterNamespace, secondCluster, logger)).To(BeFalse())
	})

	It("allows 2 free clusters when there is no valid license", func() {
		clusterNamespace := randomString()
		firstCluster := randomString()
		secondCluster := randomString()
		thirdCluster := randomString()

		controllers.GetLicenseManager().AddCluster(&libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: firstCluster},
		})
		controllers.GetLicenseManager().AddCluster(&libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: secondCluster},
		})
		controllers.GetLicenseManager().AddCluster(&libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: clusterNamespace, Name: thirdCluster},
		})

		result := &license.LicenseVerificationResult{}
		Expect(controllers.GrantsPullModeEligibility(result, clusterNamespace, firstCluster, logger)).To(BeTrue())
		Expect(controllers.GrantsPullModeEligibility(result, clusterNamespace, secondCluster, logger)).To(BeTrue())
		Expect(controllers.GrantsPullModeEligibility(result, clusterNamespace, thirdCluster, logger)).To(BeFalse())
	})
})

// grantsClusterPromotionEligibility itself (the license-validity + Features/Plan/top-X
// decision) moved to the Sveltos Enterprise library and is tested there. The top-X
// counting addon-controller still performs on its behalf stays here.
var _ = Describe("LicenseManager ClusterPromotion top-X", func() {
	BeforeEach(func() {
		controllers.NewLicenseManager()
	})

	It("allows only the top 2 free ClusterPromotions when there is no valid license", func() {
		first := &configv1beta1.ClusterPromotion{ObjectMeta: metav1.ObjectMeta{Name: randomString()}}
		second := &configv1beta1.ClusterPromotion{ObjectMeta: metav1.ObjectMeta{Name: randomString()}}
		third := &configv1beta1.ClusterPromotion{ObjectMeta: metav1.ObjectMeta{Name: randomString()}}

		controllers.GetLicenseManager().AddClusterPromotion(first)
		controllers.GetLicenseManager().AddClusterPromotion(second)
		controllers.GetLicenseManager().AddClusterPromotion(third)

		const maxFreeStages = 2
		Expect(controllers.GetLicenseManager().IsClusterPromotionInTopX("", first.Name, maxFreeStages)).To(BeTrue())
		Expect(controllers.GetLicenseManager().IsClusterPromotionInTopX("", second.Name, maxFreeStages)).To(BeTrue())
		Expect(controllers.GetLicenseManager().IsClusterPromotionInTopX("", third.Name, maxFreeStages)).To(BeFalse())
	})
})
