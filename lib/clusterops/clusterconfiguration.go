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

package clusterops

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

// No action in DryRun mode.
func UpdateClusterConfiguration(ctx context.Context, c client.Client, isDryRun, clean bool,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType,
	profileRef *corev1.ObjectReference, featureID libsveltosv1beta1.FeatureID,
	policyDeployed []libsveltosv1beta1.Resource, chartDeployed []configv1beta1.Chart) error {

	// No-op in DryRun mode
	if isDryRun {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get ClusterConfiguration for managed Cluster
		clusterConfiguration := &configv1beta1.ClusterConfiguration{}
		err := c.Get(ctx,
			types.NamespacedName{
				Namespace: clusterNamespace,
				Name:      getClusterConfigurationName(clusterName, clusterType),
			},
			clusterConfiguration)
		if err != nil {
			if apierrors.IsNotFound(err) && clean {
				return nil
			}
			return err
		}

		var index int
		index, err = configv1beta1.GetClusterConfigurationSectionIndex(clusterConfiguration, profileRef.Kind,
			profileRef.Name)
		if err != nil {
			return err
		}

		if profileRef.Kind == configv1beta1.ClusterProfileKind {
			return updateClusterProfileResources(ctx, c, profileRef, clusterConfiguration,
				index, featureID, policyDeployed, chartDeployed)
		} else {
			return updateProfileResources(ctx, c, profileRef, clusterConfiguration,
				index, featureID, policyDeployed, chartDeployed)
		}
	})

	return err
}

func getClusterConfigurationName(clusterName string, clusterType libsveltosv1beta1.ClusterType) string {
	// TODO: shorten this value
	return strings.ToLower(string(clusterType)) + nameSeparator + clusterName
}

func updateClusterProfileResources(ctx context.Context, c client.Client, profileRef *corev1.ObjectReference,
	clusterConfiguration *configv1beta1.ClusterConfiguration, index int,
	featureID libsveltosv1beta1.FeatureID, policyDeployed []libsveltosv1beta1.Resource,
	chartDeployed []configv1beta1.Chart) error {

	isPresent := false

	profileResources := &clusterConfiguration.Status.ClusterProfileResources[index]
	for i := range profileResources.Features {
		if profileResources.Features[i].FeatureID == featureID {
			if policyDeployed != nil {
				profileResources.Features[i].Resources = policyDeployed
			}
			if chartDeployed != nil {
				profileResources.Features[i].Charts = chartDeployed
			}
			isPresent = true
			break
		}
	}

	if !isPresent {
		if profileResources.Features == nil {
			profileResources.Features = make([]configv1beta1.Feature, 0)
		}
		profileResources.Features = append(
			profileResources.Features,
			configv1beta1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
		)
	}

	clusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences,
		objectRefToOwnerRef(profileRef))

	return c.Status().Update(ctx, clusterConfiguration)
}

func updateProfileResources(ctx context.Context, c client.Client, profileRef *corev1.ObjectReference,
	clusterConfiguration *configv1beta1.ClusterConfiguration, index int,
	featureID libsveltosv1beta1.FeatureID, policyDeployed []libsveltosv1beta1.Resource,
	chartDeployed []configv1beta1.Chart) error {

	isPresent := false

	profileResources := &clusterConfiguration.Status.ProfileResources[index]
	for i := range profileResources.Features {
		if profileResources.Features[i].FeatureID == featureID {
			if policyDeployed != nil {
				profileResources.Features[i].Resources = policyDeployed
			}
			if chartDeployed != nil {
				profileResources.Features[i].Charts = chartDeployed
			}
			isPresent = true
			break
		}
	}

	if !isPresent {
		if profileResources.Features == nil {
			profileResources.Features = make([]configv1beta1.Feature, 0)
		}
		profileResources.Features = append(
			profileResources.Features,
			configv1beta1.Feature{FeatureID: featureID, Resources: policyDeployed, Charts: chartDeployed},
		)
	}

	clusterConfiguration.OwnerReferences = util.EnsureOwnerRef(clusterConfiguration.OwnerReferences,
		objectRefToOwnerRef(profileRef))

	return c.Status().Update(ctx, clusterConfiguration)
}

func objectRefToOwnerRef(objRef *corev1.ObjectReference) metav1.OwnerReference {
	// Controller and BlockOwnerDeletion are set to nil/false by default
	// Set these appropriately for your use case
	controller := false
	blockOwnerDeletion := false

	return metav1.OwnerReference{
		APIVersion:         objRef.APIVersion,
		Kind:               objRef.Kind,
		Name:               objRef.Name,
		UID:                objRef.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}
}
