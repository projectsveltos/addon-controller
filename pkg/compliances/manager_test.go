/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

package compliances_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/addon-controller/pkg/compliances"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
)

var _ = Describe("Constraints", func() {

	BeforeEach(func() {
		compliances.Reset()
	})

	AfterEach(func() {
		addonCompliances := &libsveltosv1alpha1.AddonComplianceList{}
		err := testEnv.List(ctx, addonCompliances)
		Expect(err).To(BeNil())
		for i := range addonCompliances.Items {
			ac := &addonCompliances.Items[i]
			if ac.DeletionTimestamp.IsZero() {
				Expect(testEnv.Delete(context.TODO(), ac)).To(Succeed())
			}
		}
	})

	It("getOpenapiPolicies returns all openapi/lua policies in an AddonCompliance", func() {
		cluster1 := corev1.ObjectReference{
			Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		}
		cluster2 := corev1.ObjectReference{
			Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		}
		cluster3 := corev1.ObjectReference{
			Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		}
		addonCompliance := &libsveltosv1alpha1.AddonCompliance{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1alpha1.AddonComplianceStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					cluster1, cluster2,
				},
				OpenapiValidations: map[string][]byte{
					randomString(): []byte(randomString()),
					randomString(): []byte(randomString()),
					randomString(): []byte(randomString()),
				},
				LuaValidations: map[string][]byte{
					randomString(): []byte(randomString()),
					randomString(): []byte(randomString()),
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), addonCompliance)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, addonCompliance)).To(Succeed())

		compliances.InitializeManagerWithSkip(context.TODO(), klogr.New(), testEnv.Config, testEnv.Client, 10)
		manager := compliances.GetManager()

		clusterType := libsveltosv1alpha1.ClusterTypeSveltos
		manager.MarkClusterReady(cluster1.Namespace, cluster1.Name, &clusterType)
		manager.MarkClusterReady(cluster2.Namespace, cluster2.Name, &clusterType)
		manager.MarkClusterReady(cluster3.Namespace, cluster3.Name, &clusterType)

		clusterTpe := clusterproxy.GetClusterType(&cluster1)
		policies, err := manager.GetClusterOpenapiPolicies(cluster1.Namespace, cluster1.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(len(addonCompliance.Status.OpenapiValidations)))
		policies, err = manager.GetClusterLuaPolicies(cluster1.Namespace, cluster1.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(len(addonCompliance.Status.LuaValidations)))

		clusterTpe = clusterproxy.GetClusterType(&cluster2)
		policies, err = manager.GetClusterOpenapiPolicies(cluster2.Namespace, cluster2.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(len(addonCompliance.Status.OpenapiValidations)))
		policies, err = manager.GetClusterLuaPolicies(cluster2.Namespace, cluster2.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(len(addonCompliance.Status.LuaValidations)))

		clusterTpe = clusterproxy.GetClusterType(&cluster3)
		policies, err = manager.GetClusterOpenapiPolicies(cluster3.Namespace, cluster3.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(0))
		policies, err = manager.GetClusterLuaPolicies(cluster3.Namespace, cluster3.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(policies)).To(Equal(0))
	})

	It("processAddonCompliance returns current policy map considering all AddonCompliances", func() {
		cluster1 := corev1.ObjectReference{
			Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		}
		cluster2 := corev1.ObjectReference{
			Namespace: randomString(), Name: randomString(),
			Kind: libsveltosv1alpha1.SveltosClusterKind, APIVersion: libsveltosv1alpha1.GroupVersion.String(),
		}
		addonCompliance1 := &libsveltosv1alpha1.AddonCompliance{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1alpha1.AddonComplianceStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					cluster1, cluster2,
				},
				OpenapiValidations: map[string][]byte{
					randomString(): []byte(randomString()),
					randomString(): []byte(randomString()),
					randomString(): []byte(randomString()),
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), addonCompliance1)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, addonCompliance1)).To(Succeed())

		addonCompliance2 := &libsveltosv1alpha1.AddonCompliance{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Status: libsveltosv1alpha1.AddonComplianceStatus{
				MatchingClusterRefs: []corev1.ObjectReference{
					cluster1,
				},
				OpenapiValidations: map[string][]byte{
					randomString(): []byte(randomString()),
					randomString(): []byte(randomString()),
					randomString(): []byte(randomString()),
				},
			},
		}

		Expect(testEnv.Create(context.TODO(), addonCompliance2)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, addonCompliance2)).To(Succeed())

		compliances.InitializeManagerWithSkip(context.TODO(), klogr.New(), testEnv.Config, testEnv.Client, 10)
		manager := compliances.GetManager()

		clusterType := libsveltosv1alpha1.ClusterTypeSveltos
		manager.MarkClusterReady(cluster1.Namespace, cluster1.Name, &clusterType)
		manager.MarkClusterReady(cluster2.Namespace, cluster2.Name, &clusterType)

		Expect(compliances.ReEvaluateAddonCompliances(manager, context.TODO())).To(Succeed())

		clusterTpe := clusterproxy.GetClusterType(&cluster1)
		result, err := manager.GetClusterOpenapiPolicies(cluster1.Namespace, cluster1.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(len(addonCompliance1.Status.OpenapiValidations) + len(addonCompliance2.Status.OpenapiValidations)))

		clusterTpe = clusterproxy.GetClusterType(&cluster2)
		result, err = manager.GetClusterOpenapiPolicies(cluster2.Namespace, cluster2.Name, &clusterTpe)
		Expect(err).To(BeNil())
		Expect(len(result)).To(Equal(len(addonCompliance1.Status.OpenapiValidations)))
	})

	It("reEvaluateClusters finds all annotated clusters and update internal clusters map", func() {
		cluster1 := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Annotations: map[string]string{
					libsveltosv1alpha1.GetClusterAnnotation(): "ok",
				},
			},
		}
		cluster2 := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		sveltosCluster1 := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Annotations: map[string]string{
					libsveltosv1alpha1.GetClusterAnnotation(): "ok",
				},
			},
		}

		sveltosCluster2 := &libsveltosv1alpha1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}

		initObjects := []client.Object{
			cluster1,
			cluster2,
			sveltosCluster1,
			sveltosCluster2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		compliances.InitializeManagerWithSkip(context.TODO(), klogr.New(), nil, c, 10)
		manager := compliances.GetManager()
		compliances.ReEvaluateClusters(manager, context.TODO())

		clusterType := libsveltosv1alpha1.ClusterTypeCapi
		Expect(compliances.CanAddonBeDeployed(manager, cluster1.Namespace, cluster1.Name, &clusterType)).To(BeTrue())
		Expect(compliances.CanAddonBeDeployed(manager, cluster2.Namespace, cluster2.Name, &clusterType)).To(BeFalse())

		clusterType = libsveltosv1alpha1.ClusterTypeSveltos
		Expect(compliances.CanAddonBeDeployed(manager, sveltosCluster1.Namespace, sveltosCluster1.Name, &clusterType)).To(BeTrue())
		Expect(compliances.CanAddonBeDeployed(manager, sveltosCluster2.Namespace, sveltosCluster2.Name, &clusterType)).To(BeFalse())
	})
})
