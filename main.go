package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/zhming0/fly-tunnel-operator/internal/controller"
	"github.com/zhming0/fly-tunnel-operator/internal/flyio"
	"github.com/zhming0/fly-tunnel-operator/internal/tunnel"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr       string
		healthProbeAddr   string
		flyAPIToken       string
		flyOrg            string
		flyRegion         string
		flyMachineSize    string
		loadBalancerClass string
		frpsImage         string
		frpcImage         string
		operatorNamespace string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", ":8081", "The address the health probe endpoint binds to.")
	flag.StringVar(&flyAPIToken, "fly-api-token", "", "Fly.io API token. Can also be set via FLY_API_TOKEN env var.")
	flag.StringVar(&flyOrg, "fly-org", "", "Fly.io organization slug. Can also be set via FLY_ORG env var.")
	flag.StringVar(&flyRegion, "fly-region", "", "Fly.io region. Can also be set via FLY_REGION env var.")
	flag.StringVar(&flyMachineSize, "fly-machine-size", "shared-cpu-1x", "Fly.io Machine size preset.")
	flag.StringVar(&loadBalancerClass, "load-balancer-class", controller.DefaultLoadBalancerClass, "LoadBalancer class string to watch.")
	flag.StringVar(&frpsImage, "frps-image", "snowdreamtech/frps:latest", "Container image for frps.")
	flag.StringVar(&frpcImage, "frpc-image", "snowdreamtech/frpc:latest", "Container image for frpc.")
	flag.StringVar(&operatorNamespace, "namespace", "", "Namespace for frpc deployments. Can also be set via OPERATOR_NAMESPACE env var.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	// Resolve configuration from flags and environment variables.
	if flyAPIToken == "" {
		flyAPIToken = os.Getenv("FLY_API_TOKEN")
	}
	if flyOrg == "" {
		flyOrg = os.Getenv("FLY_ORG")
	}
	if flyRegion == "" {
		flyRegion = os.Getenv("FLY_REGION")
	}
	if operatorNamespace == "" {
		operatorNamespace = os.Getenv("OPERATOR_NAMESPACE")
	}
	if operatorNamespace == "" {
		operatorNamespace = "fly-tunnel-operator-system"
	}

	// Validate required configuration.
	if flyAPIToken == "" {
		setupLog.Error(nil, "fly-api-token or FLY_API_TOKEN is required")
		os.Exit(1)
	}
	if flyOrg == "" {
		setupLog.Error(nil, "fly-org or FLY_ORG is required")
		os.Exit(1)
	}
	if flyRegion == "" {
		setupLog.Error(nil, "fly-region or FLY_REGION is required")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthProbeAddr,
		LeaderElection:         true,
		LeaderElectionID:       "fly-tunnel-operator",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Create the Fly.io API client.
	flyClient := flyio.NewClient(flyAPIToken)

	// Create the tunnel manager.
	tunnelMgr := tunnel.NewManager(flyClient, mgr.GetClient(), tunnel.Config{
		FlyOrg:            flyOrg,
		FlyRegion:         flyRegion,
		FlyMachineSize:    flyMachineSize,
		FrpsImage:         frpsImage,
		FrpcImage:         frpcImage,
		OperatorNamespace: operatorNamespace,
	})

	// Set up the Service reconciler.
	reconciler := controller.NewServiceReconciler(mgr.GetClient(), tunnelMgr, loadBalancerClass)
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Service")
		os.Exit(1)
	}

	// Add health and readiness checks.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"flyOrg", flyOrg,
		"flyRegion", flyRegion,
		"loadBalancerClass", loadBalancerClass,
		"namespace", operatorNamespace,
	)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
