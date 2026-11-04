/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

package fv_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Stage Promotions with manual approval", func() {
	const (
		namePrefix = "promotion-manual-"

		// Each stage is checked every couple of minutes so allow for a whole promotion.
		approvalTimeout = 10 * time.Minute

		// Third stage, selecting no cluster
		canaryValue = "canary"

		awaitingApproval = "Awaiting Manual Approval"

		payloadPolicy = `apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  version: v1`
	)

	getClusterPromotion := func(name string) *configv1beta1.ClusterPromotion {
		clusterPromotion := &configv1beta1.ClusterPromotion{}
		ExpectWithOffset(1, k8sClient.Get(context.TODO(), types.NamespacedName{Name: name},
			clusterPromotion)).To(Succeed())
		return clusterPromotion
	}

	getStageStatus := func(clusterPromotion *configv1beta1.ClusterPromotion, stageName string,
	) *configv1beta1.StageStatus {

		for i := range clusterPromotion.Status.Stages {
			if clusterPromotion.Status.Stages[i].Name == stageName {
				return &clusterPromotion.Status.Stages[i]
			}
		}
		return nil
	}

	// isAwaitingApproval returns true when the promotion is paused on the stage, waiting for approval
	isAwaitingApproval := func(name, stageName string) bool {
		clusterPromotion := getClusterPromotion(name)
		if clusterPromotion.Status.CurrentStageName != stageName {
			return false
		}

		stageStatus := getStageStatus(clusterPromotion, stageName)
		return stageStatus != nil && stageStatus.CurrentStatusDescription != nil &&
			strings.Contains(*stageStatus.CurrentStatusDescription, awaitingApproval)
	}

	// approveStage approves the promotion to the next stage, as a user would by editing the ClusterPromotion
	approveStage := func(name, stageName string) {
		Eventually(func() error {
			clusterPromotion := getClusterPromotion(name)
			for i := range clusterPromotion.Spec.Stages {
				stage := &clusterPromotion.Spec.Stages[i]
				if stage.Name != stageName {
					continue
				}
				approved := true
				stage.Trigger.Manual.Approved = &approved
				// The controller also writes the ClusterPromotion: retried on conflict
				return k8sClient.Update(context.TODO(), clusterPromotion)
			}
			return fmt.Errorf("stage %s not found", stageName)
		}, timeout, pollingInterval).Should(Succeed())
	}

	newManualStage := func(name string, selector map[string]string) configv1beta1.Stage {
		return configv1beta1.Stage{
			Name: name,
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{MatchLabels: selector},
			},
			Trigger: &configv1beta1.Trigger{
				Manual: &configv1beta1.ManualTrigger{AutomaticReset: true},
			},
		}
	}

	It("Advances through stages as each one is approved", Label("Enterprise"), func() {
		payloadName := namePrefix + randomString()

		Byf("Create the ConfigMap ClusterPromotion references")
		configMap := createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(),
			fmt.Sprintf(payloadPolicy, payloadName, defaultNamespace))
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		// The first stage selects the workload cluster. The next two select no cluster: they are
		// provisioned right away and only wait for the approval. The last one has no trigger.
		staging := newManualStage(stagingValue, map[string]string{key: value})
		production := newManualStage(productionValue, map[string]string{key: productionValue})
		canary := configv1beta1.Stage{
			Name: canaryValue,
			ClusterSelector: libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{key: canaryValue}},
			},
		}

		Byf("Create a ClusterPromotion with three stages, the first two requiring manual approval")
		clusterPromotion := &configv1beta1.ClusterPromotion{
			ObjectMeta: metav1.ObjectMeta{Name: namePrefix + randomString()},
			Spec: configv1beta1.ClusterPromotionSpec{
				ProfileSpec: configv1beta1.ProfileSpec{
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: configMap.Namespace,
							Name:      configMap.Name,
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
					},
				},
				Stages: []configv1beta1.Stage{staging, production, canary},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterPromotion)).To(Succeed())

		Byf("Verify the promotion is paused on stage %s waiting for approval", staging.Name)
		Eventually(func() bool {
			return isAwaitingApproval(clusterPromotion.Name, staging.Name)
		}, approvalTimeout, pollingInterval).Should(BeTrue())

		Byf("Approve stage %s", staging.Name)
		approveStage(clusterPromotion.Name, staging.Name)

		// The status must have an entry for the stage the promotion moves to. Without it the stage
		// can never be provisioned and the promotion is stuck.
		Byf("Verify the promotion moves to stage %s and waits for approval", production.Name)
		Eventually(func() bool {
			return isAwaitingApproval(clusterPromotion.Name, production.Name)
		}, approvalTimeout, pollingInterval).Should(BeTrue())

		Byf("Approve stage %s", production.Name)
		approveStage(clusterPromotion.Name, production.Name)

		Byf("Verify the promotion completes: all stages are provisioned")
		Eventually(func() bool {
			current := getClusterPromotion(clusterPromotion.Name)
			if current.Status.CurrentStageName != canary.Name {
				return false
			}
			for _, stageName := range []string{staging.Name, production.Name, canary.Name} {
				stageStatus := getStageStatus(current, stageName)
				if stageStatus == nil || stageStatus.LastSuccessfulAppliedTime == nil {
					return false
				}
			}
			return true
		}, approvalTimeout, pollingInterval).Should(BeTrue())

		Byf("Verify the status has one entry per stage")
		current := getClusterPromotion(clusterPromotion.Name)
		Expect(current.Status.Stages).To(HaveLen(len(clusterPromotion.Spec.Stages)))

		Byf("Verify the approvals were reset")
		for i := range current.Spec.Stages {
			trigger := current.Spec.Stages[i].Trigger
			if trigger != nil && trigger.Manual != nil {
				Expect(trigger.Manual.Approved == nil || !*trigger.Manual.Approved).To(BeTrue())
			}
		}

		Byf("Deleting ClusterPromotion %s", clusterPromotion.Name)
		Expect(k8sClient.Delete(context.TODO(), current)).To(Succeed())

		Byf("Verify ClusterProfiles are deleted")
		Eventually(func() bool {
			for _, stageName := range []string{staging.Name, production.Name, canary.Name} {
				clusterProfile := &configv1beta1.ClusterProfile{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: fmt.Sprintf("%s-%s", clusterPromotion.Name, stageName)}, clusterProfile)
				if err == nil && clusterProfile.DeletionTimestamp.IsZero() {
					return false
				}
				if err != nil && !apierrors.IsNotFound(err) {
					return false
				}
			}
			return true
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(k8sClient.Delete(context.TODO(), configMap)).To(Succeed())
	})
})
