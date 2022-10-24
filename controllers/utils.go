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
	"os"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubectl/pkg/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

// +kubebuilder:rbac:groups=lib.projectsveltos.io,resources=debuggingconfigurations,verbs=get;list;watch

const (
	//nolint: gosec // CAPI secret postfix
	kubeconfigSecretNamePostfix = "-kubeconfig"
)

type conflictError struct {
	message string
}

func (e *conflictError) Error() string {
	return e.message
}

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
	return s, nil
}

// GetClusterSummaryName returns the ClusterSummary name given a ClusterProfile name and
// CAPI cluster Namespace/Name.
// This method does not guarantee that name is not already in use. Caller of this method needs
// to handle that scenario
func GetClusterSummaryName(clusterProfileName, clusterName string) string {
	// generate random name.
	return fmt.Sprintf("%s-%s", clusterProfileName, clusterName)
}

// getClusterSummary returns the ClusterSummary instance created by a specific ClusterProfile for a specific
// CAPI Cluster
func getClusterSummary(ctx context.Context, c client.Client,
	clusterProfileName, clusterNamespace, clusterName string) (*configv1alpha1.ClusterSummary, error) {

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			ClusterProfileLabelName: clusterProfileName,
			ClusterLabelNamespace:   clusterNamespace,
			ClusterLabelName:        clusterName,
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
	clusterNamespace, clusterName string) (*configv1alpha1.ClusterConfiguration, error) {

	clusterConfiguration := &configv1alpha1.ClusterConfiguration{}
	if err := c.Get(ctx,
		types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, clusterConfiguration); err != nil {
		return nil, err
	}

	return clusterConfiguration, nil
}

func getKubernetesRestConfig(ctx context.Context, logger logr.Logger, c client.Client,
	clusterNamespace, clusterName string) (*rest.Config, error) {

	kubeconfigContent, err := getSecretData(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return nil, err
	}

	kubeconfig, err := createKubeconfig(logger, kubeconfigContent)
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

// getKubernetesClient returns a client to access CAPI Cluster
func getKubernetesClient(ctx context.Context, logger logr.Logger, c client.Client,
	clusterNamespace, clusterName string) (client.Client, error) {

	config, err := getKubernetesRestConfig(ctx, logger, c, clusterNamespace, clusterName)
	if err != nil {
		return nil, err
	}
	logger.V(logs.LogVerbose).Info("return new client")
	s, err := InitScheme()
	if err != nil {
		return nil, err
	}
	return client.New(config, client.Options{Scheme: s})
}

// getSecretData verifies Cluster exists and returns the content of secret containing
// the kubeconfig for CAPI cluster
func getSecretData(ctx context.Context, logger logr.Logger, c client.Client,
	clusterNamespace, clusterName string) ([]byte, error) {

	logger.WithValues("namespace", clusterNamespace, "cluster", clusterName)
	logger.V(logs.LogVerbose).Info("Get secret")
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
		logger.V(logs.LogVerbose).Info("Reading secret", "key", k)
		return contents, nil
	}

	return nil, nil
}

// createKubeconfig creates a temporary file with the Kubeconfig to access CAPI cluster
func createKubeconfig(logger logr.Logger, kubeconfigContent []byte) (string, error) {
	tmpfile, err := os.CreateTemp("", "kubeconfig")
	if err != nil {
		logger.Error(err, "failed to create temporary file")
		return "", errors.Wrap(err, "os.CreateTemp")
	}
	defer tmpfile.Close()

	if _, err := tmpfile.Write(kubeconfigContent); err != nil {
		logger.Error(err, "failed to write to temporary file")
		return "", errors.Wrap(err, "failed to write to temporary file")
	}

	return tmpfile.Name(), nil
}

// getUnstructured returns an unstructured given a []bytes containing it
func getUnstructured(object []byte) (*unstructured.Unstructured, error) {
	request := &unstructured.Unstructured{}
	universalDeserializer := scheme.Codecs.UniversalDeserializer()
	_, _, err := universalDeserializer.Decode(object, nil, request)
	if err != nil {
		return nil, fmt.Errorf("failed to decode k8s resource %.50s. Err: %w",
			string(object), err)
	}

	return request, nil
}

// getDynamicResourceInterface returns a dynamic ResourceInterface for the policy's GroupVersionKind
func getDynamicResourceInterface(config *rest.Config, policy *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	if config == nil {
		return nil, fmt.Errorf("rest.Config is nil")
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	gvk := policy.GroupVersionKind()

	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	var dr dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		// namespaced resources should specify the namespace
		dr = dynClient.Resource(mapping.Resource).Namespace(policy.GetNamespace())
	} else {
		// for cluster-wide resources
		dr = dynClient.Resource(mapping.Resource)
	}

	return dr, nil
}

// validateObjectForUpdate finds if object currently exists. If object exists:
// - verifies this object was created by same ConfigMap/Secret. Returns an error otherwise.
// This is needed to prevent misconfigurations. An example would be when different
// ConfigMaps are referenced by ClusterProfile(s) and contain same policy namespace/name
// (content might be different) and are about to be deployed in the same CAPI Cluster;
// Return an error if validation fails. Return also whether the object currently exists or not.
// If object exists, return value of PolicyHash annotation.
func validateObjectForUpdate(ctx context.Context, dr dynamic.ResourceInterface,
	object *unstructured.Unstructured,
	referenceKind, referenceNamespace, referenceName string) (exist bool, hash string, err error) {

	if object == nil {
		return false, "", nil
	}

	currentObject, err := dr.Get(ctx, object.GetName(), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, "", nil
		}
		return false, "", err
	}

	if labels := currentObject.GetLabels(); labels != nil {
		kind, kindOk := labels[ReferenceLabelKind]
		namespace, namespaceOk := labels[ReferenceLabelNamespace]
		name, nameOk := labels[ReferenceLabelName]

		if kindOk {
			if kind != referenceKind {
				return true, "", &conflictError{
					message: fmt.Sprintf("conflict: policy (kind: %s) %s is currently deployed by %s: %s/%s",
						object.GetKind(), object.GetName(), kind, namespace, name)}
			}
		}
		if namespaceOk {
			if namespace != referenceNamespace {
				return true, "", &conflictError{
					message: fmt.Sprintf("conflict: policy (kind: %s) %s is currently deployed by %s: %s/%s",
						object.GetKind(), object.GetName(), kind, namespace, name)}
			}
		}
		if nameOk {
			if name != referenceName {
				return true, "", &conflictError{
					message: fmt.Sprintf("conflict: policy (kind: %s) %s is currently deployed by %s: %s/%s",
						object.GetKind(), object.GetName(), kind, namespace, name)}
			}
		}
	}

	// Only in case object exists and there are no conflicts, return hash
	if annotations := currentObject.GetAnnotations(); annotations != nil {
		hash = annotations[PolicyHash]
	}

	return true, hash, nil
}

// getOwnerMessage returns a message listing why this object is deployed. The message lists:
// - which ClusterProfile(s) is currently causing it to be deployed
// - which Secret/ConfigMap contains it
func getOwnerMessage(ctx context.Context, dr dynamic.ResourceInterface,
	objectName string) (string, error) {

	currentObject, err := dr.Get(ctx, objectName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	var message string

	if labels := currentObject.GetLabels(); labels != nil {
		kind := labels[ReferenceLabelKind]
		namespace := labels[ReferenceLabelNamespace]
		name := labels[ReferenceLabelName]

		message += fmt.Sprintf("Object currently deployed because of %s %s/%s.", kind, namespace, name)
	}

	message += "List of ClusterProfiles:"
	ownerRefs := currentObject.GetOwnerReferences()
	for i := range ownerRefs {
		or := &ownerRefs[i]
		if or.Kind == configv1alpha1.ClusterProfileKind {
			message += fmt.Sprintf("%s;", or.Name)
		}
	}

	return message, nil
}

// addOwnerReference adds clusterProfile as an object's OwnerReference.
// OwnerReferences are used as ref count. Different ClusterProfiles might match same cluster and
// reference same ConfigMap. This means a policy contained in a ConfigMap is deployed in a CAPI Cluster
// because of different ClusterSummary. When cleaning up, a policy can be removed only if no more ClusterSummary
// are listed as OwnerReferences.
func addOwnerReference(object *unstructured.Unstructured, clusterProfile *configv1alpha1.ClusterProfile) {
	onwerReferences := object.GetOwnerReferences()
	if onwerReferences == nil {
		onwerReferences = make([]metav1.OwnerReference, 0)
	}

	for i := range onwerReferences {
		ref := &onwerReferences[i]
		if ref.Kind == clusterProfile.Kind &&
			ref.Name == clusterProfile.Name {

			return
		}
	}

	onwerReferences = append(onwerReferences,
		metav1.OwnerReference{
			APIVersion: clusterProfile.APIVersion,
			Kind:       clusterProfile.Kind,
			Name:       clusterProfile.Name,
			UID:        clusterProfile.UID,
		},
	)

	object.SetOwnerReferences(onwerReferences)
}

// removeOwnerReference removes clusterProfile as an OwnerReference from object.
// OwnerReferences are used as ref count. Different ClusterProfiles might match same cluster and
// reference same ConfigMap. This means a policy contained in a ConfigMap is deployed in a CAPI Cluster
// because of different ClusterProfiles. When cleaning up, a policy can be removed only if no more ClusterProfiles
// are listed as OwnerReferences.
func removeOwnerReference(object *unstructured.Unstructured, clusterprofile *configv1alpha1.ClusterProfile) {
	onwerReferences := object.GetOwnerReferences()
	if onwerReferences == nil {
		return
	}

	for i := range onwerReferences {
		ref := &onwerReferences[i]
		if ref.Kind == clusterprofile.Kind &&
			ref.Name == clusterprofile.Name {

			onwerReferences[i] = onwerReferences[len(onwerReferences)-1]
			onwerReferences = onwerReferences[:len(onwerReferences)-1]
			break
		}
	}

	object.SetOwnerReferences(onwerReferences)
}

// isOnlyhOwnerReference returns true if clusterprofile is the only ownerreference for object
func isOnlyhOwnerReference(object *unstructured.Unstructured, clusterprofile *configv1alpha1.ClusterProfile) bool {
	onwerReferences := object.GetOwnerReferences()
	if onwerReferences == nil {
		return false
	}

	if len(onwerReferences) != 1 {
		return false
	}

	ref := &onwerReferences[0]
	return ref.Kind == clusterprofile.Kind &&
		ref.Name == clusterprofile.Name
}

func getEntryKey(resourceKind, resourceNamespace, resourceName string) string {
	if resourceNamespace != "" {
		return fmt.Sprintf("%s-%s-%s", resourceKind, resourceNamespace, resourceName)
	}
	return fmt.Sprintf("%s-%s", resourceKind, resourceName)
}

func getClusterReportName(clusterProfileName, clusterName string) string {
	return clusterProfileName + "--" + clusterName
}
