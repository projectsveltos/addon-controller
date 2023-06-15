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

package constraints_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/projectsveltos/addon-controller/internal/test/helpers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/utils"
)

var (
	testEnv *helpers.TestEnvironment
	cancel  context.CancelFunc
	ctx     context.Context
	scheme  *runtime.Scheme
)

var (
	cacheSyncBackoff = wait.Backoff{
		Duration: 100 * time.Millisecond,
		Factor:   1.5,
		Steps:    8,
		Jitter:   0.4,
	}
)

const (
	timeout         = 60 * time.Second
	pollingInterval = 2 * time.Second
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AddonConstraint Manager Suite")
}

var _ = BeforeSuite(func() {
	By("bootstrapping test environment")

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	scheme = setupScheme()

	testEnvConfig := helpers.NewTestEnvironmentConfiguration([]string{}, scheme)
	testEnv, err = testEnvConfig.Build(scheme)
	if err != nil {
		panic(err)
	}

	go func() {
		By("Starting the manager")
		err = testEnv.StartManager(ctx)
		if err != nil {
			panic(fmt.Sprintf("Failed to start the envtest manager: %v", err))
		}
	}()

	addonConstraintCRD, err := utils.GetUnstructured(crd.GetAddonConstraintCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(ctx, addonConstraintCRD)).To(Succeed())
	Expect(waitForObject(ctx, testEnv.Client, addonConstraintCRD)).To(Succeed())

	dcCRD, err := utils.GetUnstructured(crd.GetDebuggingConfigurationCRDYAML())
	Expect(err).To(BeNil())
	Expect(testEnv.Create(ctx, dcCRD)).To(Succeed())
	Expect(waitForObject(ctx, testEnv.Client, dcCRD)).To(Succeed())

	// Wait for synchronization
	// Sometimes we otherwise get "no matches for kind "AddonConstraint" in version "lib.projectsveltos.io/v1alpha1"
	time.Sleep(2 * time.Second)

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

func setupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	Expect(libsveltosv1alpha1.AddToScheme(s)).To(Succeed())
	Expect(clusterv1.AddToScheme(s)).To(Succeed())
	return s
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

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
		return errors.Wrapf(err, "object %s, %s is not being added to the testenv client cache",
			obj.GetObjectKind().GroupVersionKind().String(), key)
	}
	return nil
}
