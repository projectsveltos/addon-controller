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

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var (
	getManagerLock  = &sync.Mutex{}
	managerInstance *manager
)

type manager struct {
	log logr.Logger
	client.Client
	config *rest.Config

	watchMu *sync.RWMutex

	// List of gvk with a watcher
	// Key: GroupResourceVersion currently being watched
	// Value: stop channel
	watchers map[schema.GroupVersionKind]context.CancelFunc

	// List of ClusterSummary requesting a watcher for a given GVK
	// Those are resources deployed in the management cluster. ClusterSummaries
	// need to watch those resources and reconcile when those resources are modified
	// if mode is ContinuousWithDriftDetection.
	requestorForMgmtResourcesKustomizeRef map[schema.GroupVersionKind]*libsveltosset.Set
	requestorForMgmtResourcesPolicyRef    map[schema.GroupVersionKind]*libsveltosset.Set

	// key: resource being watched, value: list of ClusterSummary interested in this resource
	mgmtResourcesWatchedKustomizeRef map[corev1.ObjectReference]*libsveltosset.Set
	mgmtResourcesWatchedPolicyRef    map[corev1.ObjectReference]*libsveltosset.Set

	// List of ClusterSummary requesting a watcher for a given GVK
	// TemplateResourceRefs is a list of resource to collect from the management cluster.
	// Those resources' values will be used to instantiate templates contained in referenced
	requestorForTemplateResourceRefs map[schema.GroupVersionKind]*libsveltosset.Set

	// key: resource being watched, value: list of ClusterSummary interested in ALL changes (including status)
	templateResourceRefsWatched map[corev1.ObjectReference]*libsveltosset.Set

	// key: resource being watched, value: list of ClusterSummary interested ONLY in spec/metadata changes
	templateResourceRefsWatchedIgnoreStatus map[corev1.ObjectReference]*libsveltosset.Set
}

// initializeManager initializes a manager implementing the Watchers
func initializeManager(l logr.Logger, config *rest.Config, c client.Client) {
	if managerInstance == nil {
		getManagerLock.Lock()
		defer getManagerLock.Unlock()
		if managerInstance == nil {
			l.V(logs.LogInfo).Info("Creating manager now")
			managerInstance = &manager{log: l, Client: c, config: config}
			managerInstance.watchMu = &sync.RWMutex{}
			managerInstance.watchers = make(map[schema.GroupVersionKind]context.CancelFunc)
			managerInstance.requestorForMgmtResourcesKustomizeRef = make(map[schema.GroupVersionKind]*libsveltosset.Set)
			managerInstance.requestorForMgmtResourcesPolicyRef = make(map[schema.GroupVersionKind]*libsveltosset.Set)
			managerInstance.requestorForTemplateResourceRefs = make(map[schema.GroupVersionKind]*libsveltosset.Set)
			managerInstance.mgmtResourcesWatchedKustomizeRef = make(map[corev1.ObjectReference]*libsveltosset.Set)
			managerInstance.mgmtResourcesWatchedPolicyRef = make(map[corev1.ObjectReference]*libsveltosset.Set)
			managerInstance.templateResourceRefsWatched = make(map[corev1.ObjectReference]*libsveltosset.Set)
			managerInstance.templateResourceRefsWatchedIgnoreStatus = make(map[corev1.ObjectReference]*libsveltosset.Set)
		}
	}
}

// getManager returns the manager instance
func getManager() *manager {
	return managerInstance
}

// startWatcherForMgmtResource starts a watcher if one does not exist already.
// ClusterSummary is listed as one of the consumer for this watcher.
// ClusterSummary will only receive updates when the specific resource itself changes, not when other resources of the same type change.
func (m *manager) startWatcherForMgmtResource(ctx context.Context, gvk schema.GroupVersionKind,
	resource *corev1.ObjectReference, clusterSummary *configv1beta1.ClusterSummary, fID libsveltosv1beta1.FeatureID) error {

	consumer := &corev1.ObjectReference{
		APIVersion: configv1beta1.GroupVersion.Group,
		Kind:       configv1beta1.ClusterSummaryKind,
		Namespace:  clusterSummary.Namespace,
		Name:       clusterSummary.Name,
	}

	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	m.log.V(logs.LogDebug).Info(fmt.Sprintf("request to watch %s %s/%s from %s/%s",
		gvk.String(),
		resource.Namespace, resource.Name,
		clusterSummary.Namespace, clusterSummary.Name))

	err := m.startWatcher(ctx, &gvk)
	if err != nil {
		return err
	}

	gvkSet := m.getGVKMapEntryForFeatureID(gvk, fID)
	gvkSet.Insert(consumer)

	resourceSet := m.getResourceMapEntryForFeatureID(resource, fID)
	resourceSet.Insert(consumer)
	return nil
}

// startWatcherForTemplateResourceRef starts a watcher if one does not exist already
// ClusterSummary is listed as one of the consumer for this watcher.
// ClusterSummary will only receive updates when the specific resource itself changes,
// not when other resources of the same type change.
func (m *manager) startWatcherForTemplateResourceRef(ctx context.Context, gvk schema.GroupVersionKind,
	ref *configv1beta1.TemplateResourceRef, clusterSummary *configv1beta1.ClusterSummary) error {

	consumer := &corev1.ObjectReference{
		APIVersion: configv1beta1.GroupVersion.Group,
		Kind:       configv1beta1.ClusterSummaryKind,
		Namespace:  clusterSummary.Namespace,
		Name:       clusterSummary.Name,
	}

	resource := ref.Resource

	var err error
	// If namespace is not defined, default to cluster namespace
	resource.Namespace, err = getTemplateResourceNamespace(ctx, clusterSummary, ref)
	if err != nil {
		return err
	}

	resource.Name, err = getTemplateResourceName(ctx, clusterSummary, ref)
	if err != nil {
		return err
	}

	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	m.log.V(logs.LogDebug).Info(fmt.Sprintf("request to watch %s %s/%s from %s/%s",
		gvk.String(),
		resource.Namespace, resource.Name,
		clusterSummary.Namespace, clusterSummary.Name))

	err = m.startWatcher(ctx, &gvk)
	if err != nil {
		return err
	}
	if _, ok := m.requestorForTemplateResourceRefs[gvk]; !ok {
		s := &libsveltosset.Set{}
		m.requestorForTemplateResourceRefs[gvk] = s
	}

	m.requestorForTemplateResourceRefs[gvk].Insert(consumer)

	if ref.IgnoreStatusChanges {
		// Ensure it's NOT in the "All Changes" bucket
		if m.templateResourceRefsWatched[resource] != nil {
			m.templateResourceRefsWatched[resource].Erase(consumer)
		}

		// Add to "Ignore Status" bucket
		if _, ok := m.templateResourceRefsWatchedIgnoreStatus[resource]; !ok {
			m.templateResourceRefsWatchedIgnoreStatus[resource] = &libsveltosset.Set{}
		}
		m.templateResourceRefsWatchedIgnoreStatus[resource].Insert(consumer)
	} else {
		// Ensure it's NOT in the "Ignore Status" bucket
		if m.templateResourceRefsWatchedIgnoreStatus[resource] != nil {
			m.templateResourceRefsWatchedIgnoreStatus[resource].Erase(consumer)
		}

		// Add to "All Changes" bucket
		if _, ok := m.templateResourceRefsWatched[resource]; !ok {
			m.templateResourceRefsWatched[resource] = &libsveltosset.Set{}
		}
		m.templateResourceRefsWatched[resource].Insert(consumer)
	}

	return nil
}

// This function identifies resources that the ClusterSummary object is no longer interested in.
// It then stops any watchers that were previously set up to deliver notifications about those specific
// resources to ClusterSummary.
// Resources that are still included in the currentResources map will continue to be watched.
func (m *manager) stopStaleWatchForMgmtResource(currentResources map[corev1.ObjectReference]bool,
	clusterSummary *configv1beta1.ClusterSummary, fId libsveltosv1beta1.FeatureID) {

	consumer := &corev1.ObjectReference{
		APIVersion: configv1beta1.GroupVersion.Group,
		Kind:       configv1beta1.ClusterSummaryKind,
		Namespace:  clusterSummary.Namespace,
		Name:       clusterSummary.Name,
	}

	currentGVKs := make(map[schema.GroupVersionKind]bool)
	for resource := range currentResources {
		gvk := schema.GroupVersionKind{
			Kind:    resource.Kind,
			Group:   resource.GroupVersionKind().Group,
			Version: resource.GroupVersionKind().Version,
		}
		currentGVKs[gvk] = true
	}

	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	gvkMap := m.getGVKMapForFeatureID(fId)

	for gvk := range gvkMap {
		if currentGVKs[gvk] {
			// ClusterSummary still wants a watcher for this GVK
			continue
		}

		s := m.getGVKMapEntryForFeatureID(gvk, fId)
		s.Erase(consumer)
		m.setGVKMapEntryForFeatureID(gvk, fId, s)

		if m.canStopWatcher(gvk) {
			if cancel, ok := m.watchers[gvk]; ok {
				m.log.V(logs.LogInfo).Info(fmt.Sprintf("stop watching gvk: %s", gvk.String()))
				cancel()
				delete(m.watchers, gvk)
			}
		}
	}

	resourceMap := m.getResourceMapForFeatureID(fId)

	for resource := range resourceMap {
		if _, ok := currentResources[resource]; !ok {
			s := m.getResourceMapEntryForFeatureID(&resource, fId)
			s.Erase(consumer)
			m.setResourceMapEntryForFeatureID(&resource, fId, s)
		}
	}
}

// This function identifies resources that the ClusterSummary object is no longer interested in.
// It then stops any watchers that were previously set up to deliver notifications about those specific
// resources to ClusterSummary.
// Resources that are still included in the currentResources map will continue to be watched.
func (m *manager) stopStaleWatchForTemplateResourceRef(ctx context.Context,
	clusterSummary *configv1beta1.ClusterSummary, removeAll bool) {

	consumer := &corev1.ObjectReference{
		APIVersion: configv1beta1.GroupVersion.Group,
		Kind:       configv1beta1.ClusterSummaryKind,
		Namespace:  clusterSummary.Namespace,
		Name:       clusterSummary.Name,
	}

	currentGVKs := make(map[schema.GroupVersionKind]bool)
	currentResources := make(map[corev1.ObjectReference]bool)
	if !removeAll {
		for i := range clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs {
			resource := &clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs[i].Resource
			var err error
			resource.Namespace, err = getTemplateResourceNamespace(ctx, clusterSummary,
				&clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs[i])
			if err != nil {
				continue
			}
			resource.Name, _ = getTemplateResourceName(ctx, clusterSummary,
				&clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs[i])

			currentResources[*resource] = true

			gvk := schema.GroupVersionKind{
				Kind:    resource.Kind,
				Group:   resource.GroupVersionKind().Group,
				Version: resource.GroupVersionKind().Version,
			}
			currentGVKs[gvk] = true
		}
	}

	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	for gvk := range m.requestorForTemplateResourceRefs {
		if currentGVKs[gvk] {
			// ClusterSummary still wants a watcher for this GVK
			continue
		}

		requestors := m.requestorForTemplateResourceRefs[gvk]
		requestors.Erase(consumer)
		m.requestorForTemplateResourceRefs[gvk] = requestors

		if m.canStopWatcher(gvk) {
			if cancel, ok := m.watchers[gvk]; ok {
				m.log.V(logs.LogInfo).Info(fmt.Sprintf("stop watching gvk: %s", gvk.String()))
				cancel()
				delete(m.watchers, gvk)
			}
		}
	}

	// Clean up the "All Changes" bucket
	for resource, set := range m.templateResourceRefsWatched {
		if _, ok := currentResources[resource]; !ok {
			set.Erase(consumer)
			if set.Len() == 0 {
				delete(m.templateResourceRefsWatched, resource)
			}
		}
	}

	// Clean up the "Ignore Status" bucket
	for resource, set := range m.templateResourceRefsWatchedIgnoreStatus {
		if _, ok := currentResources[resource]; !ok {
			set.Erase(consumer)
			if set.Len() == 0 {
				delete(m.templateResourceRefsWatchedIgnoreStatus, resource)
			}
		}
	}
}

func (m *manager) startWatcher(ctx context.Context, gvk *schema.GroupVersionKind) error {
	logger := m.log.WithValues("gvk", gvk.String())

	if _, ok := m.watchers[*gvk]; ok {
		logger.V(logs.LogDebug).Info("watcher already present")
		return nil
	}

	logger.V(logs.LogInfo).Info("start watcher")

	lw, err := m.getListerWatcher(ctx, gvk)
	if err != nil {
		logger.Error(err, "Failed to get lister watcher")
		return err
	}

	watcherCtx, cancel := context.WithCancel(ctx) //nolint:gosec // cancel is stored in m.watchers and called when the watcher is stopped
	m.watchers[*gvk] = cancel

	// Nothing here ever reads a resource back out of this watcher: react() below only needs
	// the identity (and, for the "ignore status changes" case, the Generation) of the object
	// carried by the event that fired it. Back the reflector with a store that forwards each
	// event straight to react() and discards the object right after, instead of retaining
	// every instance of gvk - cluster-wide, for whatever type is referenced by any
	// ClusterSummary's policyRefs/kustomizationRefs/templateResourceRefs - in an indexed cache
	// for as long as the watcher stays active, the way a SharedIndexInformer would.
	store := newDiscardingStore(func(oldGeneration *int64, newObj client.Object) {
		m.react(oldGeneration, newObj, logger)
	})
	reflector := cache.NewReflector(lw, &unstructured.Unstructured{}, store, 0)
	go reflector.Run(watcherCtx.Done())

	return nil
}

// getListerWatcher returns a cache.ListerWatcher scoped to gvk's resource, cluster-wide -
// matching the scope the previous SharedIndexInformer-based watcher used.
func (m *manager) getListerWatcher(ctx context.Context, gvk *schema.GroupVersionKind) (cache.ListerWatcher, error) {
	d, err := dynamic.NewForConfig(m.config)
	if err != nil {
		return nil, err
	}

	dc := discovery.NewDiscoveryClientForConfigOrDie(m.config)
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// getListerWatcher is only called after verifying resource is installed.
		return nil, err
	}

	resourceId := schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: mapping.Resource.Resource,
	}

	resourceClient := d.Resource(resourceId)

	return &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return resourceClient.List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return resourceClient.Watch(ctx, options)
		},
	}, nil
}

// discardingStore implements cache.ReflectorStore for use with a cache.Reflector, but never
// retains a full copy of any object: every Add/Update/Delete - including the ones replayed
// from the initial List - is forwarded straight to the handler and then dropped. The only
// state kept is, per watched object, its last-seen Generation (a single int64, not the whole
// object): templateResourceRefsWatchedIgnoreStatus consumers only care whether Spec/Metadata
// changed (a Generation bump), not status-only updates, and a Reflector's ReflectorStore.Update
// is only ever given the new object, never the old one - so that one comparison is the one
// piece of state this store can't discard.
type discardingStore struct {
	handler func(oldGeneration *int64, newObj client.Object)

	mu          sync.Mutex
	generations map[corev1.ObjectReference]int64
}

func newDiscardingStore(handler func(oldGeneration *int64, newObj client.Object)) *discardingStore {
	return &discardingStore{
		handler:     handler,
		generations: make(map[corev1.ObjectReference]int64),
	}
}

func refForObject(obj interface{}) (ref corev1.ObjectReference, o client.Object, ok bool) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return corev1.ObjectReference{}, nil, false
	}

	ref = corev1.ObjectReference{
		Kind:       u.GetObjectKind().GroupVersionKind().Kind,
		APIVersion: u.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		Namespace:  u.GetNamespace(),
		Name:       u.GetName(),
	}
	return ref, u, true
}

func (s *discardingStore) Add(obj interface{}) error {
	ref, o, ok := refForObject(obj)
	if !ok {
		return nil
	}

	s.mu.Lock()
	s.generations[ref] = o.GetGeneration()
	s.mu.Unlock()

	s.handler(nil, o)
	return nil
}

func (s *discardingStore) Update(obj interface{}) error {
	ref, o, ok := refForObject(obj)
	if !ok {
		return nil
	}

	s.mu.Lock()
	oldGeneration, known := s.generations[ref]
	s.generations[ref] = o.GetGeneration()
	s.mu.Unlock()

	if known {
		s.handler(&oldGeneration, o)
	} else {
		s.handler(nil, o)
	}
	return nil
}

func (s *discardingStore) Delete(obj interface{}) error {
	ref, o, ok := refForObject(obj)
	if !ok {
		return nil
	}

	s.mu.Lock()
	delete(s.generations, ref)
	s.mu.Unlock()

	s.handler(nil, o)
	return nil
}

// Replace is invoked by the Reflector after each (re)list, once per listed object. Those are
// treated the same way an informer's initial sync would: as Add events.
func (s *discardingStore) Replace(list []interface{}, _ string) error {
	for i := range list {
		if err := s.Add(list[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *discardingStore) Resync() error {
	return nil
}

func (s *discardingStore) List() []interface{} {
	return nil
}

func (s *discardingStore) ListKeys() []string {
	return nil
}

func (s *discardingStore) Get(obj interface{}) (item interface{}, exists bool, err error) {
	return nil, false, nil
}

func (s *discardingStore) GetByKey(key string) (item interface{}, exists bool, err error) {
	return nil, false, nil
}

// react gets called when an instance of the watched gvk has been added, deleted, or updated.
// oldGeneration is the object's previously observed Generation - nil if this is the first time
// it's been seen (Add/Delete events are always treated this way) - used only to decide whether
// templateResourceRefsWatchedIgnoreStatus consumers should be notified of what would otherwise
// be a status-only change.
func (m *manager) react(oldGeneration *int64, newObj client.Object, logger logr.Logger) {
	m.watchMu.RLock()
	defer m.watchMu.RUnlock()

	ref := corev1.ObjectReference{
		Kind:       newObj.GetObjectKind().GroupVersionKind().Kind,
		APIVersion: newObj.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		Namespace:  newObj.GetNamespace(),
		Name:       newObj.GetName(),
	}

	logger = logger.WithValues("resource", fmt.Sprintf("%s/%s", ref.Namespace, ref.Name))

	// finds all ClusterSummary objects that want to be informed about updates to this specific resource.
	// It takes into account registrations made through KustomizationRefs
	if v, ok := m.mgmtResourcesWatchedKustomizeRef[ref]; ok {
		m.notify(v, logger)
	}

	// finds all ClusterSummary objects that want to be informed about updates to this specific resource.
	// It takes into account registrations made through PolicyRefs
	if v, ok := m.mgmtResourcesWatchedPolicyRef[ref]; ok {
		m.notify(v, logger)
	}

	// finds all ClusterSummary objects that want to be informed about updates to this specific resource.
	// It takes into account registrations made through TemplateResourceRefs.
	// Consumers interested in ALL changes
	if v, ok := m.templateResourceRefsWatched[ref]; ok {
		m.notify(v, logger)
	}

	// finds all ClusterSummary objects that want to be informed about updates to this specific resource.
	// It takes into account registrations made through TemplateResourceRefs.
	// Consumers ignoring Status changes
	if v, ok := m.templateResourceRefsWatchedIgnoreStatus[ref]; ok {
		// - Or the Generation has increased (Spec/Metadata change)
		if oldGeneration == nil || *oldGeneration != newObj.GetGeneration() {
			m.notify(v, logger)
		} else {
			logger.V(logs.LogDebug).Info("skipping notification: only status changed")
		}
	}
}

func (m *manager) notify(consumers *libsveltosset.Set, logger logr.Logger) {
	requestors := consumers.Items()
	for i := range requestors {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("got change notification. Notifying %s/%s",
			requestors[i].Namespace, requestors[i].Name))
		m.notifyConsumer(&requestors[i])
	}
}

func (m *manager) notifyConsumer(consumer *corev1.ObjectReference) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentRequestor := &configv1beta1.ClusterSummary{}
		err := m.Get(context.TODO(),
			types.NamespacedName{Namespace: consumer.Namespace, Name: consumer.Name},
			currentRequestor)
		if err != nil {
			m.log.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch ClusterSummary %v", err))
			return err
		}

		m.log.V(logs.LogInfo).Info(fmt.Sprintf("requeuing ClusterSummary %s/%s",
			currentRequestor.Namespace, currentRequestor.Name))
		// reset annotation. This will cause a new reconciliation to happen
		if currentRequestor.Annotations == nil {
			currentRequestor.Annotations = map[string]string{}
		}
		currentRequestor.Annotations[retriggerAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
		return m.Update(context.TODO(), currentRequestor)
	})
	if err != nil {
		// TODO: if this fails, there is no way to reconcile the ClusterSummary
		m.log.V(logs.LogInfo).Info(fmt.Sprintf("requeuing ClusterSummary %s/%s",
			consumer.Namespace, consumer.Name))
	}
}

func (m *manager) canStopWatcher(gvk schema.GroupVersionKind) bool {
	requestors, ok := m.requestorForMgmtResourcesKustomizeRef[gvk]
	if ok {
		if requestors.Len() != 0 {
			return false
		}
	}

	requestors, ok = m.requestorForMgmtResourcesPolicyRef[gvk]
	if ok {
		if requestors.Len() != 0 {
			return false
		}
	}

	requestors, ok = m.requestorForTemplateResourceRefs[gvk]
	if ok {
		if requestors.Len() != 0 {
			return false
		}
	}

	return true
}

func (m *manager) getGVKMapForFeatureID(fID libsveltosv1beta1.FeatureID) map[schema.GroupVersionKind]*libsveltosset.Set {
	switch fID {
	case libsveltosv1beta1.FeatureKustomize:
		return m.requestorForMgmtResourcesKustomizeRef
	case libsveltosv1beta1.FeatureResources:
		return m.requestorForMgmtResourcesPolicyRef
	case libsveltosv1beta1.FeatureHelm:
		panic(1)
	}

	return nil
}

func (m *manager) getGVKMapEntryForFeatureID(gvk schema.GroupVersionKind, fID libsveltosv1beta1.FeatureID) *libsveltosset.Set {
	var s *libsveltosset.Set
	switch fID {
	case libsveltosv1beta1.FeatureKustomize:
		s = m.requestorForMgmtResourcesKustomizeRef[gvk]
		if s == nil {
			s = &libsveltosset.Set{}
			m.requestorForMgmtResourcesKustomizeRef[gvk] = s
		}
	case libsveltosv1beta1.FeatureResources:
		s = m.requestorForMgmtResourcesPolicyRef[gvk]
		if s == nil {
			s = &libsveltosset.Set{}
			m.requestorForMgmtResourcesPolicyRef[gvk] = s
		}
	case libsveltosv1beta1.FeatureHelm:
		// No resources can be deployed in the management cluster because of helm
		panic(1)
	}

	return s
}

func (m *manager) setGVKMapEntryForFeatureID(gvk schema.GroupVersionKind, fID libsveltosv1beta1.FeatureID,
	currentSet *libsveltosset.Set) {

	switch fID {
	case libsveltosv1beta1.FeatureKustomize:
		if currentSet.Len() != 0 {
			m.requestorForMgmtResourcesKustomizeRef[gvk] = currentSet
		} else {
			delete(m.requestorForMgmtResourcesKustomizeRef, gvk)
		}
	case libsveltosv1beta1.FeatureResources:
		if currentSet.Len() != 0 {
			m.requestorForMgmtResourcesPolicyRef[gvk] = currentSet
		} else {
			delete(m.requestorForMgmtResourcesKustomizeRef, gvk)
		}
	case libsveltosv1beta1.FeatureHelm:
		// No resources can be deployed in the management cluster because of helm
		panic(1)
	}
}

func (m *manager) getResourceMapForFeatureID(fID libsveltosv1beta1.FeatureID) map[corev1.ObjectReference]*libsveltosset.Set {
	switch fID {
	case libsveltosv1beta1.FeatureKustomize:
		return m.mgmtResourcesWatchedKustomizeRef
	case libsveltosv1beta1.FeatureResources:
		return m.mgmtResourcesWatchedPolicyRef
	case libsveltosv1beta1.FeatureHelm:
		// No resources can be deployed in the management cluster because of helm
		panic(1)
	}

	return nil
}

func (m *manager) getResourceMapEntryForFeatureID(resource *corev1.ObjectReference, fID libsveltosv1beta1.FeatureID) *libsveltosset.Set {
	var s *libsveltosset.Set
	switch fID {
	case libsveltosv1beta1.FeatureKustomize:
		s = m.mgmtResourcesWatchedKustomizeRef[*resource]
		if s == nil {
			s = &libsveltosset.Set{}
			m.mgmtResourcesWatchedKustomizeRef[*resource] = s
		}
	case libsveltosv1beta1.FeatureResources:
		s = m.mgmtResourcesWatchedPolicyRef[*resource]
		if s == nil {
			s = &libsveltosset.Set{}
			m.mgmtResourcesWatchedPolicyRef[*resource] = s
		}
	case libsveltosv1beta1.FeatureHelm:
		// No resources can be deployed in the management cluster because of helm
		panic(1)
	}

	return s
}

func (m *manager) setResourceMapEntryForFeatureID(resource *corev1.ObjectReference, fID libsveltosv1beta1.FeatureID,
	currentSet *libsveltosset.Set) {

	switch fID {
	case libsveltosv1beta1.FeatureKustomize:
		if currentSet.Len() != 0 {
			m.mgmtResourcesWatchedKustomizeRef[*resource] = currentSet
		} else {
			delete(m.mgmtResourcesWatchedKustomizeRef, *resource)
		}
	case libsveltosv1beta1.FeatureResources:
		if currentSet.Len() != 0 {
			m.mgmtResourcesWatchedPolicyRef[*resource] = currentSet
		} else {
			delete(m.mgmtResourcesWatchedPolicyRef, *resource)
		}
	case libsveltosv1beta1.FeatureHelm:
		// No resources can be deployed in the management cluster because of helm
		panic(1)
	}
}
