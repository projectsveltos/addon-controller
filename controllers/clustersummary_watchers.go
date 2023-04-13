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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/addon-manager/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/logsettings"
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
	requestor map[schema.GroupVersionKind]*libsveltosset.Set
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
			managerInstance.requestor = make(map[schema.GroupVersionKind]*libsveltosset.Set)
		}
	}
}

// getManager returns the manager instance
func getManager() *manager {
	return managerInstance
}

// WatchGVK starts a watcher if one does not exist already
func (m *manager) watchGVK(ctx context.Context, gvk schema.GroupVersionKind,
	clusterSummary *configv1alpha1.ClusterSummary) error {

	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	m.log.V(logs.LogDebug).Info(fmt.Sprintf("request to watch %s from %s/%s",
		gvk.String(), clusterSummary.Namespace, clusterSummary.Name))

	err := m.startWatcher(ctx, &gvk)
	if err != nil {
		return err
	}
	if _, ok := m.requestor[gvk]; !ok {
		s := &libsveltosset.Set{}
		m.requestor[gvk] = s
	}

	m.requestor[gvk].Insert(&corev1.ObjectReference{
		APIVersion: configv1alpha1.GroupVersion.Group,
		Kind:       configv1alpha1.ClusterSummaryKind,
		Namespace:  clusterSummary.Namespace,
		Name:       clusterSummary.Name,
	})
	return nil
}

func (m *manager) stopStaleWatch(currentGVKs map[schema.GroupVersionKind]bool,
	clusterSummary *configv1alpha1.ClusterSummary) {

	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	for gvk := range m.requestor {
		if currentGVKs != nil && currentGVKs[gvk] {
			// ClusterSummary still wants a watcher for this GVK
			continue
		}

		requestors := m.requestor[gvk]
		requestors.Erase(&corev1.ObjectReference{
			APIVersion: configv1alpha1.GroupVersion.Group,
			Kind:       configv1alpha1.ClusterSummaryKind,
			Namespace:  clusterSummary.Namespace,
			Name:       clusterSummary.Name,
		})
		m.requestor[gvk] = requestors

		// If no ClusterSummary wants to watch this gvk, stop watcher
		if requestors.Len() == 0 {
			if cancel, ok := m.watchers[gvk]; ok {
				m.log.V(logs.LogInfo).Info(fmt.Sprintf("stop watching gvk: %s", gvk.String()))
				cancel()
				delete(m.watchers, gvk)
			}
		}
	}
}

func (m *manager) startWatcher(ctx context.Context, gvk *schema.GroupVersionKind) error {
	logger := m.log.WithValues("gvk", gvk.String())

	if _, ok := m.watchers[*gvk]; ok {
		logger.V(logsettings.LogDebug).Info("watcher already present")
		return nil
	}

	logger.V(logsettings.LogInfo).Info("start watcher")
	// dynamic informer needs to be told which type to watch
	dcinformer, err := m.getDynamicInformer(gvk)
	if err != nil {
		logger.Error(err, "Failed to get informer")
		return err
	}

	watcherCtx, cancel := context.WithCancel(ctx)
	m.watchers[*gvk] = cancel
	go m.runInformer(watcherCtx.Done(), dcinformer.Informer(), gvk, logger)
	return nil
}

func (m *manager) getDynamicInformer(gvk *schema.GroupVersionKind) (informers.GenericInformer, error) {
	// Grab a dynamic interface that we can create informers from
	d, err := dynamic.NewForConfig(m.config)
	if err != nil {
		return nil, err
	}
	// Create a factory object that can generate informers for resource types
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		d,
		0,
		corev1.NamespaceAll,
		nil,
	)

	dc := discovery.NewDiscoveryClientForConfigOrDie(m.config)
	groupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)

	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// getDynamicInformer is only called after verifying resource
		// is installed.
		return nil, err
	}

	resourceId := schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: mapping.Resource.Resource,
	}

	informer := factory.ForResource(resourceId)
	return informer, nil
}

func (m *manager) runInformer(stopCh <-chan struct{}, s cache.SharedIndexInformer,
	gvk *schema.GroupVersionKind, logger logr.Logger) {

	handlers := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			logger.V(logsettings.LogDebug).Info("got add notification")
			// Nothing to do
		},
		DeleteFunc: func(obj interface{}) {
			logger.V(logsettings.LogDebug).Info("got delete notification")
			// Nothing to do
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			logger.V(logsettings.LogDebug).Info("got update notification")
			m.react(gvk)
		},
	}
	_, err := s.AddEventHandler(handlers)
	if err != nil {
		panic(1)
	}
	s.Run(stopCh)
}

// react gets called when an instance of passed in gvk has been modified.
// This method queues all Classifier currently using that gvk to be evaluated.
func (m *manager) react(gvk *schema.GroupVersionKind) {
	m.watchMu.RLock()
	defer m.watchMu.RUnlock()

	if gvk == nil {
		return
	}

	if v, ok := m.requestor[*gvk]; ok {
		requestors := v.Items()
		for i := range requestors {
			currentRequestor := &configv1alpha1.ClusterSummary{}
			err := m.Get(context.TODO(),
				types.NamespacedName{Namespace: requestors[i].Namespace, Name: requestors[i].Name},
				currentRequestor)
			if err != nil {
				m.log.V(logs.LogInfo).Info(fmt.Sprintf("failed to fetch ClusterSummary %v", err))
				continue
			}
			m.log.V(logs.LogInfo).Info(fmt.Sprintf("requeuing ClusterSummary %s/%s",
				currentRequestor.Namespace, currentRequestor.Name))
			err = m.Update(context.TODO(), currentRequestor)
			if err != nil {
				m.log.V(logs.LogInfo).Info(fmt.Sprintf("requeuing ClusterSummary %s/%s",
					currentRequestor.Namespace, currentRequestor.Name))
			}
		}
	}
}
