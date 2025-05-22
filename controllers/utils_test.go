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

package controllers_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

const (
	timeout         = 40 * time.Second
	pollingInterval = 2 * time.Second
	version         = "v0.31.0"
)

const (
	upstreamClusterNamePrefix = "upstream-cluster"
	upstreamMachineNamePrefix = "upstream-machine"
	clusterProfileNamePrefix  = "cluster-profile"
)

const (
	viewClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
- apiGroups: [""] # "" indicates the core API group
  resources: ["pods"]
  verbs: ["get", "watch", "list"]`

	modifyClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
- apiGroups: [""] # "" indicates the core API group
  resources: ["pods"]
  verbs: ["get", "watch", "list", "create", "delete", "update"]`

	editClusterRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s
rules:
- apiGroups:
  - config.projectsveltos.io
  resources:
  - clustersummaries
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - config.projectsveltos.io
  resources:
  - clustersummaries/status
  verbs:
  - get
`
)

var (
	cacheSyncBackoff = wait.Backoff{
		Duration: 100 * time.Millisecond,
		Factor:   1.5,
		Steps:    8,
		Jitter:   0.4,
	}
)

func setupScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := configv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1.AddToScheme(s); err != nil {
		return nil, err
	}

	return s, nil
}

var _ = Describe("getClusterProfileOwner ", func() {
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					randomString(): randomString(),
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

		clusterSummaryName := controllers.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
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
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, cluster.Name, libsveltosv1beta1.ClusterTypeCapi)
	})

	It("getClusterProfileOwner returns ClusterProfile owner", func() {
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterProfile)).To(Succeed())

		clusterName := randomString()
		clusterSummary.OwnerReferences = []metav1.OwnerReference{
			{
				Kind:       clusterProfile.Kind,
				Name:       clusterProfile.Name,
				APIVersion: clusterProfile.APIVersion,
			},
		}

		clusterSummary.Spec = configv1beta1.ClusterSummarySpec{
			ClusterNamespace:   namespace,
			ClusterName:        clusterName,
			ClusterProfileSpec: clusterProfile.Spec,
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		owner, _, err := configv1beta1.GetProfileOwnerAndTier(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).ToNot(BeNil())
		Expect(owner.GetName()).To(Equal(clusterProfile.Name))
	})

	It("removeDuplicates removes duplicates from a slice", func() {
		ref1 := corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       randomString(),
			APIVersion: randomString(),
		}

		ref2 := corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       randomString(),
			APIVersion: randomString(),
		}

		ref3 := corev1.ObjectReference{
			Namespace:  randomString(),
			Name:       randomString(),
			Kind:       randomString(),
			APIVersion: randomString(),
		}

		original := []corev1.ObjectReference{ref1, ref2, ref1, ref3, ref2, ref3}
		result := controllers.RemoveDuplicates(original)
		Expect(len(result)).To(Equal(3))
		Expect(result).To(ContainElement(ref1))
		Expect(result).To(ContainElement(ref2))
		Expect(result).To(ContainElement(ref3))
	})

	It("getClusterProfileOwner returns nil when ClusterProfile does not exist", func() {
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterProfile)).To(Succeed())

		clusterName := randomString()
		clusterSummary.OwnerReferences = []metav1.OwnerReference{
			{
				Kind:       clusterProfile.Kind,
				Name:       clusterProfile.Name,
				APIVersion: clusterProfile.APIVersion,
			},
		}

		clusterSummary.Spec = configv1beta1.ClusterSummarySpec{
			ClusterNamespace:   namespace,
			ClusterName:        clusterName,
			ClusterProfileSpec: clusterProfile.Spec,
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		owner, _, err := configv1beta1.GetProfileOwnerAndTier(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).To(BeNil())
	})

	It("GetClusterSummary returns the ClusterSummary instance created by a ClusterProfile for a CAPI Cluster", func() {
		clusterSummary0 := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			clusterSummary0,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		currentClusterSummary, err := controllers.GetClusterSummary(context.TODO(), c, configv1beta1.ClusterProfileKind,
			clusterProfile.Name, cluster.Namespace, cluster.Name, libsveltosv1beta1.ClusterTypeCapi)
		Expect(err).To(BeNil())
		Expect(currentClusterSummary).ToNot(BeNil())
		Expect(currentClusterSummary.Name).To(Equal(clusterSummary.Name))
	})

	It("isNamespaced returns true for namespaced resources", func() {
		clusterRole, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(viewClusterRole, randomString())))
		Expect(err).To(BeNil())
		isNamespaced, err := controllers.IsNamespaced(clusterRole, testEnv.Config)
		Expect(err).To(BeNil())
		Expect(isNamespaced).To(BeFalse())

		deployment, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(deplTemplate, randomString())))
		Expect(err).To(BeNil())
		isNamespaced, err = controllers.IsNamespaced(deployment, testEnv.Config)
		Expect(err).To(BeNil())
		Expect(isNamespaced).To(BeTrue())
	})

	It("isClusterProvisioned returns true when all Features are marked Provisioned", func() {
		clusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:   clusterProfileNamePrefix + randomString(),
				Labels: map[string]string{controllers.ClusterProfileLabelName: clusterProfile.Name},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterType: libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					HelmCharts: []configv1beta1.HelmChart{
						{
							RepositoryURL:    randomString(),
							RepositoryName:   randomString(),
							ChartName:        randomString(),
							ChartVersion:     randomString(),
							ReleaseName:      randomString(),
							ReleaseNamespace: randomString(),
						},
					},
					PolicyRefs: []configv1beta1.PolicyRef{
						{
							Namespace: randomString(),
							Name:      randomString(),
							Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
						},
					},
					KustomizationRefs: []configv1beta1.KustomizationRef{
						{
							Namespace: randomString(),
							Name:      randomString(),
							Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
						},
					},
				},
			},
		}

		// Not all Features are marked as provisioned
		Expect(controllers.IsCluterSummaryProvisioned(clusterSummary)).To(BeFalse())

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: configv1beta1.FeatureHelm,
				Status:    configv1beta1.FeatureStatusProvisioned,
			},
			{
				FeatureID: configv1beta1.FeatureResources,
				Status:    configv1beta1.FeatureStatusProvisioning,
			},
		}
		// Not all Features are marked as provisioned
		Expect(controllers.IsCluterSummaryProvisioned(clusterSummary)).To(BeFalse())

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: configv1beta1.FeatureHelm,
				Status:    configv1beta1.FeatureStatusProvisioned,
			},
			{
				FeatureID: configv1beta1.FeatureResources,
				Status:    configv1beta1.FeatureStatusProvisioned,
			},
		}
		// Not all Features are marked as provisioned
		Expect(controllers.IsCluterSummaryProvisioned(clusterSummary)).To(BeFalse())

		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: configv1beta1.FeatureHelm,
				Status:    configv1beta1.FeatureStatusProvisioned,
			},
			{
				FeatureID: configv1beta1.FeatureResources,
				Status:    configv1beta1.FeatureStatusProvisioned,
			},
			{
				FeatureID: configv1beta1.FeatureKustomize,
				Status:    configv1beta1.FeatureStatusProvisioned,
			},
		}
		// all Features are marked as provisioned
		Expect(controllers.IsCluterSummaryProvisioned(clusterSummary)).To(BeTrue())
	})

	It("stringifyMap and parseMapFromString convert a map[string]string to string and back", func() {
		myMap := map[string]string{
			randomString(): randomString(),
			randomString(): fmt.Sprintf("{{ %s }}", randomString()),
			randomString(): `{{ .Cluster.spec.clusterNetwork.pods.cidrBlocks }}`,
			randomString(): `{{ (index .MgmtResources "AutoscalerSecret").data.token }}`,
			randomString(): `{{ index .Cluster.metadata.labels "region" }}`,
			randomString(): `{{ .Cluster.metadata.spec.topology.version }}`,
			randomString(): randomString() + randomString(),
		}

		stringfiedMap, err := controllers.StringifyMap(myMap)
		Expect(err).To(BeNil())

		result, err := controllers.ParseMapFromString(stringfiedMap)
		Expect(err).To(BeNil())

		for k := range myMap {
			v, ok := result[k]
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal(myMap[k]))
		}
	})

	It("deplAssociatedClusterExist returns false when associated cluster does not exist",
		func() {
			clusterNamespace := randomString()
			clusterName := randomString()
			clusterType := libsveltosv1beta1.ClusterTypeSveltos

			logger := textlogger.NewLogger(textlogger.NewConfig())

			depl := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    cluster-namespace: %s
    cluster-name: %s
    cluster-type: %s
    control-plane: drift-detection-9cfuselt9ltff9mkcg18
    feature: drift-detection
  name: drift-detection-manager
  namespace: projectsveltos
spec:
  replicas: 1
  selector:
    matchLabels:
      control-plane: drift-detection-manager
  template:
    metadata:
      annotations:
        kubectl.kubernetes.io/default-container: manager
      labels:
        control-plane: drift-detection-manager
    spec:
      containers:
      - command:
        - /manager
        image: docker.io/projectsveltos/drift-detection-manager@sha256:82525fa56bd985fb9842407262b5afce6720c4d78214a8ff69d44fa549395f4e
        `, clusterNamespace, clusterName, clusterType)

			u, err := k8s_utils.GetUnstructured([]byte(depl))
			Expect(err).To(BeNil())
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			Expect(c.Create(context.TODO(), u)).To(Succeed())

			currentDepl := &appsv1.Deployment{}
			Expect(c.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()}, currentDepl)).To(Succeed())

			exist, _, _, _ := controllers.DeplAssociatedClusterExist(context.TODO(), c, currentDepl, logger)
			Expect(exist).To(BeFalse())

			sveltosCluster := libsveltosv1beta1.SveltosCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: clusterNamespace,
					Name:      clusterName,
				},
			}
			Expect(c.Create(context.TODO(), &sveltosCluster)).To(Succeed())
			exist, _, _, _ = controllers.DeplAssociatedClusterExist(context.TODO(), c, currentDepl, logger)
			Expect(exist).To(BeTrue())
		})

	It("removeStaleResourceSummary removes stales resourceSummary instances",
		func() {
			clusterNamespace := randomString()
			clusterName := randomString()
			clusterType := libsveltosv1beta1.ClusterTypeSveltos

			logger := textlogger.NewLogger(textlogger.NewConfig())

			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterNamespace,
				},
			}

			Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

			rs := fmt.Sprintf(`apiVersion: lib.projectsveltos.io/v1beta1
kind: ResourceSummary
metadata:
  labels:
    projectsveltos.io/cluster-summary-namespace: %s
    version.projectsveltos.io/clustername: %s
    version.projectsveltos.io/clustertype: %s
  name: deploy-cert-manager-sveltos-cluster1
  namespace: %s
`, clusterNamespace, clusterName, strings.ToLower(string(clusterType)), clusterNamespace)

			u, err := k8s_utils.GetUnstructured([]byte(rs))
			Expect(err).To(BeNil())

			Expect(testEnv.Create(context.TODO(), u)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, u)).To(Succeed())

			currentRS := &libsveltosv1beta1.ResourceSummary{}

			Expect(controllers.RemoveStaleResourceSummary(context.TODO(), clusterNamespace, clusterName,
				clusterType, logger)).To(Succeed())

			Eventually(func() bool {
				err = testEnv.Get(context.TODO(),
					types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()}, currentRS)
				if err == nil {
					return false
				}
				return apierrors.IsNotFound(err)
			}, timeout, pollingInterval).Should(BeTrue())
		})
})

func getClusterRef(cluster client.Object) *corev1.ObjectReference {
	apiVersion, kind := cluster.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
	return &corev1.ObjectReference{
		Namespace:  cluster.GetNamespace(),
		Name:       cluster.GetName(),
		APIVersion: apiVersion,
		Kind:       kind,
	}
}
