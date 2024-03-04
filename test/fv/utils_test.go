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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	key              = "env"
	value            = "fv"
	defaultNamespace = "default"
)

// Byf is a simple wrapper around By.
func Byf(format string, a ...interface{}) {
	By(fmt.Sprintf(format, a...)) // ignore_by_check
}

func getClusterProfile(namePrefix string, clusterLabels map[string]string) *configv1alpha1.ClusterProfile {
	selector := ""
	for k := range clusterLabels {
		if selector != "" {
			selector += ","
		}
		selector += fmt.Sprintf("%s=%s", k, clusterLabels[k])
	}
	clusterProfile := &configv1alpha1.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
		},
		Spec: configv1alpha1.Spec{
			ClusterSelector: libsveltosv1alpha1.Selector(selector),
		},
	}

	return clusterProfile
}

func getProfile(namespace, namePrefix string, clusterLabels map[string]string) *configv1alpha1.Profile {
	selector := ""
	for k := range clusterLabels {
		if selector != "" {
			selector += ","
		}
		selector += fmt.Sprintf("%s=%s", k, clusterLabels[k])
	}
	profile := &configv1alpha1.Profile{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namePrefix + randomString(),
			Namespace: namespace,
		},
		Spec: configv1alpha1.Spec{
			ClusterSelector: libsveltosv1alpha1.Selector(selector),
		},
	}

	return profile
}

func getClusterSummaryOwnerReference(clusterSummary *configv1alpha1.ClusterSummary) (client.Object, error) {
	Byf("Checking clusterSummary %s owner reference is set", clusterSummary.Name)
	for _, ref := range clusterSummary.OwnerReferences {
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		if gv.Group != configv1alpha1.GroupVersion.Group {
			continue
		}

		if ref.Kind == configv1alpha1.ClusterProfileKind {
			clusterProfile := &configv1alpha1.ClusterProfile{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: ref.Name}, clusterProfile)
			return clusterProfile, err
		} else if ref.Kind == configv1alpha1.ProfileKind {
			profile := &configv1alpha1.Profile{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: ref.Name},
				profile)
			return profile, err
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

func verifyFeatureStatusIsProvisioned(clusterSummaryNamespace, clusterSummaryName string, featureID configv1alpha1.FeatureID) {
	Eventually(func() bool {
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName},
			currentClusterSummary)
		if err != nil {
			return false
		}
		for i := range currentClusterSummary.Status.FeatureSummaries {
			if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == featureID &&
				currentClusterSummary.Status.FeatureSummaries[i].Status == configv1alpha1.FeatureStatusProvisioned &&
				currentClusterSummary.Status.FeatureSummaries[i].FailureMessage == nil {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

// deleteClusterProfile deletes ClusterProfile and verifies all ClusterSummaries created by this ClusterProfile
// instances are also gone
func deleteClusterProfile(clusterProfile *configv1alpha1.ClusterProfile) {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			controllers.ClusterProfileLabelName: clusterProfile.Name,
		},
	}
	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	Expect(k8sClient.List(context.TODO(), clusterSummaryList, listOptions...)).To(Succeed())

	Byf("Deleting the ClusterProfile %s", clusterProfile.Name)
	currentClusterProfile := &configv1alpha1.ClusterProfile{}
	Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(BeNil())
	Expect(k8sClient.Delete(context.TODO(), currentClusterProfile)).To(Succeed())

	for i := range clusterSummaryList.Items {
		Byf("Verifying ClusterSummary %s are gone", clusterSummaryList.Items[i].Name)
	}
	Eventually(func() bool {
		for i := range clusterSummaryList.Items {
			clusterSummaryNamespace := clusterSummaryList.Items[i].Namespace
			clusterSummaryName := clusterSummaryList.Items[i].Name
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName}, currentClusterSummary)
			if err == nil || !apierrors.IsNotFound(err) {
				return false
			}
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Verifying ClusterProfile %s is gone", clusterProfile.Name)

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())
}

// deleteProfile deletes Profile and verifies all ClusterSummaries created by this Profile
// instances are also gone
func deleteProfile(profile *configv1alpha1.Profile) {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			controllers.ProfileLabelName: profile.Name,
		},
	}
	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	Expect(k8sClient.List(context.TODO(), clusterSummaryList, listOptions...)).To(Succeed())

	Byf("Deleting the Profile %s", profile.Name)
	currentProfile := &configv1alpha1.Profile{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name},
		currentProfile)).To(BeNil())
	Expect(k8sClient.Delete(context.TODO(), currentProfile)).To(Succeed())

	for i := range clusterSummaryList.Items {
		Byf("Verifying ClusterSummary %s are gone", clusterSummaryList.Items[i].Name)
	}
	Eventually(func() bool {
		for i := range clusterSummaryList.Items {
			clusterSummaryNamespace := clusterSummaryList.Items[i].Namespace
			clusterSummaryName := clusterSummaryList.Items[i].Name
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummaryNamespace, Name: clusterSummaryName}, currentClusterSummary)
			if err == nil || !apierrors.IsNotFound(err) {
				return false
			}
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Verifying ClusterProfile %s is gone", profile.Name)

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name},
			currentProfile)
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())
}

func randomString() string {
	const length = 10
	return "fv-" + util.RandomString(length)
}

func getClusterSummary(ctx context.Context,
	profileLabelKey, profileName, clusterNamespace, clusterName string) (*configv1alpha1.ClusterSummary, error) {

	listOptions := []client.ListOption{
		client.InNamespace(clusterNamespace),
		client.MatchingLabels{
			profileLabelKey:                 profileName,
			configv1alpha1.ClusterNameLabel: clusterName,
			configv1alpha1.ClusterTypeLabel: string(libsveltosv1alpha1.ClusterTypeCapi),
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
			clusterNamespace, clusterName, profileName)
	}

	return &clusterSummaryList.Items[0], nil
}

func verifyClusterSummary(profileLabelKey, profileName string,
	profileSpec *configv1alpha1.Spec,
	clusterNamespace, clusterName string) *configv1alpha1.ClusterSummary {

	Byf("Verifying ClusterSummary is created")
	Eventually(func() bool {
		clusterSummary, err := getClusterSummary(context.TODO(),
			profileLabelKey, profileName, clusterNamespace, clusterName)
		return err == nil &&
			clusterSummary != nil
	}, timeout, pollingInterval).Should(BeTrue())

	clusterSummary, err := getClusterSummary(context.TODO(),
		profileLabelKey, profileName, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	Byf("Verifying ClusterSummary ownerReference")
	ref, err := getClusterSummaryOwnerReference(clusterSummary)
	Expect(err).To(BeNil())
	Expect(ref).ToNot(BeNil())
	Expect(ref.GetName()).To(Equal(profileName))

	Byf("Verifying ClusterSummary configuration")
	Eventually(func() error {
		var currentClusterSummary *configv1alpha1.ClusterSummary
		currentClusterSummary, err = getClusterSummary(context.TODO(),
			profileLabelKey, profileName, clusterNamespace, clusterName)
		if err != nil {
			return err
		}

		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterProfileSpec.HelmCharts,
			profileSpec.HelmCharts) {

			return fmt.Errorf("helmCharts do not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs,
			profileSpec.PolicyRefs) {

			return fmt.Errorf("policyRefs do not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterProfileSpec.KustomizationRefs,
			profileSpec.KustomizationRefs) {

			return fmt.Errorf("policyRefs do not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterNamespace, clusterNamespace) {
			return fmt.Errorf("clusterNamespace does not match")
		}
		if !reflect.DeepEqual(currentClusterSummary.Spec.ClusterName, clusterName) {
			return fmt.Errorf("clusterName does not match")
		}
		return nil
	}, timeout, pollingInterval).Should(BeNil())

	clusterSummary, err = getClusterSummary(context.TODO(),
		profileLabelKey, profileName, clusterNamespace, clusterName)
	Expect(err).To(BeNil())
	Expect(clusterSummary).ToNot(BeNil())

	return clusterSummary
}

func verifyClusterProfileMatches(clusterProfile *configv1alpha1.ClusterProfile) {
	Byf("Verifying Cluster %s/%s is a match for ClusterProfile %s",
		kindWorkloadCluster.Namespace, kindWorkloadCluster.Name, clusterProfile.Name)
	Eventually(func() bool {
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)
		if err != nil {
			return false
		}
		for i := range currentClusterProfile.Status.MatchingClusterRefs {
			if currentClusterProfile.Status.MatchingClusterRefs[i].Namespace == kindWorkloadCluster.Namespace &&
				currentClusterProfile.Status.MatchingClusterRefs[i].Name == kindWorkloadCluster.Name &&
				currentClusterProfile.Status.MatchingClusterRefs[i].APIVersion == clusterv1.GroupVersion.String() {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

func verifyProfileMatches(profile *configv1alpha1.Profile) {
	Byf("Verifying Cluster %s/%s is a match for Profile %s/%s",
		kindWorkloadCluster.Namespace, kindWorkloadCluster.Name,
		profile.Namespace, profile.Name)
	Eventually(func() bool {
		currentProfile := &configv1alpha1.Profile{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: profile.Namespace, Name: profile.Name},
			currentProfile)
		if err != nil {
			return false
		}
		for i := range currentProfile.Status.MatchingClusterRefs {
			if currentProfile.Status.MatchingClusterRefs[i].Namespace == kindWorkloadCluster.Namespace &&
				currentProfile.Status.MatchingClusterRefs[i].Name == kindWorkloadCluster.Name &&
				currentProfile.Status.MatchingClusterRefs[i].APIVersion == clusterv1.GroupVersion.String() {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

// createConfigMapWithPolicy creates a configMap with passed in policies.
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
		cm.Data[key] = policyStrs[i]
	}

	return cm
}

// createSecretWithPolicy creates a Secret with Data containing base64 encoded policies
func createSecretWithPolicy(namespace, configMapName string, policyStrs ...string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      configMapName,
		},
		Type: libsveltosv1alpha1.ClusterProfileSecretType,
		Data: map[string][]byte{},
	}
	for i := range policyStrs {
		key := fmt.Sprintf("policy%d.yaml", i)
		secret.Data[key] = []byte(policyStrs[i])
	}

	return secret
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

func verifyClusterConfiguration(profileKind, profileName, clusterNamespace, clusterName string,
	featureID configv1alpha1.FeatureID, expectedPolicies []policy, expectedCharts []configv1alpha1.Chart) {

	Byf("Verifying ClusterConfiguration %s/%s", clusterNamespace, clusterName)
	Eventually(func() bool {
		currentClusterConfiguration := &configv1alpha1.ClusterConfiguration{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{
				Namespace: clusterNamespace,
				Name:      "capi--" + clusterName,
			}, currentClusterConfiguration)
		if err != nil {
			return false
		}
		if profileKind == configv1alpha1.ClusterProfileKind {
			if currentClusterConfiguration.Status.ClusterProfileResources == nil {
				return false
			}
			return verifyClusterConfigurationEntryForClusterProfile(currentClusterConfiguration, profileName,
				featureID, expectedPolicies, expectedCharts)
		} else {
			if currentClusterConfiguration.Status.ProfileResources == nil {
				return false
			}
			return verifyClusterConfigurationEntryForProfile(currentClusterConfiguration, profileName,
				featureID, expectedPolicies, expectedCharts)
		}
	}, timeout, pollingInterval).Should(BeTrue())
}

func verifyClusterConfigurationEntryForClusterProfile(clusterConfiguration *configv1alpha1.ClusterConfiguration,
	clusterProfileName string, featureID configv1alpha1.FeatureID,
	expectedPolicies []policy, expectedCharts []configv1alpha1.Chart) bool {

	for i := range clusterConfiguration.Status.ClusterProfileResources {
		if clusterConfiguration.Status.ClusterProfileResources[i].ClusterProfileName == clusterProfileName {
			return verifyClusterConfigurationPolicies(clusterConfiguration.Status.ClusterProfileResources[i].Features,
				featureID, expectedPolicies, expectedCharts)
		}
	}

	return false
}

func verifyClusterConfigurationEntryForProfile(clusterConfiguration *configv1alpha1.ClusterConfiguration,
	profileName string, featureID configv1alpha1.FeatureID,
	expectedPolicies []policy, expectedCharts []configv1alpha1.Chart) bool {

	for i := range clusterConfiguration.Status.ProfileResources {
		if clusterConfiguration.Status.ProfileResources[i].ProfileName == profileName {
			return verifyClusterConfigurationPolicies(clusterConfiguration.Status.ProfileResources[i].Features,
				featureID, expectedPolicies, expectedCharts)
		}
	}

	return false
}

func verifyClusterConfigurationPolicies(deployedFeatures []configv1alpha1.Feature, featureID configv1alpha1.FeatureID,
	expectedPolicies []policy, expectedCharts []configv1alpha1.Chart) bool {

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

	for i := range expectedCharts {
		chart := expectedCharts[i]
		found := false
		By(fmt.Sprintf("Verifying %s %s %s", chart.ReleaseName, chart.ChartVersion, chart.Namespace))
		for j := range deployedFeatures[index].Charts {
			c := deployedFeatures[index].Charts[j]
			if c.ReleaseName == chart.ReleaseName &&
				c.ChartVersion == chart.ChartVersion &&
				c.Namespace == chart.Namespace {

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

func verifyExtraLabels(u *unstructured.Unstructured, extraLabels map[string]string) {
	if extraLabels == nil {
		return
	}

	if len(extraLabels) == 0 {
		return
	}

	labels := u.GetLabels()
	Expect(labels).ToNot(BeNil())
	for k := range extraLabels {
		Expect(labels[k]).To(Equal(extraLabels[k]))
	}
}

func verifyExtraAnnotations(u *unstructured.Unstructured, extraAnnotations map[string]string) {
	if extraAnnotations == nil {
		return
	}

	if len(extraAnnotations) == 0 {
		return
	}

	annotations := u.GetAnnotations()
	Expect(annotations).ToNot(BeNil())
	for k := range extraAnnotations {
		Expect(annotations[k]).To(Equal(extraAnnotations[k]))
	}
}
