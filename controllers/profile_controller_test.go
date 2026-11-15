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

package controllers_test

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sync"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

const namespaceFieldName = "Namespace"

var indexRegexp = regexp.MustCompile(`\[\d+\]`)

// withoutIndexes turns Spec.ValidateHealths[0].Namespace into Spec.ValidateHealths.Namespace
func withoutIndexes(path string) string {
	return indexRegexp.ReplaceAllString(path, "")
}

// namespaceFieldsInManagedCluster returns the Namespace fields (paths without indexes) that
// legitimately are not limited to the Profile namespace: they identify resources living in the
// managed cluster, not in the management cluster, so a Profile tenant cannot use them to reach
// resources of other tenants in the management cluster.
func namespaceFieldsInManagedCluster() map[string]bool {
	exceptions := map[string]bool{
		// Kustomize style selectors matching resources in the managed cluster
		"Spec.Patches.Target.Namespace":         true,
		"Spec.DriftExclusions.Target.Namespace": true,
	}

	checkLists := []string{validateHealthsField, preDeployChecksField, preDeleteChecksField, postDeleteChecksField}
	for _, list := range checkLists {
		// Namespace of the resources evaluated in the managed cluster
		exceptions["Spec."+list+".Namespace"] = true
		// Secret read from the managed cluster
		exceptions["Spec."+list+".MetricSource.SecretRef.Namespace"] = true
	}

	return exceptions
}

// populateNamespaceFields walks value allocating nil pointers and empty slices (one element)
// and sets every string field named Namespace to namespace. visited holds the struct types
// on the current path, so recursive types do not loop forever.
func populateNamespaceFields(value reflect.Value, namespace string, visited map[reflect.Type]bool) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		populateNamespaceFields(value.Elem(), namespace, visited)
		return
	}

	if value.Kind() == reflect.Slice {
		if value.Len() == 0 {
			value.Set(reflect.MakeSlice(value.Type(), 1, 1))
		}
		for i := 0; i < value.Len(); i++ {
			populateNamespaceFields(value.Index(i), namespace, visited)
		}
		return
	}

	if value.Kind() != reflect.Struct || visited[value.Type()] {
		return
	}

	visited[value.Type()] = true
	defer delete(visited, value.Type())

	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if isNamespaceField(&field) {
			value.Field(i).SetString(namespace)
			continue
		}
		populateNamespaceFields(value.Field(i), namespace, visited)
	}
}

// isNamespaceField returns true for a string field named Namespace
func isNamespaceField(field *reflect.StructField) bool {
	return field.Name == namespaceFieldName && field.Type.Kind() == reflect.String
}

// findNamespaceFields returns the value of every string field named Namespace reachable
// from value, keyed by the path of the field (e.g. Spec.PolicyRefs[0].RemoteURL.SecretRef.Namespace).
func findNamespaceFields(value reflect.Value, path string) map[string]string {
	result := map[string]string{}

	if value.Kind() == reflect.Pointer {
		if !value.IsNil() {
			result = findNamespaceFields(value.Elem(), path)
		}
		return result
	}

	if value.Kind() == reflect.Slice {
		for i := 0; i < value.Len(); i++ {
			mergeMaps(result, findNamespaceFields(value.Index(i), fmt.Sprintf("%s[%d]", path, i)))
		}
		return result
	}

	if value.Kind() != reflect.Struct {
		return result
	}

	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		fieldPath := path + "." + field.Name
		if isNamespaceField(&field) {
			result[fieldPath] = value.Field(i).String()
			continue
		}
		mergeMaps(result, findNamespaceFields(value.Field(i), fieldPath))
	}

	return result
}

func mergeMaps(destination, source map[string]string) {
	for k, v := range source {
		destination[k] = v
	}
}

var _ = Describe("Profile Controller", func() {
	var profile *configv1beta1.Profile
	var logger logr.Logger

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())

		namespace := randomString()
		profile = &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      clusterProfileNamePrefix + randomString(),
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
		Expect(addTypeInformationToObject(scheme, profile)).To(Succeed())
	})

	It("limitReferencesToNamespace resets any reference to be within namespace", func() {
		profile.Spec = configv1beta1.Spec{
			ClusterRefs: []corev1.ObjectReference{
				{
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Namespace: profile.Namespace,
					Name:      randomString(),
				},
			},
			PolicyRefs: []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
			},
			KustomizationRefs: []configv1beta1.KustomizationRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Kind:      sourcev1.GitRepositoryKind,
					Namespace: randomString(),
					Name:      randomString(),
					ValuesFrom: []configv1beta1.ValueFrom{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
				},
			},
			HelmCharts: []configv1beta1.HelmChart{
				{
					RepositoryURL: randomString(),
					ChartName:     randomString(),
					ValuesFrom: []configv1beta1.ValueFrom{
						{
							Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
						{
							Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
					SignatureVerification: &configv1beta1.CosignVerification{
						Provider: configv1beta1.CosignProviderPublicKey,
						SecretRef: &corev1.SecretReference{
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
				},
				{
					RepositoryURL: randomString(),
					ChartName:     randomString(),
					ProvenanceVerification: &configv1beta1.ProvenanceVerification{
						KeyringSecretRef: corev1.SecretReference{
							Namespace: randomString(),
							Name:      randomString(),
						},
					},
				},
			},
			PatchesFrom: []configv1beta1.ValueFrom{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
				{
					Kind:      string(libsveltosv1beta1.SecretReferencedResourceKind),
					Namespace: randomString(),
					Name:      randomString(),
				},
			},
			TemplateResourceRefs: []configv1beta1.TemplateResourceRef{
				{
					Resource: corev1.ObjectReference{
						Kind:       testKindSecret,
						APIVersion: testV1APIVersion,
						Namespace:  randomString(),
						Name:       randomString(),
					},
					Identifier: randomString(),
				},
				{
					Resource: corev1.ObjectReference{
						Kind:       testKindConfigMap,
						APIVersion: testV1APIVersion,
						Name:       randomString(),
					},
					Identifier: randomString(),
				},
			},
		}

		initObjects := []client.Object{
			profile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ProfileReconciler{
			Client:        c,
			Scheme:        scheme,
			ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		controllers.LimitReferencesToNamespace(reconciler, profile)

		for i := range profile.Spec.ClusterRefs {
			Expect(profile.Spec.ClusterRefs[i].Namespace).To(Equal(profile.Namespace))
		}

		for i := range profile.Spec.PolicyRefs {
			Expect(profile.Spec.PolicyRefs[i].Namespace).To(Equal(profile.Namespace))
		}

		for i := range profile.Spec.KustomizationRefs {
			Expect(profile.Spec.KustomizationRefs[i].Namespace).To(Equal(profile.Namespace))
			for j := range profile.Spec.KustomizationRefs[i].ValuesFrom {
				Expect(profile.Spec.KustomizationRefs[i].ValuesFrom[j].Namespace).To(Equal(profile.Namespace))
			}
		}

		for i := range profile.Spec.HelmCharts {
			for j := range profile.Spec.HelmCharts[i].ValuesFrom {
				Expect(profile.Spec.HelmCharts[i].ValuesFrom[j].Namespace).To(Equal(profile.Namespace))
			}
			if sv := profile.Spec.HelmCharts[i].SignatureVerification; sv != nil {
				Expect(sv.SecretRef.Namespace).To(Equal(profile.Namespace))
			}
			if pv := profile.Spec.HelmCharts[i].ProvenanceVerification; pv != nil {
				Expect(pv.KeyringSecretRef.Namespace).To(Equal(profile.Namespace))
			}
		}

		for i := range profile.Spec.PatchesFrom {
			Expect(profile.Spec.PatchesFrom[i].Namespace).To(Equal(profile.Namespace))
		}

		for i := range profile.Spec.TemplateResourceRefs {
			Expect(profile.Spec.TemplateResourceRefs[i].Resource.Namespace).To(Equal(profile.Namespace))
		}
	})

	It("limitReferencesToNamespace resets every Namespace field reachable from Profile.Spec", func() {
		// The Spec is populated through reflection: every pointer and slice is allocated and every
		// field named Namespace is set to a foreign namespace. A reference field added to the Spec
		// in the future is therefore covered by this test without having to update it. If a field
		// named Namespace is legitimately not a reference to a management cluster resource, it needs
		// an explicit exception in namespaceFieldsInManagedCluster.
		foreignNamespace := randomString()
		populateNamespaceFields(reflect.ValueOf(&profile.Spec).Elem(), foreignNamespace, map[reflect.Type]bool{})

		before := findNamespaceFields(reflect.ValueOf(profile.Spec), "Spec")
		Expect(before).ToNot(BeEmpty())
		for path, namespace := range before {
			Expect(namespace).To(Equal(foreignNamespace), "test setup did not populate "+path)
		}

		reconciler := &controllers.ProfileReconciler{}
		controllers.LimitReferencesToNamespace(reconciler, profile)

		after := findNamespaceFields(reflect.ValueOf(profile.Spec), "Spec")
		Expect(after).To(HaveLen(len(before)))
		exceptions := namespaceFieldsInManagedCluster()
		notLimited := []string{}
		for path, namespace := range after {
			if namespace == profile.Namespace || exceptions[withoutIndexes(path)] {
				continue
			}
			notLimited = append(notLimited, path)
		}
		Expect(notLimited).To(BeEmpty(), "Namespace fields not limited to the Profile namespace")
	})

	It("getClustersFromClusterSets gets cluster selected by referenced sets", func() {
		set1 := &libsveltosv1beta1.Set{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: profile.Namespace,
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: clusterKind, APIVersion: clusterv1.GroupVersion.String(),
					},
				},
			},
		}

		set2 := &libsveltosv1beta1.Set{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: profile.Namespace,
			},
			Status: libsveltosv1beta1.Status{
				SelectedClusterRefs: []corev1.ObjectReference{
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
					{
						Namespace: profile.Namespace, Name: randomString(),
						Kind: libsveltosv1beta1.SveltosClusterKind, APIVersion: libsveltosv1beta1.GroupVersion.String(),
					},
				},
			},
		}

		profile.Spec.SetRefs = []string{
			set1.Name,
			set2.Name,
		}

		initObjects := []client.Object{
			set1,
			set2,
			profile,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.ProfileReconciler{
			Client:        c,
			Scheme:        scheme,
			SetMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
			ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		clusters, err := controllers.GetClustersFromSets(reconciler, context.TODO(),
			profile.Namespace, profile.Spec.SetRefs, logger)
		Expect(err).To(BeNil())
		Expect(clusters).ToNot(BeNil())
		for i := range set1.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  set1.Status.SelectedClusterRefs[i].Namespace,
				Name:       set1.Status.SelectedClusterRefs[i].Name,
				Kind:       set1.Status.SelectedClusterRefs[i].Kind,
				APIVersion: set1.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
		for i := range set2.Status.SelectedClusterRefs {
			Expect(clusters).To(ContainElement(corev1.ObjectReference{
				Namespace:  set2.Status.SelectedClusterRefs[i].Namespace,
				Name:       set2.Status.SelectedClusterRefs[i].Name,
				Kind:       set2.Status.SelectedClusterRefs[i].Kind,
				APIVersion: set2.Status.SelectedClusterRefs[i].APIVersion,
			}))
		}
	})

	It("reconcileDelete of a Profile waits for clusters to be unpaused when ClusterSummaries still present are for paused clusters",
		func() {
			paused := true
			cluster := &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Namespace: profile.Namespace, Name: randomString()},
				Spec:       clusterv1.ClusterSpec{Paused: &paused},
			}
			Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

			clusterSummary := getClusterSummaryWithFinalizer(configv1beta1.ProfileKind, profile, cluster)

			initObjects := []client.Object{profile, cluster, clusterSummary}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
				WithObjects(initObjects...).Build()

			reconciler := &controllers.ProfileReconciler{
				Client:        c,
				Scheme:        scheme,
				ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
				Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
				ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
				Mux:           sync.Mutex{},
			}

			profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
				Client:         c,
				Logger:         logger,
				Profile:        profile,
				ControllerName: testControllerNameProfile,
			})
			Expect(err).To(BeNil())

			result := controllers.ProfileReconcileDelete(reconciler, context.TODO(), profileScope)
			Expect(result.RequeueAfter).To(Equal(controllers.PausedClustersRequeueAfter))

			// ClusterProfile is reconciled again when the cluster changes (unpaused)
			clusterRef := corev1.ObjectReference{
				Namespace:  cluster.Namespace,
				Name:       cluster.Name,
				Kind:       clusterv1.ClusterKind,
				APIVersion: clusterv1.GroupVersion.String(),
			}
			Expect(reconciler.ClusterMap).To(HaveKey(clusterRef))
			consumers := reconciler.ClusterMap[clusterRef].Items()
			Expect(consumers).To(HaveLen(1))
			Expect(consumers[0].Name).To(Equal(profile.Name))
			Expect(consumers[0].Kind).To(Equal(configv1beta1.ProfileKind))
		})

	It("reconcileDelete of a Profile keeps reconciling when ClusterSummaries still present are for clusters not paused", func() {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: profile.Namespace, Name: randomString()},
		}
		Expect(addTypeInformationToObject(scheme, cluster)).To(Succeed())

		clusterSummary := getClusterSummaryWithFinalizer(configv1beta1.ProfileKind, profile, cluster)

		initObjects := []client.Object{profile, cluster, clusterSummary}
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.ProfileReconciler{
			Client:        c,
			Scheme:        scheme,
			ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        profile,
			ControllerName: testControllerNameProfile,
		})
		Expect(err).To(BeNil())

		result := controllers.ProfileReconcileDelete(reconciler, context.TODO(), profileScope)
		Expect(result.RequeueAfter).To(Equal(controllers.DeleteRequeueAfter))
		Expect(reconciler.ClusterMap).To(BeEmpty())
	})

	It("reconcileDelete of a Profile removes its matching clusters metric once the finalizer is removed", func() {
		controllerutil.AddFinalizer(profile, configv1beta1.ProfileFinalizer)

		initObjects := []client.Object{profile}
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()

		reconciler := &controllers.ProfileReconciler{
			Client:        c,
			Scheme:        scheme,
			ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
			Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
			ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
			Mux:           sync.Mutex{},
		}

		profileScope, err := scope.NewProfileScope(scope.ProfileScopeParams{
			Client:         c,
			Logger:         logger,
			Profile:        profile,
			ControllerName: testControllerNameProfile,
		})
		Expect(err).To(BeNil())

		// A ClusterProfile, and a Profile in another namespace, with the same name: their metric must stay.
		otherNamespace := randomString()
		controllers.TrackMatchingClusters(configv1beta1.ProfileKind, profile.Namespace, profile.Name, 2, logger)
		controllers.TrackMatchingClusters(configv1beta1.ClusterProfileKind, "", profile.Name, 1, logger)
		controllers.TrackMatchingClusters(configv1beta1.ProfileKind, otherNamespace, profile.Name, 1, logger)

		profileLabels := profileMetricLabels(configv1beta1.ProfileKind, profile.Namespace, profile.Name)
		clusterProfileLabels := profileMetricLabels(configv1beta1.ClusterProfileKind, "", profile.Name)
		otherProfileLabels := profileMetricLabels(configv1beta1.ProfileKind, otherNamespace, profile.Name)
		Expect(hasMetricSeries("projectsveltos_matching_clusters", profileLabels)).To(BeTrue())

		result := controllers.ProfileReconcileDelete(reconciler, context.TODO(), profileScope)
		Expect(result.RequeueAfter).To(BeZero())
		Expect(controllerutil.ContainsFinalizer(profile, configv1beta1.ProfileFinalizer)).To(BeFalse())

		Expect(hasMetricSeries("projectsveltos_matching_clusters", profileLabels)).To(BeFalse())
		Expect(hasMetricSeries("projectsveltos_matching_clusters", clusterProfileLabels)).To(BeTrue())
		Expect(hasMetricSeries("projectsveltos_matching_clusters", otherProfileLabels)).To(BeTrue())
	})
})
