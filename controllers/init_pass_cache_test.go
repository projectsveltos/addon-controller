/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	initCacheTestKey    = "setting"
	initCacheTestValue  = "enabled"
	initCacheTestValues = "replicaCount: 2"
)

var _ = Describe("Init pass cache", func() {
	var apiGets int
	var cachingClient client.Client
	var configMap *corev1.ConfigMap

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		apiGets = 0
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: randomString(), Name: randomString()},
			Data:       map[string]string{initCacheTestKey: initCacheTestValue},
		}

		// Count the Get calls that reach the (fake) API server
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
					obj client.Object, opts ...client.GetOption) error {

					apiGets++
					return cl.Get(ctx, key, obj, opts...)
				},
			}).Build()
		cachingClient = controllers.NewInitCachingClient(c)
	})

	It("reaches the API server on every Get when the context carries no cache", func() {
		for i := 0; i < 3; i++ {
			cm := &corev1.ConfigMap{}
			Expect(cachingClient.Get(context.TODO(), client.ObjectKeyFromObject(configMap), cm)).To(Succeed())
		}
		Expect(apiGets).To(Equal(3))
	})

	It("reaches the API server once for each object when the context carries a cache", func() {
		ctx := controllers.WithInitPassCache(context.TODO())

		for i := 0; i < 3; i++ {
			cm := &corev1.ConfigMap{}
			Expect(cachingClient.Get(ctx, client.ObjectKeyFromObject(configMap), cm)).To(Succeed())
			Expect(cm.Data).To(Equal(configMap.Data))
		}
		Expect(apiGets).To(Equal(1))
	})

	It("hands out copies: modifying a returned object does not change what is returned next", func() {
		ctx := controllers.WithInitPassCache(context.TODO())

		first := &corev1.ConfigMap{}
		Expect(cachingClient.Get(ctx, client.ObjectKeyFromObject(configMap), first)).To(Succeed())
		first.Data[initCacheTestKey] = "modified"

		second := &corev1.ConfigMap{}
		Expect(cachingClient.Get(ctx, client.ObjectKeyFromObject(configMap), second)).To(Succeed())
		Expect(second.Data[initCacheTestKey]).To(Equal(initCacheTestValue))
	})

	It("does not share entries across caches", func() {
		for i := 0; i < 2; i++ {
			ctx := controllers.WithInitPassCache(context.TODO())
			cm := &corev1.ConfigMap{}
			Expect(cachingClient.Get(ctx, client.ObjectKeyFromObject(configMap), cm)).To(Succeed())
		}
		Expect(apiGets).To(Equal(2))
	})

	It("does not cache errors", func() {
		ctx := controllers.WithInitPassCache(context.TODO())
		key := types.NamespacedName{Namespace: configMap.Namespace, Name: randomString()}

		for i := 0; i < 2; i++ {
			err := cachingClient.Get(ctx, key, &corev1.ConfigMap{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		Expect(apiGets).To(Equal(2))
	})

	It("tells apart unstructured objects of different kinds with the same namespace and name", func() {
		ctx := controllers.WithInitPassCache(context.TODO())
		key := client.ObjectKeyFromObject(configMap)

		asConfigMap := &unstructured.Unstructured{}
		asConfigMap.SetAPIVersion("v1")
		asConfigMap.SetKind("ConfigMap")
		Expect(cachingClient.Get(ctx, key, asConfigMap)).To(Succeed())

		// Same namespace/name, different kind: must not be served the ConfigMap
		asSecret := &unstructured.Unstructured{}
		asSecret.SetAPIVersion("v1")
		asSecret.SetKind("Secret")
		err := cachingClient.Get(ctx, key, asSecret)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(apiGets).To(Equal(2))
	})
})

var _ = Describe("Init pass cache and template resources", func() {
	var nsName string
	var cluster *clusterv1.Cluster
	var configMap *corev1.ConfigMap
	var clusterSummary *configv1beta1.ClusterSummary
	var identifier string

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		nsName = randomString()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: upstreamClusterNamePrefix + randomString(), Namespace: nsName},
		}
		Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, cluster)).To(Succeed())

		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: randomString()},
			Data:       map[string]string{"values": initCacheTestValues},
		}
		Expect(testEnv.Create(context.TODO(), configMap)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, configMap)).To(Succeed())

		identifier = randomString()
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{Name: randomString(), Namespace: nsName, ResourceVersion: "1"},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: nsName,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					TemplateResourceRefs: []configv1beta1.TemplateResourceRef{
						{
							Resource: corev1.ObjectReference{
								Kind:       testKindConfigMap,
								APIVersion: testV1APIVersion,
								Namespace:  nsName,
								Name:       configMap.Name,
							},
							Identifier: identifier,
						},
					},
					HelmCharts: []configv1beta1.HelmChart{
						{
							RepositoryURL:    testRepoURLNginxStable,
							RepositoryName:   testRepoNameNginxStable,
							ChartName:        testChartNameNginxIngress,
							ReleaseName:      testReleaseNameNginxLatest,
							ReleaseNamespace: testNginxRepo,
							ChartVersion:     testChartVersion100,
							HelmChartAction:  configv1beta1.HelmChartActionInstall,
							Values:           "configMapName: {{ (getResource \"" + identifier + "\").metadata.name }}",
							ValuesFrom: []configv1beta1.ValueFrom{
								{
									Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
									Namespace: nsName,
									Name:      configMap.Name,
								},
							},
						},
					},
				},
			},
		}
	})

	It("collectTemplateResourceRefs reads each resource once while the same cache is in use", func() {
		ctx := controllers.WithInitPassCache(context.TODO())

		result, err := controllers.CollectTemplateResourceRefs(ctx, clusterSummary)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(HaveKey(identifier))

		// Without a cache the resource is gone
		Expect(testEnv.Delete(context.TODO(), configMap)).To(Succeed())
		Eventually(func() bool {
			_, err := controllers.CollectTemplateResourceRefs(context.TODO(), clusterSummary)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// With the cache it is still served
		result, err = controllers.CollectTemplateResourceRefs(ctx, clusterSummary)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(HaveKey(identifier))
		Expect(result[identifier].GetName()).To(Equal(configMap.Name))
	})

	It("collectTemplateResourceRefs hands out copies: modifying a result does not change what is returned next", func() {
		ctx := controllers.WithInitPassCache(context.TODO())

		first, err := controllers.CollectTemplateResourceRefs(ctx, clusterSummary)
		Expect(err).ToNot(HaveOccurred())
		Expect(first[identifier].GetUID()).ToNot(BeEmpty())
		// this is what instantiating a template does to the resources it uses
		first[identifier].SetUID("")
		first[identifier].SetManagedFields(nil)
		first[identifier].SetResourceVersion("")

		second, err := controllers.CollectTemplateResourceRefs(ctx, clusterSummary)
		Expect(err).ToNot(HaveOccurred())
		Expect(second[identifier].GetUID()).ToNot(BeEmpty())
		Expect(second[identifier].GetResourceVersion()).ToNot(BeEmpty())
	})

	It("collectTemplateResourceRefs does not serve a ClusterSummary at a different resourceVersion from the cache", func() {
		ctx := controllers.WithInitPassCache(context.TODO())

		_, err := controllers.CollectTemplateResourceRefs(ctx, clusterSummary)
		Expect(err).ToNot(HaveOccurred())

		Expect(testEnv.Delete(context.TODO(), configMap)).To(Succeed())

		// The ClusterSummary changed in the meantime: what was collected for the previous version
		// does not apply to it
		updated := clusterSummary.DeepCopy()
		updated.ResourceVersion = "2"
		Eventually(func() bool {
			_, err := controllers.CollectTemplateResourceRefs(ctx, updated)
			return apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("helmHash computes the same hash with and without the cache", func() {
		logger := textlogger.NewLogger(textlogger.NewConfig())

		withoutCache, err := controllers.HelmHash(context.TODO(), testEnv.Client, clusterSummary, logger)
		Expect(err).ToNot(HaveOccurred())

		ctx := controllers.WithInitPassCache(context.TODO())
		cachingClient := controllers.NewInitCachingClient(testEnv.Client)
		withCache, err := controllers.HelmHash(ctx, cachingClient, clusterSummary, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(withCache).To(Equal(withoutCache))

		// and it is stable when computed again from the same cache
		again, err := controllers.HelmHash(ctx, cachingClient, clusterSummary, logger)
		Expect(err).ToNot(HaveOccurred())
		Expect(again).To(Equal(withoutCache))
	})
})
