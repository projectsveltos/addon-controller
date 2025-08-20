/*
Copyright 2025. projectsveltos.io. All rights reserved.

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

package clusterops_test

import (
	"context"
	"fmt"
	"path"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/api/v1beta1/index"
	"github.com/projectsveltos/addon-controller/controllers/dependencymanager"
	"github.com/projectsveltos/addon-controller/internal/test/helpers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/clusterproxy"
	libsveltoscrd "github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
)

var (
	testEnv *helpers.TestEnvironment
	cancel  context.CancelFunc
	ctx     context.Context
	scheme  *runtime.Scheme
)

const (
	timeout         = 40 * time.Second
	pollingInterval = 2 * time.Second
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

	if synced := testEnv.GetCache().WaitForCacheSync(ctx); !synced {
		time.Sleep(time.Second)
	}

	// Do this read so library is initialized with CAPI present
	_, err = clusterproxy.GetListOfClusters(context.TODO(), testEnv.Client, "", "",
		textlogger.NewLogger(textlogger.NewConfig()))
	Expect(err).To(BeNil())

	dependencymanager.InitializeManagerInstance(context.TODO(), testEnv.Client, false, ctrl.Log.WithName("controller_test_suite"))
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

func randomString() string {
	const length = 10
	return "a-" + util.RandomString(length)
}

func setupScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := configv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := libsveltosv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := sourcev1.AddToScheme(s); err != nil {
		return nil, err
	}

	return s, nil
}

var (
	cacheSyncBackoff = wait.Backoff{
		Duration: 100 * time.Millisecond,
		Factor:   1.5,
		Steps:    8,
		Jitter:   0.4,
	}
)

// waitForObject waits for the cache to be updated helps in preventing test flakes due to the cache sync delays.
func waitForObject(ctx context.Context, c client.Client, obj client.Object) error {
	// Makes sure the cache is updated with the new object
	objCopy := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	if err := wait.ExponentialBackoff(
		cacheSyncBackoff,
		func() (done bool, err error) {
			if err := c.Get(ctx, key, objCopy); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}); err != nil {
		return fmt.Errorf("object %s, %s is not being added to the testenv client cache: %w",
			obj.GetObjectKind().GroupVersionKind().String(), key, err)
	}
	return nil
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) error {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return fmt.Errorf("missing apiVersion or kind and cannot assign it; %w", err)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}

	return nil
}
