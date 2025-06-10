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
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// UpdateReloaderWithDeployedResources updates corresponding Reloader instance in the
// managed cluster.
// Reload indicates whether reloader instance needs to be removed, which can happen
// because ClusterSummary is being deleted or ClusterProfile.Spec.Reloader is set to false.
func UpdateReloaderWithDeployedResources(ctx context.Context, reloaderClient client.Client,
	profileRef *corev1.ObjectReference, feature libsveltosv1beta1.FeatureID,
	resources []corev1.ObjectReference, removeReloader bool,
	logger logr.Logger) error {

	// if ClusterSummary is being deleted or Reloader knob is not set, clean Reloader
	if removeReloader {
		return removeReloaderInstance(ctx, reloaderClient, profileRef.Name, feature, logger)
	}

	return deployReloaderInstance(ctx, reloaderClient, profileRef.Name, feature, resources, logger)
}

// IsReloaderInstalled returns true if Reloader CRD is installed, false otherwise
func IsReloaderInstalled(ctx context.Context, c client.Client) (bool, error) {
	clusterCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "reloaders.lib.projectsveltos.io"}, clusterCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// removeReloaderInstance removes Reloader instance from the management or the managed cluster
// If Sveltos agents are deployed in the management cluster, Reloader instances are deployed to
// the management cluster. Otherwise to the managed cluster.
func removeReloaderInstance(ctx context.Context, reloaderClient client.Client, profileName string,
	feature libsveltosv1beta1.FeatureID, logger logr.Logger) error {

	installed, err := IsReloaderInstalled(ctx, reloaderClient)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to verify if Reloader is installed %v", err))
		return err
	}

	if !installed {
		return nil
	}

	reloader, err := getReloaderInstance(ctx, reloaderClient, profileName,
		feature, textlogger.NewLogger(textlogger.NewConfig()))
	if err != nil {
		return err
	}

	if reloader == nil {
		return nil
	}

	logger = logger.WithValues("reloader", reloader.Name)
	logger.V(logs.LogDebug).Info("deleting reloader")
	return reloaderClient.Delete(ctx, reloader)
}

// deployReloaderInstance creates/updates Reloader instance to the management or managed cluster.
// If Sveltos agents are deployed in the management cluster, Reloader instances are deployed to
// the management cluster. Otherwise to the managed cluster.
// Any Deployment, StatefulSet, DaemonSet instance deployed by Sveltos and mounting either
// a ConfigMap or Secret as volume, need to be reloaded (via rolling upgrade) when mounted
// resources are modified.
// Reloader instance contains list of Deployment, StatefulSet, DaemonSet instances sveltos-agent needs
// to watch (along with mounted ConfigMaps/Secrets) to detect when is time to trigger a rolling upgrade.
func deployReloaderInstance(ctx context.Context, reloaderClient client.Client,
	profileName string, feature libsveltosv1beta1.FeatureID, resources []corev1.ObjectReference,
	logger logr.Logger) error {

	reloaderInfo := make([]libsveltosv1beta1.ReloaderInfo, 0, len(resources))
	for i := range resources {
		resource := &resources[i]
		if watchForRollingUpgrade(resource) {
			reloaderInfo = append(reloaderInfo,
				libsveltosv1beta1.ReloaderInfo{
					Namespace: resource.Namespace,
					Name:      resource.Name,
					Kind:      resource.Kind,
				})
		}
	}

	reloader, err := getReloaderInstance(ctx, reloaderClient, profileName, feature, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to get reloader instance: %v", err))
		return err
	}

	if reloader == nil {
		// Reloader is not present in the managed cluster
		return createReloaderInstance(ctx, reloaderClient, profileName, feature, reloaderInfo)
	}

	reloader.Spec.ReloaderInfo = reloaderInfo
	return reloaderClient.Update(ctx, reloader)
}

func getReloaderName(profileName string, feature libsveltosv1beta1.FeatureID) string {
	return fmt.Sprintf("%s--%s", profileName, strings.ToLower(string(feature)))
}

func getReloader(profileName string, feature libsveltosv1beta1.FeatureID,
	reloaderInfo []libsveltosv1beta1.ReloaderInfo) *libsveltosv1beta1.Reloader {

	return &libsveltosv1beta1.Reloader{
		ObjectMeta: metav1.ObjectMeta{
			Name:        getReloaderName(profileName, feature),
			Labels:      getReloaderLabels(profileName, feature),
			Annotations: getReloaderAnnotations(),
		},
		Spec: libsveltosv1beta1.ReloaderSpec{
			ReloaderInfo: reloaderInfo,
		},
	}
}

// createReloaderInstance creates Reloader instance to managed cluster.
func createReloaderInstance(ctx context.Context, remoteClient client.Client, clusterProfileName string,
	feature libsveltosv1beta1.FeatureID, reloaderInfo []libsveltosv1beta1.ReloaderInfo) error {

	reloader := getReloader(clusterProfileName, feature, reloaderInfo)
	return remoteClient.Create(ctx, reloader)
}

// getReloaderInstance returns ReloaderInstance if present in the managed cluster.
func getReloaderInstance(ctx context.Context, remoteClient client.Client, clusterProfileName string,
	feature libsveltosv1beta1.FeatureID, logger logr.Logger) (*libsveltosv1beta1.Reloader, error) {

	reloaders := &libsveltosv1beta1.ReloaderList{}
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
func getReloaderLabels(clusterProfileName string, feature libsveltosv1beta1.FeatureID) map[string]string {
	return map[string]string{
		"clusterprofile": clusterProfileName,
		"feature":        string(feature),
	}
}

// getReloaderAnnotations returns annotations a Reloader instance has in a managed cluster
func getReloaderAnnotations() map[string]string {
	return map[string]string{
		libsveltosv1beta1.DeployedBySveltosAnnotation: "ok",
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

// ConvertHelmResourcesToObjectReference converts a slice of HelmResources to
// a slice of ObjectReference
func ConvertHelmResourcesToObjectReference(helmResources []libsveltosv1beta1.HelmResources,
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
