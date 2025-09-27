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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/utils"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	nameSeparator = "--"
)

// UpdateClusterReportWithResourceReports updates ClusterReport Status with ResourceReports.
// This is no-op unless mode is DryRun
func UpdateClusterReportWithResourceReports(ctx context.Context, c client.Client,
	clusterNamespace, clusterName string, clusterType libsveltosv1beta1.ClusterType, isDryRun bool,
	profileRef *corev1.ObjectReference, resourceReports []libsveltosv1beta1.ResourceReport,
	featureID libsveltosv1beta1.FeatureID) error {

	// This is no-op unless in DryRun mode
	if !isDryRun {
		return nil
	}

	fullName := GetClusterReportName(profileRef.Kind, profileRef.Name, clusterName, clusterType)
	clusterReportName, err := utils.GetNameManager().AllocateName(ctx, clusterNamespace, fullName, &configv1beta1.ClusterReport{})
	if err != nil {
		return err
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		clusterReport := &configv1beta1.ClusterReport{}
		err := c.Get(ctx,
			types.NamespacedName{Namespace: clusterNamespace, Name: clusterReportName}, clusterReport)
		if err != nil {
			return err
		}

		switch featureID {
		case libsveltosv1beta1.FeatureResources:
			clusterReport.Status.ResourceReports = resourceReports
		case libsveltosv1beta1.FeatureKustomize:
			clusterReport.Status.KustomizeResourceReports = resourceReports
		case libsveltosv1beta1.FeatureHelm:
			clusterReport.Status.HelmResourceReports = resourceReports
		}
		return c.Status().Update(ctx, clusterReport)
	})
	return err
}

func GetClusterReportName(profileKind, profileName, clusterName string, clusterType libsveltosv1beta1.ClusterType) string {
	prefix := "" // For backward compatibility (before addition of Profile) leave this empty for ClusterProfiles
	if profileKind == configv1beta1.ProfileKind {
		prefix = "p--"
	}
	name := prefix + profileName + nameSeparator + strings.ToLower(string(clusterType)) +
		nameSeparator + clusterName
	return name
}

// ConvertResourceReportsToObjectReference converts a slice of ResourceReports to
// a slice of ObjectReference
func ConvertResourceReportsToObjectReference(resourceReports []libsveltosv1beta1.ResourceReport,
) []corev1.ObjectReference {

	resources := make([]corev1.ObjectReference, len(resourceReports))

	for i := range resourceReports {
		rr := &resourceReports[i]
		resources[i] = corev1.ObjectReference{
			Kind:      rr.Resource.Kind,
			Namespace: rr.Resource.Namespace,
			Name:      rr.Resource.Name,
		}
	}

	return resources
}
