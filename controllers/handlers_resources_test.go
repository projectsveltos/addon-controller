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
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gdexlab/go-render/render"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/controllers/dependencymanager"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
)

var _ = Describe("HandlersResource", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

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

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, cluster.Name, false)
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: cluster.Namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		prepareForDeployment(clusterProfile, clusterSummary, cluster)

		// Get ClusterSummary so OwnerReference is set
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, clusterSummary)).To(Succeed())
	})

	AfterEach(func() {
		deleteResources(namespace, clusterProfile, clusterSummary)
	})

	It("DeployResources creates referenced ClusterRole", func() {
		clusterRoleName := randomString()
		configMap := createConfigMapWithPolicy("default", randomString(), fmt.Sprintf(viewClusterRole, clusterRoleName))

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		// We are using testEnv for both management and managed cluster. So ask Sveltos to deploy same ClusterRole in both
		// managed and management cluster. If for instance we had deployed ClusterRole just to the managed cluster,
		// then as part of cleaning stale resources in the management cluster, Sveltos would have removed it.
		currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace:      configMap.Namespace,
				Name:           configMap.Name,
				Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				DeploymentType: configv1beta1.DeploymentTypeLocal,
			},
			{
				Namespace:      configMap.Namespace,
				Name:           configMap.Name,
				Kind:           string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				DeploymentType: configv1beta1.DeploymentTypeRemote,
			},
		}
		Expect(testEnv.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, configMap)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterProfile)).To(Succeed())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			return controllers.GenericDeploy(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
				string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, deployer.Options{},
				textlogger.NewLogger(textlogger.NewConfig()))
		}, timeout, pollingInterval).Should(BeNil())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() error {
			currentClusterRole := &rbacv1.ClusterRole{}
			return testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterRoleName}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		currentClusterRole := &rbacv1.ClusterRole{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterRoleName}, currentClusterRole)).To(Succeed())
		// OwnerReference is not set anymore
		Expect(currentClusterRole.OwnerReferences).To(BeNil())
		Expect(currentClusterRole.Annotations).ToNot(BeNil())
		v, ok := currentClusterRole.Annotations[deployer.OwnerName]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(clusterProfile.Name))

		v, ok = currentClusterRole.Annotations[deployer.OwnerKind]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(configv1beta1.ClusterProfileKind))
	})

	It("unDeployResources removes all ClusterRole and Role created by a ClusterSummary", func() {
		role0 := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
				Labels: map[string]string{
					deployer.ReferenceKindLabel:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					deployer.ReferenceNameLabel:      randomString(),
					deployer.ReferenceNamespaceLabel: randomString(),
					deployer.ReasonLabel:             string(libsveltosv1beta1.FeatureResources),
				},
			},
		}

		role1 := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}

		clusterRole0 := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					deployer.ReferenceKindLabel:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					deployer.ReferenceNameLabel:      randomString(),
					deployer.ReferenceNamespaceLabel: randomString(),
					deployer.ReasonLabel:             string(libsveltosv1beta1.FeatureResources),
				},
			},
		}

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		Expect(testEnv.Create(context.TODO(), role0)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), role1)).To(Succeed())
		Expect(testEnv.Create(context.TODO(), clusterRole0)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterRole0)).To(Succeed())
		addOwnerReference(ctx, testEnv.Client, role0, clusterProfile)
		addOwnerReference(ctx, testEnv.Client, clusterRole0, clusterProfile)

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}
		currentClusterSummary.Status.DeployedGVKs = []libsveltosv1beta1.FeatureDeploymentInfo{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				DeployedGroupVersionKind: []string{
					"ClusterRole.v1.rbac.authorization.k8s.io",
					"Role.v1.rbac.authorization.k8s.io",
				},
			},
		}
		Expect(testEnv.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Wait for cache to be updated
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			return err == nil &&
				currentClusterSummary.Status.FeatureSummaries != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.GenericUndeploy(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, deployer.Options{},
			textlogger.NewLogger(textlogger.NewConfig()))).To(Succeed())

		// UnDeployResources finds all policies deployed because of a clusterSummary and deletes those.
		// Expect role0 and cluster0 to be deleted. role1 should remain as ConfigLabelName is not set on it

		currentRole := &rbacv1.Role{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: role1.Namespace, Name: role1.Name}, currentRole)
			if err != nil {
				return false
			}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: role0.Namespace, Name: role0.Name}, currentRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		currentClusterRole := &rbacv1.ClusterRole{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRole0.Name}, currentClusterRole)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("updateDeployedGroupVersionKind updates ClusterSummary Status with list of deployed GroupVersionKinds", func() {
		Expect(waitForObject(context.TODO(), testEnv.Client, clusterProfile)).To(Succeed())

		localReports := []libsveltosv1beta1.ResourceReport{
			{
				Resource: libsveltosv1beta1.Resource{
					Name:      randomString(),
					Namespace: randomString(),
					Group:     randomString(),
					Version:   randomString(),
					Kind:      randomString(),
				},
			},
		}

		remoteReports := []libsveltosv1beta1.ResourceReport{
			{
				Resource: libsveltosv1beta1.Resource{
					Name:      randomString(),
					Namespace: randomString(),
					Group:     randomString(),
					Version:   randomString(),
					Kind:      randomString(),
				},
			},
		}

		_, err := controllers.UpdateDeployedGroupVersionKind(context.TODO(), clusterSummary, libsveltosv1beta1.FeatureResources,
			localReports, remoteReports, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		// wait for cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				clusterSummary)
			return err == nil && clusterSummary.Status.DeployedGVKs != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			clusterSummary)).To(Succeed())
		Expect(clusterSummary.Status.DeployedGVKs).ToNot(BeNil())
		Expect(len(clusterSummary.Status.DeployedGVKs)).To(Equal(1))
		Expect(clusterSummary.Status.DeployedGVKs[0].FeatureID).To(Equal(libsveltosv1beta1.FeatureResources))
		Expect(clusterSummary.Status.DeployedGVKs[0].DeployedGroupVersionKind).To(ContainElement(
			fmt.Sprintf("%s.%s.%s", localReports[0].Resource.Kind, localReports[0].Resource.Version, localReports[0].Resource.Group)))
		Expect(clusterSummary.Status.DeployedGVKs[0].DeployedGroupVersionKind).To(ContainElement(
			fmt.Sprintf("%s.%s.%s", remoteReports[0].Resource.Kind, remoteReports[0].Resource.Version, remoteReports[0].Resource.Group)))
	})
})

var _ = Describe("Hash methods", func() {
	It("ResourcesHash returns hash considering all referenced core resources", func() {
		clusterRole1 := rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
				{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
			},
		}
		Expect(addTypeInformationToObject(scheme, &clusterRole1)).To(Succeed())
		configMap1 := createConfigMapWithPolicy(randomString(), randomString(), render.AsCode(clusterRole1))

		clusterRole2 := rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"get", "list"}, APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}},
			},
		}
		Expect(addTypeInformationToObject(scheme, &clusterRole2)).To(Succeed())
		configMap2 := createConfigMapWithPolicy(randomString(), randomString(), render.AsCode(clusterRole2))

		namespace := randomString()
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: configMap1.Namespace, Name: configMap1.Name,
							Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
						{
							Namespace: configMap2.Namespace, Name: configMap2.Name,
							Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
						{
							Namespace: randomString(), Name: randomString(),
							Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						},
					},
					Patches: []libsveltosv1beta1.Patch{
						{
							Patch: `- op: add
  path: /metadata/labels/environment
  value: production`,
						},
					},
					Tier: 100,
				},
			},
		}

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummary.Spec.ClusterName,
				Namespace: clusterSummary.Spec.ClusterNamespace,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			configMap1,
			configMap2,
			cluster,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		config := fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.SyncMode)
		config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Reloader)
		config += fmt.Sprintf("%v", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.Tier)
		config += fmt.Sprintf("%t", clusterSummaryScope.ClusterSummary.Spec.ClusterProfileSpec.ContinueOnConflict)
		config += render.AsCode(clusterSummary.Spec.ClusterProfileSpec.Patches)
		h := sha256.New()
		h.Write([]byte(config))
		tmpHash := h.Sum(nil)

		config = string(tmpHash)

		referencedObjects := make([]corev1.ObjectReference, len(clusterSummary.Spec.ClusterProfileSpec.PolicyRefs))
		for i := range clusterSummary.Spec.ClusterProfileSpec.PolicyRefs {
			referencedObjects[i] = corev1.ObjectReference{
				Kind:      clusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Kind,
				Namespace: clusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Namespace,
				Name:      clusterSummary.Spec.ClusterProfileSpec.PolicyRefs[i].Name,
			}
		}
		sort.Sort(dependencymanager.SortedCorev1ObjectReference(referencedObjects))
		for i := range referencedObjects {
			switch referencedObjects[i].Name {
			case configMap1.Name:
				config += controllers.GetStringDataSectionHash(configMap1.Data)
			case configMap2.Name:
				config += controllers.GetStringDataSectionHash(configMap2.Data)
			}
		}

		h = sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.ResourcesHash(context.TODO(), c, clusterSummaryScope.ClusterSummary,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
