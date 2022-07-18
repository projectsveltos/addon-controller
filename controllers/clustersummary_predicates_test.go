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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/event"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

var _ = Describe("Clustersummary Predicates: WorkloadRolePredicates", func() {
	var logger logr.Logger
	var workloadRole *configv1alpha1.WorkloadRole

	BeforeEach(func() {
		logger = klogr.New()
		workloadRole = &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
		}
	})

	It("Create returns true", func() {
		workloadRolePredicate := controllers.WorkloadRolePredicates(logger)

		e := event.CreateEvent{
			Object: workloadRole,
		}

		result := workloadRolePredicate.Create(e)
		Expect(result).To(BeTrue())
	})

	It("Delete returns true", func() {
		workloadRolePredicate := controllers.WorkloadRolePredicates(logger)

		e := event.DeleteEvent{
			Object: workloadRole,
		}

		result := workloadRolePredicate.Delete(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns true when spec has changed", func() {
		workloadRolePredicate := controllers.WorkloadRolePredicates(logger)
		workloadRole.Spec.Rules = []rbacv1.PolicyRule{
			{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
			{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
		}

		oldWorkloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: workloadRole.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: workloadRole,
			ObjectOld: oldWorkloadRole,
		}

		result := workloadRolePredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns false when spec has not changed", func() {
		workloadRolePredicate := controllers.WorkloadRolePredicates(logger)
		workloadRole.Spec.Rules = []rbacv1.PolicyRule{
			{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
			{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
		}

		oldWorkloadRole := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   workloadRole.Name,
				Labels: map[string]string{"env": "testing"},
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Rules: workloadRole.Spec.Rules,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: workloadRole,
			ObjectOld: oldWorkloadRole,
		}

		result := workloadRolePredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})
