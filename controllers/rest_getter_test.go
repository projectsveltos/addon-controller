/*
Copyright 2026. projectsveltos.io. All rights reserved.

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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/projectsveltos/addon-controller/controllers"
)

// unreachableHost is a well-formed address nothing listens on: clients built against it
// never contact a server unless a request is actually issued, and any request fails fast.
const unreachableHost = "https://127.0.0.1:1"

var _ = Describe("restConfigGetter", func() {
	var getter *controllers.RestConfigGetter

	BeforeEach(func() {
		// Building a discovery client or a mapper does not contact the server
		getter = controllers.NewRestConfigGetter(&rest.Config{Host: unreachableHost}, "default")
	})

	It("ToDiscoveryClient returns the same discovery client on every call", func() {
		first, err := getter.ToDiscoveryClient()
		Expect(err).To(BeNil())
		Expect(first).ToNot(BeNil())

		second, err := getter.ToDiscoveryClient()
		Expect(err).To(BeNil())
		Expect(second).To(BeIdenticalTo(first))
	})

	It("ToRESTMapper returns the same mapper on every call", func() {
		first, err := getter.ToRESTMapper()
		Expect(err).To(BeNil())
		Expect(first).ToNot(BeNil())

		second, err := getter.ToRESTMapper()
		Expect(err).To(BeNil())
		Expect(second).To(BeIdenticalTo(first))
	})

	It("ToRESTConfig returns a copy with a 5 minutes timeout and leaves the cached config untouched", func() {
		cfg, err := getter.ToRESTConfig()
		Expect(err).To(BeNil())
		Expect(cfg.Timeout).To(Equal(5 * time.Minute))

		cfg.Host = "https://changed"
		again, err := getter.ToRESTConfig()
		Expect(err).To(BeNil())
		Expect(again.Host).To(Equal(unreachableHost))
	})

	It("ToDiscoveryClient and ToRESTMapper can be called concurrently and agree on the result", func() {
		const callers = 20
		clients := make([]discovery.CachedDiscoveryInterface, callers)
		mappers := make([]meta.RESTMapper, callers)

		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				var err error
				clients[i], err = getter.ToDiscoveryClient()
				Expect(err).To(BeNil())
				mappers[i], err = getter.ToRESTMapper()
				Expect(err).To(BeNil())
			}()
		}
		wg.Wait()

		for i := 1; i < callers; i++ {
			Expect(clients[i]).To(BeIdenticalTo(clients[0]))
			Expect(mappers[i]).To(BeIdenticalTo(mappers[0]))
		}
	})
})
