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
	"reflect"
	"strings"

	"github.com/gdexlab/go-render/render"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayapi "sigs.k8s.io/gateway-api/apis/v1beta1"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/contour"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

const (
	gatewayClass = `kind: GatewayClass
apiVersion: gateway.networking.k8s.io/v1beta1
metadata:
  name: contour
spec:
  controllerName: projectcontour.io/gateway-controller`

	deplInfoLength = 2
)

var _ = Describe("HandlersContour", func() {
	var logger logr.Logger
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

		logger = klogr.New()
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, cluster.Namespace, cluster.Name)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
			},
		}

		prepareForDeployment(clusterFeature, clusterSummary, cluster)
	})

	AfterEach(func() {
		deleteResources(namespace, clusterFeature, clusterSummary)
	})

	It("shouldInstallContourGateway returns trye only if InstallationMode is ContourInstallationModeGateway", func() {
		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{
			InstallationMode: configv1alpha1.ContourInstallationModeContour,
		}

		Expect(controllers.ShouldInstallContourGateway(clusterSummary)).To(BeFalse())

		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{
			InstallationMode: configv1alpha1.ContourInstallationModeGateway,
		}

		Expect(controllers.ShouldInstallContourGateway(clusterSummary)).To(BeTrue())
	})

	It("shouldInstallContour returns trye only if InstallationMode is ContourInstallationModeContour", func() {
		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{
			InstallationMode: configv1alpha1.ContourInstallationModeContour,
		}

		Expect(controllers.ShouldInstallContour(clusterSummary)).To(BeTrue())

		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{
			InstallationMode: configv1alpha1.ContourInstallationModeGateway,
		}

		Expect(controllers.ShouldInstallContour(clusterSummary)).To(BeFalse())
	})

	It("isContourReady returns true when Contour deployments are ready", func() {
		verifyDeploymentReady(contour.ContourDeployments, controllers.IsContourReady)
	})

	It("isContourGatewayReady returns true when Contour Gateway deployments are ready", func() {
		verifyDeploymentReady(contour.ContourGatewayDeployments, controllers.IsContourGatewayReady)
	})

	It("deployContourGateway deploys Contour Gateway Provisioner", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{}

		err := controllers.DeployContourGateway(context.TODO(), c, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("contour gateway provisioner deployment is not ready yet"))

		for i := range contour.ContourGatewayDeployments {
			currentDepl := &appsv1.Deployment{}
			deplInfo := strings.Split(contour.ContourGatewayDeployments[i], "/")
			Expect(c.Get(context.TODO(),
				types.NamespacedName{Namespace: deplInfo[0], Name: deplInfo[1]},
				currentDepl)).To(Succeed())
		}
	})

	It("deployRegularContour deploys Contour deployments", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{}

		err := controllers.DeployRegularContour(context.TODO(), c, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("contour deployment is not ready yet"))

		for i := range contour.ContourDeployments {
			currentDepl := &appsv1.Deployment{}
			deplInfo := strings.Split(contour.ContourDeployments[i], "/")
			Expect(c.Get(context.TODO(),
				types.NamespacedName{Namespace: deplInfo[0], Name: deplInfo[1]},
				currentDepl)).To(Succeed())
		}
	})

	It("deployContourGatewayInWorklaodCluster installs Gateway CRDs in a cluster", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{}

		Expect(controllers.DeployContourGatewayInWorklaodCluster(context.TODO(), c, klogr.New())).To(Succeed())

		customResourceDefinitions := &apiextensionsv1.CustomResourceDefinitionList{}
		Expect(c.List(context.TODO(), customResourceDefinitions)).To(Succeed())
		gatewayclassesFound := false
		for i := range customResourceDefinitions.Items {
			if customResourceDefinitions.Items[i].Spec.Group == "gateway.networking.k8s.io" {
				if customResourceDefinitions.Items[i].Spec.Names.Plural == "gatewayclasses" {
					gatewayclassesFound = true
				}
			}
		}
		Expect(gatewayclassesFound).To(BeTrue())
	})

	It("DeployContourInWorklaodCluster installs projectcontour.io CRDs in a cluster", func() {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummary.Spec.ClusterFeatureSpec.ContourConfiguration = &configv1alpha1.ContourConfiguration{}

		Expect(controllers.DeployContourInWorklaodCluster(context.TODO(), c, klogr.New())).To(Succeed())

		customResourceDefinitions := &apiextensionsv1.CustomResourceDefinitionList{}
		Expect(c.List(context.TODO(), customResourceDefinitions)).To(Succeed())
		foundCRD := false
		for i := range customResourceDefinitions.Items {
			if customResourceDefinitions.Items[i].Spec.Group == "projectcontour.io" {
				foundCRD = true
			}
		}
		Expect(foundCRD).To(BeTrue())
	})

	It("unDeployContour does nothing when CAPI Cluster is not found", func() {
		initObjects := []client.Object{
			clusterSummary,
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()
		err := controllers.UnDeployContour(context.TODO(), c,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).To(BeNil())
	})

	It("unDeployContour removes all Countor policies created by a ClusterSummary", func() {
		// Install ContraintTemplate CRD
		elements := strings.Split(string(contour.ContourGatewayYAML), "---\n")
		for i := range elements {
			if elements[i] == "" {
				continue
			}
			policy, err := controllers.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			if policy.GetKind() == customResourceDefinitionCRD {
				Expect(testEnv.Client.Create(context.TODO(), policy)).To(Succeed())
			}
		}

		policy0 := &gatewayapi.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: gatewayapi.GatewayClassSpec{
				ControllerName: gatewayapi.GatewayController("k8s-gateway.nginx.org/nginx-gateway/gateway"),
			},
		}

		policy1 := &gatewayapi.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
				Labels: map[string]string{
					controllers.ConfigLabelName:      randomString(),
					controllers.ConfigLabelNamespace: randomString(),
				},
			},
			Spec: gatewayapi.GatewayClassSpec{
				ControllerName: gatewayapi.GatewayController("projectcontour.io/gateway-controller"),
			},
		}

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		Expect(testEnv.Client.Create(context.TODO(), policy1)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, policy1)).To(Succeed())

		addOwnerReference(ctx, testEnv.Client, policy1, clusterSummary)

		Expect(testEnv.Client.Create(context.TODO(), policy0)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, policy0)).To(Succeed())

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)).To(Succeed())
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureContour,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"GatewayClass.v1beta1.gateway.networking.k8s.io",
				},
			},
		}
		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Wait for cache to be updated
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterSummary.Name}, currentClusterSummary)
			return err == nil &&
				currentClusterSummary.Status.FeatureSummaries != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(controllers.UnDeployContour(ctx, testEnv.Client, cluster.Namespace, cluster.Name, clusterSummary.Name,
			string(configv1alpha1.FeatureContour), logger)).To(Succeed())

		// UnDeployContour finds all contour policies deployed because of a clusterSummary and deletes those.
		// Expect all GatewayClass policies but policy0 (ConfigLabelName is not set on it) to be deleted.

		policy := &gatewayapi.GatewayClass{}
		Eventually(func() bool {
			err := testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: policy0.Name}, policy)
			if err != nil {
				return false
			}
			err = testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: policy1.Name}, policy)
			return err != nil &&
				apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("deployContour returns an error when CAPI Cluster does not exist", func() {
		currentCluster := &clusterv1.Cluster{}
		Expect(testEnv.Client.Get(context.TODO(),
			types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name}, currentCluster)).To(Succeed())
		Expect(testEnv.Delete(context.TODO(), currentCluster)).To(Succeed())
		err := controllers.DeployContour(context.TODO(), testEnv.Client,
			cluster.Namespace, cluster.Name, clusterSummary.Name, "", klogr.New())
		Expect(err).ToNot(BeNil())
	})
})

var _ = Describe("Hash methods", func() {
	It("contourHash returns hash considering all referenced configmap contents", func() {
		configMapNs := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), gatewayClass)

		namespace := "reconcile" + randomString()
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      randomString(),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					ContourConfiguration: &configv1alpha1.ContourConfiguration{
						PolicyRefs: []corev1.ObjectReference{
							{Namespace: configMapNs, Name: configMap1.Name},
							{Namespace: configMapNs, Name: randomString()},
						},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			configMap1,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		config := render.AsCode(configMap1.Data)
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.ContourHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})

func verifyDeploymentReady(depls []string,
	verify func(ctx context.Context, c client.Client, logger logr.Logger) (present bool, ready bool, err error)) {

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	replicas := int32(1)
	for i := range depls {
		deplInfo := strings.Split(depls[i], "/")
		Expect(len(deplInfo)).To(Equal(deplInfoLength))
		currentDepl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: deplInfo[0],
				Name:      deplInfo[1],
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
			},
		}
		Expect(c.Create(context.TODO(), currentDepl)).To(Succeed())
	}

	present, ready, err := verify(context.TODO(), c, klogr.New())
	Expect(err).To(BeNil())
	Expect(present).To(BeTrue())
	Expect(ready).To(BeFalse())

	for i := range depls {
		currentDepl := &appsv1.Deployment{}
		deplInfo := strings.Split(depls[i], "/")
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Namespace: deplInfo[0], Name: deplInfo[1]},
			currentDepl)).To(Succeed())
		currentDepl.Status.AvailableReplicas = 1
		currentDepl.Status.Replicas = 1
		currentDepl.Status.ReadyReplicas = 1
		Expect(c.Status().Update(context.TODO(), currentDepl)).To(Succeed())
	}

	present, ready, err = verify(context.TODO(), c, klogr.New())
	Expect(err).To(BeNil())
	Expect(present).To(BeTrue())
	Expect(ready).To(BeTrue())
}
