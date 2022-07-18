package controllers_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"sync"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
	fakedeployer "github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer/fake"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

var _ = Describe("ClustersummaryDeployer", func() {
	var logger logr.Logger
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var namespace string
	var clusterName string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		logger = klogr.New()

		namespace = "reconcile" + util.RandomString(5)

		logger = klogr.New()

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + util.RandomString(5),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}

		clusterName = util.RandomString(7)
		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, namespace, clusterName)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      clusterName,
			},
		}
	})

	It("isFeatureDeployed returns false when feature is not deployed", func() {
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          nil,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		Expect(controllers.IsFeatureDeployed(reconciler, clusterSummaryScope, configv1alpha1.FeatureKyverno)).To(BeFalse())
	})

	It("isFeatureDeployed returns true when feature is deployed", func() {
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          nil,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		Expect(controllers.IsFeatureDeployed(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole)).To(BeTrue())
	})

	It("getHash returns nil when hash is not stored", func() {
		hash := []byte(util.RandomString(13))
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				Hash:      hash,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          nil,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		currentHash := controllers.GetHash(reconciler, clusterSummaryScope, configv1alpha1.FeatureKyverno)
		Expect(currentHash).To(BeNil())
	})

	It("getHash returns stored hash", func() {
		hash := []byte(util.RandomString(13))
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				Hash:      hash,
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          nil,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		currentHash := controllers.GetHash(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole)
		Expect(reflect.DeepEqual(currentHash, hash)).To(BeTrue())
	})

	It("updateFeatureStatus updates ClusterSummary Status FeatureSummary", func() {
		initObjects := []client.Object{
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          nil,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		hash := []byte(util.RandomString(13))
		result := deployer.Result{
			ResultStatus: deployer.Failed,
			Err:          fmt.Errorf("failed to deploy"),
		}
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole, result, hash)

		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureRole))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusFailed))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(Equal(result.Err.Error()))

		result = deployer.Result{
			ResultStatus: deployer.Deployed,
		}
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole, result, hash)
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureRole))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusProvisioned))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(BeNil())
	})

	It("deployFeature when feature is deployed and hash has not changed, does nothing", func() {
		workload := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
					{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
				},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		hash, err := controllers.WorkloadRoleHash(ctx, c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Hash:      hash,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          dep,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles)

		// ClusterSummary Status is reporting feature has deployed. Configuration that needs to be deployed has not
		// changed (so hash in ClusterSummary Status matches hash of all referenced WorkloadRole Specs).
		// DeployeFeature is supposed to return before calling dep.Deploy (fake deployer Deploy once called simply
		// adds key to InProgress).
		// So run DeployFeature then validate key is not added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f)
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole))
		Expect(dep.IsInProgress(key)).To(BeFalse())
	})

	It("deployFeature when feature is deployed and hash has changed, calls Deploy", func() {
		workload := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
					{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
				},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		hash, err := controllers.WorkloadRoleHash(ctx, c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Hash:      hash,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		// Change workloadRole so the configuration that now needs to be deployed does not match the hash in ClusterSummary Status anymore
		workload.Spec.Rules = append(workload.Spec.Rules,
			rbacv1.PolicyRule{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}})
		Expect(c.Update(context.TODO(), workload)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          dep,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles)

		// Even though the feature is marked as deployed in ClusterSummary Status, the configuration has changed (ClusterSummary Status Hash
		// does not match anymore the hash of all referenced WorkloadRole Specs). In such situation, DeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run DeployFeature then validate key is added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f)
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name, string(configv1alpha1.FeatureRole))
		Expect(dep.IsInProgress(key)).To(BeTrue())
	})

	It("deployFeature when feature is not deployed, calls Deploy", func() {
		workload := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
					{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
				},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         logger,
			ClusterFeature: clusterFeature,
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Log:               klogr.New(),
			Scheme:            scheme,
			Deployer:          dep,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles)

		// The feature is not marked as deployed in ClusterSummary Status. In such situation, DeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run DeployFeature then validate key is added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f)
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, clusterSummary.Name, string(configv1alpha1.FeatureRole))
		Expect(dep.IsInProgress(key)).To(BeTrue())
	})
})

var _ = Describe("Hash methods", func() {
	It("workloadRoleHash returns hash considering all referenced workloadroles", func() {
		workload1 := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
					{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
				},
			},
		}

		workload2 := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(6),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeNamespaced,
				Rules: []rbacv1.PolicyRule{
					{Verbs: []string{"get", "list"}, APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}},
				},
			},
		}

		namespace := "reconcile" + util.RandomString(5)
		clusterSummary := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(12),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: namespace,
				ClusterName:      util.RandomString(5),
				ClusterFeatureSpec: configv1alpha1.ClusterFeatureSpec{
					WorkloadRoles: []corev1.ObjectReference{
						{Name: workload1.Name},
						{Name: workload2.Name},
						{Name: util.RandomString(5)},
					},
				},
			},
		}

		initObjects := []client.Object{
			clusterSummary,
			workload1,
			workload2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
			Client:         c,
			Logger:         klogr.New(),
			ClusterSummary: clusterSummary,
			ControllerName: "clustersummary",
		})
		Expect(err).To(BeNil())

		config := render.AsCode(workload1.Spec)
		config += render.AsCode(workload2.Spec)
		h := sha256.New()
		h.Write([]byte(config))
		expectHash := h.Sum(nil)

		hash, err := controllers.WorkloadRoleHash(context.TODO(), c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		Expect(reflect.DeepEqual(hash, expectHash)).To(BeTrue())
	})
})
