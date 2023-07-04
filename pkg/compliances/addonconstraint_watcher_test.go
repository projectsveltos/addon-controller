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

package compliances_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2/klogr"

	"github.com/projectsveltos/addon-controller/pkg/compliances"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("AddonCompliance watcher", func() {
	var watcherCtx context.Context
	var cancel context.CancelFunc

	BeforeEach(func() {
		compliances.Reset()
		watcherCtx, cancel = context.WithCancel(context.Background())
		compliances.InitializeManagerWithSkip(watcherCtx, klogr.New(), testEnv.Config, testEnv.Client, 10)
	})

	AfterEach(func() {
		addonCompliances := &libsveltosv1alpha1.AddonComplianceList{}
		Expect(testEnv.List(context.TODO(), addonCompliances)).To(Succeed())

		for i := range addonCompliances.Items {
			if addonCompliances.Items[i].DeletionTimestamp.IsZero() {
				Expect(testEnv.Delete(context.TODO(), &addonCompliances.Items[i]))
			}
		}

		cancel()
	})

	It("watchAddonCompliance starts watcher which reacts to AddonCompliance add events", func() {
		manager := compliances.GetManager()
		manager.SetReEvaluate(false)

		go compliances.WatchAddonCompliance(watcherCtx, testEnv.Config, klogr.New())

		addonCompliance := &libsveltosv1alpha1.AddonCompliance{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonComplianceSpec{
				ClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		Expect(testEnv.Client.Create(watcherCtx, addonCompliance)).To(Succeed())
		Expect(waitForObject(watcherCtx, testEnv.Client, addonCompliance)).To(Succeed())

		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("watchAddonCompliance starts watcher which reacts to AddonCompliance update events (labels change)", func() {
		go compliances.WatchAddonCompliance(watcherCtx, testEnv.Config, klogr.New())

		addonCompliance := &libsveltosv1alpha1.AddonCompliance{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonComplianceSpec{
				ClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		manager := compliances.GetManager()
		manager.SetReEvaluate(false)

		Expect(testEnv.Client.Create(watcherCtx, addonCompliance)).To(Succeed())
		Expect(waitForObject(watcherCtx, testEnv.Client, addonCompliance)).To(Succeed())

		// reEvaluate was set to false. Since an AddonCompliance has been created,
		// reEvaluate should be set to true
		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())

		manager.SetReEvaluate(false)

		manager.SetReEvaluate(false)

		// Update AddonCompliance. reEvaluate is set to true only when Labels and Status change
		// Since test is updating Labels, expect reEvaluate to be reset to true
		currentAddonCompliance := &libsveltosv1alpha1.AddonCompliance{}
		Expect(testEnv.Get(watcherCtx, types.NamespacedName{Name: addonCompliance.Name},
			currentAddonCompliance)).To(Succeed())
		currentAddonCompliance.Labels = map[string]string{randomString(): randomString()}
		Expect(testEnv.Update(watcherCtx, currentAddonCompliance)).To(Succeed())

		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("watchAddonCompliance starts watcher which reacts to AddonCompliance update events (spec change)", func() {
		go compliances.WatchAddonCompliance(watcherCtx, testEnv.Config, klogr.New())

		addonCompliance := &libsveltosv1alpha1.AddonCompliance{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: libsveltosv1alpha1.AddonComplianceSpec{
				ClusterSelector: libsveltosv1alpha1.Selector("env=production"),
			},
		}

		manager := compliances.GetManager()
		manager.SetReEvaluate(false)

		Expect(testEnv.Client.Create(watcherCtx, addonCompliance)).To(Succeed())
		Expect(waitForObject(watcherCtx, testEnv.Client, addonCompliance)).To(Succeed())

		// reEvaluate was set to false. Since an AddonCompliance has been created,
		// reEvaluate should be set to true
		Eventually(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeTrue())

		manager.SetReEvaluate(false)

		// Update AddonCompliance. reEvaluate is set to true only when Labels and Status change
		// Since test is updating Spec, expect reEvaluate to consistently remain set to false
		currentAddonCompliance := &libsveltosv1alpha1.AddonCompliance{}
		Expect(testEnv.Get(watcherCtx, types.NamespacedName{Name: addonCompliance.Name},
			currentAddonCompliance)).To(Succeed())
		currentAddonCompliance.Spec.ClusterSelector = libsveltosv1alpha1.Selector("env=preproduction")
		Expect(testEnv.Update(watcherCtx, currentAddonCompliance)).To(Succeed())

		Consistently(func() bool {
			return manager.GetReEvaluate()
		}, timeout, pollingInterval).Should(BeFalse())
	})
})
