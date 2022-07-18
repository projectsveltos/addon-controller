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
	"fmt"
	"io/ioutil"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

const (
	kubeconfigSecretNamePostfix = "-kubeconfig"
)

func DeployWorkloadRoles(ctx context.Context, c client.Client,
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
		return err
	}

	clusterClient, err := getKubernetesClient(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return err
	}

	for i := range clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles[i]
		workloadRole := &configv1alpha1.WorkloadRole{}
		err := c.Get(ctx, types.NamespacedName{Name: reference.Name}, workloadRole)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("workloadRole %s does not exist yet", reference.Name))
				continue
			}
			return err
		}

		if err := deployWorkloadRole(ctx, clusterClient, workloadRole, clusterSummary, logger); err != nil {
			return err
		}
	}

	return nil
}

// getKubernetesClient returns a client to access CAPI Cluster
func getKubernetesClient(ctx context.Context, logger logr.Logger, c client.Client,
	clusterNamespace, clusterName string) (client.Client, error) {
	kubeconfigContent, err := getSecretData(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return nil, err
	}

	kubeconfig, err := createKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return nil, err
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		logger.Error(err, "BuildConfigFromFlags")
		return nil, errors.Wrap(err, "BuildConfigFromFlags")
	}
	logger.V(10).Info("return new client")
	return client.New(config, client.Options{})
}

// getSecretData verifies Cluster exists and returns the content of secret containing
// the kubeconfig for CAPI cluster
func getSecretData(ctx context.Context, logger logr.Logger, c client.Client,
	clusterNamespace, clusterName string) ([]byte, error) {

	logger.WithValues("namespace", clusterNamespace, "cluster", clusterName)
	logger.V(10).Info("Get secret")
	key := client.ObjectKey{
		Namespace: clusterNamespace,
		Name:      clusterName,
	}

	cluster := clusterv1.Cluster{}
	if err := c.Get(ctx, key, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Cluster does not exist")
			return nil, errors.Wrap(err,
				fmt.Sprintf("Cluster %s/%s does not exist",
					clusterNamespace,
					clusterName,
				))
		}
		return nil, err
	}

	secretName := cluster.Name + kubeconfigSecretNamePostfix
	logger = logger.WithValues("secret", secretName)

	secret := &corev1.Secret{}
	key = client.ObjectKey{
		Namespace: clusterNamespace,
		Name:      secretName,
	}

	if err := c.Get(ctx, key, secret); err != nil {
		logger.Error(err, "failed to get secret")
		return nil, errors.Wrap(err,
			fmt.Sprintf("Failed to get secret %s/%s",
				clusterNamespace, secretName))
	}

	for k, contents := range secret.Data {
		logger.V(10).Info("Reading secret", "key", k)
		return contents, nil
	}

	return nil, nil
}

// createKubeconfig creates a temporary file with the Kubeconfig to access CAPI cluster
func createKubeconfig(logger logr.Logger, kubeconfigContent []byte) (string, error) {
	tmpfile, err := ioutil.TempFile("", "kubeconfig")
	if err != nil {
		logger.Error(err, "failed to create temporary file")
		return "", errors.Wrap(err, "ioutil.TempFile")
	}
	defer tmpfile.Close()

	if _, err := tmpfile.Write(kubeconfigContent); err != nil {
		logger.Error(err, "failed to write to temporary file")
		return "", errors.Wrap(err, "failed to write to temporary file")
	}

	return tmpfile.Name(), nil
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

	clusterRoleName := getRoleName(workloadRole, clusterSummary.Name)

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

	roleName := getRoleName(workloadRole, clusterSummary.Name)

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

func getRoleName(workloadRole *configv1alpha1.WorkloadRole, clusterSummaryName string) string {
	return clusterSummaryName + "-" + workloadRole.Name
}

func createNamespace(ctx context.Context, clusterClient client.Client, namespaceName string) error {
	currentNs := &corev1.Namespace{}
	if err := clusterClient.Get(ctx, client.ObjectKey{Name: namespaceName}, currentNs); err != nil {
		if apierrors.IsNotFound(err) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceName,
				},
			}
			return clusterClient.Create(ctx, ns)
		}
		return err
	}
	return nil
}
