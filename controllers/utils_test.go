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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

const (
	timeout         = 40 * time.Second
	pollingInterval = 2 * time.Second
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
	if err := configv1alpha1.AddToScheme(s); err != nil {
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
	if err := libsveltosv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1.AddToScheme(s); err != nil {
		return nil, err
	}

	return s, nil
}

var _ = Describe("getClusterProfileOwner ", func() {
	var clusterProfile *configv1alpha1.ClusterProfile
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		namespace = "reconcile" + randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
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

		clusterSummaryName := controllers.GetClusterSummaryName(clusterProfile.Name, cluster.Name, false)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: cluster.Namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, cluster.Name, libsveltosv1alpha1.ClusterTypeCapi)
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

		clusterSummary.Spec = configv1alpha1.ClusterSummarySpec{
			ClusterNamespace:   namespace,
			ClusterName:        clusterName,
			ClusterProfileSpec: clusterProfile.Spec,
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		owner, err := configv1alpha1.GetClusterProfileOwner(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).ToNot(BeNil())
		Expect(owner.Name).To(Equal(clusterProfile.Name))
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

		clusterSummary.Spec = configv1alpha1.ClusterSummarySpec{
			ClusterNamespace:   namespace,
			ClusterName:        clusterName,
			ClusterProfileSpec: clusterProfile.Spec,
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		owner, err := configv1alpha1.GetClusterProfileOwner(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).To(BeNil())
	})

	It("GetClusterSummary returns the ClusterSummary instance created by a ClusterProfile for a CAPI Cluster", func() {
		clusterSummary0 := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cs" + randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		initObjects := []client.Object{
			clusterProfile,
			clusterSummary,
			clusterSummary0,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		currentClusterSummary, err := controllers.GetClusterSummary(context.TODO(), c, clusterProfile.Name, cluster.Namespace, cluster.Name,
			libsveltosv1alpha1.ClusterTypeCapi)
		Expect(err).To(BeNil())
		Expect(currentClusterSummary).ToNot(BeNil())
		Expect(currentClusterSummary.Name).To(Equal(clusterSummary.Name))
	})

	It("getMgmtResourceName returns the correct name", func() {
		ref := &configv1alpha1.TemplateResourceRef{
			Resource: corev1.ObjectReference{
				Name: "{{ .ClusterNamespace }}-{{ .ClusterName }}",
			},
			Identifier: randomString(),
		}

		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cs" + randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		value, err := controllers.GetMgmtResourceName(clusterSummary, ref)
		Expect(err).To(BeNil())
		Expect(value).To(Equal(cluster.Namespace + "-" + cluster.Name))
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
