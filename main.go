/*
Copyright 2022.

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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	_ "embed"

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	"github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	"github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
	"github.com/projectsveltos/sveltos-manager/api/v1alpha1/index"
	"github.com/projectsveltos/sveltos-manager/controllers"
	//+kubebuilder:scaffold:imports
)

//go:generate bash hack/get_version.sh
//go:embed version.txt
//nolint:grouper // run generate embed
var version string

var (
	setupLog             = ctrl.Log.WithName("setup")
	metricsAddr          string
	enableLeaderElection bool
	probeAddr            string
	workers              int
	concurrentReconciles int
)

const (
	defaultReconcilers = 10
	defaultWorkers     = 20
)

func main() {
	scheme, err := controllers.InitScheme()
	if err != nil {
		os.Exit(1)
	}

	klog.InitFlags(nil)

	initFlags(pflag.CommandLine)
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	ctrl.SetLogger(klog.Background())

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "86dad58d.projectsveltos.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()

	d := deployer.GetClient(ctx, ctrl.Log.WithName("deployer"), mgr.GetClient(), workers)
	controllers.RegisterFeatures(d, setupLog)

	controllers.SetManagementClusterAccess(mgr.GetClient(), mgr.GetConfig())

	logsettings.RegisterForLogSettings(ctx,
		libsveltosv1alpha1.ComponentSveltosManager, ctrl.Log.WithName("log-setter"),
		ctrl.GetConfigOrDie())

	var clusterProfileController controller.Controller
	clusterProfileReconciler := &controllers.ClusterProfileReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		ClusterMap:           make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterProfileMap:    make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterProfiles:      make(map[corev1.ObjectReference]configv1alpha1.Selector),
		Mux:                  sync.Mutex{},
		ConcurrentReconciles: concurrentReconciles,
	}
	clusterProfileController, err = clusterProfileReconciler.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", configv1alpha1.ClusterProfileKind)
		os.Exit(1)
	}

	var clusterSummaryController controller.Controller
	clusterSummaryReconciler := &controllers.ClusterSummaryReconciler{
		Config:               mgr.GetConfig(),
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		Deployer:             d,
		ClusterMap:           make(map[corev1.ObjectReference]*libsveltosset.Set),
		ReferenceMap:         make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterSummaryMap:    make(map[types.NamespacedName]*libsveltosset.Set),
		PolicyMux:            sync.Mutex{},
		ConcurrentReconciles: concurrentReconciles,
	}
	clusterSummaryController, err = clusterSummaryReconciler.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", configv1alpha1.ClusterSummaryKind)
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	setupChecks(mgr)

	setupIndexes(ctx, mgr)

	go capiWatchers(ctx, mgr,
		clusterProfileReconciler, clusterProfileController,
		clusterSummaryReconciler, clusterSummaryController,
		setupLog)

	setupLog.Info(fmt.Sprintf("starting manager (version %s)", version))
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initFlags(fs *pflag.FlagSet) {
	fs.StringVar(&metricsAddr,
		"metrics-bind-address",
		":8080",
		"The address the metric endpoint binds to.")

	fs.StringVar(&probeAddr,
		"health-probe-bind-address",
		":8081",
		"The address the probe endpoint binds to.")

	fs.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	fs.IntVar(
		&workers,
		"worker-number",
		defaultWorkers,
		"Number of worker. Workers are used to deploy features in CAPI clusters")

	fs.IntVar(
		&concurrentReconciles,
		"concurrent-reconciles",
		defaultReconcilers,
		"concurrent reconciles is the maximum number of concurrent Reconciles which can be run. Defaults to 10")
}

func setupIndexes(ctx context.Context, mgr ctrl.Manager) {
	if err := index.AddDefaultIndexes(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to setup indexes")
		os.Exit(1)
	}
}

func setupChecks(mgr ctrl.Manager) {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
}

// capiCRDHandler restarts process if a CAPI CRD is updated
func capiCRDHandler(gvk *schema.GroupVersionKind) {
	if gvk.Group == clusterv1.GroupVersion.Group {
		if killErr := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); killErr != nil {
			panic("kill -TERM failed")
		}
	}
}

// isCAPIInstalled returns true if CAPI is installed, false otherwise
func isCAPIInstalled(ctx context.Context, c client.Client) (bool, error) {
	clusterCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "clusters.cluster.x-k8s.io"}, clusterCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func capiWatchers(ctx context.Context, mgr ctrl.Manager,
	clusterProfileReconciler *controllers.ClusterProfileReconciler, clusterProfileController controller.Controller,
	clusterSummaryReconciler *controllers.ClusterSummaryReconciler, clusterSummaryController controller.Controller,
	logger logr.Logger) {

	const maxRetries = 20
	retries := 0
	for {
		capiPresent, err := isCAPIInstalled(ctx, mgr.GetClient())
		if err != nil {
			if retries < maxRetries {
				logger.Info(fmt.Sprintf("failed to verify if CAPI is present: %v", err))
				time.Sleep(time.Second)
			}
			retries++
		} else {
			if !capiPresent {
				setupLog.V(logsettings.LogInfo).Info("CAPI currently not present. Starting CRD watcher")
				go crd.WatchCustomResourceDefinition(ctx, mgr.GetConfig(), capiCRDHandler, setupLog)
			} else {
				setupLog.V(logsettings.LogInfo).Info("CAPI present.")
				err = clusterProfileReconciler.WatchForCAPI(mgr, clusterProfileController)
				if err != nil {
					continue
				}
				err = clusterSummaryReconciler.WatchForCAPI(mgr, clusterSummaryController)
				if err != nil {
					continue
				}
			}
			return
		}
	}
}
