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
	"crypto/sha256"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// removeReloaderInstance removes Reloader instance from the managed cluster
func removeReloaderInstance(ctx context.Context, remoteClient client.Client,
	clusterProfileName string, feature configv1alpha1.FeatureID, logger logr.Logger) error {

	reloader, err := getReloaderInstance(ctx, remoteClient, clusterProfileName,
		feature, klogr.New())
	if err != nil {
		return err
	}

	if reloader == nil {
		return nil
	}

	logger = logger.WithValues("reloader", reloader.Name)
	logger.V(logs.LogDebug).Info("deleting reloader")
	return remoteClient.Delete(ctx, reloader)
}

// deployReloaderInstance creates/updates Reloader instance to the managed cluster.
// Any Deployment, StatefulSet, DaemonSet instance deployed by Sveltos and mounting either
// a ConfigMap or Secret as volume, need to be reloaded (via rolling upgrade) when mounted
// resources are modified.
// Reloader instance contains list of Deployment, StatefulSet, DaemonSet instances sveltos-agent needs
// to watch (along with mounted ConfigMaps/Secrets) to detect when is time to trigger a rolling upgrade.
func deployReloaderInstance(ctx context.Context, remoteClient client.Client,
	clusterProfileName string, feature configv1alpha1.FeatureID, resources []corev1.ObjectReference,
	logger logr.Logger) error {

	reloaderInfo := make([]libsveltosv1alpha1.ReloaderInfo, 0)
	for i := range resources {
		resource := &resources[i]
		if watchForRollingUpgrade(resource) {
			reloaderInfo = append(reloaderInfo,
				libsveltosv1alpha1.ReloaderInfo{
					Namespace: resource.Namespace,
					Name:      resource.Name,
					Kind:      resource.Kind,
				})
		}
	}

	reloader, err := getReloaderInstance(ctx, remoteClient, clusterProfileName, feature, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get reloader instance: %v", err))
		return err
	}

	if reloader == nil {
		// Reloader is not present in the managed cluster
		return createReloaderInstance(ctx, remoteClient, clusterProfileName, feature, reloaderInfo)
	}

	reloader.Spec.ReloaderInfo = reloaderInfo
	return remoteClient.Update(ctx, reloader)
}

// createReloaderInstance creates Reloader instance to managed cluster.
func createReloaderInstance(ctx context.Context, remoteClient client.Client, clusterProfileName string,
	feature configv1alpha1.FeatureID, reloaderInfo []libsveltosv1alpha1.ReloaderInfo) error {

	h := sha256.New()
	fmt.Fprintf(h, "%s--%s", clusterProfileName, feature)
	hash := h.Sum(nil)
	reloader := &libsveltosv1alpha1.Reloader{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%x", hash),
			Labels:      getReloaderLabels(clusterProfileName, feature),
			Annotations: getReloaderAnnotations(),
		},
		Spec: libsveltosv1alpha1.ReloaderSpec{
			ReloaderInfo: reloaderInfo,
		},
	}

	return remoteClient.Create(ctx, reloader)
}

// getReloaderInstance returns ReloaderInstance if present in the managed cluster.
func getReloaderInstance(ctx context.Context, remoteClient client.Client, clusterProfileName string,
	feature configv1alpha1.FeatureID, logger logr.Logger) (*libsveltosv1alpha1.Reloader, error) {

	reloaders := &libsveltosv1alpha1.ReloaderList{}
	listOptions := []client.ListOption{
		client.MatchingLabels(getReloaderLabels(clusterProfileName, feature)),
	}

	err := remoteClient.List(ctx, reloaders, listOptions...)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to list Reloaders: %v", err))
		return nil, err
	}

	switch len(reloaders.Items) {
	case 0:
		return nil, nil
	case 1:
		return &reloaders.Items[0], nil
	default:
		return nil, fmt.Errorf("found %d matches", len(reloaders.Items))
	}
}

// getReloaderLabels returns labels a Reloader instance has in a managed cluster
func getReloaderLabels(clusterProfileName string, feature configv1alpha1.FeatureID) map[string]string {
	return map[string]string{
		"clusterprofile": clusterProfileName,
		"feature":        string(feature),
	}
}

// getReloaderAnnotations returns annotations a Reloader instance has in a managed cluster
func getReloaderAnnotations() map[string]string {
	return map[string]string{
		libsveltosv1alpha1.DeployedBySveltosAnnotation: "ok",
	}
}

// watchForRollingUpgrade returns true if the resource should be watched for rolling upgrades
func watchForRollingUpgrade(resource *corev1.ObjectReference) bool {
	switch resource.Kind {
	case "Deployment":
		return true
	case "StatefulSet":
		return true
	case "DaemonSet":
		return true
	default:
		return false
	}
}

// updateReloaderWithDeployedResources updates corresponding Reloader instance in the
// managed cluster.
// Reload indicates whether reloader instance needs to be removed, which can happen
// because ClusterSummary is being deleted or ClusterProfile.Spec.Reloader is set to false.
func updateReloaderWithDeployedResources(ctx context.Context, c client.Client,
	clusterProfileOwnerRef *metav1.OwnerReference, feature configv1alpha1.FeatureID,
	resources []corev1.ObjectReference, clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	// Ignore admin. Deploying Reloaders must be done as Sveltos.
	// There is no need to ask tenant to be granted Reloader permissions
	remoteClient, err := clusterproxy.GetKubernetesClient(ctx, c, clusterSummary.Spec.ClusterNamespace,
		clusterSummary.Spec.ClusterName, "", "", clusterSummary.Spec.ClusterType, logger)
	if err != nil {
		return err
	}

	// if ClusterSummary is being deleted or Reloader knob is not set, clean Reloader
	if !clusterSummary.DeletionTimestamp.IsZero() ||
		!clusterSummary.Spec.ClusterProfileSpec.Reloader {

		return removeReloaderInstance(ctx, remoteClient, clusterProfileOwnerRef.Name,
			feature, logger)
	}

	return deployReloaderInstance(ctx, remoteClient, clusterProfileOwnerRef.Name,
		feature, resources, logger)
}

// convertResourceReportsToObjectReference converts a slice of ResourceReports to
// a slice of ObjectReference
func convertResourceReportsToObjectReference(resourceReports []configv1alpha1.ResourceReport,
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

// convertHelmResourcesToObjectReference converts a slice of HelmResources to
// a slice of ObjectReference
func convertHelmResourcesToObjectReference(helmResources []libsveltosv1alpha1.HelmResources,
) []corev1.ObjectReference {

	resources := make([]corev1.ObjectReference, 0)

	for i := range helmResources {
		for j := range helmResources[i].Resources {
			resources = append(resources, corev1.ObjectReference{
				Kind:      helmResources[i].Resources[j].Kind,
				Namespace: helmResources[i].Resources[j].Namespace,
				Name:      helmResources[i].Resources[j].Name,
			})
		}
	}

	return resources
}
