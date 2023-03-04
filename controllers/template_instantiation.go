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
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

type currentClusterObjects struct {
	Cluster                *clusterv1.Cluster
	SveltosCluster         *libsveltosv1alpha1.SveltosCluster
	KubeadmControlPlane    client.Object
	InfrastructureProvider client.Object
	SecretRef              client.Object
}

func fetchResource(ctx context.Context, config *rest.Config, namespace, name, apiVersion, kind string,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to parse apiversion %v", err))
		return nil, err
	}
	gvk := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    kind,
	}
	var dr dynamic.ResourceInterface
	dr, err = utils.GetDynamicResourceInterface(config, gvk, namespace)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch %s: %v", kind, err))
		return nil, err
	}
	var resource *unstructured.Unstructured
	resource, err = dr.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch %s %v", kind, err))
		return nil, err
	}

	return resource, nil
}

func fetchInfrastructureProvider(ctx context.Context, config *rest.Config, cluster *clusterv1.Cluster,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	var provider *unstructured.Unstructured
	var err error
	if cluster.Spec.InfrastructureRef != nil {
		provider, err = fetchResource(ctx, config, cluster.Namespace, cluster.Spec.InfrastructureRef.Name,
			cluster.Spec.InfrastructureRef.APIVersion, cluster.Spec.InfrastructureRef.Kind, logger)
	}

	return provider, err
}

func fetchKubeadmControlPlane(ctx context.Context, config *rest.Config, cluster *clusterv1.Cluster,
	logger logr.Logger) (*unstructured.Unstructured, error) {

	var kubeadmControlPlane *unstructured.Unstructured
	var err error
	if cluster.Spec.ControlPlaneRef != nil {
		kubeadmControlPlane, err = fetchResource(ctx, config, cluster.Namespace, cluster.Spec.ControlPlaneRef.Name,
			cluster.Spec.ControlPlaneRef.APIVersion, cluster.Spec.ControlPlaneRef.Kind, logger)
	}

	return kubeadmControlPlane, err
}

// fecthClusterObjects fetches resources representing a cluster.
// All fetched objects are in the management cluster.
// Currently limited to Cluster and Infrastructure Provider
func fecthClusterObjects(ctx context.Context, config *rest.Config, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1alpha1.ClusterType, logger logr.Logger) (*currentClusterObjects, error) {

	logger.V(logs.LogInfo).Info(fmt.Sprintf("Fetch %s/%s", clusterNamespace, clusterName))

	genericCluster, err := clusterproxy.GetCluster(ctx, c, clusterNamespace, clusterName, clusterType)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch cluster %v", err))
		return nil, err
	}

	var cluster *clusterv1.Cluster
	var sveltosCluster *libsveltosv1alpha1.SveltosCluster
	var provider *unstructured.Unstructured
	var kubeadmControlPlane *unstructured.Unstructured
	if clusterType == libsveltosv1alpha1.ClusterTypeCapi {
		cluster = genericCluster.(*clusterv1.Cluster)
		provider, err = fetchInfrastructureProvider(ctx, config, cluster, logger)
		if err != nil {
			return nil, err
		}

		kubeadmControlPlane, err = fetchKubeadmControlPlane(ctx, config, cluster, logger)
		if err != nil {
			return nil, err
		}
	} else {
		sveltosCluster = genericCluster.(*libsveltosv1alpha1.SveltosCluster)
	}
	return &currentClusterObjects{
		Cluster:                cluster,
		SveltosCluster:         sveltosCluster,
		InfrastructureProvider: provider,
		KubeadmControlPlane:    kubeadmControlPlane,
	}, nil
}

func fetchSecretRef(ctx context.Context, c client.Client, secretRef *corev1.ObjectReference) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Namespace: secretRef.Namespace, Name: secretRef.Name}, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	return secret, nil
}

func instantiateTemplateValues(ctx context.Context, config *rest.Config, c client.Client,
	clusterType libsveltosv1alpha1.ClusterType, clusterNamespace, clusterName, requestorName, values string,
	secretRef *corev1.ObjectReference, logger logr.Logger) (string, error) {

	objects, err := fecthClusterObjects(ctx, config, c, clusterNamespace, clusterName, clusterType, logger)
	if err != nil {
		return "", err
	}

	if secretRef != nil {
		var secret *corev1.Secret
		secret, err = fetchSecretRef(ctx, c, secretRef)
		if err != nil {
			return "", err
		}
		if secret != nil {
			objects.SecretRef = secret
		} else {
			logger.V(logs.LogInfo).Info(fmt.Sprintf("secret %s/%s not found", secretRef.Namespace, secretRef.Name))
		}
	}

	templateName := getTemplateName(clusterNamespace, clusterName, requestorName)
	tmpl, err := template.New(templateName).Funcs(sprig.TxtFuncMap()).Parse(values)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer

	if err := tmpl.Execute(&buffer, objects); err != nil {
		return "", errors.Wrapf(err, "error executing template %q", values)
	}
	instantiatedValues := buffer.String()

	logger.V(logs.LogDebug).Info("Values %q", instantiatedValues)
	return instantiatedValues, nil
}

func getTemplateName(clusterNamespace, clusterName, requestorName string) string {
	return fmt.Sprintf("%s-%s-%s", clusterNamespace, clusterName, requestorName)
}
