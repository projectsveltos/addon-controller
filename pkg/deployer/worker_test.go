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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
)

var messages chan string

func writeToChannelHandler(ctx context.Context, c client.Client, namespace, name, featureID string) error {
	By("writeToChannelHandler: writing to channel")
	messages <- "done deploying"
	return nil
}

func doNothingHandler(ctx context.Context, c client.Client, namespace, name, featureID string) error {
	return nil
}

var _ = Describe("Worker", func() {
	It("getKey and getFromKey return correct values", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)

		outNs, outName, outFeatureID := deployer.GetFromKey(key)
		Expect(outNs).To(Equal(ns))
		Expect(outName).To(Equal(name))
		Expect(outFeatureID).To(Equal(featureID))
	})

	It("removeFromSlice should remove element from slice", func() {
		tmp := []string{"eng", "sale", "hr"}
		tmp = deployer.RemoveFromSlice(tmp, 1)
		Expect(len(tmp)).To(Equal(2))
		Expect(tmp[0]).To(Equal("eng"))
		Expect(tmp[1]).To(Equal("hr"))

		tmp = deployer.RemoveFromSlice(tmp, 1)
		Expect(len(tmp)).To(Equal(1))

		tmp = deployer.RemoveFromSlice(tmp, 0)
		Expect(len(tmp)).To(Equal(0))
	})

	It("storeResult saves results and removes key from inProgress", func() {
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		d := deployer.GetClient(context.TODO(), klogr.New(), c)
		defer d.ClearInternalStruct()

		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)
		d.SetInProgress([]string{key})
		Expect(len(d.GetInProgress())).To(Equal(1))

		deployer.StoreResult(d, key, nil, doNothingHandler, klogr.New())
		Expect(len(d.GetInProgress())).To(Equal(0))
	})

	It("storeResult saves results and removes key from dirty and adds to jobQueue", func() {
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		d := deployer.GetClient(context.TODO(), klogr.New(), c)
		defer d.ClearInternalStruct()

		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)
		d.SetInProgress([]string{key})
		Expect(len(d.GetInProgress())).To(Equal(1))

		d.SetDirty([]string{key})
		Expect(len(d.GetDirty())).To(Equal(1))

		deployer.StoreResult(d, key, nil, doNothingHandler, klogr.New())
		Expect(len(d.GetInProgress())).To(Equal(0))
		Expect(len(d.GetDirty())).To(Equal(0))
		Expect(len(d.GetJobQueue())).To(Equal(1))
	})

	It("getRequestStatus returns result when available", func() {
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		d := deployer.GetClient(context.TODO(), klogr.New(), c)
		defer d.ClearInternalStruct()

		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)

		r := map[string]error{key: nil}
		d.SetResults(r)
		Expect(len(d.GetResults())).To(Equal(1))

		resp, err := deployer.GetRequestStatus(d, ns, name, featureID)
		Expect(err).To(BeNil())
		Expect(resp).ToNot(BeNil())
		Expect(deployer.IsResponseDeployed(resp)).To(BeTrue())
	})

	It("getRequestStatus returns result when available and reports error", func() {
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		d := deployer.GetClient(context.TODO(), klogr.New(), c)
		defer d.ClearInternalStruct()

		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)

		r := map[string]error{key: fmt.Errorf("failed to deploy")}
		d.SetResults(r)
		Expect(len(d.GetResults())).To(Equal(1))

		resp, err := deployer.GetRequestStatus(d, ns, name, featureID)
		Expect(err).To(BeNil())
		Expect(resp).ToNot(BeNil())
		Expect(deployer.IsResponseFailed(resp)).To(BeTrue())
	})

	It("getRequestStatus returns nil response when request is still queued (currently in progress)", func() {
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		d := deployer.GetClient(context.TODO(), klogr.New(), c)
		defer d.ClearInternalStruct()

		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)

		d.SetInProgress([]string{key})
		Expect(len(d.GetInProgress())).To(Equal(1))

		resp, err := deployer.GetRequestStatus(d, ns, name, featureID)
		Expect(err).To(BeNil())
		Expect(resp).To(BeNil())
	})

	It("getRequestStatus returns nil response when request is still queued (currently queued)", func() {
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		d := deployer.GetClient(context.TODO(), klogr.New(), c)
		defer d.ClearInternalStruct()

		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)

		d.SetJobQueue(key, nil)
		Expect(len(d.GetJobQueue())).To(Equal(1))

		resp, err := deployer.GetRequestStatus(d, ns, name, featureID)
		Expect(err).To(BeNil())
		Expect(resp).To(BeNil())
	})

	It("processRequests proces request and stores results", func() {
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		d := deployer.GetClient(ctx, klogr.New(), c)
		defer d.ClearInternalStruct()

		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		featureID := util.RandomString(5)
		key := deployer.GetKey(ns, name, featureID)
		d.SetJobQueue(key, writeToChannelHandler)
		Expect(len(d.GetJobQueue())).To(Equal(1))

		messages = make(chan string)

		go deployer.ProcessRequests(d, 1, klogr.New())

		gotResult := false
		go func() {
			// wait for processRequest to process the request
			// processRequest processes queued request every second
			<-messages
			By("read from channel. Request is processed")
			gotResult = true
			cancel()
		}()

		// wait for result to be available
		Eventually(func() bool {
			return gotResult
		}, 20*time.Second, time.Second).Should(BeTrue())

		resp, err := deployer.GetRequestStatus(d, ns, name, featureID)
		Expect(err).To(BeNil())
		Expect(deployer.IsResponseDeployed(resp)).To(BeTrue())
	})
})
