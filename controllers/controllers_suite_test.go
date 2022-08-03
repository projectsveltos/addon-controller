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
	"k8s.io/klog/v2/klogr"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/test/helpers"
	fakedeployer "github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer/fake"
)

var (
	testEnv *helpers.TestEnvironment
	cancel  context.CancelFunc
	ctx     context.Context
	scheme  *runtime.Scheme

	clusterFeatureReconciler *controllers.ClusterFeatureReconciler
	clusterSummaryReconciler *controllers.ClusterSummaryReconciler
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

	clusterFeatureReconciler = &controllers.ClusterFeatureReconciler{
		Client:            testEnv.Client,
		Scheme:            scheme,
		ClusterMap:        make(map[string]*controllers.Set),
		ClusterFeatureMap: make(map[string]*controllers.Set),
		ClusterFeatures:   make(map[string]configv1alpha1.Selector),
		Mux:               sync.Mutex{},
	}

	dep := fakedeployer.GetClient(context.TODO(), klogr.New(), testEnv.Client)
	Expect(dep.RegisterFeatureID(string(configv1alpha1.FeatureRole))).To(Succeed())
	clusterSummaryReconciler = &controllers.ClusterSummaryReconciler{
		Client:            testEnv.Client,
		Scheme:            scheme,
		ReferenceMap:      make(map[string]*controllers.Set),
		ClusterSummaryMap: make(map[string]*controllers.Set),
		Mux:               sync.Mutex{},
		Deployer:          dep,
	}

	Expect(clusterFeatureReconciler.SetupWithManager(testEnv.Manager)).To(Succeed())
	Expect(clusterSummaryReconciler.SetupWithManager(testEnv.Manager)).To(Succeed())

	go func() {
		By("Starting the manager")
		if err := testEnv.StartManager(ctx); err != nil {
			panic(fmt.Sprintf("Failed to start the envtest manager: %v", err))
		}
	}()

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
