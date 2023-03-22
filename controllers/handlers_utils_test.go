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
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/utils"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/controllers"
)

const (
	serviceTemplate = `apiVersion: v1
kind: Service
metadata:
  name: service0
  namespace: %s
spec:
  selector:
    app.kubernetes.io/name: service0
  ports:
    - protocol: TCP
      port: 80
      targetPort: 9376
---
apiVersion: v1
kind: Service
metadata:
  name: service1
  namespace: %s
spec:
  selector:
    app.kubernetes.io/name: service1
  ports:
  - name: name-of-service-port
    protocol: TCP
    port: 80
    targetPort: http-web-svc
`

	deplTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
  namespace: %s
spec:
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: nginx
  replicas: 3
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx
        ports:
        - containerPort: 80`
)

var _ = Describe("HandlersUtils", func() {
	var clusterSummary *configv1alpha1.ClusterSummary
	var clusterProfile *configv1alpha1.ClusterProfile
	var namespace string

	BeforeEach(func() {
		namespace = "reconcile" + randomString()

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

		clusterProfile = &configv1alpha1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterProfileSpec{
				ClusterSelector: selector,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterProfile.Name, cluster.Name, false)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: cluster.Namespace,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1alpha1.ClusterTypeCapi,
			},
		}

		prepareForDeployment(clusterProfile, clusterSummary, cluster)

		// Get ClusterSummary so OwnerReference is set
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name}, clusterSummary)).To(Succeed())
	})

	AfterEach(func() {
		deleteResources(namespace, clusterProfile, clusterSummary)
	})

	It("getClusterSummaryAdmin returns the admin for a given ClusterSummary", func() {
		Expect(controllers.GetClusterSummaryAdmin(clusterSummary)).To(BeEmpty())
		admin := randomString()
		clusterSummary.Labels[configv1alpha1.AdminLabel] = admin
		Expect(controllers.GetClusterSummaryAdmin(clusterSummary)).To(Equal(admin))
	})

	It("addClusterSummaryLabel adds label with clusterSummary name", func() {
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}

		controllers.AddLabel(role, controllers.ClusterSummaryLabelName, clusterSummary.Name)
		Expect(role.Labels).ToNot(BeNil())
		Expect(len(role.Labels)).To(Equal(1))
		for k := range role.Labels {
			Expect(role.Labels[k]).To(Equal(clusterSummary.Name))
		}

		role.Labels = map[string]string{"reader": "ok"}
		controllers.AddLabel(role, controllers.ClusterSummaryLabelName, clusterSummary.Name)
		Expect(role.Labels).ToNot(BeNil())
		Expect(len(role.Labels)).To(Equal(2))
		found := false
		for k := range role.Labels {
			if role.Labels[k] == clusterSummary.Name {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue())
	})

	It("createNamespace creates namespace", func() {
		initObjects := []client.Object{}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.CreateNamespace(context.TODO(), c, clusterSummary, namespace)).To(BeNil())

		currentNs := &corev1.Namespace{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
	})

	It("createNamespace does not namespace in DryRun mode", func() {
		initObjects := []client.Object{}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(controllers.CreateNamespace(context.TODO(), c, clusterSummary, namespace)).To(BeNil())

		currentNs := &corev1.Namespace{}
		err := c.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)
		Expect(err).ToNot(BeNil())
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("createNamespace returns no error if namespace already exists", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		initObjects := []client.Object{ns}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		Expect(controllers.CreateNamespace(context.TODO(), c, clusterSummary, namespace)).To(BeNil())

		currentNs := &corev1.Namespace{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
	})

	It("getSecret returns an error when type is different than ClusterProfileSecretType", func() {
		wrongSecretType := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
			Data: map[string][]byte{
				randomString(): []byte(randomString()),
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), wrongSecretType)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, wrongSecretType)).To(Succeed())

		secretName := types.NamespacedName{Namespace: wrongSecretType.Namespace, Name: wrongSecretType.Name}
		_, err := controllers.GetSecret(context.TODO(), testEnv.Client, secretName)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(Equal(libsveltosv1alpha1.ErrSecretTypeNotSupported.Error()))

		services := fmt.Sprintf(serviceTemplate, namespace, namespace)
		depl := fmt.Sprintf(deplTemplate, namespace)

		// Create a secret containing two services.
		secret := createSecretWithPolicy(namespace, randomString(), depl, services)
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())
		Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

		secretName = types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}
		_, err = controllers.GetSecret(context.TODO(), testEnv.Client, secretName)
		Expect(err).To(BeNil())
	})

	It("deployContent in DryRun mode returns policies which will be created, updated, no action", func() {
		services := fmt.Sprintf(serviceTemplate, namespace, namespace)
		depl := fmt.Sprintf(deplTemplate, namespace)

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun

		// Create a secret containing two services.
		secret := createSecretWithPolicy(namespace, randomString(), depl, services)
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(ctx, testEnv.Client, secret)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), secret)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		// Because those services do not exist in the workload cluster yet, both will be reported
		// as created (if the ClusterProfile were to be changed from DryRun, both services would be
		// created)
		resourceReports, err := controllers.DeployContent(context.TODO(),
			testEnv.Config, testEnv.Client, testEnv.Client,
			secret, map[string]string{"service": services}, clusterSummary, klogr.New())
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is Create")
		validateResourceReports(resourceReports, 2, 0, 0, 0)

		// Create services in the workload cluster and have their content exactly match
		// the content contained in the secret referenced by ClusterProfile.
		elements := strings.Split(services, "---")
		for i := range elements {
			var policy *unstructured.Unstructured
			policy, err = utils.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			var policyHash string
			policyHash, err = controllers.ComputePolicyHash(policy)
			Expect(err).To(BeNil())
			controllers.AddLabel(policy, deployer.ReferenceLabelKind, secret.Kind)
			controllers.AddLabel(policy, deployer.ReferenceLabelName, secret.Name)
			controllers.AddLabel(policy, deployer.ReferenceLabelNamespace, secret.Namespace)
			controllers.AddAnnotation(policy, deployer.PolicyHash, policyHash)
			Expect(testEnv.Client.Create(context.TODO(), policy))
			Expect(waitForObject(ctx, testEnv.Client, policy)).To(Succeed())
		}

		// Because services are now existing in the workload cluster and match the content in
		// the secret referenced by ClusterProfile, both obejcts will be reported as no action
		// ( if the ClusterProfile were to be changed from DryRun, nothing would happen).
		resourceReports, err = controllers.DeployContent(context.TODO(),
			testEnv.Config, testEnv.Client, testEnv.Client,
			secret, map[string]string{"service": services}, clusterSummary, klogr.New())
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is NoAction")
		validateResourceReports(resourceReports, 0, 0, 2, 0)

		// Update the secret referenced by ClusterProfile by changing the content of the
		// two services by adding extra label
		newContent := ""
		for i := range elements {
			var policy *unstructured.Unstructured
			policy, err = utils.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			labels := policy.GetLabels()
			if labels == nil {
				labels = make(map[string]string)
			}
			labels[randomString()] = randomString()
			policy.SetLabels(labels)
			var b []byte
			b, err = policy.MarshalJSON()
			Expect(err).To(BeNil())
			newContent += fmt.Sprintf("%s\n---\n", string(b))
		}
		secret = createSecretWithPolicy(namespace, secret.Name, depl, newContent)
		Expect(testEnv.Update(context.TODO(), secret)).To(Succeed())

		// Because objects are now existing in the workload cluster but don't match the content
		// in the secret referenced by ClusterProfile, both services will be reported as updated
		// ( if the ClusterProfile were to be changed from DryRun, both service would be updated).
		resourceReports, err = controllers.DeployContent(context.TODO(),
			testEnv.Config, testEnv.Client, testEnv.Client,
			secret, map[string]string{"service": newContent}, clusterSummary, klogr.New())
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is Update")
		validateResourceReports(resourceReports, 0, 2, 0, 0)

		// Pass a different secret to DeployContent, which means the services are contained in a different Secret
		// and that is the one referenced by ClusterSummary. DeployContent will report conflicts in this case.
		tmpSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: randomString(), Name: randomString()}}
		resourceReports, err = controllers.DeployContent(context.TODO(), testEnv.Config, testEnv.Client, testEnv.Client,
			tmpSecret, map[string]string{"service": services}, clusterSummary, klogr.New())
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is Conflict")
		validateResourceReports(resourceReports, 0, 0, 0, 2)
		for i := range resourceReports {
			rr := &resourceReports[i]
			Expect(rr.Message).To(ContainSubstring(fmt.Sprintf("Object currently deployed because of %s %s/%s.", secret.Kind,
				secret.Namespace, secret.Name)))
		}
	})

	It("getReferenceResourceNamespace returns the referenced resource namespace when set. cluster namespace otherwise.", func() {
		referecedResource := libsveltosv1alpha1.PolicyRef{
			Namespace: "",
			Name:      randomString(),
			Kind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
		}

		clusterNamespace := randomString()
		Expect(controllers.GetReferenceResourceNamespace(clusterNamespace, referecedResource.Namespace)).To(
			Equal(clusterNamespace))

		referecedResource.Namespace = randomString()
		Expect(controllers.GetReferenceResourceNamespace(clusterNamespace, referecedResource.Namespace)).To(
			Equal(referecedResource.Namespace))
	})

	It("deployContentOfSecret deploys all policies contained in a ConfigMap", func() {
		services := fmt.Sprintf(serviceTemplate, namespace, namespace)
		depl := fmt.Sprintf(deplTemplate, namespace)

		secret := createSecretWithPolicy(namespace, randomString(), depl, services)

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(ctx, testEnv.Client, secret)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		resourceReports, err := controllers.DeployContentOfSecret(context.TODO(), testEnv.Config, testEnv.Client, testEnv.Client,
			secret, clusterSummary, klogr.New())
		Expect(err).To(BeNil())
		Expect(len(resourceReports)).To(Equal(3))
	})

	It("deployContentOfConfigMap deploys all policies contained in a Secret", func() {
		services := fmt.Sprintf(serviceTemplate, namespace, namespace)
		depl := fmt.Sprintf(deplTemplate, namespace)

		configMap := createConfigMapWithPolicy(namespace, randomString(), depl, services)

		Expect(testEnv.Client.Create(context.TODO(), configMap)).To(Succeed())

		Expect(waitForObject(ctx, testEnv.Client, configMap)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		resourceReports, err := controllers.DeployContentOfConfigMap(context.TODO(), testEnv.Config, testEnv.Client, testEnv.Client,
			configMap, clusterSummary, klogr.New())
		Expect(err).To(BeNil())
		Expect(len(resourceReports)).To(Equal(3))
	})

	It("undeployStaleResources does not remove resources in dryRun mode", func() {
		// Set ClusterSummary to be DryRun
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1alpha1.SyncModeDryRun
		Expect(testEnv.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Add list of GroupVersionKind this ClusterSummary has deployed in the CAPI Cluster
		// because of the PolicyRefs feature. This is used by UndeployStaleResources.
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			if err != nil {
				return err
			}
			currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
				{
					FeatureID: configv1alpha1.FeatureResources,
					Status:    configv1alpha1.FeatureStatusProvisioned,
					DeployedGroupVersionKind: []string{
						"ClusterRole.v1.rbac.authorization.k8s.io",
					},
				},
			}
			return testEnv.Status().Update(context.TODO(), currentClusterSummary)
		})
		Expect(err).To(BeNil())

		configMapNs := randomString()
		viewClusterRoleName := randomString()
		configMap := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(viewClusterRole, viewClusterRoleName))
		Expect(configMap).ToNot(BeNil())

		// Create ClusterRole policy in the cluster, pretending it was created because of this ConfigMap and because
		// of this ClusterSummary (owner is ClusterProfile owning the ClusterSummary)
		clusterRole, err := utils.GetUnstructured([]byte(fmt.Sprintf(viewClusterRole, viewClusterRoleName)))
		Expect(err).To(BeNil())
		clusterRole.SetLabels(map[string]string{
			deployer.ReferenceLabelKind:      string(libsveltosv1alpha1.ConfigMapReferencedResourceKind),
			deployer.ReferenceLabelName:      configMap.Name,
			deployer.ReferenceLabelNamespace: configMap.Namespace,
		})
		clusterRole.SetOwnerReferences([]metav1.OwnerReference{
			{Kind: configv1alpha1.ClusterProfileKind, Name: clusterProfile.Name,
				UID: clusterProfile.UID, APIVersion: "config.projectsveltos.io/v1beta1"},
		})
		Expect(testEnv.Create(context.TODO(), clusterRole)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, clusterRole)).To(Succeed())

		deployedGKVs := controllers.GetDeployedGroupVersionKinds(currentClusterSummary, configv1alpha1.FeatureResources)
		Expect(deployedGKVs).ToNot(BeEmpty())

		// Because ClusterSummary is not referencing any ConfigMap/Resource and because test created a ClusterRole
		// pretending it was created by this ClusterSummary instance, UndeployStaleResources will remove no instance as
		// syncMode is dryRun and will report one instance (ClusterRole created above) would be undeployed
		undeploy, err := controllers.UndeployStaleResources(context.TODO(), testEnv.Config, testEnv.Client, testEnv.Client,
			currentClusterSummary, deployedGKVs, nil, klogr.New())
		Expect(err).To(BeNil())
		Expect(len(undeploy)).To(Equal(1))

		// Verify clusterRole is still present
		currentClusterRole := &rbacv1.ClusterRole{}
		Expect(testEnv.Get(context.TODO(), types.NamespacedName{Name: clusterRole.GetName()}, currentClusterRole)).To(BeNil())
	})

	It(`undeployStaleResources removes all policies created by ClusterSummary due to ConfigMaps not referenced anymore`, func() {
		configMapNs := randomString()
		viewClusterRoleName := randomString()
		configMap1 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(viewClusterRole, viewClusterRoleName))
		editClusterRoleName := randomString()
		configMap2 := createConfigMapWithPolicy(configMapNs, randomString(), fmt.Sprintf(editClusterRole, editClusterRoleName))

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []libsveltosv1alpha1.PolicyRef{
			{Namespace: configMapNs, Name: configMap1.Name, Kind: string(libsveltosv1alpha1.ConfigMapReferencedResourceKind)},
			{Namespace: configMapNs, Name: configMap2.Name, Kind: string(libsveltosv1alpha1.ConfigMapReferencedResourceKind)},
		}
		Expect(testEnv.Update(context.TODO(), currentClusterSummary)).To(Succeed())

		// Wait for cache to be updated
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
				currentClusterSummary)
			return err == nil &&
				currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(addTypeInformationToObject(testEnv.Scheme(), currentClusterSummary)).To(Succeed())

		clusterRoleName1 := viewClusterRoleName
		clusterRole1 := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterRoleName1,
				Labels: map[string]string{
					deployer.ReferenceLabelKind:      configMap1.Kind,
					deployer.ReferenceLabelNamespace: configMap1.Namespace,
					deployer.ReferenceLabelName:      configMap1.Name,
				},
			},
		}

		clusterRoleName2 := editClusterRoleName
		clusterRole2 := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterRoleName2,
				Namespace: "default",
				Labels: map[string]string{
					deployer.ReferenceLabelKind:      configMap2.Kind,
					deployer.ReferenceLabelNamespace: configMap2.Namespace,
					deployer.ReferenceLabelName:      configMap2.Name,
				},
			},
		}

		// Add list of GroupVersionKind this ClusterSummary has deployed in the CAPI Cluster
		// because of the PolicyRefs feature. This is used by UndeployStaleResources.
		currentClusterSummary.Status.FeatureSummaries = []configv1alpha1.FeatureSummary{
			{
				FeatureID: configv1alpha1.FeatureResources,
				Status:    configv1alpha1.FeatureStatusProvisioned,
				DeployedGroupVersionKind: []string{
					"ClusterRole.v1.rbac.authorization.k8s.io",
				},
			},
		}

		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole1)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole2)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, clusterRole2)).To(Succeed())

		currentClusterProfile := &configv1alpha1.ClusterProfile{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())

		addOwnerReference(context.TODO(), testEnv.Client, clusterRole1, currentClusterProfile)
		addOwnerReference(context.TODO(), testEnv.Client, clusterRole2, currentClusterProfile)

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterRole1)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterRole2)).To(Succeed())

		currentClusterRoles := map[string]configv1alpha1.Resource{}
		clusterRoleResource1 := &configv1alpha1.Resource{
			Name:  clusterRole1.Name,
			Kind:  clusterRole1.GroupVersionKind().Kind,
			Group: clusterRole1.GetObjectKind().GroupVersionKind().Group,
		}
		currentClusterRoles[controllers.GetPolicyInfo(clusterRoleResource1)] = *clusterRoleResource1
		clusterRoleResource2 := &configv1alpha1.Resource{
			Name:  clusterRole2.Name,
			Kind:  clusterRole2.GroupVersionKind().Kind,
			Group: clusterRole2.GetObjectKind().GroupVersionKind().Group,
		}
		currentClusterRoles[controllers.GetPolicyInfo(clusterRoleResource2)] = *clusterRoleResource2

		deployedGKVs := controllers.GetDeployedGroupVersionKinds(currentClusterSummary, configv1alpha1.FeatureResources)
		Expect(deployedGKVs).ToNot(BeEmpty())
		// undeployStaleResources finds all instances of policies deployed because of clusterSummary and
		// removes the stale ones.
		_, err := controllers.UndeployStaleResources(context.TODO(), testEnv.Config, testEnv.Client, testEnv.Client,
			currentClusterSummary, deployedGKVs, currentClusterRoles, klogr.New())
		Expect(err).To(BeNil())

		// Consistently loop so testEnv Cache is synced
		Consistently(func() error {
			// Since ClusterSummary is referencing configMap, expect ClusterRole to not be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName1}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		// Consistently loop so testEnv Cache is synced
		Consistently(func() error {
			// Since ClusterSummary is referencing configMap, expect Policy to not be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			return testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName2}, currentClusterRole)
		}, timeout, pollingInterval).Should(BeNil())

		currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs = nil
		delete(currentClusterRoles, controllers.GetPolicyInfo(clusterRoleResource1))
		delete(currentClusterRoles, controllers.GetPolicyInfo(clusterRoleResource2))

		_, err = controllers.UndeployStaleResources(context.TODO(), testEnv.Config, testEnv.Client, testEnv.Client,
			currentClusterSummary, deployedGKVs, currentClusterRoles, klogr.New())
		Expect(err).To(BeNil())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			// Since ClusterSummary is not referencing configMap with ClusterRole, expect ClusterRole to be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName1}, currentClusterRole)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		// Eventual loop so testEnv Cache is synced
		Eventually(func() bool {
			// Since ClusterSummary is not referencing configMap with ClusterRole, expect ClusterRole to be deleted
			currentClusterRole := &rbacv1.ClusterRole{}
			err = testEnv.Get(context.TODO(),
				types.NamespacedName{Name: clusterRoleName2}, currentClusterRole)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("canDelete returns false when ClusterProfile is not referencing the policies anymore", func() {
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
			},
		}
		Expect(addTypeInformationToObject(scheme, depl)).To(Succeed())

		Expect(controllers.CanDelete(depl, map[string]configv1alpha1.Resource{})).To(BeTrue())

		name := controllers.GetPolicyInfo(&configv1alpha1.Resource{
			Kind:      depl.GetObjectKind().GroupVersionKind().Kind,
			Group:     depl.GetObjectKind().GroupVersionKind().Group,
			Name:      depl.GetName(),
			Namespace: depl.GetNamespace(),
		})
		Expect(controllers.CanDelete(depl, map[string]configv1alpha1.Resource{name: {}})).To(BeFalse())
	})

	It("handleResourceDelete leaves policies on Cluster when mode is LeavePolicies", func() {
		randomKey := randomString()
		randomValue := randomString()
		depl := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: randomString(),
				Name:      randomString(),
				Labels: map[string]string{
					deployer.ReferenceLabelKind:      randomString(),
					deployer.ReferenceLabelName:      randomString(),
					deployer.ReferenceLabelNamespace: randomString(),
					randomKey:                        randomValue,
				},
			},
		}
		Expect(addTypeInformationToObject(scheme, depl)).To(Succeed())
		controllerutil.AddFinalizer(clusterSummary, configv1alpha1.ClusterSummaryFinalizer)
		clusterSummary.Spec.ClusterProfileSpec.StopMatchingBehavior = configv1alpha1.LeavePolicies
		initObjects := []client.Object{depl, clusterSummary}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())

		Expect(c.Delete(context.TODO(), currentClusterSummary)).To(Succeed())

		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())

		Expect(controllers.HandleResourceDelete(ctx, c, depl, currentClusterSummary, klogr.New())).To(Succeed())

		currentDepl := &appsv1.Deployment{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Namespace: depl.Namespace, Name: depl.Name}, currentDepl)).To(Succeed())
		Expect(len(currentDepl.Labels)).To(Equal(1))
		v, ok := currentDepl.Labels[randomKey]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(randomValue))
	})
})

// validateResourceReports validates that number of resourceResources with certain actions
// match the expected number per action
func validateResourceReports(resourceReports []configv1alpha1.ResourceReport,
	created, updated, noAction, conflict int) {

	var foundCreated, foundUpdated, foundNoAction, foundConflict int
	for i := range resourceReports {
		rr := &resourceReports[i]
		if rr.Action == string(configv1alpha1.CreateResourceAction) {
			foundCreated++
		} else if rr.Action == string(configv1alpha1.UpdateResourceAction) {
			foundUpdated++
		} else if rr.Action == string(configv1alpha1.NoResourceAction) {
			foundNoAction++
		} else if rr.Action == string(configv1alpha1.ConflictResourceAction) {
			foundConflict++
		}
	}

	Expect(foundCreated).To(Equal(created))
	Expect(foundUpdated).To(Equal(updated))
	Expect(foundNoAction).To(Equal(noAction))
	Expect(foundConflict).To(Equal(conflict))
}
