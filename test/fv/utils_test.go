package fv_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
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
			Name: namePrefix + util.RandomString(5),
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
