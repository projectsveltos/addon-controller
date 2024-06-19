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

package v1alpha1_test

import (
	"fmt"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util"

	configv1alpha1 "github.com/projectsveltos/addon-controller/api/v1alpha1"
	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("Conversion", func() {
	Context("Convert from v1alpha1 to v1beta1 and back", func() {
		It("ClusterProfile conversion", func() {
			key := randomString()
			value := randomString()

			const tier = 100
			clusterProfile := configv1alpha1.ClusterProfile{
				ObjectMeta: metav1.ObjectMeta{
					Name: randomString(),
				},
				Spec: configv1alpha1.Spec{
					Tier:                 tier,
					StopMatchingBehavior: configv1alpha1.LeavePolicies,
					ValidateHealths: []configv1alpha1.ValidateHealth{
						{
							Name:      randomString(),
							FeatureID: configv1alpha1.FeatureHelm,
						},
					},
					ClusterSelector: libsveltosv1alpha1.Selector(fmt.Sprintf("%s=%s", key, value)),
					HelmCharts: []configv1alpha1.HelmChart{
						{
							RepositoryURL:    randomString(),
							RepositoryName:   randomString(),
							ChartName:        randomString(),
							ChartVersion:     randomString(),
							ReleaseName:      randomString(),
							ReleaseNamespace: randomString(),
							Values:           randomString(),
							Options: &configv1alpha1.HelmOptions{
								Labels: map[string]string{
									randomString(): randomString(),
								},
							},
						},
					},
					PolicyRefs: []configv1alpha1.PolicyRef{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
					KustomizationRefs: []configv1alpha1.KustomizationRef{
						{
							Namespace: randomString(),
							Name:      randomString(),
							Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
						},
					},
					ClusterRefs: []corev1.ObjectReference{
						{
							Kind:      libsveltosv1alpha1.SveltosClusterKind,
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
				},
			}

			dst := &configv1beta1.ClusterProfile{}
			Expect(clusterProfile.ConvertTo(dst)).To(Succeed())

			Expect(len(dst.Spec.ClusterSelector.LabelSelector.MatchLabels)).To(Equal(1))
			Expect(dst.Spec.ClusterSelector.LabelSelector.MatchLabels[key]).To(Equal(value))

			final := &configv1alpha1.ClusterProfile{}
			Expect(final.ConvertFrom(dst)).To(Succeed())

			Expect(reflect.DeepEqual(final.ObjectMeta, clusterProfile.ObjectMeta)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.PolicyRefs, clusterProfile.Spec.PolicyRefs)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.KustomizationRefs, clusterProfile.Spec.KustomizationRefs)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.ClusterRefs, clusterProfile.Spec.ClusterRefs)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Status, clusterProfile.Status)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.ValidateHealths, clusterProfile.Spec.ValidateHealths)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.StopMatchingBehavior, clusterProfile.Spec.StopMatchingBehavior)).To(BeTrue())
			Expect(final.Spec.Tier).To(Equal(clusterProfile.Spec.Tier))
		})

		It("Profile conversion", func() {
			key1 := randomString()
			value1 := randomString()
			key2 := randomString()
			value2 := randomString()

			profile := configv1alpha1.Profile{
				ObjectMeta: metav1.ObjectMeta{
					Name: randomString(),
				},
				Spec: configv1alpha1.Spec{
					ClusterSelector: libsveltosv1alpha1.Selector(fmt.Sprintf("%s=%s, %s=%s", key1, value1, key2, value2)),
					HelmCharts: []configv1alpha1.HelmChart{
						{
							RepositoryURL:    randomString(),
							RepositoryName:   randomString(),
							ChartName:        randomString(),
							ChartVersion:     randomString(),
							ReleaseName:      randomString(),
							ReleaseNamespace: randomString(),
							Values:           randomString(),
							Options: &configv1alpha1.HelmOptions{
								Labels: map[string]string{
									randomString(): randomString(),
								},
							},
						},
						{
							RepositoryURL:    randomString(),
							RepositoryName:   randomString(),
							ChartName:        randomString(),
							ChartVersion:     randomString(),
							ReleaseName:      randomString(),
							ReleaseNamespace: randomString(),
							Values:           randomString(),
							Options: &configv1alpha1.HelmOptions{
								Labels: map[string]string{
									randomString(): randomString(),
								},
							},
						},
					},
					PolicyRefs: []configv1alpha1.PolicyRef{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
					KustomizationRefs: []configv1alpha1.KustomizationRef{
						{
							Namespace: randomString(),
							Name:      randomString(),
							Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
						},
						{
							Namespace: randomString(),
							Name:      randomString(),
							Kind:      string(libsveltosv1alpha1.SecretReferencedResourceKind),
						},
					},
					ClusterRefs: []corev1.ObjectReference{
						{
							Kind:      libsveltosv1alpha1.SveltosClusterKind,
							Namespace: randomString(),
							Name:      randomString(),
						},
						{
							Kind:      libsveltosv1alpha1.SveltosClusterKind,
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
				},
			}

			dst := &configv1beta1.Profile{}
			Expect(profile.ConvertTo(dst)).To(Succeed())

			Expect(len(dst.Spec.ClusterSelector.LabelSelector.MatchLabels)).To(Equal(2))
			Expect(dst.Spec.ClusterSelector.LabelSelector.MatchLabels[key1]).To(Equal(value1))
			Expect(dst.Spec.ClusterSelector.LabelSelector.MatchLabels[key2]).To(Equal(value2))

			final := &configv1alpha1.Profile{}
			Expect(final.ConvertFrom(dst)).To(Succeed())

			Expect(reflect.DeepEqual(final.ObjectMeta, profile.ObjectMeta)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.PolicyRefs, profile.Spec.PolicyRefs)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.KustomizationRefs, profile.Spec.KustomizationRefs)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Spec.ClusterRefs, profile.Spec.ClusterRefs)).To(BeTrue())
			Expect(reflect.DeepEqual(final.Status, profile.Status)).To(BeTrue())
		})
	})
})

func randomString() string {
	const length = 10
	return util.RandomString(length)
}
