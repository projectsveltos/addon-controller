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
	"os"
	"path/filepath"
	"reflect"
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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/k8s_utils"
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

	serviceAccountTemplate = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: service0
  namespace: %s
  labels:
    %s: %s`

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

	deplTemplateWithStatus = `apiVersion: apps/v1
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
        - containerPort: 80
status:
  replicas: %d
  unavailableReplicas: %d
  readyReplicas: %d
  availableReplicas: %d`
)

var _ = Describe("HandlersUtils", func() {
	var clusterSummary *configv1beta1.ClusterSummary
	var clusterProfile *configv1beta1.ClusterProfile
	var namespace string

	BeforeEach(func() {
		namespace = randomString()

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					randomString(): randomString(),
				},
			},
		}

		clusterProfile = &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterProfileNamePrefix + randomString(),
			},
			Spec: configv1beta1.Spec{
				ClusterSelector: libsveltosv1beta1.Selector{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							randomString(): randomString(),
						},
					},
				},
			},
		}

		clusterSummaryName := clusterops.GetClusterSummaryName(configv1beta1.ClusterProfileKind,
			clusterProfile.Name, cluster.Name, false)
		clusterSummary = &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterSummaryName,
				Namespace: cluster.Namespace,
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
				ClusterType:      libsveltosv1beta1.ClusterTypeCapi,
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
		adminName := randomString()
		adminNamespace := randomString()
		clusterSummary.Labels[libsveltosv1beta1.ServiceAccountNameLabel] = adminName
		clusterSummary.Labels[libsveltosv1beta1.ServiceAccountNamespaceLabel] = adminNamespace
		saNamespace, saName := controllers.GetClusterSummaryAdmin(clusterSummary)
		Expect(saNamespace).To(Equal(adminNamespace))
		Expect(saName).To(Equal(adminName))
	})

	It("addClusterSummaryLabel adds label with clusterSummary name", func() {
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randomString(),
			},
		}

		deployer.AddLabel(role, clusterops.ClusterSummaryLabelName, clusterSummary.Name)
		Expect(role.Labels).ToNot(BeNil())
		Expect(len(role.Labels)).To(Equal(1))
		for k := range role.Labels {
			Expect(role.Labels[k]).To(Equal(clusterSummary.Name))
		}

		role.Labels = map[string]string{"reader": "ok"}
		deployer.AddLabel(role, clusterops.ClusterSummaryLabelName, clusterSummary.Name)
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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		isDryRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		Expect(deployer.CreateNamespace(context.TODO(), c, isDryRun, namespace)).To(BeNil())

		currentNs := &corev1.Namespace{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
	})

	It("createNamespace does not namespace in DryRun mode", func() {
		initObjects := []client.Object{}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		isDryRun := true
		Expect(deployer.CreateNamespace(context.TODO(), c, isDryRun, namespace)).To(BeNil())

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

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(initObjects...).WithObjects(initObjects...).Build()

		Expect(deployer.CreateNamespace(context.TODO(), c, false, namespace)).To(BeNil())

		currentNs := &corev1.Namespace{}
		Expect(c.Get(context.TODO(), types.NamespacedName{Name: namespace}, currentNs)).To(Succeed())
	})

	It("updateResource does not reset paths in DriftExclusions", func() {
		depl := fmt.Sprintf(deplTemplate, namespace)
		u, err := k8s_utils.GetUnstructured([]byte(depl))
		Expect(err).To(BeNil())

		dr, err := k8s_utils.GetDynamicResourceInterface(testEnv.Config, u.GroupVersionKind(), u.GetNamespace())
		Expect(err).To(BeNil())

		isDryRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		isDriftDetection := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection
		// following will successfully create deployment
		_, err = deployer.UpdateResource(context.TODO(), dr, isDriftDetection, isDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			u, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		currentDeployment := &appsv1.Deployment{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentDeployment)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		clusterSummary.Spec.ClusterProfileSpec.DriftExclusions = []libsveltosv1beta1.DriftExclusion{
			{
				Target: &libsveltosv1beta1.PatchSelector{
					Kind:    "Deployment",
					Group:   "apps",
					Version: "v1",
				},
				Paths: []string{"/spec/replicas"},
			},
		}

		// Update deployment.spec.replicas
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
			currentDeployment)).To(Succeed())
		newReplicas := int32(5)
		currentDeployment.Spec.Replicas = &newReplicas
		Expect(testEnv.Update(context.TODO(), currentDeployment)).To(Succeed())

		// Wait cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentDeployment)
			return err == nil &&
				*currentDeployment.Spec.Replicas == newReplicas
		}, timeout, pollingInterval).Should(BeTrue())

		isDryRun = clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		isDriftDetection = clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection
		// New deploy will not override replicas
		_, err = deployer.UpdateResource(context.TODO(), dr, isDriftDetection, isDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			u, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		Consistently(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentDeployment)
			return err == nil &&
				*currentDeployment.Spec.Replicas == newReplicas
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("updateResource and DriftExclusions with non matching targets", func() {
		key := randomString()
		value := randomString()
		serviceAccount := fmt.Sprintf(serviceAccountTemplate, namespace, key, value)
		u, err := k8s_utils.GetUnstructured([]byte(serviceAccount))
		Expect(err).To(BeNil())

		dr, err := k8s_utils.GetDynamicResourceInterface(testEnv.Config, u.GroupVersionKind(), u.GetNamespace())
		Expect(err).To(BeNil())

		isDryRun := false
		isDriftDetection := true
		_, err = deployer.UpdateResource(context.TODO(), dr, isDriftDetection, isDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			u, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		currentServiceAccount := &corev1.ServiceAccount{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentServiceAccount)
			return err == nil && currentServiceAccount.Labels != nil &&
				currentServiceAccount.Labels[key] == value
		}, timeout, pollingInterval).Should(BeTrue())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		clusterSummary.Spec.ClusterProfileSpec.DriftExclusions = []libsveltosv1beta1.DriftExclusion{
			{
				Target: &libsveltosv1beta1.PatchSelector{
					Kind:    "Deployment",
					Group:   "apps",
					Version: "v1",
				},
				Paths: []string{"/spec/replicas"},
			},
		}

		// Update deployment.spec.replicas
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
			currentServiceAccount)).To(Succeed())
		currentServiceAccount.Labels = map[string]string{
			randomString(): randomString(),
			randomString(): randomString(),
		}
		Expect(testEnv.Update(context.TODO(), currentServiceAccount)).To(Succeed())

		// Wait cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentServiceAccount)
			return err == nil && len(currentServiceAccount.Labels) == 2
		}, timeout, pollingInterval).Should(BeTrue())

		// New deploy will not override replicas
		_, err = deployer.UpdateResource(context.TODO(), dr, isDriftDetection, isDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			u, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		Consistently(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentServiceAccount)
			return err == nil && currentServiceAccount.Labels != nil &&
				currentServiceAccount.Labels[key] == value
		}, timeout, pollingInterval).Should(BeTrue())
	})

	It("updateResource: subresources and driftExclusions", func() {
		depl := fmt.Sprintf(deplTemplate, namespace)
		u, err := k8s_utils.GetUnstructured([]byte(depl))
		Expect(err).To(BeNil())

		dr, err := k8s_utils.GetDynamicResourceInterface(testEnv.Config, u.GroupVersionKind(), u.GetNamespace())
		Expect(err).To(BeNil())

		isDryRun := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		isDriftDetection := clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection
		// following will successfully create deployment
		_, err = deployer.UpdateResource(context.TODO(), dr, isDriftDetection, isDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			u, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		currentDeployment := &appsv1.Deployment{}
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentDeployment)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeContinuousWithDriftDetection
		clusterSummary.Spec.ClusterProfileSpec.DriftExclusions = []libsveltosv1beta1.DriftExclusion{
			{
				Target: &libsveltosv1beta1.PatchSelector{
					Kind:    "Deployment",
					Group:   "apps",
					Version: "v1",
				},
				Paths: []string{"/spec/replicas"},
			},
		}

		// Update deployment.spec.replicas
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
			currentDeployment)).To(Succeed())
		newReplicas := int32(5)
		currentDeployment.Spec.Replicas = &newReplicas
		Expect(testEnv.Update(context.TODO(), currentDeployment)).To(Succeed())

		// Wait cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentDeployment)
			return err == nil &&
				*currentDeployment.Spec.Replicas == newReplicas
		}, timeout, pollingInterval).Should(BeTrue())

		readyReplicas := 3
		availableReplicas := 3
		unavailableReplicas := 2
		depl = fmt.Sprintf(deplTemplateWithStatus, namespace, newReplicas,
			unavailableReplicas, readyReplicas, availableReplicas)
		u, err = k8s_utils.GetUnstructured([]byte(depl))
		Expect(err).To(BeNil())

		isDryRun = clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeDryRun
		isDriftDetection = clusterSummary.Spec.ClusterProfileSpec.SyncMode == configv1beta1.SyncModeContinuousWithDriftDetection
		// New deploy will not override replicas
		_, err = deployer.UpdateResource(context.TODO(), dr, isDriftDetection, isDryRun, clusterSummary.Spec.ClusterProfileSpec.DriftExclusions,
			u, []string{"status"}, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		Consistently(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()},
				currentDeployment)
			return err == nil &&
				*currentDeployment.Spec.Replicas == newReplicas &&
				currentDeployment.Status.AvailableReplicas == int32(availableReplicas) &&
				currentDeployment.Status.UnavailableReplicas == int32(unavailableReplicas)
		}, timeout, pollingInterval).Should(BeTrue())
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
		Expect(err.Error()).To(Equal(libsveltosv1beta1.ErrSecretTypeNotSupported.Error()))

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

		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeDryRun

		// Create a secret containing two services.
		secret := createSecretWithPolicy(namespace, randomString(), depl, services)
		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(ctx, testEnv.Client, secret)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), secret)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		// Because those services do not exist in the workload cluster yet, both will be reported
		// as created (if the ClusterProfile were to be changed from DryRun, both services would be
		// created)
		resourceReports, err := controllers.DeployContent(context.TODO(), false,
			testEnv.Config, testEnv.Client,
			secret, map[string]string{"service": services}, clusterSummary, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is Create")
		validateResourceReports(resourceReports, 2, 0, 0, 0)

		// Create services in the workload cluster and have their content exactly match
		// the content contained in the secret referenced by ClusterProfile.
		elements := strings.Split(services, "---")
		for i := range elements {
			var policy *unstructured.Unstructured
			policy, err = k8s_utils.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			var policyHash string
			policyHash, err = deployer.ComputePolicyHash(policy)
			Expect(err).To(BeNil())
			deployer.AddLabel(policy, deployer.ReferenceKindLabel, secret.Kind)
			deployer.AddLabel(policy, deployer.ReferenceNameLabel, secret.Name)
			deployer.AddLabel(policy, deployer.ReferenceNamespaceLabel, secret.Namespace)
			deployer.AddAnnotation(policy, deployer.PolicyHash, policyHash)
			deployer.AddAnnotation(policy, deployer.OwnerTier, "100")
			Expect(testEnv.Client.Create(context.TODO(), policy))
			Expect(waitForObject(ctx, testEnv.Client, policy)).To(Succeed())
		}

		// Because services are now existing in the workload cluster and match the content in
		// the secret referenced by ClusterProfile, both obejcts will be reported as no action
		// ( if the ClusterProfile were to be changed from DryRun, nothing would happen).
		resourceReports, err = controllers.DeployContent(context.TODO(), false,
			testEnv.Config, testEnv.Client,
			secret, map[string]string{"service": services}, clusterSummary, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is NoAction")
		validateResourceReports(resourceReports, 0, 0, 2, 0)

		// Update the secret referenced by ClusterProfile by changing the content of the
		// two services by adding extra label
		newContent := ""
		for i := range elements {
			var policy *unstructured.Unstructured
			policy, err = k8s_utils.GetUnstructured([]byte(elements[i]))
			Expect(err).To(BeNil())
			Expect(addTypeInformationToObject(scheme, policy)).To(Succeed())
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

		newValue := []string{depl}
		splitValue, err := deployer.CustomSplit(newContent)
		Expect(err).To(BeNil())
		newValue = append(newValue, splitValue...)
		secret = createSecretWithPolicy(namespace, secret.Name, newValue...)
		Expect(testEnv.Update(context.TODO(), secret)).To(Succeed())

		// Because objects are now existing in the workload cluster but don't match the content
		// in the secret referenced by ClusterProfile, both services will be reported as updated
		// (if the ClusterProfile were to be changed from DryRun, both service would be updated).
		resourceReports, err = controllers.DeployContent(context.TODO(), false,
			testEnv.Config, testEnv.Client,
			secret, map[string]string{"service": newContent}, clusterSummary, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is Update")
		validateResourceReports(resourceReports, 0, 2, 0, 0)

		// Pass a different secret to DeployContent, which means the services are contained in a different Secret
		// and that is the one referenced by ClusterSummary. DeployContent will report conflicts in this case.
		tmpSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: randomString(), Name: randomString()}}
		resourceReports, err = controllers.DeployContent(context.TODO(), false,
			testEnv.Config, testEnv.Client, tmpSecret, map[string]string{"service": services},
			clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		By("Validating action for all resourceReports is Conflict")
		validateResourceReports(resourceReports, 0, 0, 0, 2)
		for i := range resourceReports {
			rr := &resourceReports[i]
			Expect(rr.Message).To(ContainSubstring(fmt.Sprintf("Object Service:%s/service%d currently deployed because of %s %s/%s.",
				namespace, i, secret.Kind, secret.Namespace, secret.Name)))
		}
	})

	It("deployContentOfSecret deploys all policies contained in a ConfigMap", func() {
		services := fmt.Sprintf(serviceTemplate, namespace, namespace)
		depl := fmt.Sprintf(deplTemplate, namespace)

		secret := createSecretWithPolicy(namespace, randomString(), depl, services)

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(ctx, testEnv.Client, secret)).To(Succeed())

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		resourceReports, err := controllers.DeployContentOfSecret(context.TODO(), false,
			testEnv.Config, testEnv.Client, secret, clusterSummary, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
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

		resourceReports, err := controllers.DeployContentOfConfigMap(context.TODO(), false,
			testEnv.Config, testEnv.Client, configMap, clusterSummary, nil,
			textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(len(resourceReports)).To(Equal(3))
	})

	It("undeployStaleResources does not remove resources in dryRun mode", func() {
		// Set ClusterSummary to be DryRun
		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeDryRun
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
			currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
				{
					FeatureID: libsveltosv1beta1.FeatureResources,
					Status:    libsveltosv1beta1.FeatureStatusProvisioned,
				},
			}
			currentClusterSummary.Status.DeployedGVKs = []libsveltosv1beta1.FeatureDeploymentInfo{
				{
					FeatureID: libsveltosv1beta1.FeatureResources,
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
		clusterRole, err := k8s_utils.GetUnstructured([]byte(fmt.Sprintf(viewClusterRole, viewClusterRoleName)))
		Expect(err).To(BeNil())
		clusterRole.SetLabels(map[string]string{
			deployer.ReferenceKindLabel:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			deployer.ReferenceNameLabel:      configMap.Name,
			deployer.ReferenceNamespaceLabel: configMap.Namespace,
			deployer.ReasonLabel:             string(libsveltosv1beta1.FeatureResources),
		})
		clusterRole.SetOwnerReferences([]metav1.OwnerReference{
			{Kind: configv1beta1.ClusterProfileKind, Name: clusterProfile.Name,
				UID: clusterProfile.UID, APIVersion: "config.projectsveltos.io/v1beta1"},
		})
		Expect(testEnv.Create(context.TODO(), clusterRole)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, clusterRole)).To(Succeed())

		deployedGKVs := controllers.GetDeployedGroupVersionKinds(currentClusterSummary, libsveltosv1beta1.FeatureResources)
		Expect(deployedGKVs).ToNot(BeEmpty())

		// Because ClusterSummary is not referencing any ConfigMap/Resource and because test created a ClusterRole
		// pretending it was created by this ClusterSummary instance, UndeployStaleResources will remove no instance as
		// syncMode is dryRun and will report one instance (ClusterRole created above) would be undeployed
		undeploy, err := controllers.UndeployStaleResources(context.TODO(), false, testEnv.Config, testEnv.Client,
			libsveltosv1beta1.FeatureResources, currentClusterSummary, deployedGKVs, nil, textlogger.NewLogger(textlogger.NewConfig()))
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

		currentClusterSummary := &configv1beta1.ClusterSummary{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Namespace: clusterSummary.Namespace, Name: clusterSummary.Name},
			currentClusterSummary)).To(Succeed())
		currentClusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{
				Namespace: configMapNs, Name: configMap1.Name,
				Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
			{
				Namespace: configMapNs, Name: configMap2.Name,
				Kind: string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
			},
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
					deployer.ReferenceKindLabel:      configMap1.Kind,
					deployer.ReferenceNamespaceLabel: configMap1.Namespace,
					deployer.ReferenceNameLabel:      configMap1.Name,
					deployer.ReasonLabel:             string(libsveltosv1beta1.FeatureResources),
				},
			},
		}

		clusterRoleName2 := editClusterRoleName
		clusterRole2 := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterRoleName2,
				Namespace: "default",
				Labels: map[string]string{
					deployer.ReferenceKindLabel:      configMap2.Kind,
					deployer.ReferenceNamespaceLabel: configMap2.Namespace,
					deployer.ReferenceNameLabel:      configMap2.Name,
					deployer.ReasonLabel:             string(libsveltosv1beta1.FeatureResources),
				},
			},
		}

		// Add list of GroupVersionKind this ClusterSummary has deployed in the CAPI Cluster
		// because of the PolicyRefs feature. This is used by UndeployStaleResources.
		currentClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				Status:    libsveltosv1beta1.FeatureStatusProvisioned,
			},
		}
		currentClusterSummary.Status.DeployedGVKs = []libsveltosv1beta1.FeatureDeploymentInfo{
			{
				FeatureID: libsveltosv1beta1.FeatureResources,
				DeployedGroupVersionKind: []string{
					"ClusterRole.v1.rbac.authorization.k8s.io",
				},
			},
		}

		Expect(testEnv.Client.Status().Update(context.TODO(), currentClusterSummary)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole1)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterRole2)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, clusterRole2)).To(Succeed())

		currentClusterProfile := &configv1beta1.ClusterProfile{}
		Expect(testEnv.Get(context.TODO(),
			types.NamespacedName{Name: clusterProfile.Name},
			currentClusterProfile)).To(Succeed())

		addOwnerReference(context.TODO(), testEnv.Client, clusterRole1, currentClusterProfile)
		addOwnerReference(context.TODO(), testEnv.Client, clusterRole2, currentClusterProfile)

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterRole1)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterRole2)).To(Succeed())

		currentClusterRoles := map[string]libsveltosv1beta1.Resource{}
		clusterRoleResource1 := &libsveltosv1beta1.Resource{
			Name:  clusterRole1.Name,
			Kind:  clusterRole1.GroupVersionKind().Kind,
			Group: clusterRole1.GetObjectKind().GroupVersionKind().Group,
		}
		currentClusterRoles[deployer.GetPolicyInfo(clusterRoleResource1)] = *clusterRoleResource1
		clusterRoleResource2 := &libsveltosv1beta1.Resource{
			Name:  clusterRole2.Name,
			Kind:  clusterRole2.GroupVersionKind().Kind,
			Group: clusterRole2.GetObjectKind().GroupVersionKind().Group,
		}
		currentClusterRoles[deployer.GetPolicyInfo(clusterRoleResource2)] = *clusterRoleResource2

		deployedGKVs := controllers.GetDeployedGroupVersionKinds(currentClusterSummary, libsveltosv1beta1.FeatureResources)
		Expect(deployedGKVs).ToNot(BeEmpty())
		// undeployStaleResources finds all instances of policies deployed because of clusterSummary and
		// removes the stale ones.
		_, err := controllers.UndeployStaleResources(context.TODO(), false, testEnv.Config, testEnv.Client,
			libsveltosv1beta1.FeatureResources, currentClusterSummary, deployedGKVs, currentClusterRoles,
			textlogger.NewLogger(textlogger.NewConfig()))
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
		delete(currentClusterRoles, deployer.GetPolicyInfo(clusterRoleResource1))
		delete(currentClusterRoles, deployer.GetPolicyInfo(clusterRoleResource2))

		_, err = controllers.UndeployStaleResources(context.TODO(), false, testEnv.Config, testEnv.Client,
			libsveltosv1beta1.FeatureResources, currentClusterSummary, deployedGKVs, currentClusterRoles,
			textlogger.NewLogger(textlogger.NewConfig()))
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

	It("addExtraLabels adds extra labels on unstructured", func() {
		u := &unstructured.Unstructured{}
		extraLabels := map[string]string{
			randomString(): randomString(),
			randomString(): randomString(),
		}

		controllers.AddExtraLabels(u, extraLabels)
		labels := u.GetLabels()
		Expect(labels).ToNot(BeNil())
		for k := range extraLabels {
			Expect(labels[k]).To(Equal(extraLabels[k]))
		}

		// Add extra labels again
		extraLabels = map[string]string{
			randomString(): randomString(),
			randomString(): randomString(),
		}

		controllers.AddExtraLabels(u, extraLabels)
		labels = u.GetLabels()
		Expect(labels).ToNot(BeNil())
		for k := range extraLabels {
			Expect(labels[k]).To(Equal(extraLabels[k]))
		}
	})

	It("addExtraAnnotations adds extra annotations on unstructured", func() {
		u := &unstructured.Unstructured{}
		extraAnnotations := map[string]string{
			randomString(): randomString(),
			randomString(): randomString(),
		}

		controllers.AddExtraAnnotations(u, extraAnnotations)
		annotations := u.GetAnnotations()
		Expect(annotations).ToNot(BeNil())
		for k := range extraAnnotations {
			Expect(annotations[k]).To(Equal(extraAnnotations[k]))
		}

		// Add extra annotations again
		extraAnnotations = map[string]string{
			randomString(): randomString(),
			randomString(): randomString(),
		}

		controllers.AddExtraAnnotations(u, extraAnnotations)
		annotations = u.GetAnnotations()
		Expect(annotations).ToNot(BeNil())
		for k := range extraAnnotations {
			Expect(annotations[k]).To(Equal(extraAnnotations[k]))
		}
	})

	It("adjustNamespace adjusts namespace for both namespaced and cluster wide resources", func() {
		deployment := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test`

		u, err := k8s_utils.GetUnstructured([]byte(deployment))
		Expect(err).To(BeNil())

		Expect(controllers.AdjustNamespace(u, testEnv.Config)).To(BeNil())
		// For namespaced resources if namespace is not set, namespace gets set to default
		Expect(u.GetNamespace()).To(Equal("default"))

		clusterIssuer := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: view
  namespace: cert-manager`

		u, err = k8s_utils.GetUnstructured([]byte(clusterIssuer))
		Expect(err).To(BeNil())

		Expect(controllers.AdjustNamespace(u, testEnv.Config)).To(BeNil())
		// For cluster wide resources if namespace is set, namespace gets reset
		Expect(u.GetNamespace()).To(Equal(""))
	})

	It("readFiles loads content of all files in a directory", func() {
		dir, err := os.MkdirTemp("", "my-temp-dir")
		Expect(err).To(BeNil())
		defer os.RemoveAll(dir)

		const permission0600 = 0600
		err = os.WriteFile(filepath.Join(dir, "file1.txt"), []byte(serviceTemplate), permission0600)
		Expect(err).To(BeNil())

		// Create a subdirectory
		const subdir = "subdir"
		err = os.MkdirAll(filepath.Join(dir, subdir), 0755)
		Expect(err).To(BeNil())

		err = os.WriteFile(filepath.Join(dir, subdir, "file2.txt"), []byte(deplTemplate), permission0600)
		Expect(err).To(BeNil())

		result, err := controllers.ReadFiles(dir)
		Expect(err).To(BeNil())

		Expect(result).ToNot(BeEmpty())

		v, ok := result["file1.txt"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(serviceTemplate))

		v, ok = result["file2.txt"]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(deplTemplate))
	})

	It("collectContent collect contents with no error even when there are section with just comments", func() {
		content := `# This file is generated from the individual YAML files by generate-provisioner-deployment.sh. Do not
# edit this file directly but instead edit the source files and re-render.
#
# Generated from:
#       examples/contour/01-crds.yaml
#       examples/gateway/00-crds.yaml
#       examples/gateway/00-namespace.yaml
#       examples/gateway/01-admission_webhook.yaml
#       examples/gateway/02-certificate_config.yaml
#       examples/gateway-provisioner/00-common.yaml
#       examples/gateway-provisioner/01-roles.yaml
#       examples/gateway-provisioner/02-rolebindings.yaml
#       examples/gateway-provisioner/03-gateway-provisioner.yaml

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: contour-gateway-provisioner
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: contour-gateway-provisioner
subjects:
- kind: ServiceAccount
  name: contour-gateway-provisioner
  namespace: projectcontour
`
		data := map[string]string{"policy.yaml": content}
		u, err := controllers.CollectContent(context.TODO(), clusterSummary, nil, data, false,
			false, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(len(u)).To(Equal(1))
		Expect(u[0].GetName()).To(Equal("contour-gateway-provisioner"))
	})

	It("collectContent collect contents with no error even when there are section with multiple resources", func() {
		service := `apiVersion: v1
kind: Service
metadata:
  name: sample-app
  namespace: staging
  labels:
    environment: staging
spec:
  selector:
    app: sample-app
  ports:
  - protocol: TCP
    port: 80
    targetPort: 8080`

		deployment := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: sample-app
  namespace: staging
  labels:
    environment: staging
spec:
  replicas: 1
  selector:
    matchLabels:
      environment: staging
  template:
    metadata:
      labels:
        environment: staging
    spec:
      containers:
      - name: sample-app
        image: nginx:latest
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 8080`

		//nolint: gosec // this is a kubernetes secret in a test
		secret := `apiVersion: v1
kind: Secret
metadata:
  name: application-settings
  namespace: staging
stringData:
  app_mode: staging
  certificates: /etc/ssl/staging
  db_user: staging-user
  db_password: staging-password`

		policies := []string{service, deployment, secret}
		configMap := createConfigMapWithPolicy(randomString(), randomString(), policies...)
		u, err := controllers.CollectContent(context.TODO(), clusterSummary, nil, configMap.Data, false,
			false, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())
		Expect(len(u)).To(Equal(3))
	})

	It("patchRessource with subresources correctly update instance", func() {
		serviceName := randomString()
		key := randomString()
		value := randomString()
		servicePatch := `apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: default
  labels:
    %s: %s
spec:
  selector:
    %s: %s
status:
  loadBalancer:
    ingress:
    - ip: 1.1.1.1`

		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceName,
				Namespace: "default",
			},
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeLoadBalancer,
				Selector: map[string]string{
					"app.kubernetes.io/name": "service0",
				},
				Ports: []corev1.ServicePort{
					{
						Protocol:   "TCP",
						Port:       80,
						TargetPort: intstr.FromInt(1234),
					},
				},
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), service)).To(Succeed())
		Expect(waitForObject(ctx, testEnv.Client, service)).To(Succeed())
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		configMap := createConfigMapWithPolicy(namespace, randomString(), fmt.Sprintf(servicePatch,
			serviceName, key, value, key, value))
		configMap.Annotations = map[string]string{
			"projectsveltos.io/subresources": "status"}
		_, err := controllers.DeployContentOfConfigMap(context.TODO(), false, testEnv.Config, testEnv.Client,
			configMap, clusterSummary, nil, textlogger.NewLogger(textlogger.NewConfig()))
		Expect(err).To(BeNil())

		serviceOut := corev1.Service{}
		// wait for cache to sync
		Eventually(func() bool {
			err := testEnv.Get(context.TODO(),
				types.NamespacedName{Namespace: "default", Name: serviceName},
				&serviceOut)
			return err == nil &&
				serviceOut.Status.LoadBalancer.Ingress != nil
		}, timeout, pollingInterval).Should(BeTrue())

		Expect(testEnv.Client.Get(context.TODO(),
			types.NamespacedName{Namespace: "default", Name: serviceName}, &serviceOut)).To(Succeed())

		// verify status has been updated
		Expect(serviceOut.Status.LoadBalancer.Ingress).ToNot(BeNil())
		Expect(serviceOut.Status.LoadBalancer.Ingress[0].IP).To(Equal("1.1.1.1"))
		// verify metadata has been updated
		Expect(serviceOut.Labels).To(Not(BeNil()))
		Expect(serviceOut.Labels[key]).To(Equal(value))
		// verify spec has been updated
		Expect(serviceOut.Spec.Selector).To(Not(BeNil()))
		Expect(serviceOut.Spec.Selector[key]).To(Equal(value))
	})

	It("getTemplateResourceRefHash sorts the resources referenced in TemplateResourceRefs", func() {
		clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs = []configv1beta1.TemplateResourceRef{}
		for i := 0; i < 50; i++ {
			name := "trs-" + randomString()
			namespace := "trs-" + randomString()

			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(testEnv.Create(context.TODO(), ns)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, ns)).To(Succeed())

			key := randomString()
			value := randomString()
			var replicas int32 = 3
			depl := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels: map[string]string{
						key: value,
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							key: value,
						},
					},
					Replicas: &replicas,
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								key: value,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:            randomString(),
									Image:           randomString(),
									ImagePullPolicy: corev1.PullAlways,
								},
							},
							ServiceAccountName: randomString(),
						},
					},
				},
			}
			Expect(testEnv.Create(context.TODO(), depl)).To(Succeed())
			Expect(waitForObject(context.TODO(), testEnv.Client, depl)).To(Succeed())

			templateResourceRef := configv1beta1.TemplateResourceRef{
				Resource: corev1.ObjectReference{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Namespace:  namespace,
					Name:       name,
				},
				Identifier: randomString(),
			}

			clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs = append(
				clusterSummary.Spec.ClusterProfileSpec.TemplateResourceRefs,
				templateResourceRef)
		}
		hash, err := controllers.GetTemplateResourceRefHash(context.TODO(), clusterSummary)
		Expect(err).To(BeNil())
		Expect(hash).ToNot(BeEmpty())

		for i := 0; i < 10; i++ {
			tmpHash, err := controllers.GetTemplateResourceRefHash(context.TODO(), clusterSummary)
			Expect(err).To(BeNil())
			Expect(reflect.DeepEqual(tmpHash, hash))
		}
	})
})

// validateResourceReports validates that number of resourceResources with certain actions
// match the expected number per action
func validateResourceReports(resourceReports []libsveltosv1beta1.ResourceReport,
	created, updated, noAction, conflict int) {

	var foundCreated, foundUpdated, foundNoAction, foundConflict int
	for i := range resourceReports {
		rr := &resourceReports[i]
		if rr.Action == string(libsveltosv1beta1.CreateResourceAction) {
			foundCreated++
		} else if rr.Action == string(libsveltosv1beta1.UpdateResourceAction) {
			foundUpdated++
		} else if rr.Action == string(libsveltosv1beta1.NoResourceAction) {
			foundNoAction++
		} else if rr.Action == string(libsveltosv1beta1.ConflictResourceAction) {
			foundConflict++
		}
	}

	Expect(foundCreated).To(Equal(created))
	Expect(foundUpdated).To(Equal(updated))
	Expect(foundNoAction).To(Equal(noAction))
	Expect(foundConflict).To(Equal(conflict))
}
