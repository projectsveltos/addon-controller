/*
Copyright 2020 The Kubernetes Authors.
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

package helpers

import (
	"context"
	"path"
	goruntime "runtime"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimachineryscheme "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/projectsveltos/sveltos-manager/internal/test/helpers/external"
)

var (
	root string
)

func init() {
	// Get the root of the current file to use in CRD paths.
	//nolint: dogsled // test
	_, filename, _, _ := goruntime.Caller(0)
	root = path.Join(path.Dir(filename), "..", "..", "..")
}

type TestEnvironmentConfiguration struct {
	env *envtest.Environment
}

// TestEnvironment encapsulates a Kubernetes local test environment.
type TestEnvironment struct {
	manager.Manager
	client.Client
	Config     *rest.Config
	Kubeconfig []byte
	env        *envtest.Environment
	cancel     context.CancelFunc
}

// NewTestEnvironmentConfiguration creates a new test environment configuration for running tests.
func NewTestEnvironmentConfiguration(crdDirectoryPaths []string, s *apimachineryscheme.Scheme) *TestEnvironmentConfiguration {
	resolvedCrdDirectoryPaths := make([]string, len(crdDirectoryPaths))

	for i, p := range crdDirectoryPaths {
		resolvedCrdDirectoryPaths[i] = path.Join(root, p)
	}

	clusterCRD := external.TestClusterCRD.DeepCopy()
	machineCRD := external.TestMachineCRD.DeepCopy()
	return &TestEnvironmentConfiguration{
		env: &envtest.Environment{
			Scheme:                s,
			ErrorIfCRDPathMissing: true,
			CRDDirectoryPaths:     resolvedCrdDirectoryPaths,
			CRDs: []*apiextensionsv1.CustomResourceDefinition{
				clusterCRD, machineCRD,
			},
		},
	}
}

// Build creates a new environment spinning up a local api-server.
// This function should be called only once for each package you're running tests within,
// usually the environment is initialized in a suite_test.go file within a `BeforeSuite` ginkgo block.
func (t *TestEnvironmentConfiguration) Build(s *apimachineryscheme.Scheme) (*TestEnvironment, error) {
	if _, err := t.env.Start(); err != nil {
		panic(err)
	}

	user, err := t.env.ControlPlane.AddUser(envtest.User{
		Name:   "cluster-admin",
		Groups: []string{"system:masters"},
	}, nil)
	if err != nil {
		klog.Fatalf("unable to add user: %v", err)
	}

	options := manager.Options{
		Scheme:             s,
		MetricsBindAddress: "0",
	}

	kubeconfig, err := user.KubeConfig()
	if err != nil {
		klog.Fatalf("unable to create kubeconfig: %v", err)
	}

	mgr, err := ctrl.NewManager(t.env.Config, options)

	if err != nil {
		klog.Fatalf("Failed to start testenv manager: %v", err)
	}

	return &TestEnvironment{
		Manager:    mgr,
		Client:     mgr.GetClient(),
		Config:     mgr.GetConfig(),
		env:        t.env,
		Kubeconfig: kubeconfig,
	}, nil
}

// StartManager starts the test controller against the local API server.
func (t *TestEnvironment) StartManager(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	return t.Manager.Start(ctx)
}

// Stop stops the test environment.
func (t *TestEnvironment) Stop() error {
	t.cancel()
	return t.env.Stop()
}
