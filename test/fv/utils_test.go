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

package fv_test

import (
	"context"
	"fmt"
	"reflect"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

const (
	key   = "env"
	value = "fv"
)

// Byf is a simple wrapper around By.
func Byf(format string, a ...interface{}) {
	By(fmt.Sprintf(format, a...)) // ignore_by_check
}

func getClusterfeature(namePrefix string, clusterLabels map[string]string) *configv1alpha1.ClusterFeature {
	selector := ""
	for k := range clusterLabels {
		if selector != "" {
			selector += ","
		}
		selector += fmt.Sprintf("%s=%s", k, clusterLabels[k])
	}
	clusterFeature := &configv1alpha1.ClusterFeature{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
		},
		Spec: configv1alpha1.ClusterFeatureSpec{
			ClusterSelector: configv1alpha1.Selector(selector),
		},
	}

	return clusterFeature
}

func getClusterSummaryOwnerReference(clusterSummary *configv1alpha1.ClusterSummary) (*configv1alpha1.ClusterFeature, error) {
	Byf("Checking clusterSummary %s owner reference is set", clusterSummary.Name)
	for _, ref := range clusterSummary.OwnerReferences {
		if ref.Kind != configv1alpha1.ClusterFeatureKind {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if gv.Group == configv1alpha1.GroupVersion.Group {
			clusterFeature := &configv1alpha1.ClusterFeature{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: ref.Name}, clusterFeature)
			return clusterFeature, err
		}
	}
	return nil, nil
}

// getKindWorkloadClusterKubeconfig returns client to access the kind cluster used as workload cluster
func getKindWorkloadClusterKubeconfig() (client.Client, error) {
	kubeconfigPath := "workload_kubeconfig" // this file is created in this directory by Makefile during cluster creation
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	return client.New(restConfig, client.Options{Scheme: scheme})
}

// nolint: unparam // this method will be used to verify failed statuses as well
func verifyFeatureStatus(clusterSummaryName string, featureID configv1alpha1.FeatureID, status configv1alpha1.FeatureStatus) {
	Eventually(func() bool {
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSummaryName}, currentClusterSummary)
		if err != nil {
			return false
		}
		for i := range currentClusterSummary.Status.FeatureSummaries {
			if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == featureID &&
				currentClusterSummary.Status.FeatureSummaries[i].Status == status {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

// deleteClusterFeature deletes ClusterFeature and verifies all ClusterSummaries created by this ClusterFeature
// instances are also gone
func deleteClusterFeature(clusterFeature *configv1alpha1.ClusterFeature) {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			controllers.ClusterFeatureLabelName: clusterFeature.Name,
		},
	}
	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	Expect(k8sClient.List(context.TODO(), clusterSummaryList, listOptions...)).To(Succeed())

	Byf("Deleting the ClusterFeature %s", clusterFeature.Name)
	currentClusterFeature := &configv1alpha1.ClusterFeature{}
	Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(BeNil())
	Expect(k8sClient.Delete(context.TODO(), currentClusterFeature)).To(Succeed())

	for i := range clusterSummaryList.Items {
		Byf("Verifying ClusterSummary %s are gone", clusterSummaryList.Items[i].Name)
	}
	Eventually(func() bool {
		for i := range clusterSummaryList.Items {
			clusterSummaryName := clusterSummaryList.Items[i].Name
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSummaryName}, currentClusterSummary)
			if err == nil || !apierrors.IsNotFound(err) {
				return false
			}
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Verifying ClusterFeature %s is gone", clusterFeature.Name)

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

func getClusterSummary(ctx context.Context,
	clusterFeatureName, clusterNamespace, clusterName string) (*configv1alpha1.ClusterSummary, error) {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			controllers.ClusterFeatureLabelName: clusterFeatureName,
			controllers.ClusterLabelNamespace:   clusterNamespace,
			controllers.ClusterLabelName:        clusterName,
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := k8sClient.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1alpha1.GroupVersion.Group, Resource: configv1alpha1.ClusterSummaryKind}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s",
			clusterNamespace, clusterName, clusterFeatureName)
	}

	return &clusterSummaryList.Items[0], nil
}

func verifyClusterSummary(clusterFeature *configv1alpha1.ClusterFeature,
	clusterNamespace, clusterName string) *configv1alpha1.ClusterSummary {

	Byf("Verifying ClusterSummary is created")
	Eventually(func() bool {
		clusterSummary, err := getClusterSummary(context.TODO(),
			clusterFeature.Name, clusterNamespace, clusterName)
		return err == nil &&
			clusterSummary != nil
	}, timeout, pollingInterval).Should(BeTrue())

	clusterSummary, err := getClusterSummary(context.TODO(),
		clusterFeature.Name, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	Byf("Verifying ClusterSummary ownerReference")
	ref, err := getClusterSummaryOwnerReference(clusterSummary)
	Expect(err).To(BeNil())
	Expect(ref).ToNot(BeNil())
	Expect(ref.Name).To(Equal(clusterFeature.Name))

	Byf("Verifying ClusterSummary configuration")
	Eventually(func() bool {
		var currentClusterSummary *configv1alpha1.ClusterSummary
		currentClusterSummary, err = getClusterSummary(context.TODO(),
			clusterFeature.Name, clusterNamespace, clusterName)
		if err != nil {
			return false
		}

		return reflect.DeepEqual(currentClusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration,
			clusterFeature.Spec.KyvernoConfiguration) &&
			reflect.DeepEqual(currentClusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration,
				clusterFeature.Spec.PrometheusConfiguration) &&
			reflect.DeepEqual(currentClusterSummary.Spec.ClusterFeatureSpec.PolicyRefs,
				clusterFeature.Spec.PolicyRefs) &&
			reflect.DeepEqual(currentClusterSummary.Spec.ClusterNamespace, clusterNamespace) &&
			reflect.DeepEqual(currentClusterSummary.Spec.ClusterName, clusterName)
	}, timeout, pollingInterval).Should(BeTrue())

	clusterSummary, err = getClusterSummary(context.TODO(),
		clusterFeature.Name, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	return clusterSummary
}

func verifyClusterFeatureMatches(clusterFeature *configv1alpha1.ClusterFeature) {
	Byf("Verifying Cluster %s/%s is a match for ClusterFeature %s",
		kindWorkloadCluster.Namespace, kindWorkloadCluster.Name, clusterFeature.Name)
	Eventually(func() bool {
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)
		return err == nil &&
			len(currentClusterFeature.Status.MatchingClusterRefs) == 1 &&
			currentClusterFeature.Status.MatchingClusterRefs[0].Namespace == kindWorkloadCluster.Namespace &&
			currentClusterFeature.Status.MatchingClusterRefs[0].Name == kindWorkloadCluster.Name
	}, timeout, pollingInterval).Should(BeTrue())
}

// createConfigMapWithPolicy creates a configMap with passed in policies
// nolint: unparam // want to keep namespace as args
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

type policy struct {
	name      string
	namespace string
	kind      string
	group     string
}

func verifyClusterConfiguration(clusterFeatureName, clusterNamespace, clusterName string,
	featureID configv1alpha1.FeatureID, expectedPolicies []policy) {

	Eventually(func() bool {
		currentClusterConfiguration := &configv1alpha1.ClusterConfiguration{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterNamespace, Name: clusterName}, currentClusterConfiguration)
		if err != nil {
			return false
		}
		if currentClusterConfiguration.Status.ClusterFeatureResources == nil {
			return false
		}
		return verifyClusterConfigurationEntryForClusterFeature(currentClusterConfiguration, clusterFeatureName,
			featureID, expectedPolicies)
	}, timeout, pollingInterval).Should(BeTrue())
}

func verifyClusterConfigurationEntryForClusterFeature(clusterConfiguration *configv1alpha1.ClusterConfiguration,
	clusterFeatureName string, featureID configv1alpha1.FeatureID, expectedPolicies []policy) bool {

	for i := range clusterConfiguration.Status.ClusterFeatureResources {
		if clusterConfiguration.Status.ClusterFeatureResources[i].ClusterFeatureName == clusterFeatureName {
			return verifyClusterConfigurationPolicies(clusterConfiguration.Status.ClusterFeatureResources[i].Features, featureID, expectedPolicies)
		}
	}

	return false
}

func verifyClusterConfigurationPolicies(deployedFeatures []configv1alpha1.Feature, featureID configv1alpha1.FeatureID,
	expectedPolicies []policy) bool {

	index := -1
	for i := range deployedFeatures {
		if deployedFeatures[i].FeatureID == featureID {
			index = i
		}
	}

	if index < 0 {
		return false
	}

	for i := range expectedPolicies {
		policy := expectedPolicies[i]
		found := false
		for j := range deployedFeatures[index].Resources {
			r := deployedFeatures[index].Resources[j]
			if r.Namespace == policy.namespace &&
				r.Name == policy.name &&
				r.Kind == policy.kind &&
				r.Group == policy.group {

				found = true
				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}
