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

package controllers_test

import (
	"encoding/base64"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
)

var _ = Describe("Clustersummary Predicates: ConfigMapPredicates", func() {
	var logger logr.Logger
	var configMap *corev1.ConfigMap

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
	})

	It("Create returns true", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)

		e := event.CreateEvent{
			Object: configMap,
		}

		result := configMapPredicate.Create(e)
		Expect(result).To(BeTrue())
	})

	It("Delete returns true", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)

		e := event.DeleteEvent{
			Object: configMap,
		}

		result := configMapPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns true when data has changed", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)
		configMap.Data = map[string]string{"change": "now"}

		oldConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMap.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: configMap,
			ObjectOld: oldConfigMap,
		}

		result := configMapPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns false when Data has not changed", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)
		configMap = createConfigMapWithPolicy("default", configMap.Name, fmt.Sprintf(viewClusterRole, randomString()))

		oldConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:   configMap.Name,
				Labels: map[string]string{"env": "testing"},
			},
			Data: configMap.Data,
		}

		e := event.UpdateEvent{
			ObjectNew: configMap,
			ObjectOld: oldConfigMap,
		}

		result := configMapPredicate.Update(e)
		Expect(result).To(BeFalse())
	})

	It("Update returns true when binaryData has changed", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)
		configMap.BinaryData = map[string][]byte{randomString(): []byte(randomString())}

		oldConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMap.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: configMap,
			ObjectOld: oldConfigMap,
		}

		result := configMapPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns false when binaryData has not changed", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)
		configMap.BinaryData = map[string][]byte{randomString(): []byte(randomString())}

		oldConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:   configMap.Name,
				Labels: map[string]string{"env": "testing"},
			},
			BinaryData: configMap.BinaryData,
		}

		e := event.UpdateEvent{
			ObjectNew: configMap,
			ObjectOld: oldConfigMap,
		}

		result := configMapPredicate.Update(e)
		Expect(result).To(BeFalse())
	})

	It("Update returns true when annotations changed", func() {
		configMapPredicate := controllers.ConfigMapPredicates(logger)
		configMap.Annotations = map[string]string{
			libsveltosv1alpha1.PolicyTemplateAnnotation: "true",
		}

		oldConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMap.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: configMap,
			ObjectOld: oldConfigMap,
		}

		result := configMapPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("Clustersummary Predicates: SecretPredicates", func() {
	var logger logr.Logger
	var secret *corev1.Secret

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
		}
	})

	It("Create returns true", func() {
		secretPredicate := controllers.SecretPredicates(logger)

		e := event.CreateEvent{
			Object: secret,
		}

		result := secretPredicate.Create(e)
		Expect(result).To(BeTrue())
	})

	It("Delete returns true", func() {
		secretPredicate := controllers.SecretPredicates(logger)

		e := event.DeleteEvent{
			Object: secret,
		}

		result := secretPredicate.Delete(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns true when data has changed", func() {
		secretPredicate := controllers.SecretPredicates(logger)
		str := base64.StdEncoding.EncodeToString([]byte("password"))
		secret.Data = map[string][]byte{"change": []byte(str)}

		oldSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: secret.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: secret,
			ObjectOld: oldSecret,
		}

		result := secretPredicate.Update(e)
		Expect(result).To(BeTrue())
	})

	It("Update returns false when Data has not changed", func() {
		secretPredicate := controllers.SecretPredicates(logger)

		oldSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:   secret.Name,
				Labels: map[string]string{"env": "testing"},
			},
		}

		e := event.UpdateEvent{
			ObjectNew: secret,
			ObjectOld: oldSecret,
		}

		result := secretPredicate.Update(e)
		Expect(result).To(BeFalse())
	})

	It("Update returns true when annotations changed", func() {
		secretPredicate := controllers.SecretPredicates(logger)
		secret.Annotations = map[string]string{
			libsveltosv1alpha1.PolicyTemplateAnnotation: "true",
		}

		oldSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: secret.Name,
			},
		}

		e := event.UpdateEvent{
			ObjectNew: secret,
			ObjectOld: oldSecret,
		}

		result := secretPredicate.Update(e)
		Expect(result).To(BeTrue())
	})
})

var _ = Describe("ClusterProfile Predicates: FluxSourcePredicates", func() {
	var logger logr.Logger
	var gitRepository *sourcev1.GitRepository

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		gitRepository = &sourcev1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: predicates + randomString(),
			},
		}

		Expect(addTypeInformationToObject(scheme, gitRepository)).To(Succeed())
	})

	It("Create reprocesses", func() {
		sourcePredicate := controllers.FluxSourcePredicates(logger)

		e := event.CreateEvent{
			Object: gitRepository,
		}

		result := sourcePredicate.Create(e)
		Expect(result).To(BeTrue())
	})
	It("Delete does reprocess", func() {
		sourcePredicate := controllers.FluxSourcePredicates(logger)

		e := event.DeleteEvent{
			Object: gitRepository,
		}

		result := sourcePredicate.Delete(e)
		Expect(result).To(BeTrue())
	})
	It("Update reprocesses when artifact has changed", func() {
		sourcePredicate := controllers.FluxSourcePredicates(logger)

		gitRepository.Status.Artifact = &sourcev1.Artifact{
			Revision: randomString(),
		}

		oldGitRepository := &sourcev1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      gitRepository.Name,
				Namespace: gitRepository.Namespace,
			},
		}

		Expect(addTypeInformationToObject(scheme, oldGitRepository)).To(Succeed())

		e := event.UpdateEvent{
			ObjectNew: gitRepository,
			ObjectOld: oldGitRepository,
		}

		result := sourcePredicate.Update(e)
		Expect(result).To(BeTrue())
	})
	It("Update does not reprocess when artifact has not changed", func() {
		sourcePredicate := controllers.FluxSourcePredicates(logger)

		gitRepository.Status.Artifact = &sourcev1.Artifact{
			Revision: randomString(),
		}

		oldGitRepository := &sourcev1.GitRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      gitRepository.Name,
				Namespace: gitRepository.Namespace,
			},
		}
		oldGitRepository.Status.Artifact = gitRepository.GetArtifact()

		Expect(addTypeInformationToObject(scheme, oldGitRepository)).To(Succeed())

		e := event.UpdateEvent{
			ObjectNew: gitRepository,
			ObjectOld: oldGitRepository,
		}

		result := sourcePredicate.Update(e)
		Expect(result).To(BeFalse())
	})
})
