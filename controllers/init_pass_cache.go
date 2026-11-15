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

package controllers

import (
	"context"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

// While the init container computes the hashes of a ClusterSummary, the same management cluster
// objects (the Cluster, the ConfigMaps and Secrets referenced by valuesFrom, the templateResourceRefs
// resources) are read over and over: once per chart, and again by every helper that needs them.
// initPassCache memoizes those reads for the duration of one ClusterSummary, so each object is
// fetched once. It is only ever put in a context by the init container (see withInitPassCache),
// so the reconcilers are not affected.
//
// An initPassCache is not safe for concurrent use: it belongs to the single goroutine processing
// one ClusterSummary. It is dropped with its context, so memory is bounded by the number of
// ClusterSummary instances processed at the same time, not by the total number.
type initPassCache struct {
	objects           map[string]client.Object
	templateResources map[string]map[string]*unstructured.Unstructured
}

type initPassCacheContextKey struct{}

// withInitPassCache returns a context carrying a new, empty initPassCache.
func withInitPassCache(ctx context.Context) context.Context {
	cache := &initPassCache{
		objects:           make(map[string]client.Object),
		templateResources: make(map[string]map[string]*unstructured.Unstructured),
	}
	return context.WithValue(ctx, initPassCacheContextKey{}, cache)
}

// getInitPassCache returns the cache carried by ctx, or nil if there is none.
func getInitPassCache(ctx context.Context) *initPassCache {
	cache, _ := ctx.Value(initPassCacheContextKey{}).(*initPassCache)
	return cache
}

// templateResourcesCacheKey identifies the templateResourceRefs of one version of a ClusterSummary.
// The resourceVersion is part of the key, so a ClusterSummary that changed in the meantime
// never gets the resources collected for its previous spec.
func templateResourcesCacheKey(clusterSummary *configv1beta1.ClusterSummary) string {
	return fmt.Sprintf("%s/%s/%s", clusterSummary.Namespace, clusterSummary.Name, clusterSummary.ResourceVersion)
}

// initCachingClient is a client.Client whose Get is served from the initPassCache carried by the
// context, when there is one. Without a cache in the context every call goes to the API server.
type initCachingClient struct {
	client.Client
}

// NewInitCachingClient wraps c so that Get calls made with a context carrying an initPassCache
// are memoized.
func NewInitCachingClient(c client.Client) client.Client {
	return &initCachingClient{Client: c}
}

func (c *initCachingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object,
	opts ...client.GetOption) error {

	cache := getInitPassCache(ctx)
	if cache == nil || len(opts) > 0 {
		return c.Client.Get(ctx, key, obj, opts...)
	}

	cacheKey := objectCacheKey(key, obj)
	if cached, ok := cache.objects[cacheKey]; ok {
		// Hand out a copy: callers are free to modify what they get.
		reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(cached.DeepCopyObject()).Elem())
		return nil
	}

	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}

	cache.objects[cacheKey] = obj.DeepCopyObject().(client.Object)
	return nil
}

// objectCacheKey identifies an object by its Go type, its GroupVersionKind (which tells apart
// unstructured objects, all of the same Go type) and its namespace/name.
func objectCacheKey(key client.ObjectKey, obj client.Object) string {
	return fmt.Sprintf("%T/%s/%s", obj, obj.GetObjectKind().GroupVersionKind().String(), key.String())
}
