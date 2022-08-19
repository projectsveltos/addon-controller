package controllers_test

import (
	"context"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/klogr"
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
	var workload *configv1alpha1.WorkloadRole
	var namespace string
	var clusterName string

	BeforeEach(func() {
		logger = klogr.New()

		namespace = "reconcile" + randomString()

		logger = klogr.New()

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}

		clusterName = randomString()
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
		addLabelsToClusterSummary(clusterSummary, clusterFeature.Name, namespace, clusterName)

		workload = &configv1alpha1.WorkloadRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: randomString(),
			},
			Spec: configv1alpha1.WorkloadRoleSpec{
				Type: configv1alpha1.RoleTypeCluster,
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

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

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

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		Expect(controllers.IsFeatureDeployed(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole)).To(BeTrue())
	})

	It("getHash returns nil when hash is not stored", func() {
		hash := []byte(randomString())
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

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		currentHash := controllers.GetHash(reconciler, clusterSummaryScope, configv1alpha1.FeatureKyverno)
		Expect(currentHash).To(BeNil())
	})

	It("getHash returns stored hash", func() {
		hash := []byte(randomString())
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

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		currentHash := controllers.GetHash(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole)
		Expect(reflect.DeepEqual(currentHash, hash)).To(BeTrue())
	})

	It("updateFeatureStatus updates ClusterSummary Status FeatureSummary", func() {
		initObjects := []client.Object{
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		reconciler := getClusterSummaryReconciler(c, nil)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		hash := []byte(randomString())
		status := configv1alpha1.FeatureStatusFailed
		statusErr := fmt.Errorf("failed to deploy")
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole, &status,
			hash, statusErr, klogr.New())

		Expect(len(clusterSummary.Status.FeatureSummaries)).To(Equal(1))
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureRole))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusFailed))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).ToNot(BeNil())
		Expect(*clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(Equal(statusErr.Error()))

		status = configv1alpha1.FeatureStatusProvisioned
		controllers.UpdateFeatureStatus(reconciler, clusterSummaryScope, configv1alpha1.FeatureRole, &status,
			hash, nil, klogr.New())
		Expect(clusterSummary.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureRole))
		Expect(clusterSummary.Status.FeatureSummaries[0].Status).To(Equal(configv1alpha1.FeatureStatusProvisioned))
		Expect(clusterSummary.Status.FeatureSummaries[0].FailureMessage).To(BeNil())
	})

	It("deployFeature when feature is deployed and hash has not changed, does nothing", func() {
		workload.Spec = configv1alpha1.WorkloadRoleSpec{
			Type: configv1alpha1.RoleTypeCluster,
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
				{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Name: workload.Name},
		}
		clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			Replicas: 1,
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		workloadHash, err := controllers.WorkloadRoleHash(ctx, c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		kyvernoHash, err := controllers.KyvernoHash(ctx, c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Hash:      workloadHash,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
			{
				FeatureID: configv1alpha1.FeatureKyverno,
				Hash:      kyvernoHash,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles, controllers.GetWorkloadRoleRefs)

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

		f = controllers.GetFeature(configv1alpha1.FeatureKyverno,
			controllers.KyvernoHash, controllers.DeployKyverno, controllers.GetKyvernoRefs)

		// ClusterSummary Status is reporting feature has deployed. Configuration that needs to be deployed has not
		// changed (no change in Kyverno configuration).
		// DeployeFeature is supposed to return before calling dep.Deploy (fake deployer Deploy once called simply
		// adds key to InProgress).
		// So run DeployFeature then validate key is not added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).To(BeNil())

		key = deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureKyverno), false)
		Expect(dep.IsKeyInProgress(key)).To(BeFalse())
	})

	It("deployFeature when feature is deployed and hash has changed, calls Deploy", func() {
		workload.Spec = configv1alpha1.WorkloadRoleSpec{
			Type: configv1alpha1.RoleTypeCluster,
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
				{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
			},
		}

		configMapNs := randomString()
		configMap := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(addLabelPolicyStr, randomString()))

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Name: workload.Name},
		}
		clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			Replicas: 1,
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMapNs, Name: configMap.Name},
			},
		}

		initObjects := []client.Object{
			workload,
			configMap,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		workloadHash, err := controllers.WorkloadRoleHash(ctx, c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		kyvernoHash, err := controllers.KyvernoHash(ctx, c, clusterSummaryScope, klogr.New())
		Expect(err).To(BeNil())
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Hash:      workloadHash,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
			{
				FeatureID: configv1alpha1.FeatureKyverno,
				Hash:      kyvernoHash,
				Status:    configv1alpha1.FeatureStatusProvisioned,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		// Change workloadRole so the configuration that now needs to be deployed does not match the hash in ClusterSummary Status anymore
		workload.Spec.Rules = append(workload.Spec.Rules,
			rbacv1.PolicyRule{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}})
		Expect(c.Update(context.TODO(), workload)).To(Succeed())
		configMap = createConfigMapWithPolicy(configMapNs, configMap.Name, fmt.Sprintf(checkSa, randomString()))
		Expect(c.Update(context.TODO(), configMap)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles, controllers.GetWorkloadRoleRefs)

		// Even though the feature is marked as deployed in ClusterSummary Status, the configuration has changed (ClusterSummary Status Hash
		// does not match anymore the hash of all referenced WorkloadRole Specs). In such situation, DeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run DeployFeature then validate key is added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("request is queued"))

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), false)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())

		f = controllers.GetFeature(configv1alpha1.FeatureKyverno,
			controllers.KyvernoHash, controllers.DeployKyverno, controllers.GetKyvernoRefs)

		// Even though the feature is marked as deployed in ClusterSummary Status, the configuration has changed (ClusterSummary Status Hash
		// does not match anymore the hash of all referenced WorkloadRole Specs). In such situation, DeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run DeployFeature then validate key is added to InProgress
		err = controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("request is queued"))

		key = deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureKyverno), false)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	It("deployFeature when feature is not deployed, calls Deploy", func() {
		workload.Spec = configv1alpha1.WorkloadRoleSpec{
			Type: configv1alpha1.RoleTypeCluster,
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"create", "get"}, APIGroups: []string{"cert-manager.io"}, Resources: []string{"certificaterequests"}},
				{Verbs: []string{"create", "delete"}, APIGroups: []string{""}, Resources: []string{"namespaces", "deployments"}},
			},
		}

		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles, controllers.GetWorkloadRoleRefs)

		// The feature is not marked as deployed in ClusterSummary Status. In such situation, DeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run DeployFeature then validate key is added to InProgress
		err := controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("request is queued"))

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), false)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	It("undeployFeature when feature is removed, does nothing", func() {
		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureRole,
				Status:    configv1alpha1.FeatureStatusRemoved,
			},
		}

		Expect(c.Status().Update(context.TODO(), clusterSummary)).To(Succeed())

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.UnDeployWorkloadRoles, controllers.GetWorkloadRoleRefs)

		// ClusterSummary Status is reporting feature has removed.
		// UndeployFeature is supposed to return before calling dep.Deploy (fake deployer Deploy once called simply
		// adds key to InProgress).
		// So run UndeployFeature then validate key is not added to InProgress
		err := controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).To(BeNil())

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), true)
		Expect(dep.IsKeyInProgress(key)).To(BeFalse())
	})

	It("undeployFeature when feature is not removed, calls Deploy", func() {
		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.UnDeployWorkloadRoles, controllers.GetWorkloadRoleRefs)

		// The feature is not marked as removed in ClusterSummary Status. In such situation, UndeployFeature calls dep.Deploy.
		// fake deployer Deploy simply adds key to InProgress.
		// So run UndeployFeature then validate key is added to InProgress
		err := controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("cleanup request is queued"))

		key := deployer.GetKey(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), true)
		Expect(dep.IsKeyInProgress(key)).To(BeTrue())
	})

	It("deployFeature return an error if cleaning up roles is in progress", func() {
		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		dep.StoreInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), true)
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureRole, Status: configv1alpha1.FeatureStatusRemoving},
		}

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.DeployWorkloadRoles, controllers.GetWorkloadRoleRefs)

		err := controllers.DeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("cleanup of Role still in progress. Wait before redeploying"))
	})

	It("undeployFeatures returns an error if deploying roles is in progress", func() {
		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Name: workload.Name},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		dep.StoreInProgress(clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummary.Name, string(configv1alpha1.FeatureRole), false)
		clusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{FeatureID: configv1alpha1.FeatureRole, Status: configv1alpha1.FeatureStatusProvisioning},
		}

		reconciler := getClusterSummaryReconciler(c, dep)

		f := controllers.GetFeature(configv1alpha1.FeatureRole,
			controllers.WorkloadRoleHash, controllers.UnDeployWorkloadRoles, controllers.GetWorkloadRoleRefs)

		err := controllers.UndeployFeature(reconciler, context.TODO(), clusterSummaryScope, *f, klogr.New())
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal("deploying Role still in progress. Wait before cleanup"))
	})

	It("updateDeployedGroupVersionKind updates ClusterSummary Status with list of deployed GroupVersionKinds", func() {
		configMapNs := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(addLabelPolicyStr, randomString()))
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(checkSa, randomString()))

		clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMapNs, Name: configMap1.Name},
				{Namespace: configMapNs, Name: configMap2.Name},
			},
		}

		initObjects := []client.Object{
			workload,
			clusterSummary,
			clusterFeature,
			configMap1,
			configMap2,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		dep := fakedeployer.GetClient(context.TODO(), klogr.New(), c)
		reconciler := getClusterSummaryReconciler(c, dep)

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)

		Expect(controllers.UpdateDeployedGroupVersionKind(reconciler, context.TODO(), clusterSummaryScope,
			configv1alpha1.FeatureKyverno, clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs,
			logger)).To(Succeed())

		cs := clusterSummaryScope.ClusterSummary
		Expect(cs.Status.FeatureSummaries).ToNot(BeNil())
		Expect(len(cs.Status.FeatureSummaries)).To(Equal(1))
		Expect(cs.Status.FeatureSummaries[0].FeatureID).To(Equal(configv1alpha1.FeatureKyverno))
		Expect(cs.Status.FeatureSummaries[0].DeployedGroupVersionKind).To(ContainElement("Policy.v1.kyverno.io"))
		Expect(cs.Status.FeatureSummaries[0].DeployedGroupVersionKind).To(ContainElement("ClusterPolicy.v1.kyverno.io"))
	})

	It("getCurrentReferences collects all ClusterSummary referenced objects", func() {
		clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			Replicas: 1,
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: randomString(), Name: randomString()},
			},
		}
		clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration = &configv1alpha1.PrometheusConfiguration{
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: randomString(), Name: randomString()},
				{Namespace: randomString(), Name: randomString()},
			},
		}
		clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs = []corev1.ObjectReference{
			{Namespace: randomString(), Name: randomString()},
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		clusterSummaryScope := getClusterSummaryScope(c, logger, clusterFeature, clusterSummary)
		reconciler := getClusterSummaryReconciler(nil, nil)
		set := controllers.GetCurrentReferences(reconciler, clusterSummaryScope)
		expectedLength := len(clusterSummary.Spec.ClusterFeatureSpec.KyvernoConfiguration.PolicyRefs) +
			len(clusterSummary.Spec.ClusterFeatureSpec.PrometheusConfiguration.PolicyRefs) +
			len(clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoleRefs)
		Expect(controllers.Len(set)).To(Equal(expectedLength))
	})
})

var _ = Describe("Convert result", func() {
	It("convertResultStatus correctly converts deployer.ResultStatus to FeatureStatus", func() {
		reconciler := getClusterSummaryReconciler(nil, nil)

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

func getClusterSummaryScope(c client.Client, logger logr.Logger,
	clusterFeature *configv1alpha1.ClusterFeature, clusterSummary *configv1alpha1.ClusterSummary,
) *scope.ClusterSummaryScope {

	clusterSummaryScope, err := scope.NewClusterSummaryScope(scope.ClusterSummaryScopeParams{
		Client:         c,
		Logger:         logger,
		ClusterFeature: clusterFeature,
		ClusterSummary: clusterSummary,
		ControllerName: "clustersummary",
	})
	Expect(err).To(BeNil())
	return clusterSummaryScope
}
