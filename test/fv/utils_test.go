package fv_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/controllers"
)

// Byf is a simple wrapper around By.
func Byf(format string, a ...interface{}) {
	By(fmt.Sprintf(format, a...)) // ignore_by_check
}

func getClusterfeature(namePrefix string, clusterLabels map[string]string) *configv1alpha1.ClusterFeature {
	selector := ""
	for k := range clusterLabels {
		if selector != "" {
			selector += ","
		}
		selector += fmt.Sprintf("%s=%s", k, clusterLabels[k])
	}
	clusterFeature := &configv1alpha1.ClusterFeature{
		ObjectMeta: metav1.ObjectMeta{
			Name: namePrefix + randomString(),
		},
		Spec: configv1alpha1.ClusterFeatureSpec{
			ClusterSelector: configv1alpha1.Selector(selector),
		},
	}

	return clusterFeature
}

func getClusterSummaryOwnerReference(clusterSummary *configv1alpha1.ClusterSummary) (*configv1alpha1.ClusterFeature, error) {
	Byf("Checking clusterSummary %s owner reference is set", clusterSummary.Name)
	for _, ref := range clusterSummary.OwnerReferences {
		if ref.Kind != "ClusterFeature" {
			continue
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if gv.Group == configv1alpha1.GroupVersion.Group {
			clusterFeature := &configv1alpha1.ClusterFeature{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: ref.Name}, clusterFeature)
			return clusterFeature, err
		}
	}
	return nil, nil
}

// getKindWorkloadClusterKubeconfig returns client to access the kind cluster used as workload cluster
func getKindWorkloadClusterKubeconfig() (client.Client, error) {
	kubeconfigPath := "workload_kubeconfig" // this file is created in this directory by Makefile during cluster creation
	config, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	restConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	return client.New(restConfig, client.Options{Scheme: scheme})
}

func verifyFeatureStatus(clusterSummaryName string, featureID configv1alpha1.FeatureID, status configv1alpha1.FeatureStatus) {
	Eventually(func() bool {
		currentClusterSummary := &configv1alpha1.ClusterSummary{}
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSummaryName}, currentClusterSummary)
		if err != nil {
			return false
		}
		for i := range currentClusterSummary.Status.FeatureSummaries {
			if currentClusterSummary.Status.FeatureSummaries[i].FeatureID == featureID &&
				currentClusterSummary.Status.FeatureSummaries[i].Status == status {

				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())
}

// deleteClusterFeature deletes ClusterFeature and verifies all ClusterSummaries created by this ClusterFeature
// instances are also gone
func deleteClusterFeature(clusterFeature *configv1alpha1.ClusterFeature) {
	listOptions := []client.ListOption{
		client.MatchingLabels{
			controllers.ClusterFeatureLabelName: clusterFeature.Name,
		},
	}
	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	Expect(k8sClient.List(context.TODO(), clusterSummaryList, listOptions...)).To(Succeed())

	Byf("Deleting the ClusterFeature %s", clusterFeature.Name)
	currentClusterFeature := &configv1alpha1.ClusterFeature{}
	Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)).To(BeNil())
	Expect(k8sClient.Delete(context.TODO(), currentClusterFeature)).To(Succeed())

	for i := range clusterSummaryList.Items {
		Byf("Verifying ClusterSummary %s are gone", clusterSummaryList.Items[i].Name)
	}
	Eventually(func() bool {
		for i := range clusterSummaryList.Items {
			clusterSummaryName := clusterSummaryList.Items[i].Name
			currentClusterSummary := &configv1alpha1.ClusterSummary{}
			err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterSummaryName}, currentClusterSummary)
			if err == nil || !apierrors.IsNotFound(err) {
				return false
			}
		}
		return true
	}, timeout, pollingInterval).Should(BeTrue())

	Byf("Verifying ClusterFeature %s is gone", clusterFeature.Name)

	Eventually(func() bool {
		err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: clusterFeature.Name}, currentClusterFeature)
		return apierrors.IsNotFound(err)
	}, timeout, pollingInterval).Should(BeTrue())
}

func randomString() string {
	const length = 10
	return util.RandomString(length)
}

func getClusterSummary(ctx context.Context,
	clusterFeatureName, clusterNamespace, clusterName string) (*configv1alpha1.ClusterSummary, error) {

	listOptions := []client.ListOption{
		client.MatchingLabels{
			controllers.ClusterFeatureLabelName: clusterFeatureName,
			controllers.ClusterLabelNamespace:   clusterNamespace,
			controllers.ClusterLabelName:        clusterName,
		},
	}

	clusterSummaryList := &configv1alpha1.ClusterSummaryList{}
	if err := k8sClient.List(ctx, clusterSummaryList, listOptions...); err != nil {
		return nil, err
	}

	if len(clusterSummaryList.Items) == 0 {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: configv1alpha1.GroupVersion.Group, Resource: "ClusterSummary"}, "")
	}

	if len(clusterSummaryList.Items) != 1 {
		return nil, fmt.Errorf("more than one clustersummary found for cluster %s/%s created by %s",
			clusterNamespace, clusterName, clusterFeatureName)
	}

	return &clusterSummaryList.Items[0], nil
}
