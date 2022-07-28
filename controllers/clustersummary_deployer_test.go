package controllers_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"

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
		status := configv1alpha1.FeatureStatusFailed
		statusErr := fmt.Errorf("failed to deploy")
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole, &status, hash, statusErr)

		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureRole))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusFailed))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(Equal(statusErr.Error()))

		status = configv1alpha1.FeatureStatusProvisioned
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole, &status, hash, nil)
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
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), false)
		Expect(dep.IsKeyInProgress(key)).To(BeFalse())
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
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), false)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
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
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), false)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	It("undeployFeature when feature is removed, does nothing", func() {
		workload := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
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

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Status:    configv1alpha1.FeatureStatusRemoved,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          dep,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.UnDeployWorkloadRoles)

		// ClusterSummary Status is reporting feature has removed.
		// UndeployFeature is supposed to return before calling dep.Deploy (fake deployer Deploy once called simply
		// adds key to InProgress).
		// So run UndeployFeature then validate key is not added to InProgress
		err = controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), true)
		Expect(dep.IsKeyInProgress(key)).To(BeFalse())
	})

	It("undeployFeature when feature is not removed, calls Deploy", func() {
		workload := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
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
			Scheme:            scheme,
			Deployer:          dep,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.UnDeployWorkloadRoles)

		// The feature is not marked as removed in ClusterSummary Status. In such situation, UndeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run UndeployFeature then validate key is added to InProgress
		err = controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), true)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	It("deployFeature returns an error if cleaning up roles is in progress", func() {
		workload := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
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
		dep.StoreInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), true)
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureRole, Status: configv1alpha1.FeatureStatusRemoving},
		}

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          dep,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles)

		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("cleanup of Role still in progress. Wait before redeploying"))
	})

	It("undeployFeatures returns an error if deploying roles is in progress", func() {
		workload := &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: util.RandomString(5),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
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
		dep.StoreInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), false)
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureRole, Status: configv1alpha1.FeatureStatusProvisioning},
		}

		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            c,
			Scheme:            scheme,
			Deployer:          dep,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.UnDeployWorkloadRoles)

		err = controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("deploying Role still in progress. Wait before cleanup"))
	})
})

var _ = Describe("Convert result", func() {
	It("convertResultStatus correctly converts deployer.ResultStatus to FeatureStatus", func() {
		reconciler := &controllers.ClusterSummaryReconciler{
			Client:            nil,
			Scheme:            scheme,
			Deployer:          nil,
			WorkloadRoleMap:   make(map[string]*controllers.Set),
			ClusterSummaryMap: make(map[string]*controllers.Set),
			Mux:               sync.Mutex{},
		}

		result := deployer.Result{
			ResultStatus: deployer.InProgress,
		}
		featureStatus := controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(configv1alpha1.FeatureStatusProvisioning))

		result.ResultStatus = deployer.Deployed
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(configv1alpha1.FeatureStatusProvisioned))

		result.ResultStatus = deployer.Failed
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(configv1alpha1.FeatureStatusFailed))

		result.ResultStatus = deployer.Removed
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).ToNot(BeNil())
		Expect(*featureStatus).To(Equal(configv1alpha1.FeatureStatusRemoved))

		result.ResultStatus = deployer.Unavailable
		featureStatus = controllers.ConvertResultStatus(reconciler, result)
		Expect(featureStatus).To(BeNil())
	})
})
