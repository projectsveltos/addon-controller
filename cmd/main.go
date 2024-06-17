/*
Copyright 2022-23 projectsveltos.io. All rights reserved.

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
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	_ "embed"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/api/v1beta1/index"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/crd"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	logsettings "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	//+kubebuilder:scaffold:imports
)

var (
	setupLog             = ctrl.Log.WithName("setup")
	diagnosticsAddress   string
	insecureDiagnostics  bool
	shardKey             string
	workers              int
	concurrentReconciles int
	agentInMgmtCluster   bool
	reportMode           controllers.ReportMode
	tmpReportMode        int
	restConfigQPS        float32
	restConfigBurst      int
	webhookPort          int
	syncPeriod           time.Duration
	conflictRetryTime    time.Duration
	version              string
	healthAddr           string
	profilerAddress      string
)

const (
	addonComplianceTimer = 5
	defaultReconcilers   = 10
	defaultWorkers       = 20
	defaulReportMode     = int(controllers.CollectFromManagementCluster)
	mebibytes_bytes      = 1 << 20
	gibibytes_per_bytes  = 1 << 30
)

// Add RBAC for the authorized diagnostics endpoint.
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

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

	reportMode = controllers.ReportMode(tmpReportMode)

	ctrl.SetLogger(klog.Background())
	ctrlOptions := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                getDiagnosticsOptions(),
		HealthProbeBindAddress: healthAddr,
		WebhookServer: webhook.NewServer(
			webhook.Options{
				Port: webhookPort,
			}),
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
		},
		PprofBindAddress: profilerAddress,
	}

	restConfig := ctrl.GetConfigOrDie()
	restConfig.QPS = restConfigQPS
	restConfig.Burst = restConfigBurst

	mgr, err := ctrl.NewManager(restConfig, ctrlOptions)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()
	controllers.SetManagementClusterAccess(mgr.GetClient(), mgr.GetConfig())

	logsettings.RegisterForLogSettings(ctx,
		libsveltosv1beta1.ComponentAddonManager, ctrl.Log.WithName("log-setter"),
		ctrl.GetConfigOrDie())

	debug.SetMemoryLimit(gibibytes_per_bytes)
	go printMemUsage(ctrl.Log.WithName("memory-usage"))

	startControllersAndWatchers(ctx, mgr)

	setupChecks(mgr)
	controllers.SetVersion(version)

	setupIndexes(ctx, mgr)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initFlags(fs *pflag.FlagSet) {
	fs.IntVar(&tmpReportMode,
		"report-mode",
		defaulReportMode,
		"Indicates how ReportSummaries need to be collected")

	fs.BoolVar(&agentInMgmtCluster,
		"agent-in-mgmt-cluster",
		false,
		"When set, indicates drift-detection-manager needs to be started in the management cluster")

	fs.StringVar(&diagnosticsAddress, "diagnostics-address", ":8443",
		"The address the diagnostics endpoint binds to. Per default metrics are served via https and with"+
			"authentication/authorization. To serve via http and without authentication/authorization set --insecure-diagnostics."+
			"If --insecure-diagnostics is not set the diagnostics endpoint also serves pprof endpoints")

	fs.BoolVar(&insecureDiagnostics, "insecure-diagnostics", false,
		"Enable insecure diagnostics serving. For more details see the description of --diagnostics-address.")

	fs.StringVar(&shardKey,
		"shard-key",
		"",
		"If set, only clusters will annotation matching this shard key will be reconciled by this deployment")

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

	fs.StringVar(&version,
		"version",
		"",
		"current sveltos version")

	fs.StringVar(&healthAddr, "health-addr", ":9440",
		"The address the health endpoint binds to.")

	fs.StringVar(&profilerAddress, "profiler-address", "",
		"Bind address to expose the pprof profiler (e.g. localhost:6060)")

	const defautlRestConfigQPS = 20
	fs.Float32Var(&restConfigQPS, "kube-api-qps", defautlRestConfigQPS,
		fmt.Sprintf("Maximum queries per second from the controller client to the Kubernetes API server. Defaults to %d",
			defautlRestConfigQPS))

	const defaultRestConfigBurst = 30
	fs.IntVar(&restConfigBurst, "kube-api-burst", defaultRestConfigBurst,
		fmt.Sprintf("Maximum number of queries that should be allowed in one burst from the controller client to the Kubernetes API server. Default %d",
			defaultRestConfigBurst))

	const defaultWebhookPort = 9443
	fs.IntVar(&webhookPort, "webhook-port", defaultWebhookPort,
		"Webhook Server port")

	const defaultSyncPeriod = 10
	fs.DurationVar(&syncPeriod, "sync-period", defaultSyncPeriod*time.Minute,
		fmt.Sprintf("The minimum interval at which watched resources are reconciled (e.g. 15m). Default: %d minutes",
			defaultSyncPeriod))

	const defaultConflictRetryTime = 30
	fs.DurationVar(&conflictRetryTime, "conflict-retry-time", defaultConflictRetryTime*time.Second,
		fmt.Sprintf("The minimum interval at which watched ClusterProfile with conflicts are retried. Defaul: %d seconds",
			defaultConflictRetryTime))
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

// fluxCRDHandler restarts process if a Flux CRD is updated
func fluxCRDHandler(gvk *schema.GroupVersionKind) {
	if gvk.Group == sourcev1.GroupVersion.Group {
		if killErr := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); killErr != nil {
			panic("kill -TERM failed")
		}
	}
}

// isFluxInstalled returns true if Flux is installed, false otherwise
func isFluxInstalled(ctx context.Context, c client.Client) (bool, error) {
	gitRepositoryCRD := &apiextensionsv1.CustomResourceDefinition{}

	err := c.Get(ctx, types.NamespacedName{Name: "gitrepositories.source.toolkit.fluxcd.io"},
		gitRepositoryCRD)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func capiWatchers(ctx context.Context, mgr ctrl.Manager, watchersForCAPI []watcherForCAPI, logger logr.Logger) {
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
				setupLog.V(logsettings.LogInfo).Info("CAPI present. Start CAPI watchers")
				for i := range watchersForCAPI {
					watcher := watchersForCAPI[i]
					err = watcher.WatchForCAPI(mgr, watcher.GetController())
					if err != nil {
						setupLog.V(logsettings.LogInfo).Info(
							fmt.Sprintf("failed to start CAPI watcher: %v", err))
						continue
					}
				}
			}
			return
		}
	}
}

func fluxWatchers(ctx context.Context, mgr ctrl.Manager, watchersForFlux []watcherForFlux, logger logr.Logger) {
	const maxRetries = 20
	retries := 0
	for {
		fluxPresent, err := isFluxInstalled(ctx, mgr.GetClient())
		if err != nil {
			if retries < maxRetries {
				logger.Info(fmt.Sprintf("failed to verify if Flux is present: %v", err))
				time.Sleep(time.Second)
			}
			retries++
		} else {
			if !fluxPresent {
				setupLog.V(logsettings.LogInfo).Info("Flux currently not present. Starting CRD watcher")
				go crd.WatchCustomResourceDefinition(ctx, mgr.GetConfig(), fluxCRDHandler, setupLog)
			} else {
				setupLog.V(logsettings.LogInfo).Info("Flux present. Start Flux watchers")
				for i := range watchersForFlux {
					watcher := watchersForFlux[i]
					err = watcher.WatchForFlux(mgr, watcher.GetController())
					if err != nil {
						continue
					}
				}
			}
			return
		}
	}
}

type watcherForCAPI interface {
	WatchForCAPI(mgr manager.Manager, c controller.Controller) error
	GetController() controller.Controller
}

type watcherForFlux interface {
	WatchForFlux(mgr manager.Manager, c controller.Controller) error
	GetController() controller.Controller
}

func startWatchers(ctx context.Context, mgr manager.Manager,
	watchersForCAPI []watcherForCAPI, watchersForFlux []watcherForFlux) {

	go capiWatchers(ctx, mgr, watchersForCAPI, setupLog)

	go fluxWatchers(ctx, mgr, watchersForFlux, setupLog)
}

func getProfileReconciler(mgr manager.Manager) *controllers.ProfileReconciler {
	return &controllers.ProfileReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		SetMap:               make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterMap:           make(map[corev1.ObjectReference]*libsveltosset.Set),
		Profiles:             make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		ClusterLabels:        make(map[corev1.ObjectReference]map[string]string),
		Mux:                  sync.Mutex{},
		ConcurrentReconciles: concurrentReconciles,
		Logger:               ctrl.Log.WithName("profilereconciler"),
	}
}

func getClusterProfileReconciler(mgr manager.Manager) *controllers.ClusterProfileReconciler {
	return &controllers.ClusterProfileReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		ClusterSetMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterMap:           make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterProfiles:      make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		ClusterLabels:        make(map[corev1.ObjectReference]map[string]string),
		Mux:                  sync.Mutex{},
		ConcurrentReconciles: concurrentReconciles,
		Logger:               ctrl.Log.WithName("clusterprofilereconciler"),
	}
}

func getClusterSummaryReconciler(ctx context.Context, mgr manager.Manager) *controllers.ClusterSummaryReconciler {
	d := deployer.GetClient(ctx, ctrl.Log.WithName("deployer"), mgr.GetClient(), workers)
	controllers.RegisterFeatures(d, setupLog)

	return &controllers.ClusterSummaryReconciler{
		Config:               mgr.GetConfig(),
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		ShardKey:             shardKey,
		ReportMode:           reportMode,
		AgentInMgmtCluster:   agentInMgmtCluster,
		Deployer:             d,
		ClusterMap:           make(map[corev1.ObjectReference]*libsveltosset.Set),
		ReferenceMap:         make(map[corev1.ObjectReference]*libsveltosset.Set),
		PolicyMux:            sync.Mutex{},
		ConcurrentReconciles: concurrentReconciles,
		ConflictRetryTime:    conflictRetryTime,
		Logger:               ctrl.Log.WithName("clustersummaryreconciler"),
	}
}

func getSetReconciler(mgr manager.Manager) *controllers.SetReconciler {
	return &controllers.SetReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		ConcurrentReconciles: concurrentReconciles,
		Mux:                  sync.Mutex{},
		ClusterMap:           make(map[corev1.ObjectReference]*libsveltosset.Set),
		SetMap:               make(map[corev1.ObjectReference]*libsveltosset.Set),
		Sets:                 make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		ClusterLabels:        make(map[corev1.ObjectReference]map[string]string),
		Logger:               ctrl.Log.WithName("setreconciler"),
	}
}

func getClusterSetReconciler(mgr manager.Manager) *controllers.ClusterSetReconciler {
	return &controllers.ClusterSetReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		ConcurrentReconciles: concurrentReconciles,
		Mux:                  sync.Mutex{},
		ClusterMap:           make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterSetMap:        make(map[corev1.ObjectReference]*libsveltosset.Set),
		ClusterSets:          make(map[corev1.ObjectReference]libsveltosv1beta1.Selector),
		ClusterLabels:        make(map[corev1.ObjectReference]map[string]string),
		Logger:               ctrl.Log.WithName("clustersetreconciler"),
	}
}

// getDiagnosticsOptions returns metrics options which can be used to configure a Manager.
func getDiagnosticsOptions() metricsserver.Options {
	// If "--insecure-diagnostics" is set, serve metrics via http
	// and without authentication/authorization.
	if insecureDiagnostics {
		return metricsserver.Options{
			BindAddress:   diagnosticsAddress,
			SecureServing: false,
		}
	}

	// If "--insecure-diagnostics" is not set, serve metrics via https
	// and with authentication/authorization. As the endpoint is protected,
	// we also serve pprof endpoints and an endpoint to change the log level.
	return metricsserver.Options{
		BindAddress:    diagnosticsAddress,
		SecureServing:  true,
		FilterProvider: filters.WithAuthenticationAndAuthorization,
		ExtraHandlers: map[string]http.Handler{
			// Add pprof handler.
			"/debug/pprof/":        http.HandlerFunc(pprof.Index),
			"/debug/pprof/cmdline": http.HandlerFunc(pprof.Cmdline),
			"/debug/pprof/profile": http.HandlerFunc(pprof.Profile),
			"/debug/pprof/symbol":  http.HandlerFunc(pprof.Symbol),
			"/debug/pprof/trace":   http.HandlerFunc(pprof.Trace),
			"/debug/pprof/heap":    pprof.Handler("heap"),
		},
	}
}

// startControllers starts all reconcilers:
// - ClusterProfile/Profile
// - clusterSummary
// - ClusterSet/Set
//
// It also starts needed watchers:
// - cluster API watchers for ClusterProfile/Profile, ClusterSet/Set
// - Flux watcher for ClusterSummary
func startControllersAndWatchers(ctx context.Context, mgr manager.Manager) {
	var clusterProfileReconciler *controllers.ClusterProfileReconciler
	var profileReconciler *controllers.ProfileReconciler
	var clusterSetReconciler *controllers.ClusterSetReconciler
	var setReconciler *controllers.SetReconciler

	var err error

	//+kubebuilder:scaffold:builder

	watchersForCAPI := make([]watcherForCAPI, 0)
	watchersForFlux := make([]watcherForFlux, 0)

	if shardKey == "" {
		// Only if shardKey is not set, start ClusterProfile/Profile and ClusterSet/Set reconcilers.
		// When shardKey is set, only ClusterSummary reconciler will be started and only
		// cluster matching the shardkey will be managed
		clusterProfileReconciler = getClusterProfileReconciler(mgr)
		err = clusterProfileReconciler.SetupWithManager(mgr)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", configv1beta1.ClusterProfileKind)
			os.Exit(1)
		}
		watchersForCAPI = append(watchersForCAPI, clusterProfileReconciler)

		profileReconciler = getProfileReconciler(mgr)
		err = profileReconciler.SetupWithManager(mgr)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", configv1beta1.ProfileKind)
			os.Exit(1)
		}
		watchersForCAPI = append(watchersForCAPI, profileReconciler)

		clusterSetReconciler = getClusterSetReconciler(mgr)
		err = clusterSetReconciler.SetupWithManager(mgr)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", libsveltosv1beta1.ClusterSetKind)
			os.Exit(1)
		}
		watchersForCAPI = append(watchersForCAPI, clusterSetReconciler)

		setReconciler = getSetReconciler(mgr)
		err = setReconciler.SetupWithManager(mgr)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", libsveltosv1beta1.SetKind)
			os.Exit(1)
		}
		watchersForCAPI = append(watchersForCAPI, setReconciler)
	}

	clusterSummaryReconciler := getClusterSummaryReconciler(ctx, mgr)
	err = clusterSummaryReconciler.SetupWithManager(ctx, mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", configv1beta1.ClusterSummaryKind)
		os.Exit(1)
	}
	watchersForCAPI = append(watchersForCAPI, clusterSummaryReconciler)
	watchersForFlux = append(watchersForFlux, clusterSummaryReconciler)

	startWatchers(ctx, mgr, watchersForCAPI, watchersForFlux)
}

// printMemUsage memory stats. Call GC
func printMemUsage(logger logr.Logger) {
	for {
		time.Sleep(time.Minute)
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		// For info on each, see: /pkg/runtime/#MemStats
		l := logger.WithValues("Alloc (MiB)", bToMb(m.Alloc)).
			WithValues("TotalAlloc (MiB)", bToMb(m.TotalAlloc)).
			WithValues("Sys (MiB)", bToMb(m.Sys)).
			WithValues("NumGC", m.NumGC)
		l.V(logsettings.LogInfo).Info("memory stats")
		runtime.GC()
	}
}

func bToMb(b uint64) uint64 {
	return b / mebibytes_bytes
}
