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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

const (
	separator                   = "--"
	kubeconfigSecretNamePostfix = "-kubeconfig"
)

// GetClusterSummaryName returns the ClusterSummary name given a ClusterFeature name and
// CAPI cluster Namespace/Name
func GetClusterSummaryName(clusterFeatureName, clusterNamespace, clusterName string) string {
	return fmt.Sprintf("%s%s%s%s%s", clusterFeatureName, separator, clusterNamespace, separator, clusterName)
}

// getClusterFeatureOwner returns the ClusterFeature owning this clusterSummary.
// Returns nil if ClusterFeature does not exist anymore.
func getClusterFeatureOwner(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary) (*configv1alpha1.ClusterFeature, error) {
	for _, ref := range clusterSummary.OwnerReferences {
		if ref.Kind != "ClusterFeature" {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if gv.Group == configv1alpha1.GroupVersion.Group {
			clusterFeature := &configv1alpha1.ClusterFeature{}
			err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, clusterFeature)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil, nil
				}
				return nil, err
			}
			return clusterFeature, nil
		}
	}
	return nil, nil
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
