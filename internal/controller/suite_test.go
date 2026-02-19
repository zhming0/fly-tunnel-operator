package controller_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/zhiming0/fly-frp-tunnel/internal/controller"
	"github.com/zhiming0/fly-frp-tunnel/internal/fakefly"
	"github.com/zhiming0/fly-frp-tunnel/internal/flyio"
	"github.com/zhiming0/fly-frp-tunnel/internal/tunnel"
)

var (
	testEnv    *envtest.Environment
	cfg        *rest.Config
	k8sClient  client.Client
	testCtx    context.Context
	testCancel context.CancelFunc

	// Shared fake Fly.io server for all integration tests.
	flyServer *fakefly.Server
)

const operatorNamespace = "fly-frp-tunnel-system"

func TestMain(m *testing.M) {
	log.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	testCtx, testCancel = context.WithCancel(context.Background())

	// Find envtest binaries.
	binDir := findEnvtestBinDir()
	if binDir != "" {
		os.Setenv("KUBEBUILDER_ASSETS", binDir)
	}

	testEnv = &envtest.Environment{}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic("failed to create client: " + err.Error())
	}

	// Start the shared fake Fly.io server.
	flyServer = fakefly.NewServer()

	// Start a single manager + controller for all tests.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		panic("failed to create manager: " + err.Error())
	}

	flyClient := flyio.NewClient("test-token").
		WithBaseURL(flyServer.URL).
		WithGraphQLURL(flyServer.URL + "/graphql")

	tunnelMgr := tunnel.NewManager(flyClient, mgr.GetClient(), tunnel.Config{
		FlyApp:            "test-app",
		FlyRegion:         "syd",
		FlyMachineSize:    "shared-cpu-1x",
		FrpsImage:         "snowdreamtech/frps:latest",
		FrpcImage:         "snowdreamtech/frpc:latest",
		OperatorNamespace: operatorNamespace,
	})

	reconciler := controller.NewServiceReconciler(
		mgr.GetClient(),
		tunnelMgr,
		controller.DefaultLoadBalancerClass,
	)
	if err := reconciler.SetupWithManager(mgr); err != nil {
		panic("failed to setup reconciler: " + err.Error())
	}

	mgrCtx, mgrCancel := context.WithCancel(testCtx)
	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			log.Log.Error(err, "manager stopped")
		}
	}()

	code := m.Run()

	mgrCancel()
	testCancel()
	flyServer.Close()
	_ = testEnv.Stop()
	os.Exit(code)
}

func findEnvtestBinDir() string {
	// Check if already set.
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "kube-apiserver")); err == nil {
			return dir
		}
	}

	// Look in common locations.
	candidates := []string{}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		// setup-envtest stores binaries here.
		matches, _ := filepath.Glob(filepath.Join(homeDir, ".local/share/kubebuilder-envtest/k8s/*"))
		candidates = append(candidates, matches...)
	}

	// Also check /usr/local/kubebuilder/bin.
	candidates = append(candidates, "/usr/local/kubebuilder/bin")

	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "kube-apiserver")); err == nil {
			return c
		}
	}
	return ""
}
