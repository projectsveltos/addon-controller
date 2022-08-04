package fv_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TwinProduction/go-color"
	kyvernoapi "github.com/kyverno/kyverno/api/kyverno/v1"
	. "github.com/onsi/ginkgo/v2"
	ginkgotypes "github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
)

var (
	k8sClient           client.Client
	scheme              *runtime.Scheme
	kindWorkloadCluster *clusterv1.Cluster // This is the name of the kind workload cluster, in the form namespace/name
)

const (
	timeout         = 2 * time.Minute
	pollingInterval = 2 * time.Second
)

func TestFv(t *testing.T) {
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	reporterConfig.FullTrace = true
	reporterConfig.JSONReport = "out.json"
	report := func(report ginkgotypes.Report) {
		for i := range report.SpecReports {
			specReport := report.SpecReports[i]
			if specReport.State.String() == "skipped" {
				GinkgoWriter.Printf(color.Colorize(color.Blue, fmt.Sprintf("[Skipped]: %s\n", specReport.FullText())))
			}
		}
		for i := range report.SpecReports {
			specReport := report.SpecReports[i]
			if specReport.Failed() {
				GinkgoWriter.Printf(color.Colorize(color.Red, fmt.Sprintf("[Failed]: %s\n", specReport.FullText())))
			}
		}
	}
	ReportAfterSuite("report", report)

	RunSpecs(t, "FV Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	restConfig := ctrl.GetConfigOrDie()
	// To get rid of the annoying request.go log
	restConfig.QPS = 100
	restConfig.Burst = 100

	scheme = runtime.NewScheme()

	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(clusterv1.AddToScheme(scheme)).To(Succeed())
	Expect(configv1alpha1.AddToScheme(scheme)).To(Succeed())
	Expect(kyvernoapi.AddToScheme(scheme)).To(Succeed())

	var err error
	k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	clusterList := &clusterv1.ClusterList{}
	listOptions := []client.ListOption{
		client.MatchingLabels(
			map[string]string{clusterv1.ClusterLabelName: "sveltos-management-workload"},
		),
	}

	Expect(k8sClient.List(context.TODO(), clusterList, listOptions...)).To(Succeed())
	Expect(len(clusterList.Items)).To(Equal(1))
	kindWorkloadCluster = &clusterList.Items[0]

	Byf("Wait for machine in cluster %s/%s to be ready", kindWorkloadCluster.Namespace, kindWorkloadCluster.Name)
	Eventually(func() bool {
		machineList := &clusterv1.MachineList{}
		listOptions = []client.ListOption{
			client.InNamespace(kindWorkloadCluster.Namespace),
			client.MatchingLabels{clusterv1.ClusterLabelName: kindWorkloadCluster.Name},
		}
		err := k8sClient.List(context.TODO(), machineList, listOptions...)
		if err != nil {
			return false
		}
		for i := range machineList.Items {
			m := machineList.Items[i]
			if m.Status.Phase == string(clusterv1.MachinePhaseRunning) {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(BeTrue())

})
