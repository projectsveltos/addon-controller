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

package deployer_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/sveltos-manager/pkg/deployer"
)

const namespacePrefix = "worker"

var (
	cancel context.CancelFunc
)

func TestDeployer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Deployer Suite")
}

var _ = BeforeSuite(func() {
	By("Creating deployer client")
	c := fake.NewClientBuilder().WithObjects(nil...).Build()
	var ctx context.Context
	ctx, cancel = context.WithCancel(context.TODO())
	deployer.GetClient(ctx, klogr.New(), c, 10)
})

var _ = AfterSuite(func() {
	cancel()
})

func randomString() string {
	const length = 10
	return util.RandomString(length)
}
