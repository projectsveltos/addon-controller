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

package dependencymanager_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2/textlogger"

	"github.com/projectsveltos/addon-controller/controllers/dependencymanager"

	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	c      client.Client
	scheme *runtime.Scheme
)

const (
	timeout         = 2 * time.Minute
	pollingInterval = 5 * time.Second
)

func TestChartmanager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Chartmanager Suite")
}

var _ = BeforeSuite(func() {
	By("bootstrapping test environment")

	scheme = setupScheme()
	c = fake.NewClientBuilder().WithScheme(scheme).Build()

	logger := textlogger.NewLogger(textlogger.NewConfig())

	dependencymanager.InitializeManagerInstance(context.TODO(), c, true, logger)

	By("Wait for dependency manager to be ready")
	Eventually(func() error {
		_, err := dependencymanager.GetManagerInstance()
		return err
	}, timeout, pollingInterval).Should(BeNil())
})

func randomString() string {
	const length = 10
	return util.RandomString(length)
}
