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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

var _ = Describe("instantiateWithLuaScript sandbox", func() {
	var cluster *libsveltosv1beta1.SveltosCluster

	BeforeEach(func() {
		cluster = &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      randomString(),
				Namespace: randomString(),
			},
		}
	})

	instantiate := func(script string) (string, error) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
		return controllers.InstantiateWithLuaScript(context.TODO(), nil, c,
			libsveltosv1beta1.ClusterTypeSveltos, cluster.Namespace, cluster.Name, script, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
	}

	It("rejects a script reaching the host file system through io", func() {
		script := `
function evaluate()
  local f = io.open("/etc/hosts", "r")
  return {resources=tostring(f)}
end`
		_, err := instantiate(script)
		Expect(err).To(HaveOccurred())
	})

	It("rejects a script reading the environment through os.getenv", func() {
		script := `
function evaluate()
  return {resources=os.getenv("HOME")}
end`
		_, err := instantiate(script)
		Expect(err).To(HaveOccurred())
	})

	It("still runs a regular script", func() {
		script := `
function evaluate()
  return {resources="apiVersion: v1"}
end`
		resources, err := instantiate(script)
		Expect(err).To(BeNil())
		Expect(resources).To(Equal("apiVersion: v1"))
	})
})
