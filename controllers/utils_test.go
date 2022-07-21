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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

const (
	timeout         = 20 * time.Second
	pollingInterval = 2 * time.Second
)

const (
	upstreamClusterNamePrefix = "upstream-cluster"
	upstreamMachineNamePrefix = "upstream-machine"
	clusterFeatureNamePrefix  = "cluster-feature"
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

	return s, nil
}

var _ = Describe("getClusterFeatureOwner ", func() {
	It("getClusterFeatureOwner returns ClusterFeature owner", func() {
		clusterFeature := &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterFeature)).To(Succeed())

		namespace := "reconcile" + util.RandomString(5)
		clusterName := util.RandomString(10)
		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, namespace, clusterName)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       clusterFeature.Kind,
						Name:       clusterFeature.Name,
						APIVersion: clusterFeature.APIVersion,
					},
				},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace:   namespace,
				ClusterName:        clusterName,
				ClusterFeatureSpec: clusterFeature.Spec,
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		owner, err := controllers.GetClusterFeatureOwner(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).ToNot(BeNil())
		Expect(owner.Name).To(Equal(clusterFeature.Name))
	})

	It("getClusterFeatureOwner returns nil when ClusterFeature does not exist", func() {
		clusterFeature := &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterFeature)).To(Succeed())

		namespace := "reconcile" + util.RandomString(5)
		clusterName := util.RandomString(10)
		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, namespace, clusterName)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind:       clusterFeature.Kind,
						Name:       clusterFeature.Name,
						APIVersion: clusterFeature.APIVersion,
					},
				},
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace:   namespace,
				ClusterName:        clusterName,
				ClusterFeatureSpec: clusterFeature.Spec,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		owner, err := controllers.GetClusterFeatureOwner(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).To(BeNil())
	})
})
