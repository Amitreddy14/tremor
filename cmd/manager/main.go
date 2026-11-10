// Command manager is the Tremor operator entrypoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/controller"
	"github.com/Amitreddy14/tremor/internal/fault"
	"github.com/Amitreddy14/tremor/internal/metrics"
	"github.com/Amitreddy14/tremor/internal/prober"
	"github.com/Amitreddy14/tremor/internal/runstore"
	"github.com/Amitreddy14/tremor/internal/runstore/mongostore"
	"github.com/Amitreddy14/tremor/internal/version"
)

func main() {
	var (
		metricsAddr string
		probeAddr   string
		leaderElect bool
		showVersion bool
		historyURI  string
		clusterName string
		stressImage string
		netemImage  string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address the Prometheus metrics endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address the health probe endpoint binds to")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election so only one replica is active")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.StringVar(&historyURI, "history-mongo-uri", os.Getenv("TREMOR_HISTORY_MONGO_URI"),
		"MongoDB URI for run history (default $TREMOR_HISTORY_MONGO_URI); history is disabled when empty")
	flag.StringVar(&clusterName, "cluster-name", os.Getenv("TREMOR_CLUSTER_NAME"),
		"label stored with each run (default $TREMOR_CLUSTER_NAME)")
	flag.StringVar(&stressImage, "stress-image", fault.DefaultStressImage,
		"image for CPU and memory stress containers; needs sh, yes, sleep, kill and dd (BusyBox has them)")
	flag.StringVar(&netemImage, "netem-image", fault.DefaultNetemImage,
		"image for network latency and loss containers; needs sh, ip, tc (with netem), awk and grep (see Dockerfile.netem)")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	if showVersion {
		fmt.Println(version.String())
		return
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	setupLog := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tremorv1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		// DependencyOutage and NodeDrain must see their own writes: a cached
		// read just after scaling a workload down or cordoning a node could miss
		// the hold marker and skip the restore. These kinds are rarely read, so
		// go straight to the API server (which also needs no node watch).
		Client: client.Options{Cache: &client.CacheOptions{
			DisableFor: []client.Object{&appsv1.Deployment{}, &appsv1.StatefulSet{}, &corev1.Node{}},
		}},
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "tremor-manager.tremor.dev",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// History is optional: with no URI the operator behaves exactly as before.
	var history runstore.Store
	if historyURI != "" {
		st, err := mongostore.New(context.Background(), historyURI, mongostore.DefaultDatabase)
		if err != nil {
			setupLog.Error(err, "unable to configure history store")
			os.Exit(1)
		}
		// Indexes are best effort: the database may still be starting, and the
		// operator must not depend on it (ADR 0004).
		ictx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := st.EnsureIndexes(ictx); err != nil {
			setupLog.Info("could not create history indexes yet; continuing", "error", err.Error())
		}
		cancel()
		history = st
		setupLog.Info("run history enabled")
	}

	if err := (&controller.ChaosExperimentReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Prober:          prober.NewManager(),
		Metrics:         metrics.New(ctrlmetrics.Registry),
		History:         history,
		ClusterName:     clusterName,
		OperatorVersion: version.Version,
		StressImage:     stressImage,
		NetemImage:      netemImage,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "version", version.String())
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
