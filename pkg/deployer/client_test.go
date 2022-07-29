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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
)

var _ = Describe("Client", func() {
	It("RegisterFeatureID returns error only if featureID is already registered", func() {
		featureID := util.RandomString(5)
		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		err := d.RegisterFeatureID(featureID)
		Expect(err).To(BeNil())

		err = d.RegisterFeatureID(featureID)
		Expect(err).ToNot(BeNil())
	})

	It("GetResult returns result when available", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		r := map[string]error{key: nil}
		d.SetResults(r)
		Expect(len(d.GetResults())).To(Equal(1))

		result := d.GetResult(ctx, ns, name, applicant, featureID, cleanup)
		Expect(result.Err).To(BeNil())
		Expect(result.ResultStatus).To(Equal(deployer.Deployed))
	})

	It("GetResult returns result when available with error", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		r := map[string]error{key: fmt.Errorf("failed to deploy")}
		d.SetResults(r)
		Expect(len(d.GetResults())).To(Equal(1))

		result := d.GetResult(ctx, ns, name, applicant, featureID, cleanup)
		Expect(result.Err).ToNot(BeNil())
		Expect(result.ResultStatus).To(Equal(deployer.Failed))
	})

	It("GetResult returns InProgress when request is still queued (currently in progress)", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := true
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		d.SetInProgress([]string{key})
		Expect(len(d.GetInProgress())).To(Equal(1))

		result := d.GetResult(ctx, ns, name, applicant, featureID, cleanup)
		Expect(result.Err).To(BeNil())
		Expect(result.ResultStatus).To(Equal(deployer.InProgress))
	})

	It("GetResult returns InProgress when request is still queued (currently queued)", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		d.SetJobQueue(key, nil)
		Expect(len(d.GetJobQueue())).To(Equal(1))

		result := d.GetResult(ctx, ns, name, applicant, featureID, cleanup)
		Expect(result.Err).To(BeNil())
		Expect(result.ResultStatus).To(Equal(deployer.InProgress))
	})

	It("GetResult returns Unavailable when request is not queued/in progress and result not available", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := true

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		result := d.GetResult(ctx, ns, name, applicant, featureID, cleanup)
		Expect(result.Err).To(BeNil())
		Expect(result.ResultStatus).To(Equal(deployer.Unavailable))
	})

	It("Deploy returns an error when featureID is not registered", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := true

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)

		err := d.Deploy(ctx, ns, name, applicant, featureID, cleanup, nil)
		Expect(err).ToNot(BeNil())
	})

	It("Deploy does nothing if already in the dirty set", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		err := d.RegisterFeatureID(featureID)
		Expect(err).To(BeNil())

		d.SetDirty([]string{key})
		Expect(len(d.GetDirty())).To(Equal(1))

		err = d.Deploy(ctx, ns, name, applicant, featureID, cleanup, nil)
		Expect(err).To(BeNil())
		Expect(len(d.GetDirty())).To(Equal(1))
		Expect(len(d.GetInProgress())).To(Equal(0))
		Expect(len(d.GetJobQueue())).To(Equal(0))
	})

	It("Deploy adds to inProgress", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		err := d.RegisterFeatureID(featureID)
		Expect(err).To(BeNil())

		err = d.Deploy(ctx, ns, name, applicant, featureID, cleanup, nil)
		Expect(err).To(BeNil())
		Expect(len(d.GetDirty())).To(Equal(1))
		Expect(len(d.GetInProgress())).To(Equal(0))
		Expect(len(d.GetJobQueue())).To(Equal(1))
	})

	It("Deploy if already in progress, does not add to jobQueue", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		err := d.RegisterFeatureID(featureID)
		Expect(err).To(BeNil())

		d.SetInProgress([]string{key})
		Expect(len(d.GetInProgress())).To(Equal(1))

		err = d.Deploy(ctx, ns, name, applicant, featureID, cleanup, nil)
		Expect(err).To(BeNil())
		Expect(len(d.GetDirty())).To(Equal(1))
		Expect(len(d.GetInProgress())).To(Equal(1))
		Expect(len(d.GetJobQueue())).To(Equal(0))
	})

	It("Deploy removes existing result", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		err := d.RegisterFeatureID(featureID)
		Expect(err).To(BeNil())

		r := map[string]error{key: nil}
		d.SetResults(r)
		Expect(len(d.GetResults())).To(Equal(1))

		err = d.Deploy(ctx, ns, name, applicant, featureID, cleanup, nil)
		Expect(err).To(BeNil())
		Expect(len(d.GetDirty())).To(Equal(1))
		Expect(len(d.GetInProgress())).To(Equal(0))
		Expect(len(d.GetJobQueue())).To(Equal(1))
		Expect(len(d.GetResults())).To(Equal(0))
	})

	It("CleanupEntries removes features from internal data structure but inProgress", func() {
		ns := namespacePrefix + util.RandomString(5)
		name := namespacePrefix + util.RandomString(5)
		applicant := util.RandomString(5)
		featureID := util.RandomString(5)
		cleanup := false
		key := deployer.GetKey(ns, name, applicant, featureID, cleanup)

		c := fake.NewClientBuilder().WithObjects(nil...).Build()
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		d := deployer.GetClient(ctx, klogr.New(), c, 10)
		defer d.ClearInternalStruct()

		err := d.RegisterFeatureID(featureID)
		Expect(err).To(BeNil())

		r := map[string]error{key: nil}
		d.SetResults(r)
		Expect(len(d.GetResults())).To(Equal(1))

		d.SetInProgress([]string{key})
		Expect(len(d.GetInProgress())).To(Equal(1))

		d.SetDirty([]string{key})
		Expect(len(d.GetDirty())).To(Equal(1))

		d.SetJobQueue(key, nil)
		Expect(len(d.GetJobQueue())).To(Equal(1))

		d.CleanupEntries(ns, name, applicant, featureID, cleanup)
		Expect(len(d.GetDirty())).To(Equal(0))
		Expect(len(d.GetInProgress())).To(Equal(1))
		Expect(len(d.GetJobQueue())).To(Equal(0))
		Expect(len(d.GetResults())).To(Equal(0))
	})
})
