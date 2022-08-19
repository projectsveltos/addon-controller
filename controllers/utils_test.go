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
	b64 "encoding/base64"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	kyvernoapi "github.com/kyverno/kyverno/api/kyverno/v1"
	opav1 "github.com/open-policy-agent/frameworks/constraint/pkg/apis/templates/v1"
	"github.com/pkg/errors"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

const (
	timeout         = 40 * time.Second
	pollingInterval = 2 * time.Second
)

const (
	upstreamClusterNamePrefix = "upstream-cluster"
	upstreamMachineNamePrefix = "upstream-machine"
	clusterFeatureNamePrefix  = "cluster-feature"
)

const (
	addLabelPolicyStr = `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: %s
  annotations:
    policies.kyverno.io/title: Add Labels
    policies.kyverno.io/category: Sample
    policies.kyverno.io/severity: medium
    policies.kyverno.io/subject: Label
    policies.kyverno.io/description: >-
      Labels are used as an important source of metadata describing objects in various ways
      or triggering other functionality. Labels are also a very basic concept and should be
      used throughout Kubernetes. This policy performs a simple mutation which adds a label
      foo=bar to Pods, Services, ConfigMaps, and Secrets.
spec:
  rules:
  - name: add-labels
    match:
      resources:
        annotations:
          imageregistry: "https://hub.docker.com/"
        kinds:
        - Pod
    mutate:
      patchStrategicMerge:
        metadata:
          labels:
            foo: bar`

	allowLabelChangeStr = `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: %s
  annotations:
    pod-policies.kyverno.io/autogen-controllers: none
    policies.kyverno.io/title: Allowed Label Changes
    policies.kyverno.io/category: Other
    policies.kyverno.io/severity: medium
    kyverno.io/kyverno-version: 1.6.0
    policies.kyverno.io/minversion: 1.6.0
    kyverno.io/kubernetes-version: "1.23"
    policies.kyverno.io/subject: Pod,Label
    policies.kyverno.io/description: >-
      In some cases, operations teams need a type of limited access to
      change resources during troubleshooting or outage mitigation.
      This policy demonstrates how to prevent modification to labels
      except one with the key breakglass. Changing, adding, or deleting
      any other labels is denied.
spec:
  validationFailureAction: enforce
  background: false
  rules:
  - name: safe-label
    match:
      any:
      - resources:
        kinds:
        - Pod
        - Deployment
        - StatefulSet
        - DaemonSet
        - Job
        - CronJob
    preconditions:
      all:
      - key: "{{ request.operation }}"
        operator: Equals
        value: UPDATE
    validate:
      message: "The only label that may be removed or changed is breakglass."
      deny:
        conditions:
          any:
            - key: "{{ request.object.metadata.labels || '{}' |  merge(@, {breakglass:null}) }}"
              operator: NotEquals
              value: "{{ request.oldObject.metadata.labels || '{}' |  merge(@, {breakglass:null}) }}"`

	checkSa = `apiVersion: kyverno.io/v1
kind: Policy
metadata:
  name: %s
  namespace: default
  annotations:
    policies.kyverno.io/title: Check ServiceAccount
    policies.kyverno.io/category: Sample
    policies.kyverno.io/subject: Pod,ServiceAccount
    kyverno.io/kyverno-version: 1.5.2
    kyverno.io/kubernetes-version: "1.21"
    policies.kyverno.io/description: >-
      ServiceAccounts with privileges to create Pods may be able to do so and name
      a ServiceAccount other than the one used to create it. This policy checks the
      Pod, if created by a ServiceAccount, and ensures the serviceAccountName field
      matches the actual ServiceAccount.      
spec:
  validationFailureAction: audit
  background: false
  rules:
    - name: check-sa
      match:
        resources:
          kinds:
          - Pod
      preconditions:
        all:
        - key: "{{serviceAccountName}}"
          operator: Equals
          value: "*?"
      validate:
        message: "The ServiceAccount used to create this Pod is confined to using the same account when running the Pod."
        pattern:
          spec:
            serviceAccountName: "{{serviceAccountName}}"`

	serviceMonitorFrontend = `apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: %s
  labels:
    team: frontend
spec:
  selector:
    matchLabels:
      app: example-app
  endpoints:
  - port: web`

	serviceMonitorKubeMtrics = `apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  labels:
    app.kubernetes.io/component: exporter
    app.kubernetes.io/name: kube-state-metrics
    app.kubernetes.io/part-of: kube-prometheus
    app.kubernetes.io/version: 2.5.0
  name: %s
  namespace: monitoring
spec:
  endpoints:
  - bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
    honorLabels: true
    interval: 30s
    port: https-main
    relabelings:
    - action: labeldrop
      regex: (pod|service|endpoint|namespace)
    scheme: https
    scrapeTimeout: 30s
    tlsConfig:
      insecureSkipVerify: true
  - bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
    interval: 30s
    port: https-self
    scheme: https
    tlsConfig:
      insecureSkipVerify: true
  jobLabel: app.kubernetes.io/name
  selector:
    matchLabels:
      app.kubernetes.io/component: exporter
      app.kubernetes.io/name: kube-state-metrics
      app.kubernetes.io/part-of: kube-prometheus`
)

var (
	cacheSyncBackoff = wait.Backoff{
		Duration: 100 * time.Millisecond,
		Factor:   1.5,
		Steps:    8,
		Jitter:   0.4,
	}
)

func setupScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := configv1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := kyvernoapi.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := opav1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := monitoringv1.AddToScheme(s); err != nil {
		return nil, err
	}

	return s, nil
}

var _ = Describe("getClusterFeatureOwner ", func() {
	var logger logr.Logger
	var clusterFeature *configv1alpha1.ClusterFeature
	var clusterSummary *configv1alpha1.ClusterSummary
	var cluster *clusterv1.Cluster
	var namespace string
	var scheme *runtime.Scheme

	BeforeEach(func() {
		var err error
		scheme, err = setupScheme()
		Expect(err).ToNot(HaveOccurred())

		logger = klogr.New()

		namespace = "reconcile" + randomString()

		logger = klogr.New()
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      upstreamClusterNamePrefix + randomString(),
				Namespace: namespace,
				Labels: map[string]string{
					"dc": "eng",
				},
			},
		}

		clusterFeature = &configv1alpha1.ClusterFeature{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterFeatureNamePrefix + randomString(),
			},
			Spec: configv1alpha1.ClusterFeatureSpec{
				ClusterSelector: selector,
			},
		}

		clusterSummaryName := controllers.GetClusterSummaryName(clusterFeature.Name, cluster.Namespace, cluster.Name)
		clusterSummary = &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterSummaryName,
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
			},
		}
		addLabelsToClusterSummary(clusterSummary, clusterFeature.Name, cluster.Namespace, cluster.Name)
	})

	It("getClusterFeatureOwner returns ClusterFeature owner", func() {
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterFeature)).To(Succeed())

		clusterName := randomString()
		clusterSummary.OwnerReferences = []metav1.OwnerReference{
			{
				Kind:       clusterFeature.Kind,
				Name:       clusterFeature.Name,
				APIVersion: clusterFeature.APIVersion,
			},
		}

		clusterSummary.Spec = configv1alpha1.ClusterSummarySpec{
			ClusterNamespace:   namespace,
			ClusterName:        clusterName,
			ClusterFeatureSpec: clusterFeature.Spec,
		}

		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		owner, err := controllers.GetClusterFeatureOwner(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).ToNot(BeNil())
		Expect(owner.Name).To(Equal(clusterFeature.Name))
	})

	It("getClusterFeatureOwner returns nil when ClusterFeature does not exist", func() {
		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterFeature)).To(Succeed())

		clusterName := randomString()
		clusterSummary.OwnerReferences = []metav1.OwnerReference{
			{
				Kind:       clusterFeature.Kind,
				Name:       clusterFeature.Name,
				APIVersion: clusterFeature.APIVersion,
			},
		}

		clusterSummary.Spec = configv1alpha1.ClusterSummarySpec{
			ClusterNamespace:   namespace,
			ClusterName:        clusterName,
			ClusterFeatureSpec: clusterFeature.Spec,
		}

		initObjects := []client.Object{
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		owner, err := controllers.GetClusterFeatureOwner(context.TODO(), c, clusterSummary)
		Expect(err).To(BeNil())
		Expect(owner).To(BeNil())
	})

	It("getUnstructured returns proper object", func() {
		policy, err := controllers.GetUnstructured([]byte(fmt.Sprintf(addLabelPolicyStr, randomString())))
		Expect(err).To(BeNil())
		Expect(policy.GetKind()).To(Equal("ClusterPolicy"))
	})

	It("getSecretData returns an error when cluster does not exist", func() {
		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		_, err := controllers.GetSecretData(context.TODO(), logger, c, cluster.Namespace, cluster.Name)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("Cluster %s/%s does not exist", cluster.Namespace, cluster.Name)))
	})

	It("getSecretData returns an error when secret does not exist", func() {
		initObjects := []client.Object{
			clusterFeature,
			cluster,
			clusterSummary,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		_, err := controllers.GetSecretData(context.TODO(), logger, c, cluster.Namespace, cluster.Name)
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("Failed to get secret %s/%s-kubeconfig", cluster.Namespace, cluster.Name)))
	})

	It("getSecretData returns secret data", func() {
		randomData := []byte(randomString())
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + "-kubeconfig",
			},
			Data: map[string][]byte{
				"data": randomData,
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			cluster,
			clusterSummary,
			&secret,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		data, err := controllers.GetSecretData(context.TODO(), logger, c, cluster.Namespace, cluster.Name)
		Expect(err).To(BeNil())
		Expect(data).To(Equal(randomData))
	})

	It("getKubernetesClient returns client to access CAPI cluster", func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(testEnv.Client.Create(context.TODO(), ns)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), cluster)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterFeature)).To(Succeed())
		Expect(testEnv.Client.Create(context.TODO(), clusterSummary)).To(Succeed())

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name + "-kubeconfig",
			},
			Data: map[string][]byte{
				"data": testEnv.Kubeconfig,
			},
		}

		Expect(testEnv.Client.Create(context.TODO(), secret)).To(Succeed())

		Expect(waitForObject(context.TODO(), testEnv.Client, secret)).To(Succeed())

		wcClient, err := controllers.GetKubernetesClient(context.TODO(), logger, testEnv.Client, cluster.Namespace, cluster.Name)
		Expect(err).To(BeNil())
		Expect(wcClient).ToNot(BeNil())
	})

	It("GetClusterSummary returns the ClusterSummary instance created by a ClusterFeature for a CAPI Cluster", func() {
		clusterSummary0 := &configv1alpha1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cs" + randomString(),
			},
			Spec: configv1alpha1.ClusterSummarySpec{
				ClusterNamespace: cluster.Namespace,
				ClusterName:      cluster.Name,
			},
		}

		initObjects := []client.Object{
			clusterFeature,
			clusterSummary,
			clusterSummary0,
		}

		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(initObjects...).Build()

		currentClusterSummary, err := controllers.GetClusterSummary(context.TODO(), c, clusterFeature.Name, cluster.Namespace, cluster.Name)
		Expect(err).To(BeNil())
		Expect(currentClusterSummary).ToNot(BeNil())
		Expect(currentClusterSummary.Name).To(Equal(clusterSummary.Name))
	})

	It("addOwnerReference adds an OwnerReference to an object. removeOwnerReference removes it", func() {
		policy, err := controllers.GetUnstructured([]byte(fmt.Sprintf(addLabelPolicyStr, randomString())))
		Expect(err).To(BeNil())
		Expect(policy.GetKind()).To(Equal("ClusterPolicy"))

		Expect(addTypeInformationToObject(testEnv.Scheme(), clusterSummary)).To(Succeed())

		controllers.AddOwnerReference(policy, clusterSummary)

		Expect(policy.GetOwnerReferences()).ToNot(BeNil())
		Expect(len(policy.GetOwnerReferences())).To(Equal(1))
		Expect(policy.GetOwnerReferences()[0].Kind).To(Equal("ClusterSummary"))
		Expect(policy.GetOwnerReferences()[0].Name).To(Equal(clusterSummary.Name))

		controllers.RemoveOwnerReference(policy, clusterSummary)
		Expect(len(policy.GetOwnerReferences())).To(Equal(0))
	})
})

// addOwnerReference adds owner as OwnerReference of obj
func addOwnerReference(ctx context.Context, c client.Client, obj, owner client.Object) {
	objCopy := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	Expect(c.Get(ctx, key, objCopy)).To(Succeed())
	refs := objCopy.GetOwnerReferences()
	if refs == nil {
		refs = make([]metav1.OwnerReference, 0)
	}
	refs = append(refs,
		metav1.OwnerReference{
			UID:        owner.GetUID(),
			Name:       owner.GetName(),
			Kind:       owner.GetObjectKind().GroupVersionKind().Kind,
			APIVersion: owner.GetResourceVersion(),
		})
	objCopy.SetOwnerReferences(refs)
	Expect(c.Update(ctx, objCopy)).To(Succeed())
}

// waitForObject waits for the cache to be updated helps in preventing test flakes due to the cache sync delays.
func waitForObject(ctx context.Context, c client.Client, obj client.Object) error {
	// Makes sure the cache is updated with the new object
	objCopy := obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	if err := wait.ExponentialBackoff(
		cacheSyncBackoff,
		func() (done bool, err error) {
			if err := c.Get(ctx, key, objCopy); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}); err != nil {
		return errors.Wrapf(err, "object %s, %s is not being added to the testenv client cache", obj.GetObjectKind().GroupVersionKind().String(), key)
	}
	return nil
}

// createConfigMapWithPolicy creates a configMap with Data containing base64 encoded policies
func createConfigMapWithPolicy(namespace, configMapName string, policyStrs ...string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      configMapName,
		},
		Data: map[string]string{},
	}
	for i := range policyStrs {
		key := fmt.Sprintf("policy%d", i)
		policyEncoded := b64.StdEncoding.EncodeToString([]byte(policyStrs[i]))
		cm.Data[key] = policyEncoded
	}

	return cm
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

func addLabelsToClusterSummary(clusterSummary *configv1alpha1.ClusterSummary, clusterFeatureName, clusterNamespace, clusterName string) {
	labels := clusterSummary.Labels
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[controllers.ClusterFeatureLabelName] = clusterFeatureName
	labels[controllers.ClusterLabelNamespace] = clusterNamespace
	labels[controllers.ClusterLabelName] = clusterName

	clusterSummary.Labels = labels
}

// deleteResources deletes following resources:
// - namespace
// - clusterFeature
// - clusterSummary
// - all WorkloadRoles
func deleteResources(namespace string,
	clusterFeature *configv1alpha1.ClusterFeature,
	clusterSummary *configv1alpha1.ClusterSummary) {

	ns := &corev1.Namespace{}
	err := testEnv.Client.Get(context.TODO(), types.NamespacedName{Name: namespace}, ns)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		return
	}
	err = testEnv.Client.Delete(context.TODO(), ns)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}
	err = testEnv.Client.Delete(context.TODO(), clusterFeature)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}
	err = testEnv.Client.Delete(context.TODO(), clusterSummary)
	if err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}

	workloadRules := &configv1alpha1.WorkloadRoleList{}
	Expect(testEnv.Client.List(context.TODO(), workloadRules)).To(Succeed())
	for i := range workloadRules.Items {
		Expect(testEnv.Client.Delete(context.TODO(), &workloadRules.Items[i])).To(Succeed())
	}
}
