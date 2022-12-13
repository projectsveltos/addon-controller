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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	libsveltoscrd "github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	"github.com/projectsveltos/libsveltos/lib/utils"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/api/v1alpha1/index"
	"github.com/projectsveltos/sveltos-manager/controllers"
	"github.com/projectsveltos/sveltos-manager/internal/test/helpers"
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
		err = testEnv.StartManager(ctx)
		if err != nil {
			panic(fmt.Sprintf("Failed to start the envtest manager: %v", err))
		}
	}()

	Expect(index.AddDefaultIndexes(ctx, testEnv.Manager)).To(Succeed())

	var sveltosCRD *unstructured.Unstructured
	sveltosCRD, err = utils.GetUnstructured(libsveltoscrd.GetClusterCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), sveltosCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, sveltosCRD)).To(Succeed())

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
		ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
		ReferenceMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterSummaryMap: make(map[types.NamespacedName]*libsveltosset.Set),
		PolicyMux:         sync.Mutex{},
	}
}

func getClusterProfileReconciler(c client.Client) *controllers.ClusterProfileReconciler {
	return &controllers.ClusterProfileReconciler{
		Client:            c,
		Scheme:            scheme,
		ClusterMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterProfileMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterProfiles:   make(map[corev1.ObjectReference]configv1alpha1.Selector),
		Mux:               sync.Mutex{},
	}
}
