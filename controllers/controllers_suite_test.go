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
	"context"
	"fmt"
	"path"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/api/v1alpha1/index"
	"github.com/projectsveltos/sveltos-manager/controllers"
	"github.com/projectsveltos/sveltos-manager/internal/test/helpers"
	"github.com/projectsveltos/sveltos-manager/pkg/deployer"
)

var (
	testEnv *helpers.TestEnvironment
	cancel  context.CancelFunc
	ctx     context.Context
	scheme  *runtime.Scheme
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controllers Suite")
}

var _ = BeforeSuite(func() {
	By("bootstrapping test environment")

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	scheme, err = setupScheme()
	Expect(err).To(BeNil())

	testEnvConfig := helpers.NewTestEnvironmentConfiguration([]string{
		path.Join("config", "crd", "bases"),
	}, scheme)
	testEnv, err = testEnvConfig.Build(scheme)
	if err != nil {
		panic(err)
	}

	controllers.SetManagementClusterAccess(testEnv.Client, testEnv.Config)
	controllers.CreatFeatureHandlerMaps()

	go func() {
		By("Starting the manager")
		if err := testEnv.StartManager(ctx); err != nil {
			panic(fmt.Sprintf("Failed to start the envtest manager: %v", err))
		}
	}()

	Expect(index.AddDefaultIndexes(ctx, testEnv.Manager)).To(Succeed())

	if synced := testEnv.GetCache().WaitForCacheSync(ctx); !synced {
		time.Sleep(time.Second)
	}
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

func getClusterSummaryReconciler(c client.Client, dep deployer.DeployerInterface) *controllers.ClusterSummaryReconciler {
	return &controllers.ClusterSummaryReconciler{
		Client:            c,
		Scheme:            scheme,
		Deployer:          dep,
		ReferenceMap:      make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
		ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
		PolicyMux:         sync.Mutex{},
	}
}

func getClusterProfileReconciler(c client.Client) *controllers.ClusterProfileReconciler {
	return &controllers.ClusterProfileReconciler{
		Client:            c,
		Scheme:            scheme,
		ClusterMap:        make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
		ClusterProfileMap: make(map[libsveltosv1alpha1.PolicyRef]*libsveltosset.Set),
		ClusterProfiles:   make(map[libsveltosv1alpha1.PolicyRef]configv1alpha1.Selector),
		Mux:               sync.Mutex{},
	}
}
