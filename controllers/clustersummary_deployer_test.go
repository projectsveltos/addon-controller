/*
Copyright 2022-24. projectsveltos.io. All rights reserved.

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
	"reflect"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
)

var _ = Describe("ClustersummaryDeployer", func() {
	var logger logr.Logger
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var clusterName string

	BeforeEach(func() {
		namespace = randomString()

		logger = textlogger.NewLogger(textlogger.NewConfig())

		clusterProfile = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
		}

		clusterName = randomString()
		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, clusterName, false)
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, clusterName, libsveltosv1beta1.ClusterTypeCapi)

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: clusterSummary.Spec.ClusterNamespace,
				Name:      clusterSummary.Spec.ClusterName,
			},
		}
	})

	It("isFeatureDeployed returns false when feature is not deployed", func() {
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		Expect(controllers.IsFeatureDeployed(reconciler, clusterSummaryScope.ClusterSummary, libsveltosv1beta1.FeatureResources)).To(BeFalse())
	})

	It("isFeatureDeployed returns true when feature is deployed", func() {
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		Expect(controllers.IsFeatureDeployed(reconciler, clusterSummaryScope.ClusterSummary, libsveltosv1beta1.FeatureHelm)).To(BeTrue())
	})

	It("IsFeatureFailedWithNonRetriableError returns false when feature has not failed", func() {
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		Expect(controllers.IsFeatureFailedWithNonRetriableError(reconciler, clusterSummaryScope.ClusterSummary,
			libsveltosv1beta1.FeatureResources)).To(BeFalse())
	})

	It("IsFeatureFailedWithNonRetriableError returns true when feature has failed with non retriable error", func() {
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Status:    libsveltosv1beta1.FeatureStatusFailedNonRetriable,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		Expect(controllers.IsFeatureFailedWithNonRetriableError(reconciler, clusterSummaryScope.ClusterSummary,
			libsveltosv1beta1.FeatureResources)).To(BeTrue())
	})

	It("getHash returns nil when hash is not stored", func() {
		hash := []byte(randomString())
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureHelm,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				Hash:      hash,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		currentHash := controllers.GetHash(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources)
		Expect(currentHash).To(BeNil())
	})

	It("getHash returns stored hash", func() {
		hash := []byte(randomString())
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				Hash:      hash,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		currentHash := controllers.GetHash(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources)
		Expect(reflect.DeepEqual(currentHash, hash)).To(BeTrue())
	})

	It("updateFeatureStatus updates ClusterSummary Status FeatureSummary", func() {
		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		hash := []byte(randomString())
		status := libsveltosv1beta1.FeatureStatusFailed
		statusErr := fmt.Errorf("failed to deploy")
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &status,
			hash, statusErr, textlogger.NewLogger(textlogger.NewConfig()))

		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(libsveltosv1beta1.FeatureResources))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(libsveltosv1beta1.FeatureStatusFailed))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(Equal(statusErr.Error()))

		status = libsveltosv1beta1.FeatureStatusProvisioned
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &status,
			hash, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(libsveltosv1beta1.FeatureResources))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(libsveltosv1beta1.FeatureStatusProvisioned))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(BeNil())
	})

	It("deployFeature when feature is deployed and hash has not changed, does nothing", func() {
		clusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
				{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
			},
		}
		Expect(addTypeInformationToObject(scheme, clusterRole)).To(Succeed())

		configMap := createConfigMapWithPolicy("default", randomString(), render.AsCode(clusterRole))
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}

		initObjects := []client.Object{
			configMap,
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		ResourcesHash, err := controllers.ResourcesHash(ctx, c, clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Hash:      ResourcesHash,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		// ClusterSummary Status is reporting feature has deployed. Configuration that needs to be deployed has not
		// changed (so hash in ClusterSummary Status matches hash of all referenced ResourceRefs).
		// DeployeFeature is supposed to return before calling dep.Deploy (fake deployer Deploy once called simply
		// adds key to InProgress).
		// So run DeployFeature then validate key is not added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false)
		Expect(dep.IsKeyInProgress(key)).To(BeFalse())
	})

	It("deployFeature when feature is deployed and hash has changed, calls Deploy", func() {
		clusterRoleName := randomString()
		configMap := createConfigMapWithPolicy("default", randomString(), fmt.Sprintf(viewClusterRole, clusterRoleName))

		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterProfile)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterProfile)).To(Succeed())

		clusterSummaryScope := getClusterSummaryScope(testEnv.Client, logger, clusterProfile, clusterSummary)

		resourcesHash, err := controllers.ResourcesHash(ctx, testEnv.Client, clusterSummary, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Hash:      resourcesHash,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}

		Expect(testEnv.Client.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		// Change clusterRole so the configuration that now needs to be deployed does not match
		// the hash in ClusterSummary Status anymore
		updateConfigMapWithPolicy(configMap, fmt.Sprintf(modifyClusterRole, clusterRoleName))
		Expect(testEnv.Client.Update(context.TODO(), configMap)).To(Succeed())
		Eventually(func() bool {
			clusterConfigMap := &corev1.ConfigMap{}
			err = testEnv.Client.Get(context.TODO(),
				types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, clusterConfigMap)
			if err != nil {
				return false
			}
			return reflect.DeepEqual(configMap.Data, clusterConfigMap.Data)
		}, timeout, pollingInterval).Should(BeTrue())

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), testEnv.Client)

		reconciler := getClusterSummaryReconciler(testEnv.Client, dep)

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		// Even though the feature is marked as deployed in ClusterSummary Status, the configuration has changed (ClusterSummary Status Hash
		// does not match anymore the hash of all referenced ResourceRefs). In such situation, DeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run DeployFeature then validate key is added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("request is queued"))

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	It("deployFeature when feature is not deployed, calls Deploy", func() {
		configMap := createConfigMapWithPolicy(namespace, randomString(), fmt.Sprintf(viewClusterRole, randomString()))
		Expect(addTypeInformationToObject(scheme, configMap)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMap.Namespace,
			},
		}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), clusterSummary)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterProfile)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterProfile)).To(Succeed())

		clusterSummaryScope := getClusterSummaryScope(testEnv.Client, logger, clusterProfile, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), testEnv.Client)

		reconciler := getClusterSummaryReconciler(testEnv.Client, dep)

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		// The feature is not marked as deployed in ClusterSummary Status. In such situation, DeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run DeployFeature then validate key is added to InProgress
		err := controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("request is queued"))

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	It("undeployFeature when feature is removed, does nothing", func() {
		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Status:    libsveltosv1beta1.FeatureStatusRemoved,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		// ClusterSummary Status is reporting feature has removed.
		// UndeployFeature is supposed to return before calling dep.Deploy (fake deployer Deploy once called simply
		// adds key to InProgress).
		// So run UndeployFeature then validate key is not added to InProgress
		err := controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, true)
		Expect(dep.IsKeyInProgress(key)).To(BeFalse())
	})

	It("undeployFeature when feature is not removed, calls Deploy", func() {
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: randomString(),
				Name:      randomString(),
				Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		// The feature is not marked as removed in ClusterSummary Status. In such situation, UndeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run UndeployFeature then validate key is added to InProgress
		err := controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("cleanup request is queued"))

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, true)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	//nolint: dupl // better readibility of test
	It("deployFeature return an error if cleaning up is in progress", func() {
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: randomString(), Name: randomString(),
				Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)
		dep.StoreInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, true)
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusRemoving},
		}

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		err := controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("cleanup of Resources still in progress. Wait before redeploying"))
	})

	//nolint: dupl // better readibility of test
	It("undeployFeatures returns an error if deploying is in progress", func() {
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: randomString(), Name: randomString(),
				Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterProfile,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)
		dep.StoreInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false)
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioning},
		}

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)

		err := controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("deploying Resources still in progress. Wait before cleanup"))
	})
})

var _ = Describe("Convert result", func() {
	It("convertResultStatus correctly converts deployer.ResultStatus to FeatureStatus", func() {
		reconciler := getClusterSummaryReconciler(nil, nil)

		result := deployer.Result{
			ResultStatus: deployer.InProgress,
		}
		featureStatus := controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(libsveltosv1beta1.FeatureStatusProvisioning))

		result.ResultStatus = deployer.Deployed
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(libsveltosv1beta1.FeatureStatusProvisioned))

		result.ResultStatus = deployer.Failed
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(libsveltosv1beta1.FeatureStatusFailed))

		result.ResultStatus = deployer.Removed
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(libsveltosv1beta1.FeatureStatusRemoved))

		result.ResultStatus = deployer.Unavailable
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).To(BeNil())
	})
})

func getClusterSummaryScope(c client.Client, logger logr.Logger,
	clusterProfile *configv1beta1.ClusterProfile, clusterSummary *configv1beta1.ClusterSummary,
) *scope.ClusterSummaryScope {

	clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
		Client:         c,
		Logger:         logger,
		Profile:        clusterProfile,
		ClusterSummary: clusterSummary,
		ControllerName: "clustersummary",
	})
	Expect(err).To(BeNil())
	return clusterSummaryScope
}
