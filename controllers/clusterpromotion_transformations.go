/*
Copyright 2025. projectsveltos.io. All rights reserved.

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

package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// requeueClusterPromotionForReference is a handler.ToRequestsFunc to be used to enqueue
// ClusterPromotion instances referencing the ConfigMap/Secret that changed. A ClusterPromotion
// snapshots referenced resources per stage, so it needs to know when the original changes.
func (r *ClusterPromotionReconciler) requeueClusterPromotionForReference(
	ctx context.Context, o client.Object,
) []reconcile.Request {

	logger := ctrl.LoggerFrom(ctx).WithValues("reference", o.GetName())

	var kind string
	switch o.(type) {
	case *corev1.ConfigMap:
		kind = string(libsveltosv1beta1.ConfigMapReferencedResourceKind)
	case *corev1.Secret:
		kind = string(libsveltosv1beta1.SecretReferencedResourceKind)
	default:
		return nil
	}

	key := corev1.ObjectReference{
		APIVersion: corev1.SchemeGroupVersion.String(),
		Kind:       kind,
		Namespace:  o.GetNamespace(),
		Name:       o.GetName(),
	}

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	consumers := r.getReferenceMapForEntry(&key).Items()
	requests := make([]reconcile.Request, len(consumers))
	for i := range consumers {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("requeue consumer: %s", consumers[i]))
		requests[i] = reconcile.Request{
			NamespacedName: client.ObjectKey{Name: consumers[i].Name},
		}
	}

	return requests
}

// updateMaps records the ConfigMaps/Secrets referenced by the ClusterPromotion, replacing
// what was recorded before.
func (r *ClusterPromotionReconciler) updateMaps(clusterPromotion *configv1beta1.ClusterPromotion) {
	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	r.eraseFromReferenceMap(clusterPromotion)

	clusterPromotionInfo := getClusterPromotionReference(clusterPromotion)
	references := getClusterPromotionReferences(&clusterPromotion.Spec.ProfileSpec)
	for i := range references {
		r.getReferenceMapForEntry(&references[i]).Insert(clusterPromotionInfo)
	}
}

// cleanMaps removes the ClusterPromotion from the recorded references
func (r *ClusterPromotionReconciler) cleanMaps(clusterPromotion *configv1beta1.ClusterPromotion) {
	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	r.eraseFromReferenceMap(clusterPromotion)
}

// eraseFromReferenceMap must be called with PolicyMux held
func (r *ClusterPromotionReconciler) eraseFromReferenceMap(clusterPromotion *configv1beta1.ClusterPromotion) {
	clusterPromotionInfo := getClusterPromotionReference(clusterPromotion)
	for k, l := range r.ReferenceMap {
		l.Erase(clusterPromotionInfo)
		if l.Len() == 0 {
			delete(r.ReferenceMap, k)
		}
	}
}

// getReferenceMapForEntry must be called with PolicyMux held
func (r *ClusterPromotionReconciler) getReferenceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	if r.ReferenceMap == nil {
		r.ReferenceMap = make(map[corev1.ObjectReference]*libsveltosset.Set)
	}

	s := r.ReferenceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ReferenceMap[*entry] = s
	}
	return s
}

func getClusterPromotionReference(clusterPromotion *configv1beta1.ClusterPromotion) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		APIVersion: configv1beta1.GroupVersion.String(),
		Kind:       configv1beta1.ClusterPromotionKind,
		Name:       clusterPromotion.Name,
	}
}

// getClusterPromotionReferences returns the ConfigMaps/Secrets referenced by the ProfileSpec
// (policyRefs, kustomizationRefs, valuesFrom, patchesFrom)
func getClusterPromotionReferences(spec *configv1beta1.ProfileSpec) []corev1.ObjectReference {
	references := make([]corev1.ObjectReference, 0)

	add := func(kind, namespace, name string) {
		if kind != string(libsveltosv1beta1.ConfigMapReferencedResourceKind) &&
			kind != string(libsveltosv1beta1.SecretReferencedResourceKind) {

			return
		}
		references = append(references, corev1.ObjectReference{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       kind,
			Namespace:  namespace,
			Name:       name,
		})
	}

	addValuesFrom := func(valuesFrom []configv1beta1.ValueFrom) {
		for i := range valuesFrom {
			add(valuesFrom[i].Kind, valuesFrom[i].Namespace, valuesFrom[i].Name)
		}
	}

	for i := range spec.PolicyRefs {
		add(spec.PolicyRefs[i].Kind, spec.PolicyRefs[i].Namespace, spec.PolicyRefs[i].Name)
	}

	for i := range spec.KustomizationRefs {
		add(spec.KustomizationRefs[i].Kind, spec.KustomizationRefs[i].Namespace, spec.KustomizationRefs[i].Name)
		addValuesFrom(spec.KustomizationRefs[i].ValuesFrom)
	}

	for i := range spec.HelmCharts {
		addValuesFrom(spec.HelmCharts[i].ValuesFrom)
	}

	addValuesFrom(spec.PatchesFrom)

	return references
}
