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

package fv_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	// The stage clusters are SveltosClusters created by the test. All of them reach the workload cluster
	// used by the other tests: this label selects only them.
	stageClustersLabelKey = "promotion-stage-clusters"

	// A stage cluster with this label fails the deployment of the ClusterPromotion policy: the policy
	// is a template which renders a resource of an unknown kind when the label is on the cluster.
	stageClustersFailLabelKey = "fail"

	stageClustersStageName = "all"
	stageClustersCount     = 3

	// The status of a stage is refreshed when the ClusterPromotion is reconciled, which happens on a
	// fixed interval. Changing this annotation makes the controller reconcile the ClusterPromotion.
	stageClustersPokeAnnotation = "fv.projectsveltos.io/poke"

	stageClustersKubeconfigKey = "kubeconfig"

	// The policy deploys a ConfigMap named after the cluster: the clusters are the same cluster.
	stageClustersPolicy = `{{ if index .Cluster.metadata.labels "fail" }}
apiVersion: nonexistent.example.com/v1
kind: Nothing
metadata:
  name: boom
{{ else }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: stage-clusters-{{ .Cluster.metadata.name }}
  namespace: default
data:
  cluster: {{ .Cluster.metadata.name }}
{{ end }}`
)

// The clusters of a stage are registered more than once: the clusters are as many SveltosClusters as needed, all
// pointing to the workload cluster. The test is Serial: registering and deleting the clusters, pausing one and making
// deployments fail on purpose must not overlap with the other tests, which use the same workload cluster.
var _ = Describe("ClusterPromotion stage clusters", Serial, func() {
	const namePrefix = "stage-clusters-"

	var (
		labelValue string
		clusters   []types.NamespacedName
		policy     *corev1.ConfigMap
		promotion  *configv1beta1.ClusterPromotion

		// pausedCluster is the cluster a spec paused: ClusterSummary instances of a paused cluster are not
		// reconciled, so it must be unpaused for the promotion to be deleted.
		pausedCluster *types.NamespacedName
	)

	BeforeEach(func() {
		if getClusterType() == string(libsveltosv1beta1.ClusterTypeSveltos) {
			cluster := &libsveltosv1beta1.SveltosCluster{}
			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: kindWorkloadCluster.GetName()},
				cluster)).To(Succeed())
			if cluster.Spec.PullMode {
				Skip("the stage clusters are registered with a kubeconfig: not available for a cluster in pull mode")
			}
		}

		labelValue = randomString()
		promotion = nil
		pausedCluster = nil

		kubeconfig := getWorkloadClusterKubeconfigData()

		clusters = make([]types.NamespacedName, 0, stageClustersCount)
		for i := 0; i < stageClustersCount; i++ {
			name := fmt.Sprintf("%s%s", namePrefix, randomString())
			createStageCluster(name, kubeconfig, labelValue)
			clusters = append(clusters, types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: name})
		}

		policy = createConfigMapWithPolicy(defaultNamespace, namePrefix+randomString(), stageClustersPolicy)
		policy.Annotations = map[string]string{libsveltosv1beta1.PolicyTemplateAnnotation: annotationOkValue}
		Expect(k8sClient.Create(context.TODO(), policy)).To(Succeed())
	})

	AfterEach(func() {
		if pausedCluster != nil {
			setStageClusterPaused(*pausedCluster, false)
		}
		if promotion != nil {
			deleteStageClustersPromotion(promotion)
		}
		if policy != nil {
			Expect(client.IgnoreNotFound(k8sClient.Delete(context.TODO(), policy))).To(Succeed())
		}
		for i := range clusters {
			deleteStageCluster(clusters[i])
		}
	})

	It("Shows the pending and the failed clusters of a stage which has a maxUpdate", Label("Enterprise"), func() {
		for i := range clusters {
			setStageClusterFailing(clusters[i], true)
		}

		maxUpdate := intstr.FromInt32(1)
		promotion = createStageClustersPromotion(policy, labelValue, &maxUpdate)

		Byf("Verifying the first cluster failed and the other clusters are pending: only one ClusterSummary is created at a time")
		Eventually(func(g Gomega) {
			status := getStageClustersStatus(promotion)
			g.Expect(status.ClusterCounts).ToNot(BeNil())
			g.Expect(*status.ClusterCounts).To(Equal(configv1beta1.StageClusterCounts{
				Matching: stageClustersCount, Failed: 1, Pending: stageClustersCount - 1,
			}))
			g.Expect(status.FailedClusters).To(HaveLen(1))
			g.Expect(status.FailedClusters[0].FailureMessage).ToNot(BeEmpty())
			g.Expect(status.PendingClusters).To(HaveLen(stageClustersCount - 1))
			g.Expect(status.ProvisioningClusters).To(BeEmpty())
			g.Expect(status.LastSuccessfulAppliedTime).To(BeNil())
		}, timeout, pollingInterval).Should(Succeed())

		Byf("Verifying the pending clusters are not the failed one")
		status := getStageClustersStatus(promotion)
		for i := range status.PendingClusters {
			Expect(status.PendingClusters[i].Name).ToNot(Equal(status.FailedClusters[0].ClusterRef.Name))
		}

		Byf("Fixing the clusters: every cluster is provisioned and the stage with them")
		for i := range clusters {
			setStageClusterFailing(clusters[i], false)
		}
		Eventually(func(g Gomega) {
			status := getStageClustersStatus(promotion)
			g.Expect(status.ClusterCounts).ToNot(BeNil())
			g.Expect(*status.ClusterCounts).To(Equal(configv1beta1.StageClusterCounts{
				Matching: stageClustersCount, Provisioned: stageClustersCount,
			}))
			g.Expect(status.PendingClusters).To(BeEmpty())
			g.Expect(status.FailedClusters).To(BeEmpty())
			g.Expect(status.LastSuccessfulAppliedTime).ToNot(BeNil())
		}, 2*timeout, pollingInterval).Should(Succeed())

		deleteStageClustersPromotion(promotion)
		promotion = nil
	})

	It("Provisions a stage when no more clusters than maxFailedClusters failed", Label("Enterprise"), func() {
		setStageClusterFailing(clusters[0], true)

		promotion = createStageClustersPromotion(policy, labelValue, nil)

		Byf("Verifying the stage is not provisioned as a cluster failed and no failure is tolerated")
		Eventually(func(g Gomega) {
			status := getStageClustersStatus(promotion)
			g.Expect(status.ClusterCounts).ToNot(BeNil())
			g.Expect(*status.ClusterCounts).To(Equal(configv1beta1.StageClusterCounts{
				Matching: stageClustersCount, Provisioned: stageClustersCount - 1, Failed: 1,
			}))
			g.Expect(status.FailedClusters).To(HaveLen(1))
			g.Expect(status.FailedClusters[0].ClusterRef.Name).To(Equal(clusters[0].Name))
			g.Expect(status.LastSuccessfulAppliedTime).To(BeNil())
		}, timeout, pollingInterval).Should(Succeed())

		Byf("Tolerating one failed cluster")
		maxFailed := intstr.FromInt32(1)
		updateStageClustersPromotion(promotion, func(stage *configv1beta1.Stage) {
			stage.MaxFailedClusters = &maxFailed
		})

		Byf("Verifying the stage is provisioned and the failed cluster is still reported")
		Eventually(func(g Gomega) {
			status := getStageClustersStatus(promotion)
			g.Expect(status.LastSuccessfulAppliedTime).ToNot(BeNil())
			g.Expect(status.ClusterCounts).ToNot(BeNil())
			g.Expect(status.ClusterCounts.Failed).To(Equal(int32(1)))
			g.Expect(status.FailedClusters).To(HaveLen(1))
		}, timeout, pollingInterval).Should(Succeed())

		deleteStageClustersPromotion(promotion)
		promotion = nil
	})

	It("Waits for a paused cluster, unless waitForPausedClusters is false", Label("Enterprise"), func() {
		paused := clusters[0]
		pausedCluster = &paused
		setStageClusterPaused(paused, true)

		promotion = createStageClustersPromotion(policy, labelValue, nil)

		Byf("Verifying the stage waits for the paused cluster")
		Eventually(func(g Gomega) {
			status := getStageClustersStatus(promotion)
			g.Expect(status.ClusterCounts).ToNot(BeNil())
			g.Expect(status.ClusterCounts.Matching).To(Equal(int32(stageClustersCount)))
			g.Expect(status.ClusterCounts.Provisioned).To(Equal(int32(stageClustersCount - 1)))
			g.Expect(status.LastSuccessfulAppliedTime).To(BeNil())
		}, timeout, pollingInterval).Should(Succeed())

		status := getStageClustersStatus(promotion)
		waiting := append(append([]corev1.ObjectReference{}, status.ProvisioningClusters...), status.PendingClusters...)
		Expect(waiting).To(HaveLen(1))
		Expect(waiting[0].Name).To(Equal(paused.Name))

		Byf("Not waiting for paused clusters anymore")
		wait := false
		updateStageClustersPromotion(promotion, func(stage *configv1beta1.Stage) {
			stage.WaitForPausedClusters = &wait
		})

		Byf("Verifying the stage is provisioned and the paused cluster is not counted")
		Eventually(func(g Gomega) {
			status := getStageClustersStatus(promotion)
			g.Expect(status.LastSuccessfulAppliedTime).ToNot(BeNil())
			g.Expect(status.ClusterCounts).ToNot(BeNil())
			g.Expect(*status.ClusterCounts).To(Equal(configv1beta1.StageClusterCounts{
				Matching: stageClustersCount - 1, Provisioned: stageClustersCount - 1,
			}))
		}, timeout, pollingInterval).Should(Succeed())

		setStageClusterPaused(paused, false)
		pausedCluster = nil
		deleteStageClustersPromotion(promotion)
		promotion = nil
	})
})

// getWorkloadClusterKubeconfigData returns the kubeconfig Sveltos uses to reach the workload cluster
func getWorkloadClusterKubeconfigData() []byte {
	secretName := kindWorkloadCluster.GetName() + "-kubeconfig"
	secretKey := "value"

	if getClusterType() == string(libsveltosv1beta1.ClusterTypeSveltos) {
		cluster := &libsveltosv1beta1.SveltosCluster{}
		Expect(k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: kindWorkloadCluster.GetName()},
			cluster)).To(Succeed())

		secretName = cluster.Spec.KubeconfigName
		if secretName == "" {
			secretName = kindWorkloadCluster.GetName() + "-sveltos-kubeconfig"
		}
		secretKey = cluster.Spec.KubeconfigKeyName
	}

	secret := &corev1.Secret{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Namespace: kindWorkloadCluster.GetNamespace(), Name: secretName}, secret)).To(Succeed())

	if secretKey == "" && len(secret.Data) == 1 {
		for k := range secret.Data {
			secretKey = k
		}
	}
	Expect(secret.Data).To(HaveKey(secretKey))

	return secret.Data[secretKey]
}

// createStageCluster registers the workload cluster once more, as a SveltosCluster with the stage label
func createStageCluster(name string, kubeconfig []byte, labelValue string) {
	namespace := kindWorkloadCluster.GetNamespace()
	secretName := name + "-sveltos-kubeconfig"

	Byf("Registering the workload cluster as SveltosCluster %s/%s", namespace, name)
	Expect(k8sClient.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: secretName},
		Data:       map[string][]byte{stageClustersKubeconfigKey: kubeconfig},
	})).To(Succeed())

	Expect(k8sClient.Create(context.TODO(), &libsveltosv1beta1.SveltosCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: name,
			Labels: map[string]string{stageClustersLabelKey: labelValue},
		},
		Spec: libsveltosv1beta1.SveltosClusterSpec{
			KubeconfigName:    secretName,
			KubeconfigKeyName: stageClustersKubeconfigKey,
		},
	})).To(Succeed())

	Eventually(func() bool {
		current := &libsveltosv1beta1.SveltosCluster{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, current)
		return err == nil && current.Status.Ready
	}, timeout, pollingInterval).Should(BeTrue())
}

// deleteStageCluster removes the SveltosCluster and its kubeconfig
func deleteStageCluster(cluster types.NamespacedName) {
	Byf("Deleting SveltosCluster %s", cluster)
	current := &libsveltosv1beta1.SveltosCluster{}
	err := k8sClient.Get(context.TODO(), cluster, current)
	if err == nil {
		Expect(k8sClient.Delete(context.TODO(), current)).To(Succeed())
	} else {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(), cluster, &libsveltosv1beta1.SveltosCluster{})
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: cluster.Namespace, Name: cluster.Name + "-sveltos-kubeconfig"}}
	Expect(client.IgnoreNotFound(k8sClient.Delete(context.TODO(), secret))).To(Succeed())
}

func updateStageCluster(cluster types.NamespacedName, update func(*libsveltosv1beta1.SveltosCluster)) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &libsveltosv1beta1.SveltosCluster{}
		if err := k8sClient.Get(context.TODO(), cluster, current); err != nil {
			return err
		}
		update(current)
		return k8sClient.Update(context.TODO(), current)
	})
	Expect(err).To(BeNil())
}

// setStageClusterFailing makes the deployment of the policy fail (or succeed) on the cluster
func setStageClusterFailing(cluster types.NamespacedName, failing bool) {
	Byf("Setting cluster %s failing: %t", cluster, failing)
	updateStageCluster(cluster, func(current *libsveltosv1beta1.SveltosCluster) {
		if failing {
			current.Labels[stageClustersFailLabelKey] = "true"
		} else {
			delete(current.Labels, stageClustersFailLabelKey)
		}
	})
}

func setStageClusterPaused(cluster types.NamespacedName, paused bool) {
	Byf("Setting cluster %s paused: %t", cluster, paused)
	updateStageCluster(cluster, func(current *libsveltosv1beta1.SveltosCluster) {
		current.Spec.Paused = paused
	})
}

// createStageClustersPromotion creates a ClusterPromotion with a single stage, selecting the stage clusters
func createStageClustersPromotion(policy *corev1.ConfigMap, labelValue string,
	maxUpdate *intstr.IntOrString) *configv1beta1.ClusterPromotion {

	promotion := &configv1beta1.ClusterPromotion{
		ObjectMeta: metav1.ObjectMeta{Name: "stage-clusters-" + randomString()},
		Spec: configv1beta1.ClusterPromotionSpec{
			ProfileSpec: configv1beta1.ProfileSpec{
				SyncMode:  configv1beta1.SyncModeContinuous,
				MaxUpdate: maxUpdate,
				PolicyRefs: []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: policy.Namespace,
						Name:      policy.Name,
					},
				},
			},
			Stages: []configv1beta1.Stage{
				{
					Name: stageClustersStageName,
					ClusterSelector: libsveltosv1beta1.Selector{
						LabelSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{stageClustersLabelKey: labelValue},
						},
					},
				},
			},
		},
	}

	Byf("Creating ClusterPromotion %s", promotion.Name)
	Expect(k8sClient.Create(context.TODO(), promotion)).To(Succeed())

	return promotion
}

// updateStageClustersPromotion changes the stage of the ClusterPromotion
func updateStageClustersPromotion(promotion *configv1beta1.ClusterPromotion, update func(*configv1beta1.Stage)) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &configv1beta1.ClusterPromotion{}
		if err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: promotion.Name}, current); err != nil {
			return err
		}
		update(&current.Spec.Stages[0])
		return k8sClient.Update(context.TODO(), current)
	})
	Expect(err).To(BeNil())
}

// getStageClustersStatus returns the status of the stage of the ClusterPromotion. Before reading it, makes the
// controller reconcile the ClusterPromotion: the status of the stage is refreshed only then. An empty status is
// returned until the stage has one.
func getStageClustersStatus(promotion *configv1beta1.ClusterPromotion) *configv1beta1.StageStatus {
	poke := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, stageClustersPokeAnnotation,
		time.Now().Format(time.RFC3339Nano))
	Expect(k8sClient.Patch(context.TODO(),
		&configv1beta1.ClusterPromotion{ObjectMeta: metav1.ObjectMeta{Name: promotion.Name}},
		client.RawPatch(types.MergePatchType, []byte(poke)))).To(Succeed())

	current := &configv1beta1.ClusterPromotion{}
	Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: promotion.Name}, current)).To(Succeed())

	for i := range current.Status.Stages {
		if current.Status.Stages[i].Name == stageClustersStageName {
			return &current.Status.Stages[i]
		}
	}

	return &configv1beta1.StageStatus{}
}

// deleteStageClustersPromotion deletes the ClusterPromotion and waits for what it deployed to be removed: this
// needs the clusters, so they are deleted after.
func deleteStageClustersPromotion(promotion *configv1beta1.ClusterPromotion) {
	Byf("Deleting ClusterPromotion %s", promotion.Name)
	Expect(client.IgnoreNotFound(k8sClient.Delete(context.TODO(),
		&configv1beta1.ClusterPromotion{ObjectMeta: metav1.ObjectMeta{Name: promotion.Name}}))).To(Succeed())

	clusterProfileName := fmt.Sprintf("%s-%s", promotion.Name, stageClustersStageName)

	Byf("Verifying ClusterProfile %s and its ClusterSummaries are gone", clusterProfileName)
	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterProfileName},
			&configv1beta1.ClusterProfile{})
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())

	Eventually(func() bool {
		clusterSummaryList := &configv1beta1.ClusterSummaryList{}
		err := k8sClient.List(context.TODO(), clusterSummaryList,
			client.MatchingLabels{clusterops.ClusterProfileLabelName: clusterProfileName})
		return err == nil && len(clusterSummaryList.Items) == 0
	}, timeout, pollingInterval).Should(BeTrue())
}
