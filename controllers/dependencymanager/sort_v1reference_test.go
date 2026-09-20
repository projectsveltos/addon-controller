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

package dependencymanager_test

import (
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/projectsveltos/addon-controller/controllers/dependencymanager"
)

var _ = Describe("SortedCorev1ObjectReference", func() {
	It("sorts by Kind, then Namespace, then Name", func() {
		refs := dependencymanager.SortedCorev1ObjectReference{
			{Kind: "Secret", Namespace: "ns1", Name: "b"},
			{Kind: "ConfigMap", Namespace: "ns2", Name: "a"},
			{Kind: "ConfigMap", Namespace: "ns1", Name: "z"},
			{Kind: "ConfigMap", Namespace: "ns1", Name: "a"},
		}

		sort.Sort(refs)

		Expect(refs).To(Equal(dependencymanager.SortedCorev1ObjectReference{
			{Kind: "ConfigMap", Namespace: "ns1", Name: "a"},
			{Kind: "ConfigMap", Namespace: "ns1", Name: "z"},
			{Kind: "ConfigMap", Namespace: "ns2", Name: "a"},
			{Kind: "Secret", Namespace: "ns1", Name: "b"},
		}))
	})

	It("is a no op on an empty or single element slice", func() {
		empty := dependencymanager.SortedCorev1ObjectReference{}
		sort.Sort(empty)
		Expect(empty).To(BeEmpty())

		single := dependencymanager.SortedCorev1ObjectReference{{Kind: "ConfigMap", Name: "only"}}
		sort.Sort(single)
		Expect(single[0].Name).To(Equal("only"))
	})

	It("reports the correct length and swaps in place", func() {
		refs := dependencymanager.SortedCorev1ObjectReference{
			{Name: "first"},
			{Name: "second"},
		}

		Expect(refs.Len()).To(Equal(2))

		refs.Swap(0, 1)
		Expect(refs[0].Name).To(Equal("second"))
		Expect(refs[1].Name).To(Equal("first"))
	})
})
