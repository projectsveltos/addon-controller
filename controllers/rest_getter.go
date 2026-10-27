/*
Copyright 2022-26. projectsveltos.io. All rights reserved.

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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	diskcached "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// contextKeyRestConfig is the key type used to store a remote cluster rest.Config
// in a context.Context. A named type prevents accidental collisions with other packages.
type contextKeyRestConfig struct{}

// withRestConfig returns a context carrying the cached *rest.Config for the remote cluster.
// actionConfigInit reads it via restConfigFromContext to avoid re-parsing the kubeconfig
// file on every Helm action call.
func withRestConfig(ctx context.Context, cfg *rest.Config) context.Context {
	return context.WithValue(ctx, contextKeyRestConfig{}, cfg)
}

func restConfigFromContext(ctx context.Context) *rest.Config {
	v, _ := ctx.Value(contextKeyRestConfig{}).(*rest.Config)
	return v
}

// restConfigGetter implements genericclioptions.RESTClientGetter using a pre-built
// *rest.Config. It replaces genericclioptions.NewConfigFlags in actionConfigInit
// so that the cached rest.Config from clustercache is reused instead of
// re-parsing the kubeconfig file on every Helm action.
//
// Helm asks for the discovery client and the REST mapper many times during a single action,
// and a new one would otherwise do its own /api and /apis discovery each time. Both are
// therefore built once per restConfigGetter, which lives as long as one action.Configuration,
// the same freshness kubectl's ConfigFlags gives. Helm itself invalidates the discovery
// cache and resets the mapper when it needs to see new CRDs.
type restConfigGetter struct {
	config    *rest.Config
	namespace string

	discoveryOnce   sync.Once
	discoveryClient discovery.CachedDiscoveryInterface
	discoveryErr    error

	mapperOnce sync.Once
	mapper     meta.RESTMapper
	mapperErr  error
}

func (r *restConfigGetter) ToRESTConfig() (*rest.Config, error) {
	cfg := *r.config // shallow copy to prevent callers from mutating the cached config
	cfg.Timeout = 5 * time.Minute
	return &cfg, nil
}

func (r *restConfigGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	r.discoveryOnce.Do(func() {
		r.discoveryClient, r.discoveryErr = r.newDiscoveryClient()
	})
	return r.discoveryClient, r.discoveryErr
}

func (r *restConfigGetter) newDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	cfg, err := r.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return diskcached.NewMemCacheClient(dc), nil
}

// ToRESTMapper returns a mapper backed by the same discovery client ToDiscoveryClient returns,
// so a discovery already done for capabilities is reused by the mapper.
func (r *restConfigGetter) ToRESTMapper() (meta.RESTMapper, error) {
	r.mapperOnce.Do(func() {
		dc, err := r.ToDiscoveryClient()
		if err != nil {
			r.mapperErr = err
			return
		}
		r.mapper = restmapper.NewDeferredDiscoveryRESTMapper(dc)
	})
	return r.mapper, r.mapperErr
}

func (r *restConfigGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return &directClientConfig{config: r.config, namespace: r.namespace}
}

// directClientConfig implements clientcmd.ClientConfig backed by a rest.Config
// obtained directly (not from a kubeconfig file). Only ClientConfig and Namespace
// are exercised by Helm's code paths; the remaining methods return safe no-ops.
type directClientConfig struct {
	config    *rest.Config
	namespace string
}

func (d *directClientConfig) RawConfig() (clientcmdapi.Config, error) {
	return clientcmdapi.Config{}, nil
}

func (d *directClientConfig) ClientConfig() (*rest.Config, error) {
	cfg := *d.config
	return &cfg, nil
}

func (d *directClientConfig) Namespace() (ns string, overridden bool, err error) {
	return d.namespace, false, nil
}

func (d *directClientConfig) ConfigAccess() clientcmd.ConfigAccess {
	return clientcmd.NewDefaultPathOptions()
}
