package fv_test

import (
	"context"
	b64 "encoding/base64"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kyvernoapi "github.com/kyverno/kyverno/api/kyverno/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/kyverno"
)

const (
	labels = `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: add-labels
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

	ingress = `apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: disallow-empty-ingress-host
  annotations:
    policies.kyverno.io/title: Disallow empty Ingress host
    policies.kyverno.io/category: Best Practices
    policies.kyverno.io/severity: medium
    policies.kyverno.io/subject: Ingress
    policies.kyverno.io/description: >-
      An ingress resource needs to define an actual host name
      in order to be valid. This policy ensures that there is a
      hostname for each rule defined.      
spec:
  validationFailureAction: enforce
  background: false
  rules:
    - name: disallow-empty-ingress-host
      match:
        resources:
          kinds:
            - Ingress
      validate:
        message: "The Ingress host name must be defined, not empty."
        deny:
          conditions:
            - key: "{{ request.object.spec.rules[].host || '[]' | length(@) }}"
              operator: NotEquals
              value: "{{ request.object.spec.rules[].http || '[]' | length(@) }}"`
)

var _ = Describe("Kyverno", func() {
	const (
		namePrefix = "kyverno"
	)

	It("Deploy and updates Kyverno correctly", Label("FV"), func() {
		Byf("Add configMap containing kyverno policy")
		labelsEncoded := b64.StdEncoding.EncodeToString([]byte(labels))
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      namePrefix + randomString(),
			},
			Data: map[string]string{
				"addlabel": labelsEncoded,
			},
		}
		Expect(k8sClient.Create(context.TODO(), configMap)).To(Succeed())

		Byf("Create a ClusterFeature matching Cluster %s/%s", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
		clusterFeature := getClusterfeature(namePrefix, map[string]string{key: value})
		clusterFeature.Spec.SyncMode = configv1alpha1.SyncModeContinuous
		clusterFeature.Spec.KyvernoConfiguration = &configv1alpha1.KyvernoConfiguration{
			Replicas: 1,
			PolicyRefs: []corev1.ObjectReference{
				{Namespace: configMap.Namespace, Name: configMap.Name},
			},
		}
		Expect(k8sClient.Create(context.TODO(), clusterFeature)).To(Succeed())

		verifyClusterFeatureMatches(clusterFeature)

		clusterSummary := verifyClusterSummary(clusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("getting client to access the workload cluster")
		workloadClient, err := getKindWorkloadClusterKubeconfig()
		Expect(err).To(BeNil())
		Expect(workloadClient).ToNot(BeNil())

		Byf("Verifying Kyverno deployment %s/%s is present", kyverno.Namespace, kyverno.Deployment)
		Eventually(func() bool {
			depl := &appsv1.Deployment{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kyverno.Namespace, Name: kyverno.Deployment}, depl)
			return err == nil
		}, timeout, pollingInterval).Should(BeTrue())

		policyName := clusterSummary.Status.PolicyPrefix + "-" + "add-labels"
		Byf("Verifying Kyverno policy %s is present", policyName)
		Eventually(func() error {
			policy := &kyvernoapi.ClusterPolicy{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: policyName}, policy)
			return err
		}, timeout, pollingInterval).Should(BeNil())

		Byf("Verifying ClusterSummary %s status is set to Deployed for kyverno", clusterSummary.Name)
		verifyFeatureStatus(clusterSummary.Name, configv1alpha1.FeatureKyverno, configv1alpha1.FeatureStatusProvisioned)

		Byf("Modifying configMap")
		currentConfigMap := &corev1.ConfigMap{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, currentConfigMap)).To(Succeed())
		ingressEncoded := b64.StdEncoding.EncodeToString([]byte(ingress))
		currentConfigMap.Data["ingress"] = ingressEncoded
		Expect(k8sClient.Update(context.TODO(), currentConfigMap)).To(Succeed())

		policyName = clusterSummary.Status.PolicyPrefix + "-" + "disallow-empty-ingress-host"
		Byf("Verifying Kyverno policy %s is present", policyName)
		Eventually(func() error {
			policy := &kyvernoapi.ClusterPolicy{}
			err = workloadClient.Get(context.TODO(), types.NamespacedName{Name: policyName}, policy)
			return err
		}, timeout, pollingInterval).Should(BeNil())

		Byf("changing clusterfeature to not require any kyverno configuration anymore")
		currentClusterFeature := &configv1alpha1.ClusterFeature{}
		Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(Succeed())
		currentClusterFeature.Spec.KyvernoConfiguration = nil
		Expect(k8sClient.Update(context.TODO(), currentClusterFeature)).To(Succeed())

		clusterSummary = verifyClusterSummary(currentClusterFeature, kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)

		Byf("Verifying proper role is removed in the workload cluster")
		Eventually(func() bool {
			policy := &kyvernoapi.ClusterPolicy{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Name: fmt.Sprintf("%s-add-labels", clusterSummary.Name)}, policy)
			return err != nil && apierrors.IsNotFound(err)
		}, timeout, pollingInterval).Should(BeTrue())

		deleteClusterFeature(clusterFeature)
	})
})
