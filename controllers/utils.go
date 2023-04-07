/*
Copyright 2022-23. projectsveltos.io. All rights reserved.

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
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=debuggingconfigurations,verbs=get;list;watch
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

func InitScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := configv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// GetClusterSummaryName returns the ClusterSummary name given a ClusterProfile name and
// CAPI cluster Namespace/Name.
// This method does not guarantee that name is not already in use. Caller of this method needs
// to handle that scenario
func GetClusterSummaryName(clusterProfileName, clusterName string, isSveltosCluster bool) string {
	prefix := "capi"
	if isSveltosCluster {
		prefix = "sveltos"
	}
	return fmt.Sprintf("%s-%s-%s", clusterProfileName, prefix, clusterName)
}

// getClusterSummary returns the ClusterSummary instance created by a specific ClusterProfile for a specific
// CAPI Cluster
func getClusterSummary(ctx context.Context, c client.Client,
	clusterProfileName, clusterNamespace, clusterName string,
	clusterType libsveltosv1alpha1.ClusterType) (*configv1alpha1.ClusterSummary, error) {

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			ClusterProfileLabelName:         clusterProfileName,
			configv1alpha1.ClusterNameLabel: clusterName,
			configv1alpha1.ClusterTypeLabel: string(clusterType),
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := c.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1alpha1.GroupVersion.Group, Resource: configv1alpha1.ClusterSummaryKind}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s",
			clusterNamespace, clusterName, clusterProfileName)
	}

	return &clusterSummaryList.Items[0], nil
}

// getClusterConfiguration returns the ClusterConfiguration instance for a specific CAPI Cluster
func getClusterConfiguration(ctx context.Context, c client.Client,
	clusterNamespace, clusterConfigurationName string) (*configv1alpha1.ClusterConfiguration, error) {

	clusterConfiguration := &configv1alpha1.ClusterConfiguration{}
	if err := c.Get(ctx,
		types.NamespacedName{
			Namespace: clusterNamespace,
			Name:      clusterConfigurationName,
		},
		clusterConfiguration); err != nil {
		return nil, err
	}

	return clusterConfiguration, nil
}

func getEntryKey(resourceKind, resourceNamespace, resourceName string) string {
	if resourceNamespace != "" {
		return fmt.Sprintf("%s-%s-%s", resourceKind, resourceNamespace, resourceName)
	}
	return fmt.Sprintf("%s-%s", resourceKind, resourceName)
}

func getClusterReportName(clusterProfileName, clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
	// TODO: shorten this value
	return clusterProfileName + "--" + strings.ToLower(string(clusterType)) + "--" + clusterName
}

func getClusterConfigurationName(clusterName string, clusterType libsveltosv1alpha1.ClusterType) string {
	// TODO: shorten this value
	return strings.ToLower(string(clusterType)) + "--" + clusterName
}

// getKeyFromObject returns the Key that can be used in the internal reconciler maps.
func getKeyFromObject(scheme *runtime.Scheme, obj client.Object) *corev1.ObjectReference {
	addTypeInformationToObject(scheme, obj)

	return &corev1.ObjectReference{
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		Kind:       obj.GetObjectKind().GroupVersionKind().Kind,
		APIVersion: obj.GetObjectKind().GroupVersionKind().String(),
	}
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		panic(1)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}
}

// getListOfCAPIClusters returns all CAPI Clusters where Classifier needs to be deployed.
// Currently a Classifier instance needs to be deployed in every existing CAPI cluster.
func getListOfCAPICluster(ctx context.Context, c client.Client, logger logr.Logger,
) ([]corev1.ObjectReference, error) {

	clusterList := &clusterv1.ClusterList{}
	if err := c.List(ctx, clusterList); err != nil {
		logger.Error(err, "failed to list all Cluster")
		return nil, err
	}

	clusters := make([]corev1.ObjectReference, 0)

	for i := range clusterList.Items {
		cluster := &clusterList.Items[i]

		if !cluster.DeletionTimestamp.IsZero() {
			// Only existing cluster can match
			continue
		}

		addTypeInformationToObject(c.Scheme(), cluster)

		clusters = append(clusters, corev1.ObjectReference{
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			APIVersion: cluster.APIVersion,
			Kind:       cluster.Kind,
		})
	}

	return clusters, nil
}

// getListOfSveltosClusters returns all Sveltos Clusters where Classifier needs to be deployed.
// Currently a Classifier instance needs to be deployed in every existing sveltosCluster.
func getListOfSveltosCluster(ctx context.Context, c client.Client, logger logr.Logger,
) ([]corev1.ObjectReference, error) {

	clusterList := &libsveltosv1alpha1.SveltosClusterList{}
	if err := c.List(ctx, clusterList); err != nil {
		logger.Error(err, "failed to list all Cluster")
		return nil, err
	}

	clusters := make([]corev1.ObjectReference, 0)

	for i := range clusterList.Items {
		cluster := &clusterList.Items[i]

		if !cluster.DeletionTimestamp.IsZero() {
			// Only existing cluster can match
			continue
		}

		addTypeInformationToObject(c.Scheme(), cluster)

		clusters = append(clusters, corev1.ObjectReference{
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			APIVersion: cluster.APIVersion,
			Kind:       cluster.Kind,
		})
	}

	return clusters, nil
}

// getListOfClusters returns all Sveltos/CAPI Clusters where Classifier needs to be deployed.
// Currently a Classifier instance needs to be deployed in every existing clusters.
func getListOfClusters(ctx context.Context, c client.Client, logger logr.Logger,
) ([]corev1.ObjectReference, error) {

	clusters, err := getListOfCAPICluster(ctx, c, logger)
	if err != nil {
		return nil, err
	}

	var tmpClusters []corev1.ObjectReference
	tmpClusters, err = getListOfSveltosCluster(ctx, c, logger)
	if err != nil {
		return nil, err
	}

	clusters = append(clusters, tmpClusters...)
	return clusters, nil
}
