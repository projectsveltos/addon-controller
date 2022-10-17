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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

const (
	installation = `apiVersion: operator.tigera.io/v1
kind: Installation
metadata:
  name: default
spec:
  # Configures Calico networking.
  calicoNetwork:
    # Note: The ipPools section cannot be modified post-install.
    ipPools:
    - blockSize: 26
      cidr:  "{{ Cluster:/spec/clusterNetwork/pods/cidrBlocks/0 }}"
      encapsulation: VXLANCrossSubnet
      natOutgoing: Enabled
      nodeSelector: all()`

	configMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: set-label
  namespace: default
  labels:
    "{{ Cluster:/metadata/labels }}"`

	templateClusterProfile = `apiVersion: v1alpha1
kind: ClusterProfile
metadata:
  name: set-list
spec:
  policyRefs:
    "{{ get-cluster-feature:/spec/policyRefs }}"`
)

var _ = Describe("Template Utils", func() {
	var cluster *clusterv1.Cluster
	var clusterProfile *configv1alpha1.ClusterProfile
	var clusterSummary *configv1alpha1.ClusterSummary
	var substituitionRule *configv1alpha1.SubstitutionRule
	var namespace string

	BeforeEach(func() {
		namespace = "template" + randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc":   "eng",
					"zone": "central",
				},
			},
		}

		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: selector,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterProfile.Name, cluster.Name)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: cluster.Namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
			},
		}

		substituitionRule = &configv1alpha1.SubstitutionRule{
			ObjectMeta: metav1.ObjectMeta{
				Name: "first",
			},
			Spec: configv1alpha1.SubstitutionRuleSpec{
				Kind:       "ConfigMap",
				APIVersion: "/v1",
				Namespace:  "Cluster:/metadata/namespace",
				Name:       "Cluster:/metadata/name",
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

	It("isTemplate returns true when PolicyTemplate annotation is set", func() {
		installation, err := controllers.GetUnstructured([]byte(installation))
		Expect(err).To(BeNil())

		Expect(controllers.IsTemplate(installation)).To(BeFalse())

		annotations := installation.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
			annotations[controllers.PolicyTemplate] = "ok"
		}
		installation.SetAnnotations(annotations)
		Expect(controllers.IsTemplate(installation)).To(BeTrue())
	})

	It("propValue returns correct property value", func() {
		key := "lab"
		value := "est"
		replicas := int32(3)
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
				Labels: map[string]string{
					randomString(): randomString(),
					key:            value,
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
			},
		}

		initObjects := []client.Object{depl}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		unstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(depl)
		Expect(err).To(BeNil())

		v, err := controllers.PropValue(context.TODO(), c, unstructured, "/metadata/name", klogr.New())
		Expect(err).To(BeNil())
		Expect(v).To(Equal(depl.Name))

		v, err = controllers.PropValue(context.TODO(), c, unstructured, "/spec/replicas", klogr.New())
		Expect(err).To(BeNil())
		Expect(v).To(Equal(fmt.Sprintf("%d", replicas)))

		v, err = controllers.PropValue(context.TODO(), c, unstructured,
			fmt.Sprintf("/metadata/labels/%s", key), klogr.New())
		Expect(err).To(BeNil())
		Expect(v).To(Equal(value))
	})

	It("getObject returns correct object. One level of indirection", func() {
		Expect(testEnv.Create(context.TODO(), substituitionRule)).To(Succeed())

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
			},
		}
		By(fmt.Sprintf("creating configMap %s/%s", configMap.Namespace, configMap.Name))
		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, configMap)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), configMap)).To(Succeed())

		object, err := controllers.GetObject(context.TODO(), testEnv.Client, testEnv.Config,
			clusterSummary, substituitionRule, klogr.New())
		Expect(err).To(BeNil())
		Expect(object).ToNot(BeNil())
		Expect(object.GetKind()).To(Equal(configMap.Kind))
		Expect(object.GetName()).To(Equal(configMap.Name))
		Expect(object.GetNamespace()).To(Equal(configMap.Namespace))
	})

	It("getObject returns correct object. Two level of indirection", func() {
		key := "zone"
		value := "west"
		// This ConfigMap is referenced from SubstitutionRule first
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Name,
				Namespace: cluster.Namespace,
				Labels:    map[string]string{key: value},
			},
		}
		By(fmt.Sprintf("creating configMap %s/%s", configMap.Namespace, configMap.Name))
		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, configMap)).To(Succeed())

		// This Role is referenced from SubstitutionRule second
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      value,
				Namespace: cluster.Namespace,
			},
		}
		By(fmt.Sprintf("creating role %s/%s", role.Namespace, role.Name))
		Expect(testEnv.Create(context.TODO(), role)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, role)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), role)).To(Succeed())

		substituitionRule2 := &configv1alpha1.SubstitutionRule{
			ObjectMeta: metav1.ObjectMeta{
				Name: "second",
			},
			Spec: configv1alpha1.SubstitutionRuleSpec{
				Kind:       "Role",
				APIVersion: "rbac.authorization.k8s.io/v1",
				Namespace:  "Cluster:/metadata/namespace",
				Name:       fmt.Sprintf("first:/metadata/labels/%s", key),
			},
		}

		Expect(testEnv.Create(context.TODO(), substituitionRule2)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, substituitionRule2)).To(Succeed())

		object, err := controllers.GetObject(context.TODO(), testEnv.Client, testEnv.Config,
			clusterSummary, substituitionRule2, klogr.New())
		Expect(err).To(BeNil())
		Expect(object).ToNot(BeNil())
		Expect(object.GetKind()).To(Equal(role.Kind))
		Expect(object.GetName()).To(Equal(role.Name))
		Expect(object.GetNamespace()).To(Equal(role.Namespace))
	})

	It("InstantiateTemplate instantiates a policy template", func() {
		currentSubstitutionRule := &configv1alpha1.SubstitutionRule{}
		Expect(testEnv.Client.Get(context.TODO(),
			types.NamespacedName{Name: substituitionRule.Name}, currentSubstitutionRule)).To(Succeed())

		By(fmt.Sprintf("Updating SubstitutionRule %s to reference Cluster", substituitionRule.Name))
		currentSubstitutionRule.Spec = configv1alpha1.SubstitutionRuleSpec{
			Kind:       "Cluster",
			APIVersion: "cluster.x-k8s.io/v1beta1",
			Namespace:  "Cluster:/metadata/namespace",
			Name:       "Cluster:/metadata/name",
		}
		Expect(testEnv.Client.Update(context.TODO(), currentSubstitutionRule)).To(Succeed())

		By("Setting Cluster.Spec.ClusterNetwork.Pods")
		podCird := "171.11.12.0/24"
		currentCluster := &clusterv1.Cluster{}
		Expect(testEnv.Client.Get(context.TODO(),
			types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, currentCluster)).To(Succeed())
		currentCluster.Spec.ClusterNetwork = &clusterv1.ClusterNetwork{
			Pods: &clusterv1.NetworkRanges{
				CIDRBlocks: []string{podCird},
			},
		}
		Expect(testEnv.Client.Update(context.TODO(), currentCluster)).To(Succeed())

		By("Instantiate a policy that requires a string as substitution value")
		By("Using a policy template  cidr:  \"{{ Cluster:/spec/clusterNetwork/pods/cidrBlocks/0 }}\"")
		Eventually(func() bool {
			policy, err := controllers.InstantiateTemplate(context.TODO(), testEnv.Client, testEnv.Config,
				clusterSummary, installation, klogr.New())
			if err != nil {
				return false
			}
			return !strings.Contains(policy, "{{ Cluster:/spec/clusterNetwork/pods/cidrBlocks/0 }}") &&
				strings.Contains(policy, podCird)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("InstantiateTemplate instantiates a policy template (map field)", func() {
		By("Instantiate a policy that requires a map as substitution value")
		policy, err := controllers.InstantiateTemplate(context.TODO(), testEnv.Client, testEnv.Config,
			clusterSummary, configMap, klogr.New())
		Expect(err).To(BeNil())
		Expect(policy).ToNot(ContainSubstring("{{ Cluster:/metadata/labels }}"))
		u, err := controllers.GetUnstructured([]byte(policy))
		Expect(err).To(BeNil())
		Expect(u.GetLabels()).ToNot(BeNil())
		Expect(len(u.GetLabels())).To(Equal(2)) // 2 is the length of labels on Cluster
	})

	It("InstantiateTemplate instantiates a policy template (slice field)", func() {
		By("Creating a ClusterProfile with same name as Cluster")
		// When instantiating later on, cf.Spec.PolicyRef will be the value of instantiation
		// Name of following clusterProfile is set to Cluster.Name and it is what
		// cfSubstituitionRule expect it.
		cp := &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: cluster.Name,
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				PolicyRefs: []configv1alpha1.PolicyRef{
					{Kind: string(configv1alpha1.SecretReferencedResourceKind), Name: randomString(), Namespace: randomString()},
					{Kind: string(configv1alpha1.SecretReferencedResourceKind), Name: randomString(), Namespace: randomString()},
					{Kind: string(configv1alpha1.SecretReferencedResourceKind), Name: randomString(), Namespace: randomString()},
				},
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), cp))
		Expect(waitForObject(ctx, testEnv.Client, cp)).To(Succeed())

		By("Creating get-cluster-feature SubstitutionRule")
		substituitionRule := &configv1alpha1.SubstitutionRule{
			ObjectMeta: metav1.ObjectMeta{
				Name: "get-cluster-feature",
			},
			Spec: configv1alpha1.SubstitutionRuleSpec{
				Kind:       "ClusterProfile",
				APIVersion: "config.projectsveltos.io/v1alpha1",
				Name:       "Cluster:/metadata/name",
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), substituitionRule)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, substituitionRule)).To(Succeed())

		By("Instantiate a policy that requires a slice as substitution value")
		policy, err := controllers.InstantiateTemplate(context.TODO(), testEnv.Client, testEnv.Config,
			clusterSummary, templateClusterProfile, klogr.New())
		Expect(err).To(BeNil())
		Expect(policy).ToNot(ContainSubstring("{{ get-cluster-feature:/spec/policyRefs }}"))
		u, err := controllers.GetUnstructured([]byte(policy))
		Expect(err).To(BeNil())

		// Convert unstructured to ClusterProfile
		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), currentClusterProfile)).To(Succeed())
		Expect(len(currentClusterProfile.Spec.PolicyRefs)).ToNot(BeZero())
		Expect(len(currentClusterProfile.Spec.PolicyRefs)).To(Equal(len(cp.Spec.PolicyRefs)))
	})
})
