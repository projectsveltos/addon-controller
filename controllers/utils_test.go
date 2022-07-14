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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
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
