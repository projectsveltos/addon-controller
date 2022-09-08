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

package controllers_test

import (
	"context"
	"fmt"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/cluster-api-feature-manager/controllers"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

// addOwnerReference adds owner as OwnerReference of obj
func addOwnerReference(ctx context.Context, c client.Client, obj, owner client.Object) {
	Expect(addTypeInformationToObject(testEnv.Scheme(), owner)).To(Succeed())

	objCopy := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	Expect(c.Get(ctx, key, objCopy)).To(Succeed())
	refs := objCopy.GetOwnerReferences()
	if refs == nil {
		refs = make([]metav1.OwnerReference, 0)
	}
	refs = append(refs,
		metav1.OwnerReference{
			UID:        owner.GetUID(),
			Name:       owner.GetName(),
			Kind:       owner.GetObjectKind().GroupVersionKind().Kind,
			APIVersion: owner.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		})
	objCopy.SetOwnerReferences(refs)
	Expect(c.Update(ctx, objCopy)).To(Succeed())
}

// waitForObject waits for the cache to be updated helps in preventing test flakes due to the cache sync delays.
func waitForObject(ctx context.Context, c client.Client, obj client.Object) error {
	// Makes sure the cache is updated with the new object
	objCopy := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	if err := wait.ExponentialBackoff(
		cacheSyncBackoff,
		func() (done bool, err error) {
			if err := c.Get(ctx, key, objCopy); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}); err != nil {
		return errors.Wrapf(err, "object %s, %s is not being added to the testenv client cache", obj.GetObjectKind().GroupVersionKind().String(), key)
	}
	return nil
}

// createConfigMapWithPolicy creates a configMap with Data containing base64 encoded policies
func createConfigMapWithPolicy(namespace, configMapName string, policyStrs ...string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      configMapName,
		},
		Data: map[string]string{},
	}
	for i := range policyStrs {
		key := fmt.Sprintf("policy%d.yaml", i)
		if utf8.Valid([]byte(policyStrs[i])) {
			cm.Data[key] = policyStrs[i]
		} else {
			cm.BinaryData[key] = []byte(policyStrs[i])
		}
	}

	return cm
}

// updateConfigMapWithPolicy updates a configMap with passed in policies
func updateConfigMapWithPolicy(cm *corev1.ConfigMap, policyStrs ...string) *corev1.ConfigMap {
	for i := range policyStrs {
		key := fmt.Sprintf("policy%d.yaml", i)
		if utf8.Valid([]byte(policyStrs[i])) {
			cm.Data[key] = policyStrs[i]
		} else {
			cm.BinaryData[key] = []byte(policyStrs[i])
		}
	}

	return cm
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

func addLabelsToClusterSummary(clusterSummary *configv1alpha1.ClusterSummary, clusterFeatureName, clusterNamespace, clusterName string) {
	labels := clusterSummary.Labels
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[controllers.ClusterFeatureLabelName] = clusterFeatureName
	labels[controllers.ClusterLabelNamespace] = clusterNamespace
	labels[controllers.ClusterLabelName] = clusterName

	clusterSummary.Labels = labels
}

// deleteResources deletes following resources:
// - clusterFeature
// - clusterSummary
// - all clusterConfigurations in namespace
// - namespace
func deleteResources(namespace string,
	clusterFeature *configv1alpha1.ClusterFeature,
	clusterSummary *configv1alpha1.ClusterSummary) {

	ns := &corev1.Namespace{}
	err := testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: namespace}, ns)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		return
	}
	err = testEnv.Client.Delete(context.TODO(), ns)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}

	listOptions := []client.ListOption{
		client.InNamespace(namespace),
	}
	clusterConfigurationList := &configv1alpha1.ClusterConfigurationList{}
	Expect(testEnv.Client.List(context.TODO(), clusterConfigurationList, listOptions...)).To(Succeed())
	for i := range clusterConfigurationList.Items {
		Expect(testEnv.Client.Delete(context.TODO(), &clusterConfigurationList.Items[i])).To(Succeed())
	}

	err = testEnv.Client.Delete(context.TODO(), clusterSummary)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}
	err = testEnv.Client.Delete(context.TODO(), clusterFeature)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) error {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return fmt.Errorf("missing apiVersion or kind and cannot assign it; %w", err)
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

	return nil
}

// prepareForDeployment creates following:
// - CAPI cluster (and its namespace)
// - secret containing kubeconfig to access CAPI Cluster
// - clusterFeature/clusterSummary/clusterConfiguration
// - adds ClusterFeature as OwnerReference for both ClusterSummary and ClusterConfiguration
func prepareForDeployment(clusterFeature *configv1alpha1.ClusterFeature,
	clusterSummary *configv1alpha1.ClusterSummary,
	cluster *clusterv1.Cluster) {

	By("Add proper labels to ClusterSummary")
	addLabelsToClusterSummary(clusterSummary, clusterFeature.Name, cluster.Namespace, cluster.Name)

	Expect(addTypeInformationToObject(testEnv.Scheme(), clusterFeature)).To(Succeed())
	Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

	By("Create the secret with cluster kubeconfig")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: clusterSummary.Spec.ClusterNamespace,
			Name:      clusterSummary.Spec.ClusterName + "-kubeconfig",
		},
		Data: map[string][]byte{
			"data": testEnv.Kubeconfig,
		},
	}

	By("Create the cluster's namespace")
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterSummary.Spec.ClusterNamespace,
		},
	}

	clusterConfiguration := &configv1alpha1.ClusterConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name,
		},
	}

	Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
	Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())
	Expect(testEnv.Client.Create(context.TODO(), clusterConfiguration)).To(Succeed())
	Expect(testEnv.Client.Create(context.TODO(), clusterFeature)).To(Succeed())

	Expect(waitForObject(context.TODO(), testEnv.Client, clusterFeature)).To(Succeed())

	currentClusterFeature := &configv1alpha1.ClusterFeature{}
	Expect(testEnv.Client.Get(context.TODO(),
		types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())

	currentClusterSummary := &configv1alpha1.ClusterSummary{}
	Expect(testEnv.Client.Get(context.TODO(),
		types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())

	By("Set ClusterSummary OwnerReference")
	addOwnerReference(context.TODO(), testEnv.Client, currentClusterSummary, currentClusterFeature)

	currentClusterConfiguration := &configv1alpha1.ClusterConfiguration{}
	Expect(testEnv.Client.Get(context.TODO(),
		types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, currentClusterConfiguration)).To(Succeed())
	By("Set ClusterConfiguration OwnerReference")
	addOwnerReference(context.TODO(), testEnv.Client, currentClusterConfiguration, currentClusterFeature)

	Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

	Expect(testEnv.Client.Get(context.TODO(),
		types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, currentClusterConfiguration)).To(Succeed())
	currentClusterConfiguration.Status.ClusterFeatureResources = []configv1alpha1.ClusterFeatureResource{
		{
			ClusterFeatureName: clusterFeature.Name,
			Features:           make([]configv1alpha1.Feature, 0),
		},
	}
	Expect(testEnv.Status().Update(ctx, currentClusterConfiguration)).To(Succeed())

	Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())
}
