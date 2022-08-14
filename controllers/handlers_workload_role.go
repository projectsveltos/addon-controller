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

package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

func deployWorkloadRoles(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, clusterClient, err := getClusterSummaryAndCAPIClusterClient(ctx, applicant, c, logger)
	if err != nil {
		return err
	}

	currentRoles := make(map[string]bool, 0)
	for i := range clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs[i]
		workloadRole := &configv1alpha1.WorkloadRole{}
		err = c.Get(ctx, types.NamespacedName{Name: reference.Name}, workloadRole)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("workloadRole %s does not exist yet", reference.Name))
				continue
			}
			return err
		}
		err = deployWorkloadRole(ctx, clusterClient, workloadRole, clusterSummary, logger)
		if err != nil {
			return err
		}

		roleName := getRoleName(workloadRole, clusterSummary)
		currentRoles[roleName] = true
	}

	// Remove all roles/clusterRoles previously deployed by this ClusterSummary and not referenced anymores
	err = undeployStaleRoleResources(ctx, clusterClient, clusterSummary, currentRoles)
	if err != nil {
		return err
	}

	return nil
}

func unDeployWorkloadRoles(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary := &configv1alpha1.ClusterSummary{}
	if err := c.Get(ctx, types.NamespacedName{Name: applicant}, clusterSummary); err != nil {
		return err
	}

	// Get CAPI Cluster
	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info(fmt.Sprintf("Cluster %s/%s not found. Nothing to cleanup", clusterNamespace, clusterName))
			return nil
		}
		return err
	}

	clusterClient, err := getKubernetesClient(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	listOptions := []client.ListOption{
		client.MatchingLabels{ClusterSummaryLabelName: clusterSummary.Name},
	}

	roles := &rbacv1.RoleList{}
	err = clusterClient.List(ctx, roles, listOptions...)
	if err != nil {
		return err
	}

	for i := range roles.Items {
		role := &roles.Items[i]
		err = clusterClient.Delete(ctx, role)
		if err != nil {
			return err
		}
	}

	clusterRoles := &rbacv1.ClusterRoleList{}
	err = clusterClient.List(ctx, clusterRoles, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterRoles.Items {
		clusterRole := &clusterRoles.Items[i]
		err = clusterClient.Delete(ctx, clusterRole)
		if err != nil {
			return err
		}
	}

	return nil
}

// workloadRoleHash returns the hash of all the ClusterSummary referenced WorkloadRole Specs.
func workloadRoleHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	for i := range clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs[i]
		workloadRole := &configv1alpha1.WorkloadRole{}
		err := c.Get(ctx, types.NamespacedName{Name: reference.Name}, workloadRole)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("workloadRole %s does not exist yet", reference.Name))
				continue
			}
			logger.Error(err, fmt.Sprintf("failed to get workloadRole %s", reference.Name))
			return nil, err
		}

		config += render.AsCode(workloadRole.Spec)
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getWorkloadRoleRefs(clusterSummaryScope *scope.ClusterSummaryScope) []corev1.ObjectReference {
	return nil
}

// deployWorkloadRole deploys a workload role in a CAPI cluster.
func deployWorkloadRole(ctx context.Context, clusterClient client.Client,
	workloadRole *configv1alpha1.WorkloadRole, clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) error {

	if workloadRole.Spec.Type == configv1alpha1.RoleTypeNamespaced {
		return deployNamespacedWorkloadRole(ctx, clusterClient, workloadRole, clusterSummary, logger)
	}

	return deployClusterWorkloadRole(ctx, clusterClient, workloadRole, clusterSummary, logger)
}

// deployClusterWorkloadRole creates, or updates if already existing, a ClusterRole in CAPI Cluster
func deployClusterWorkloadRole(ctx context.Context, clusterClient client.Client,
	workloadRole *configv1alpha1.WorkloadRole, clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) error {

	clusterRoleName := getRoleName(workloadRole, clusterSummary)

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleName,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       clusterSummary.Kind,
					Name:       clusterSummary.Name,
					UID:        clusterSummary.UID,
					APIVersion: clusterSummary.APIVersion,
				},
			},
		},
		Rules:           workloadRole.Spec.Rules,
		AggregationRule: workloadRole.Spec.AggregationRule,
	}

	addLabel(clusterRole, ClusterSummaryLabelName, clusterSummary.Name)

	currentClusterRole := &rbacv1.ClusterRole{}
	if err := clusterClient.Get(ctx, client.ObjectKey{Name: clusterRoleName}, currentClusterRole); err != nil {
		if apierrors.IsNotFound(err) {
			return clusterClient.Create(ctx, clusterRole)
		}
		return err
	}

	currentClusterRole.Rules = clusterRole.Rules
	currentClusterRole.AggregationRule = clusterRole.AggregationRule

	return clusterClient.Update(ctx, currentClusterRole)
}

// deployNamespacedWorkloadRole creates, or updates if already existing, a Role in CAPI Cluster
func deployNamespacedWorkloadRole(ctx context.Context, clusterClient client.Client,
	workloadRole *configv1alpha1.WorkloadRole, clusterSummary *configv1alpha1.ClusterSummary, logger logr.Logger) error {

	roleName := getRoleName(workloadRole, clusterSummary)

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: *workloadRole.Spec.Namespace,
			Name:      roleName,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       clusterSummary.Kind,
					Name:       clusterSummary.Name,
					UID:        clusterSummary.UID,
					APIVersion: clusterSummary.APIVersion,
				},
			},
		},
		Rules: workloadRole.Spec.Rules,
	}

	addLabel(role, ClusterSummaryLabelName, clusterSummary.Name)

	if err := createNamespace(ctx, clusterClient, role.Namespace); err != nil {
		return err
	}

	currentRole := &rbacv1.Role{}
	if err := clusterClient.Get(ctx, client.ObjectKey{Namespace: role.Namespace, Name: role.Name}, currentRole); err != nil {
		if apierrors.IsNotFound(err) {
			return clusterClient.Create(ctx, role)
		}
		return err
	}

	currentRole.Rules = role.Rules

	return clusterClient.Update(ctx, currentRole)
}

func getRoleName(workloadRole *configv1alpha1.WorkloadRole, clusterSummary *configv1alpha1.ClusterSummary) string {
	return clusterSummary.Status.PolicyPrefix + "-" + workloadRole.Name
}

func undeployStaleRoleResources(ctx context.Context, c client.Client, clusterSummary *configv1alpha1.ClusterSummary, currentRoles map[string]bool) error {
	listOptions := []client.ListOption{
		client.MatchingLabels{ClusterSummaryLabelName: clusterSummary.Name},
	}

	roles := &rbacv1.RoleList{}
	err := c.List(ctx, roles, listOptions...)
	if err != nil {
		return err
	}

	for i := range roles.Items {
		role := &roles.Items[i]
		if _, ok := currentRoles[role.Name]; !ok {
			err = c.Delete(ctx, role)
			if err != nil {
				return err
			}
		}
	}

	clusterRoles := &rbacv1.ClusterRoleList{}
	err = c.List(ctx, clusterRoles, listOptions...)
	if err != nil {
		return err
	}

	for i := range clusterRoles.Items {
		clusterRole := &clusterRoles.Items[i]
		if _, ok := currentRoles[clusterRole.Name]; !ok {
			err = c.Delete(ctx, clusterRole)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
