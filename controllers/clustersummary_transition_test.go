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
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	"github.com/projectsveltos/addon-controller/pkg/scope"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

var _ = Describe("TransitionFrom", func() {
	var predecessor *configv1beta1.ClusterProfile
	var predecessorSummary *configv1beta1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var clusterName string

	BeforeEach(func() {
		namespace = randomString()
		clusterName = randomString()

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: namespace,
				Labels: map[string]string{
					testDCLabelKey: testEngValue,
				},
			},
		}

		predecessor = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testDCLabelKey: testEngValue},
					},
				},
			},
		}

		predecessorSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			predecessor.Name, clusterName, false)
		predecessorSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      predecessorSummaryName,
				Namespace: namespace,
				// Set directly rather than via addOwnerReference: that helper only updates a
				// deep copy in the fake client's store, not this local pointer - and it's this
				// pointer, not the client's copy, that ends up in the ClusterSummaryScope below.
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: configv1beta1.GroupVersion.String(), Kind: configv1beta1.ClusterProfileKind, Name: predecessor.Name},
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
	})

	newReconciler := func(initObjects ...client.Object) *controllers.ClusterSummaryReconciler {
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		deployer := fakedeployer.GetClient(context.TODO(), textlogger.NewLogger(textlogger.NewConfig()), c)
		return &controllers.ClusterSummaryReconciler{
			Client:       c,
			Scheme:       scheme,
			Deployer:     deployer,
			ClusterMap:   make(map[corev1.ObjectReference]*libsveltosset.Set),
			ReferenceMap: make(map[corev1.ObjectReference]*libsveltosset.Set),
			PolicyMux:    sync.Mutex{},
		}
	}

	newScope := func(reconciler *controllers.ClusterSummaryReconciler) *scope.ClusterSummaryScope {
		clusterSummaryScope, err := scope.NewClusterSummaryScope(&scope.ClusterSummaryScopeParams{
			Client:         reconciler.Client,
			Logger:         textlogger.NewLogger(textlogger.NewConfig()),
			ClusterSummary: predecessorSummary,
			ControllerName: testControllerNameSummary,
		})
		Expect(err).To(BeNil())
		return clusterSummaryScope
	}

	It("getTransitionSuccessors only returns same-kind profiles naming the predecessor", func() {
		successor := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec:       configv1beta1.Spec{TransitionFrom: []string{predecessor.Name}},
		}
		unrelated := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec:       configv1beta1.Spec{TransitionFrom: []string{randomString()}},
		}
		// A Profile naming the same string as the predecessor's name must never count: TransitionFrom
		// is same-kind only.
		crossKind := &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString(), Namespace: namespace},
			Spec:       configv1beta1.Spec{TransitionFrom: []string{predecessor.Name}},
		}

		reconciler := newReconciler(predecessorSummary, predecessor, successor, unrelated, crossKind)

		successors, err := controllers.GetTransitionSuccessors(reconciler, context.TODO(),
			configv1beta1.ClusterProfileKind, predecessor.Name, namespace)
		Expect(err).To(BeNil())
		Expect(successors).To(HaveLen(1))
	})

	It("getTransitionSuccessors scopes Profile successors to the predecessor's namespace", func() {
		profilePredecessorName := randomString()
		sameNamespaceSuccessor := &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString(), Namespace: namespace},
			Spec:       configv1beta1.Spec{TransitionFrom: []string{profilePredecessorName}},
		}
		otherNamespaceSuccessor := &configv1beta1.Profile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString(), Namespace: randomString()},
			Spec:       configv1beta1.Spec{TransitionFrom: []string{profilePredecessorName}},
		}

		reconciler := newReconciler(predecessorSummary, predecessor, sameNamespaceSuccessor, otherNamespaceSuccessor)

		// A missing (or wrong) namespace scope would return both: proves scoping is enforced,
		// not just that a match exists.
		successors, err := controllers.GetTransitionSuccessors(reconciler, context.TODO(),
			configv1beta1.ProfileKind, profilePredecessorName, namespace)
		Expect(err).To(BeNil())
		Expect(successors).To(HaveLen(1))
	})

	It("areSuccessorsProvisioned returns true when no successor names the predecessor", func() {
		reconciler := newReconciler(predecessorSummary, predecessor, cluster)
		provisioned, _, err := controllers.AreSuccessorsProvisioned(reconciler, context.TODO(), newScope(reconciler),
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(provisioned).To(BeTrue())
	})

	It("areSuccessorsProvisioned returns true when the successor does not match this cluster", func() {
		successor := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec: configv1beta1.Spec{
				TransitionFrom: []string{predecessor.Name},
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{randomString(): randomString()},
					},
				},
			},
		}

		reconciler := newReconciler(predecessorSummary, predecessor, cluster, successor)
		provisioned, _, err := controllers.AreSuccessorsProvisioned(reconciler, context.TODO(), newScope(reconciler),
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(provisioned).To(BeTrue())
	})

	It("areSuccessorsProvisioned blocks when a matching successor has not created its ClusterSummary yet", func() {
		// This is the reconcile-order race: the successor's own reconcile has not run yet, so its
		// ClusterSummary does not exist. Gating on existence would let teardown proceed here; gating
		// on the selector (as implemented) must not.
		successor := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec: configv1beta1.Spec{
				TransitionFrom: []string{predecessor.Name},
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testDCLabelKey: testEngValue},
					},
				},
			},
		}

		reconciler := newReconciler(predecessorSummary, predecessor, cluster, successor)
		provisioned, msg, err := controllers.AreSuccessorsProvisioned(reconciler, context.TODO(), newScope(reconciler),
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(provisioned).To(BeFalse())
		Expect(msg).To(ContainSubstring(successor.Name))
	})

	It("areSuccessorsProvisioned blocks while the matching successor's ClusterSummary is not yet Provisioned", func() {
		successor := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec: configv1beta1.Spec{
				TransitionFrom: []string{predecessor.Name},
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testDCLabelKey: testEngValue},
					},
				},
			},
		}
		successorSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			successor.Name, clusterName, false)
		successorSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{Name: successorSummaryName, Namespace: namespace},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
				ClusterProfileSpec: configv1beta1.Spec{
					PolicyRefs: []configv1beta1.PolicyRef{
						{Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind), Namespace: namespace, Name: randomString()},
					},
				},
			},
			// No FeatureSummaries yet: not Provisioned.
		}
		successorSummary.Labels = map[string]string{
			clusterops.ClusterProfileLabelName: successor.Name,
			configv1beta1.ClusterNameLabel:     clusterName,
			configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
		}

		reconciler := newReconciler(predecessorSummary, predecessor, cluster, successor, successorSummary)
		provisioned, msg, err := controllers.AreSuccessorsProvisioned(reconciler, context.TODO(), newScope(reconciler),
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(provisioned).To(BeFalse())
		Expect(msg).To(ContainSubstring(successor.Name))
	})

	It("areSuccessorsProvisioned proceeds once the matching successor is Provisioned", func() {
		successor := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec: configv1beta1.Spec{
				TransitionFrom: []string{predecessor.Name},
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{testDCLabelKey: testEngValue},
					},
				},
			},
		}
		successorSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			successor.Name, clusterName, false)
		// Empty spec (no helm/policyRefs/kustomize) and no FeatureSummaries: trivially Provisioned,
		// same shape isCluterSummaryProvisioned already treats as done elsewhere.
		successorSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{Name: successorSummaryName, Namespace: namespace},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		successorSummary.Labels = map[string]string{
			clusterops.ClusterProfileLabelName: successor.Name,
			configv1beta1.ClusterNameLabel:     clusterName,
			configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
		}

		reconciler := newReconciler(predecessorSummary, predecessor, cluster, successor, successorSummary)
		provisioned, _, err := controllers.AreSuccessorsProvisioned(reconciler, context.TODO(), newScope(reconciler),
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(provisioned).To(BeTrue())
	})

	It("areSuccessorsProvisioned waits for every matching successor when more than one names the predecessor (fan-in)", func() {
		matchingSelector := libsveltosv1beta1.Selector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{testDCLabelKey: testEngValue},
			},
		}
		readySuccessor := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec:       configv1beta1.Spec{TransitionFrom: []string{predecessor.Name}, ClusterSelector: matchingSelector},
		}
		notReadySuccessor := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: clusterProfileNamePrefix + randomString()},
			Spec:       configv1beta1.Spec{TransitionFrom: []string{predecessor.Name}, ClusterSelector: matchingSelector},
		}

		readySummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind, readySuccessor.Name, clusterName, false)
		readySummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{Name: readySummaryName, Namespace: namespace},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace, ClusterName: cluster.Name, ClusterType: libsveltosv1beta1.ClusterTypeCapi,
			},
		}
		readySummary.Labels = map[string]string{
			clusterops.ClusterProfileLabelName: readySuccessor.Name,
			configv1beta1.ClusterNameLabel:     clusterName,
			configv1beta1.ClusterTypeLabel:     string(libsveltosv1beta1.ClusterTypeCapi),
		}

		// notReadySuccessor matches too, but never created a ClusterSummary: fan-in must wait for it too.
		reconciler := newReconciler(predecessorSummary, predecessor, cluster, readySuccessor, notReadySuccessor, readySummary)
		provisioned, msg, err := controllers.AreSuccessorsProvisioned(reconciler, context.TODO(), newScope(reconciler),
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(provisioned).To(BeFalse())
		Expect(msg).To(ContainSubstring(notReadySuccessor.Name))
	})
})

var _ = Describe("isChartTransitioningFrom", func() {
	ownedBy := func(kind, name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{
			{APIVersion: configv1beta1.GroupVersion.String(), Kind: kind, Name: name},
		}
	}

	It("returns true when the claiming profile's TransitionFrom names the current owner (same kind)", func() {
		predecessorName := randomString()
		current := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ClusterProfileKind, predecessorName)},
		}
		claiming := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ClusterProfileKind, randomString())},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{TransitionFrom: []string{predecessorName}},
			},
		}

		Expect(controllers.IsChartTransitioningFrom(current, claiming)).To(BeTrue())
	})

	It("returns false when TransitionFrom is unset", func() {
		predecessorName := randomString()
		current := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ClusterProfileKind, predecessorName)},
		}
		claiming := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ClusterProfileKind, randomString())},
		}

		Expect(controllers.IsChartTransitioningFrom(current, claiming)).To(BeFalse())
	})

	It("returns false when TransitionFrom names the owner but the owner kinds differ", func() {
		// TransitionFrom is same-kind only: a ClusterProfile named by a Profile's TransitionFrom
		// (or vice versa) must never grant a takeover, even on a name match.
		predecessorName := randomString()
		current := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ProfileKind, predecessorName)},
		}
		claiming := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ClusterProfileKind, randomString())},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{TransitionFrom: []string{predecessorName}},
			},
		}

		Expect(controllers.IsChartTransitioningFrom(current, claiming)).To(BeFalse())
	})

	It("returns false when TransitionFrom names an unrelated profile", func() {
		current := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ClusterProfileKind, randomString())},
		}
		claiming := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{OwnerReferences: ownedBy(configv1beta1.ClusterProfileKind, randomString())},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{TransitionFrom: []string{randomString()}},
			},
		}

		Expect(controllers.IsChartTransitioningFrom(current, claiming)).To(BeFalse())
	})
})
