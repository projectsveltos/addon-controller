/*
Copyright 2024. projectsveltos.io. All rights reserved.

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

package clustercache_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"

	"github.com/projectsveltos/addon-controller/controllers/clustercache"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	sveltosKubeconfigPostfix = "-sveltos-kubeconfig"
)

var _ = Describe("Clustercache", func() {
	var logger logr.Logger
	var cluster *libsveltosv1beta1.SveltosCluster

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig())
		cluster = &libsveltosv1beta1.SveltosCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cache" + randomString(),
				Namespace: "cache" + randomString(),
			},
		}
	})

	It("GetKubernetesRestConfig stores in memory first time. RemoveCluster removes any entry associated to cluster",
		func() {
			secret := createClusterResources(cluster)

			cacheMgr := clustercache.GetManager()
			_, err := cacheMgr.GetKubernetesRestConfig(context.TODO(), testEnv.Client, cluster.Namespace,
				cluster.Name, "", "", libsveltosv1beta1.ClusterTypeSveltos, logger)
			Expect(err).To(BeNil())

			clusterObj := &corev1.ObjectReference{
				Namespace:  cluster.Namespace,
				Name:       cluster.Name,
				Kind:       libsveltosv1beta1.SveltosClusterKind,
				APIVersion: libsveltosv1beta1.GroupVersion.String(),
			}
			Expect(cacheMgr.GetConfigFromMap(clusterObj)).ToNot(BeNil())

			storedSecret := cacheMgr.GetSecretForCluster(clusterObj)
			Expect(storedSecret).ToNot(BeNil())
			Expect(storedSecret.Namespace).To(Equal(secret.Namespace))
			Expect(storedSecret.Name).To(Equal(secret.Name))

			secretObj := &corev1.ObjectReference{
				Namespace:  secret.Namespace,
				Name:       secret.Name,
				Kind:       "Secret",
				APIVersion: corev1.SchemeGroupVersion.String(),
			}
			storedCluster := cacheMgr.GetClusterFromSecret(secretObj)
			Expect(storedCluster).ToNot(BeNil())
			Expect(storedCluster.Namespace).To(Equal(cluster.Namespace))
			Expect(storedCluster.Name).To(Equal(cluster.Name))

			cacheMgr.RemoveCluster(cluster.Namespace, clusterObj.Name, libsveltosv1beta1.ClusterTypeSveltos)
			Expect(cacheMgr.GetConfigFromMap(clusterObj)).To(BeNil())
			Expect(cacheMgr.GetSecretForCluster(clusterObj)).To(BeNil())
			Expect(cacheMgr.GetClusterFromSecret(secretObj)).To(BeNil())

		})

	It("RemoveSecret removes entries for all clusters using the modified secret", func() {
		secret := createClusterResources(cluster)

		cacheMgr := clustercache.GetManager()
		_, err := cacheMgr.GetKubernetesRestConfig(context.TODO(), testEnv.Client, cluster.Namespace,
			cluster.Name, "", "", libsveltosv1beta1.ClusterTypeSveltos, logger)
		Expect(err).To(BeNil())

		clusterObj := &corev1.ObjectReference{
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			Kind:       libsveltosv1beta1.SveltosClusterKind,
			APIVersion: libsveltosv1beta1.GroupVersion.String(),
		}
		Expect(cacheMgr.GetConfigFromMap(clusterObj)).ToNot(BeNil())

		secretObj := &corev1.ObjectReference{
			Namespace:  secret.Namespace,
			Name:       secret.Name,
			Kind:       "Secret",
			APIVersion: corev1.SchemeGroupVersion.String(),
		}
		cacheMgr.RemoveSecret(secretObj)
		Expect(cacheMgr.GetConfigFromMap(clusterObj)).To(BeNil())
	})
})

func createClusterResources(cluster *libsveltosv1beta1.SveltosCluster) *corev1.Secret {
	By("Create the cluster's namespace")
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: cluster.Namespace,
		},
	}

	By("Create the secret with cluster kubeconfig")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name + sveltosKubeconfigPostfix,
		},
		Data: map[string][]byte{
			"value": testEnv.Kubeconfig,
		},
	}

	Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
	Expect(testEnv.Create(context.TODO(), cluster)).To(Succeed())
	Expect(testEnv.Create(context.TODO(), secret)).To(Succeed())

	Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())
	return secret
}
