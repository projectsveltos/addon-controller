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
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/roles"
)

const (
	clusterAdmin = "cluster-admin"
)

// getSveltosCluster returns SveltosCluster
func getSveltosCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (*libsveltosv1alpha1.SveltosCluster, error) {

	clusterNamespacedName := types.NamespacedName{
		Namespace: clusterNamespace,
		Name:      clusterName,
	}

	cluster := &libsveltosv1alpha1.SveltosCluster{}
	if err := c.Get(ctx, clusterNamespacedName, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// getCAPICluster returns CAPI Cluster
func getCAPICluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (*clusterv1.Cluster, error) {

	clusterNamespacedNamed := types.NamespacedName{
		Namespace: clusterNamespace,
		Name:      clusterName,
	}

	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx, clusterNamespacedNamed, cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

// getCluster returns the cluster associated to ClusterSummary.
// ClusterSummary can be created for either a CAPI Cluster or a Sveltos Cluster.
func getCluster(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) (client.Object, error) {

	if clusterType == libsveltosv1alpha1.ClusterTypeSveltos {
		return getSveltosCluster(ctx, c, clusterNamespace, clusterName)
	}
	return getCAPICluster(ctx, c, clusterNamespace, clusterName)
}

// isCAPIClusterPaused returns true if CAPI Cluster is paused
func isCAPIClusterPaused(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (bool, error) {

	cluster, err := getCAPICluster(ctx, c, clusterNamespace, clusterName)
	if err != nil {
		return false, err
	}

	return cluster.Spec.Paused, nil
}

// isSveltosClusterPaused returns true if CAPI Cluster is paused
func isSveltosClusterPaused(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string) (bool, error) {

	cluster, err := getSveltosCluster(ctx, c, clusterNamespace, clusterName)
	if err != nil {
		return false, err
	}

	return cluster.Spec.Paused, nil
}

func isClusterPaused(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType) (bool, error) {

	if clusterType == libsveltosv1alpha1.ClusterTypeSveltos {
		return isSveltosClusterPaused(ctx, c, clusterNamespace, clusterName)
	}
	return isCAPIClusterPaused(ctx, c, clusterNamespace, clusterName)
}

func getKubernetesRestConfigForAdmin(ctx context.Context, c client.Client, clusterNamespace, clusterName, admin string,
	clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) (*rest.Config, error) {

	kubeconfigContent, err := roles.GetKubeconfig(ctx, c, clusterNamespace, clusterName, admin, clusterType)
	if err != nil {
		return nil, err
	}

	kubeconfig, err := clusterproxy.CreateKubeconfig(logger, kubeconfigContent)
	if err != nil {
		return nil, err
	}
	defer os.Remove(kubeconfig)

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		logger.Error(err, "BuildConfigFromFlags")
		return nil, errors.Wrap(err, "BuildConfigFromFlags")
	}

	return config, nil
}

func getKubernetesClientForAdmin(ctx context.Context, c client.Client, clusterNamespace, clusterName, admin string,
	clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) (client.Client, error) {

	config, err := getKubernetesRestConfigForAdmin(ctx, c, clusterNamespace, clusterName, admin, clusterType, logger)
	if err != nil {
		return nil, err
	}
	logger.V(logs.LogVerbose).Info("return new client")
	return client.New(config, client.Options{Scheme: c.Scheme()})
}

func getSecretData(ctx context.Context, c client.Client, clusterNamespace, clusterName, admin string,
	clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) ([]byte, error) {

	if admin != "" && admin != clusterAdmin {
		return roles.GetKubeconfig(ctx, c, clusterNamespace, clusterName, admin, clusterType)
	}

	if clusterType == libsveltosv1alpha1.ClusterTypeSveltos {
		return clusterproxy.GetSveltosSecretData(ctx, logger, c, clusterNamespace, clusterName)
	}
	return clusterproxy.GetCAPISecretData(ctx, logger, c, clusterNamespace, clusterName)
}

func getKubernetesRestConfig(ctx context.Context, c client.Client, clusterNamespace, clusterName, admin string,
	clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) (*rest.Config, error) {

	if admin != "" && admin != clusterAdmin {
		return getKubernetesRestConfigForAdmin(ctx, c, clusterNamespace, clusterName, admin, clusterType, logger)
	}

	if clusterType == libsveltosv1alpha1.ClusterTypeSveltos {
		return clusterproxy.GetSveltosKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	}
	return clusterproxy.GetCAPIKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
}

func getKubernetesClient(ctx context.Context, c client.Client, clusterNamespace, clusterName, admin string,
	clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) (client.Client, error) {

	if admin != "" && admin != clusterAdmin {
		return getKubernetesClientForAdmin(ctx, c, clusterNamespace, clusterName, admin, clusterType, logger)
	}

	if clusterType == libsveltosv1alpha1.ClusterTypeSveltos {
		return clusterproxy.GetSveltosKubernetesClient(ctx, logger, c, c.Scheme(), clusterNamespace, clusterName)
	}
	return clusterproxy.GetCAPIKubernetesClient(ctx, logger, c, c.Scheme(), clusterNamespace, clusterName)
}

func getClusterType(cluster *corev1.ObjectReference) libsveltosv1alpha1.ClusterType {
	// TODO: remove this
	if cluster.APIVersion != libsveltosv1alpha1.GroupVersion.String() &&
		cluster.APIVersion != clusterv1.GroupVersion.String() {

		panic(1)
	}

	clusterType := libsveltosv1alpha1.ClusterTypeCapi
	if cluster.APIVersion == libsveltosv1alpha1.GroupVersion.String() {
		clusterType = libsveltosv1alpha1.ClusterTypeSveltos
	}
	return clusterType
}
