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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/addon-controller/api/v1beta1/index"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/internal/test/helpers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	libsveltoscrd "github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
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

	ctrl.SetLogger(klog.Background())

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

	Expect(index.AddDefaultIndexes(ctx, testEnv.Manager)).To(Succeed())

	go func() {
		By("Starting the manager")
		err = testEnv.StartManager(ctx)
		if err != nil {
			panic(fmt.Sprintf("Failed to start the envtest manager: %v", err))
		}
	}()

	var sveltosCRD *unstructured.Unstructured
	sveltosCRD, err = k8s_utils.GetUnstructured(libsveltoscrd.GetSveltosClusterCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), sveltosCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, sveltosCRD)).To(Succeed())

	var resourceSummaryCRD *unstructured.Unstructured
	resourceSummaryCRD, err = k8s_utils.GetUnstructured(libsveltoscrd.GetResourceSummaryCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), resourceSummaryCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, resourceSummaryCRD)).To(Succeed())

	var dcCRD *unstructured.Unstructured
	dcCRD, err = k8s_utils.GetUnstructured(libsveltoscrd.GetDebuggingConfigurationCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), dcCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, dcCRD)).To(Succeed())

	var reloaderCRD *unstructured.Unstructured
	reloaderCRD, err = k8s_utils.GetUnstructured(libsveltoscrd.GetReloaderCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), reloaderCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, reloaderCRD)).To(Succeed())

	var setCRD *unstructured.Unstructured
	setCRD, err = k8s_utils.GetUnstructured(libsveltoscrd.GetSetCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), setCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, setCRD)).To(Succeed())

	var clusterSetCRD *unstructured.Unstructured
	clusterSetCRD, err = k8s_utils.GetUnstructured(libsveltoscrd.GetClusterSetCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(context.TODO(), clusterSetCRD)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv, clusterSetCRD)).To(Succeed())

	projectsveltosNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "projectsveltos",
		},
	}
	Expect(testEnv.Client.Create(context.TODO(), projectsveltosNs)).To(Succeed())
	Expect(waitForObject(context.TODO(), testEnv.Client, projectsveltosNs)).To(Succeed())

	// Wait for synchronization
	// Sometimes we otherwise get "no matches for kind ... in version "lib.projectsveltos.io/v1beta1"
	time.Sleep(2 * time.Second)

	controllers.InitializeManager(textlogger.NewLogger(textlogger.NewConfig()),
		testEnv.Config, testEnv.GetClient())

	if synced := testEnv.GetCache().WaitForCacheSync(ctx); !synced {
		time.Sleep(time.Second)
	}

	// Do this read so library is initialized with CAPI present
	_, err = clusterproxy.GetListOfClusters(context.TODO(), testEnv.Client, "", "",
		textlogger.NewLogger(textlogger.NewConfig()))
	Expect(err).To(BeNil())
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

func getClusterSummaryReconciler(c client.Client, dep deployer.DeployerInterface) *controllers.ClusterSummaryReconciler {
	return &controllers.ClusterSummaryReconciler{
		Client:       c,
		Scheme:       scheme,
		Deployer:     dep,
		ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
		ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
		PolicyMux:    sync.Mutex{},
	}
}

func getClusterProfileReconciler(c client.Client) *controllers.ClusterProfileReconciler {
	return &controllers.ClusterProfileReconciler{
		Client:          c,
		Scheme:          scheme,
		ClusterMap:      make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterProfiles: make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		ClusterLabels:   make(map[corev1.ObjectReference]map[string]string),
		Mux:             sync.Mutex{},
	}
}

func getProfileReconciler(c client.Client) *controllers.ProfileReconciler {
	return &controllers.ProfileReconciler{
		Client:        c,
		Scheme:        scheme,
		ClusterMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
		Profiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		ClusterLabels: make(map[corev1.ObjectReference]map[string]string),
		Mux:           sync.Mutex{},
	}
}
