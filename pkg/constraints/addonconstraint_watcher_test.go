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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"

	"github.com/projectsveltos/addon-controller/pkg/constraints"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("AddonConstraint watcher", func() {
	var watcherCtx context.Context
	var cancel context.CancelFunc

	BeforeEach(func() {
		constraints.Reset()
		watcherCtx, cancel = context.WithCancel(context.Background())
		constraints.InitializeManagerWithSkip(watcherCtx, klogr.New(), testEnv.Config, testEnv.Client, 10)
	})

	AfterEach(func() {
		addonConstraints := &libsveltosv1alpha1.AddonConstraintList{}
		Expect(testEnv.List(context.TODO(), addonConstraints)).To(Succeed())

		for i := range addonConstraints.Items {
			if addonConstraints.Items[i].DeletionTimestamp.IsZero() {
				Expect(testEnv.Delete(context.TODO(), &addonConstraints.Items[i]))
			}
		}

		cancel()
	})

	It("watchAddonConstraint starts watcher which reacts to AddonConstraint add events", func() {
		manager := constraints.GetManager()
		manager.SetReEvaluate(false)

		go constraints.WatchAddonConstraint(watcherCtx, testEnv.Config, klogr.New())

		addonConstraint := &libsveltosv1alpha1.AddonConstraint{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonConstraintSpec{
				ClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		Expect(testEnv.Client.Create(watcherCtx, addonConstraint)).To(Succeed())
		Expect(waitForObject(watcherCtx, testEnv.Client, addonConstraint)).To(Succeed())

		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("watchAddonConstraint starts watcher which reacts to AddonConstraint update events (labels change)", func() {
		go constraints.WatchAddonConstraint(watcherCtx, testEnv.Config, klogr.New())

		addonConstraint := &libsveltosv1alpha1.AddonConstraint{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonConstraintSpec{
				ClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		manager := constraints.GetManager()
		manager.SetReEvaluate(false)

		Expect(testEnv.Client.Create(watcherCtx, addonConstraint)).To(Succeed())
		Expect(waitForObject(watcherCtx, testEnv.Client, addonConstraint)).To(Succeed())

		// reEvaluate was set to false. Since an AddonConstraint has been created,
		// reEvaluate should be set to true
		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())

		manager.SetReEvaluate(false)

		manager.SetReEvaluate(false)

		// Update AddonConstraint. reEvaluate is set to true only when Labels and Status change
		// Since test is updating Labels, expect reEvaluate to be reset to true
		currentAddonConstraint := &libsveltosv1alpha1.AddonConstraint{}
		Expect(testEnv.Get(watcherCtx, types.NamespacedName{Name: addonConstraint.Name},
			currentAddonConstraint)).To(Succeed())
		currentAddonConstraint.Labels = map[string]string{randomString(): randomString()}
		Expect(testEnv.Update(watcherCtx, currentAddonConstraint)).To(Succeed())

		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("watchAddonConstraint starts watcher which reacts to AddonConstraint update events (spec change)", func() {
		go constraints.WatchAddonConstraint(watcherCtx, testEnv.Config, klogr.New())

		addonConstraint := &libsveltosv1alpha1.AddonConstraint{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonConstraintSpec{
				ClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		manager := constraints.GetManager()
		manager.SetReEvaluate(false)

		Expect(testEnv.Client.Create(watcherCtx, addonConstraint)).To(Succeed())
		Expect(waitForObject(watcherCtx, testEnv.Client, addonConstraint)).To(Succeed())

		// reEvaluate was set to false. Since an AddonConstraint has been created,
		// reEvaluate should be set to true
		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())

		manager.SetReEvaluate(false)

		// Update AddonConstraint. reEvaluate is set to true only when Labels and Status change
		// Since test is updating Spec, expect reEvaluate to consistently remain set to false
		currentAddonConstraint := &libsveltosv1alpha1.AddonConstraint{}
		Expect(testEnv.Get(watcherCtx, types.NamespacedName{Name: addonConstraint.Name},
			currentAddonConstraint)).To(Succeed())
		currentAddonConstraint.Spec.ClusterSelector = libsveltosv1alpha1.Selector("env=preproduction")
		Expect(testEnv.Update(watcherCtx, currentAddonConstraint)).To(Succeed())

		Consistently(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeFalse())
	})
})
