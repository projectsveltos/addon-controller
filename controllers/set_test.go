package controllers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/cluster-api/util"

	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

var _ = Describe("Set", func() {
	It("insert adds entry", func() {
		s := &controllers.Set{}
		entry := util.RandomString(6)
		controllers.Insert(s, entry)
		Expect(len(controllers.Items(s))).To(Equal(1))
	})

	It("erase removes entry", func() {
		s := &controllers.Set{}
		entry := util.RandomString(6)
		controllers.Insert(s, entry)
		Expect(len(controllers.Items(s))).To(Equal(1))
		controllers.Erase(s, entry)
		Expect(len(controllers.Items(s))).To(Equal(0))
	})

	It("len returns number of entries in set", func() {
		s := &controllers.Set{}
		for i := 0; i < 10; i++ {
			entry := util.RandomString(6)
			controllers.Insert(s, entry)
			Expect(len(controllers.Items(s))).To(Equal(i + 1))
		}
	})

	It("has returns true when entry is in set", func() {
		s := &controllers.Set{}
		numEntries := 10
		for i := 0; i < numEntries; i++ {
			entry := util.RandomString(6)
			controllers.Insert(s, entry)
			Expect(len(controllers.Items(s))).To(Equal(i + 1))
		}
		entry := util.RandomString(6)
		Expect(controllers.Has(s, entry)).To(BeFalse())
		controllers.Insert(s, entry)
		Expect(len(controllers.Items(s))).To(Equal(numEntries + 1))
		Expect(controllers.Has(s, entry)).To(BeTrue())
	})

	It("items returns all entries in set", func() {
		s := &controllers.Set{}
		entry0 := util.RandomString(6)
		controllers.Insert(s, entry0)
		entry1 := util.RandomString(6)
		controllers.Insert(s, entry1)
		entries := controllers.Items(s)
		Expect(entries).To(ContainElement(entry0))
		Expect(entries).To(ContainElement(entry1))
	})
})
