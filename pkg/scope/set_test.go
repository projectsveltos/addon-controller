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

package scope_test

import (
	"context"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const clusterSetNamePrefix = "scope-cp-"
const setNamePrefix = "scope-p-"

var _ = Describe("SetScope/ClusterSetScope", func() {
	var clusterSet *libsveltosv1beta1.ClusterSet
	var set *libsveltosv1beta1.Set
	var c client.Client

	BeforeEach(func() {
		clusterSet = &libsveltosv1beta1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSetNamePrefix + randomString(),
			},
		}
		set = &libsveltosv1beta1.Set{
			ObjectMeta: metav1.ObjectMeta{
				Name:      setNamePrefix + randomString(),
				Namespace: randomString(),
			},
		}
		scheme := setupScheme()
		addTypeInformationToObject(scheme, clusterSet)
		addTypeInformationToObject(scheme, set)
		initObjects := []client.Object{clusterSet, set}
		c = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()
	})

	It("Return nil,error if Set/ClusterSet is not specified", func() {
		cpParams := scope.SetScopeParams{
			Client: c,
			Logger: textlogger.NewLogger(textlogger.NewConfig()),
		}

		cpScope, err := scope.NewSetScope(cpParams)
		Expect(err).To(HaveOccurred())
		Expect(cpScope).To(BeNil())
	})

	It("Return nil,error if client is not specified", func() {
		cpParams := scope.SetScopeParams{
			Set:    clusterSet,
			Logger: textlogger.NewLogger(textlogger.NewConfig()),
		}

		cpScope, err := scope.NewSetScope(cpParams)
		Expect(err).To(HaveOccurred())
		Expect(cpScope).To(BeNil())
	})

	It("Return nil,error if any resource but Set/ClusterSet is passed", func() {
		cpParams := scope.SetScopeParams{
			Client: c,
			Logger: textlogger.NewLogger(textlogger.NewConfig()),
			Set:    &corev1.Node{},
		}

		cpScope, err := scope.NewSetScope(cpParams)
		Expect(err).To(HaveOccurred())
		Expect(cpScope).To(BeNil())
	})

	It("Name returns ClusterSet Name", func() {
		objects := []client.Object{clusterSet, set}
		for i := range objects {
			params := scope.SetScopeParams{
				Client: c,
				Set:    objects[i],
				Logger: textlogger.NewLogger(textlogger.NewConfig()),
			}

			scope, err := scope.NewSetScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(scope.Name()).To(Equal(objects[i].GetName()))
		}
	})

	It("GetSelector returns ClusterSet ClusterSelector", func() {
		clusterSet.Spec.ClusterSelector = libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"zone": "east",
				},
			},
		}
		set.Spec.ClusterSelector = clusterSet.Spec.ClusterSelector

		objects := []client.Object{clusterSet, set}
		for i := range objects {
			params := scope.SetScopeParams{
				Client: c,
				Set:    objects[i],
				Logger: textlogger.NewLogger(textlogger.NewConfig()),
			}

			scope, err := scope.NewSetScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			Expect(reflect.DeepEqual(*scope.GetSelector(), clusterSet.Spec.ClusterSelector.LabelSelector)).To(BeTrue())
		}
	})

	It("SetSelectedClusters sets ClusterSet.Status.SelectedClusterRef", func() {
		selectedClusters := []corev1.ObjectReference{
			{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}
		objects := []client.Object{clusterSet, set}
		for i := range objects {
			params := scope.SetScopeParams{
				Client: c,
				Set:    objects[i],
				Logger: textlogger.NewLogger(textlogger.NewConfig()),
			}

			scope, err := scope.NewSetScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			scope.SetSelectedClusterRefs(selectedClusters)
		}
		Expect(reflect.DeepEqual(clusterSet.Status.SelectedClusterRefs, selectedClusters)).To(BeTrue())
		Expect(reflect.DeepEqual(set.Status.SelectedClusterRefs, selectedClusters)).To(BeTrue())
	})

	It("SetMatchingClusters sets ClusterSet.Status.MatchingCluster", func() {
		matchingClusters := []corev1.ObjectReference{
			{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}
		objects := []client.Object{clusterSet, set}
		for i := range objects {
			params := scope.SetScopeParams{
				Client: c,
				Set:    objects[i],
				Logger: textlogger.NewLogger(textlogger.NewConfig()),
			}

			scope, err := scope.NewSetScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			scope.SetMatchingClusterRefs(matchingClusters)
		}
		Expect(reflect.DeepEqual(clusterSet.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
		Expect(reflect.DeepEqual(set.Status.MatchingClusterRefs, matchingClusters)).To(BeTrue())
	})

	It("Close updates ClusterSet", func() {
		key := randomString()
		value := randomString()

		objects := []client.Object{clusterSet, set}
		for i := range objects {
			params := scope.SetScopeParams{
				Client: c,
				Set:    objects[i],
				Logger: textlogger.NewLogger(textlogger.NewConfig()),
			}

			scope, err := scope.NewSetScope(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(scope).ToNot(BeNil())

			objects[i].SetLabels(map[string]string{key: value})
			Expect(scope.Close(context.TODO())).To(Succeed())
		}
		currentClusterSet := &libsveltosv1beta1.ClusterSet{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: clusterSet.Name}, currentClusterSet)).To(Succeed())
		Expect(currentClusterSet.Labels).ToNot(BeNil())
		Expect(len(currentClusterSet.Labels)).To(Equal(1))
		v, ok := currentClusterSet.Labels[key]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(value))

		currentSet := &libsveltosv1beta1.Set{}
		Expect(c.Get(context.TODO(),
			types.NamespacedName{Name: set.Name, Namespace: set.Namespace}, currentSet)).To(Succeed())
		Expect(currentSet.Labels).ToNot(BeNil())
		Expect(len(currentSet.Labels)).To(Equal(1))
		v, ok = currentSet.Labels[key]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(value))
	})
})
