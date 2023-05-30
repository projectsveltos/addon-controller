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
package constraints

import (
	"context"
	"reflect"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

func watchAddonConstraint(ctx context.Context, config *rest.Config, logger logr.Logger) {
	gvk := schema.GroupVersionKind{
		Group:   "lib.projectsveltos.io",
		Version: "v1alpha1",
		Kind:    "AddonConstraint",
	}

	dcinformer, err := getDynamicInformer(&gvk, config)
	if err != nil {
		logger.Error(err, "Failed to get informer")
		panic(1)
	}

	runInformer(ctx.Done(), dcinformer.Informer(), logger)
}

func getDynamicInformer(gvk *schema.GroupVersionKind, config *rest.Config) (informers.GenericInformer, error) {
	// Grab a dynamic interface that we can create informers from
	d, err := dynamic.NewForConfig(config)
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

	dc := discovery.NewDiscoveryClientForConfigOrDie(config)
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

func runInformer(stopCh <-chan struct{}, s cache.SharedIndexInformer, logger logr.Logger) {
	mgr := GetManager()
	handlers := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// if a new AddonConstraint is added, re-evaluate
			mgr.setReEvaluate()
		},
		DeleteFunc: func(obj interface{}) {
			// if a new AddonConstraint is added, re-evaluate
			mgr.setReEvaluate()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			newAddonConstraint := &libsveltosv1alpha1.AddonConstraint{}
			err := runtime.DefaultUnstructuredConverter.
				FromUnstructured(newObj.(*unstructured.Unstructured).UnstructuredContent(), newAddonConstraint)
			if err != nil {
				logger.Error(err, "could not convert obj to AddonConstraint")
				return
			}

			if oldObj.(*unstructured.Unstructured) == nil {
				// re-evaluate
				mgr.setReEvaluate()
			}

			oldAddonConstraint := &libsveltosv1alpha1.AddonConstraint{}
			err = runtime.DefaultUnstructuredConverter.
				FromUnstructured(oldObj.(*unstructured.Unstructured).UnstructuredContent(), oldAddonConstraint)
			if err != nil {
				logger.Error(err, "could not convert obj to AddonConstraint")
				return
			}

			if !reflect.DeepEqual(oldAddonConstraint.Status, newAddonConstraint.Status) {
				// re-evaluate
				mgr.setReEvaluate()
			} else if !reflect.DeepEqual(oldAddonConstraint.Labels, newAddonConstraint.Labels) {
				// re-evaluate
				mgr.setReEvaluate()
			}
		},
	}
	_, err := s.AddEventHandler(handlers)
	if err != nil {
		panic(1)
	}
	s.Run(stopCh)
}
