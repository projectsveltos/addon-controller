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
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
)

const eventBufferSize = 20

var _ = Describe("ClusterSummary events", func() {
	var logger logr.Logger
	var clusterProfile *configv1beta1.ClusterProfile
	var clusterSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var recorder *events.FakeRecorder

	BeforeEach(func() {
		namespace = randomString()
		logger = textlogger.NewLogger(textlogger.NewConfig())

		clusterProfile = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{randomString(): randomString()}},
				},
			},
		}

		clusterName := randomString()
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
					clusterProfile.Name, clusterName, false),
				Namespace: namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      clusterName,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterProfile.Name, clusterName, libsveltosv1beta1.ClusterTypeCapi)

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: clusterName},
		}

		recorder = events.NewFakeRecorder(eventBufferSize)
	})

	// newClient returns a client with the objects of the test
	newClient := func() client.Client {
		initObjects := []client.Object{clusterSummary, clusterProfile, cluster}
		return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).
			WithObjects(initObjects...).Build()
	}

	// newReconciler returns a reconciler recording its events in recorder, and a scope on the ClusterSummary.
	newReconciler := func(c client.Client, dep deployer.DeployerInterface,
	) (*controllers.ClusterSummaryReconciler, *scope.ClusterSummaryScope) {

		reconciler := getClusterSummaryReconciler(c, dep)
		controllers.SetEventRecorder(reconciler, recorder)

		return reconciler, getClusterSummaryScope(c, logger, clusterProfile, clusterSummary)
	}

	notePrefix := func(featureID libsveltosv1beta1.FeatureID) string {
		return fmt.Sprintf("Feature %s for cluster Capi %s/%s failed: ", featureID, namespace, clusterSummary.Spec.ClusterName)
	}

	Context("failures which are retried", func() {
		It("records FeatureFailed once, and again when the failure message changes", func() {
			reconciler, clusterSummaryScope := newReconciler(newClient(), nil)
			hash := []byte(randomString())

			failed := libsveltosv1beta1.FeatureStatusFailed
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &failed,
				hash, errors.New("boom"), logger)
			Expect(recorder.Events).To(Receive(Equal("Warning FeatureFailed " + notePrefix(libsveltosv1beta1.FeatureResources) + "boom")))

			// A retry cycle goes through Provisioning and fails the same way: not new
			provisioning := libsveltosv1beta1.FeatureStatusProvisioning
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &provisioning,
				hash, nil, logger)
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &failed,
				hash, errors.New("boom"), logger)
			Expect(recorder.Events).To(BeEmpty())

			// Different failure
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &failed,
				hash, errors.New("another failure"), logger)
			Expect(recorder.Events).To(Receive(Equal("Warning FeatureFailed " + notePrefix(libsveltosv1beta1.FeatureResources) +
				"another failure")))
		})

		It("records a failure again after the feature succeeded", func() {
			reconciler, clusterSummaryScope := newReconciler(newClient(), nil)
			hash := []byte(randomString())

			failed := libsveltosv1beta1.FeatureStatusFailed
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureHelm, &failed,
				hash, errors.New("boom"), logger)
			Expect(recorder.Events).To(Receive(ContainSubstring("Warning FeatureFailed")))

			provisioned := libsveltosv1beta1.FeatureStatusProvisioned
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureHelm, &provisioned,
				hash, nil, logger)

			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureHelm, &failed,
				hash, errors.New("boom"), logger)
			Expect(recorder.Events).To(Receive(ContainSubstring("Warning FeatureFailed")))
		})

		It("uses a different action for each feature, so the events of two features are not merged", func() {
			recorder.Verbose = true
			reconciler, clusterSummaryScope := newReconciler(newClient(), nil)
			hash := []byte(randomString())

			failed := libsveltosv1beta1.FeatureStatusFailed
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureHelm, &failed,
				hash, errors.New("boom"), logger)
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &failed,
				hash, errors.New("boom"), logger)

			Expect(recorder.Events).To(Receive(ContainSubstring("FeatureFailed DeployingHelm")))
			Expect(recorder.Events).To(Receive(ContainSubstring("FeatureFailed DeployingResources")))
		})

		It("is recorded when deployFeature gets a failure from the deployer, once for the same failure", func() {
			c := newClient()
			dep := fakedeployer.GetClient(context.TODO(), logger, c)
			reconciler, clusterSummaryScope := newReconciler(c, dep)

			f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)
			hash, err := controllers.ResourcesHash(context.TODO(), c, clusterSummary, logger)
			Expect(err).To(BeNil())
			clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
				{FeatureID: libsveltosv1beta1.FeatureResources, Hash: hash, Status: libsveltosv1beta1.FeatureStatusProvisioning},
			}

			dep.StoreResult(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
				string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false,
				errors.New("failed to apply resources"))

			Expect(controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, logger)).ToNot(BeNil())
			Expect(recorder.Events).To(Receive(Equal("Warning FeatureFailed " + notePrefix(libsveltosv1beta1.FeatureResources) +
				"failed to apply resources")))

			Expect(controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, logger)).ToNot(BeNil())
			Expect(recorder.Events).To(BeEmpty())
		})
	})

	Context("failures reported by their own event", func() {
		It("does not record FeatureFailed for a failure which will not be retried or a template error", func() {
			reconciler, clusterSummaryScope := newReconciler(newClient(), nil)
			hash := []byte(randomString())

			failed := libsveltosv1beta1.FeatureStatusFailed
			for _, failure := range []error{
				&configv1beta1.NonRetriableError{Message: "not retriable"},
				&configv1beta1.TemplateInstantiationError{Message: "bad template"},
			} {
				controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureHelm, &failed,
					hash, failure, logger)
			}
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records ProfileConflict, and not FeatureFailed, for a conflict with another profile, once for the same conflict", func() {
			reconciler, clusterSummaryScope := newReconciler(newClient(), nil)
			hash := []byte(randomString())

			// The conflict is wrapped, as the handlers return it
			conflict := fmt.Errorf("processing ConfigMap default/cm: %w",
				deployer.NewConflictError("resource is deployed by another profile"))

			failed := libsveltosv1beta1.FeatureStatusFailed
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &failed,
				hash, conflict, logger)
			Expect(recorder.Events).To(Receive(Equal("Warning ProfileConflict Conflict detected for cluster Capi " +
				namespace + "/" + clusterSummary.Spec.ClusterName + ": " + conflict.Error())))
			Expect(recorder.Events).To(BeEmpty())

			// A retry goes through Provisioning and hits the same conflict: not new
			provisioning := libsveltosv1beta1.FeatureStatusProvisioning
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &provisioning,
				hash, nil, logger)
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureResources, &failed,
				hash, conflict, logger)
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records ProfileConflict when deployFeature gets a conflict from the deployer", func() {
			c := newClient()
			dep := fakedeployer.GetClient(context.TODO(), logger, c)
			reconciler, clusterSummaryScope := newReconciler(c, dep)

			f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)
			hash, err := controllers.ResourcesHash(context.TODO(), c, clusterSummary, logger)
			Expect(err).To(BeNil())
			clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
				{FeatureID: libsveltosv1beta1.FeatureResources, Hash: hash, Status: libsveltosv1beta1.FeatureStatusProvisioning},
			}

			// The conflict is detected in the deployer worker and comes back as the result of the deployment,
			// deployFeature does not return it
			dep.StoreResult(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
				string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false,
				deployer.NewConflictError("resource is deployed by another profile"))

			Expect(controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, logger)).ToNot(BeNil())
			Expect(recorder.Events).To(Receive(ContainSubstring("Warning ProfileConflict Conflict detected for cluster Capi " +
				namespace + "/" + clusterSummary.Spec.ClusterName)))
			Expect(recorder.Events).To(BeEmpty())
		})

		It("records FeatureFailedNonRetriable, and not FeatureFailed, when deployFeature gets a non retriable error", func() {
			c := newClient()
			dep := fakedeployer.GetClient(context.TODO(), logger, c)
			reconciler, clusterSummaryScope := newReconciler(c, dep)

			f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)
			hash, err := controllers.ResourcesHash(context.TODO(), c, clusterSummary, logger)
			Expect(err).To(BeNil())
			clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
				{FeatureID: libsveltosv1beta1.FeatureResources, Hash: hash, Status: libsveltosv1beta1.FeatureStatusProvisioning},
			}

			dep.StoreResult(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
				string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false,
				&configv1beta1.NonRetriableError{Message: "invalid configuration"})

			_ = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, logger)
			Expect(recorder.Events).To(Receive(Equal("Warning FeatureFailedNonRetriable Feature Resources for cluster Capi " +
				namespace + "/" + clusterSummary.Spec.ClusterName + " failed with non-retriable error: invalid configuration")))
			Expect(recorder.Events).To(BeEmpty())
		})
	})

	Context("feature deployed", func() {
		It("records a Normal FeatureDeployed event", func() {
			c := newClient()
			dep := fakedeployer.GetClient(context.TODO(), logger, c)
			reconciler, clusterSummaryScope := newReconciler(c, dep)

			f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)
			hash, err := controllers.ResourcesHash(context.TODO(), c, clusterSummary, logger)
			Expect(err).To(BeNil())
			clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
				{FeatureID: libsveltosv1beta1.FeatureResources, Hash: hash, Status: libsveltosv1beta1.FeatureStatusProvisioning},
			}

			dep.StoreResult(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
				string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, false, nil)

			Expect(controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, logger)).To(BeNil())
			Expect(recorder.Events).To(Receive(Equal("Normal FeatureDeployed Feature: Resources deployed to cluster Capi " +
				namespace + "/" + clusterSummary.Spec.ClusterName)))
		})
	})

	Context("failures when a feature is removed", func() {
		It("records FeatureFailed with the action Removing when the undeploy fails, once for the same failure", func() {
			recorder.Verbose = true
			clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
				{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusRemoving},
			}

			c := newClient()
			dep := fakedeployer.GetClient(context.TODO(), logger, c)
			reconciler, clusterSummaryScope := newReconciler(c, dep)

			denial := apierrors.NewForbidden(schema.GroupResource{Group: "example.io", Resource: "widgets"},
				"external", errors.New("admission webhook denied the request: cannot delete the widget"))
			dep.StoreResult(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name,
				string(libsveltosv1beta1.FeatureResources), libsveltosv1beta1.ClusterTypeCapi, true, denial)

			f := controllers.GetHandlersForFeature(libsveltosv1beta1.FeatureResources)
			Expect(controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, logger)).To(BeNil())
			Expect(recorder.Events).To(Receive(And(
				ContainSubstring("FeatureFailed RemovingResources"),
				ContainSubstring(denial.Error()))))

			Expect(controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, f, logger)).To(BeNil())
			Expect(recorder.Events).To(BeEmpty())
		})
	})

	Context("pull mode without a license", func() {
		It("records a single LicenseInvalid event, and no FeatureFailed, for the three features", func() {
			reconciler, clusterSummaryScope := newReconciler(newClient(), nil)

			controllers.UpdateStatusWithMissingLicenseError(reconciler, clusterSummaryScope, logger)
			Expect(recorder.Events).To(Receive(Equal("Warning LicenseInvalid Cluster Capi " + namespace + "/" +
				clusterSummary.Spec.ClusterName + ": license is required to manage SveltosCluster in pull mode")))
			Expect(recorder.Events).To(BeEmpty())

			// Still not licensed: not new
			controllers.UpdateStatusWithMissingLicenseError(reconciler, clusterSummaryScope, logger)
			Expect(recorder.Events).To(BeEmpty())
		})
	})

	It("does not fail when no recorder is set", func() {
		reconciler, clusterSummaryScope := newReconciler(newClient(), nil)
		controllers.SetEventRecorder(reconciler, nil)

		failed := libsveltosv1beta1.FeatureStatusFailed
		Expect(func() {
			controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, libsveltosv1beta1.FeatureHelm, &failed,
				[]byte(randomString()), errors.New("boom"), logger)
		}).ToNot(Panic())
	})
})
