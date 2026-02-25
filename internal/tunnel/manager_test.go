package tunnel_test

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/zhming0/fly-tunnel-operator/internal/fakefly"
	"github.com/zhming0/fly-tunnel-operator/internal/flyio"
	"github.com/zhming0/fly-tunnel-operator/internal/tunnel"
)

const testNamespace = "fly-tunnel-operator-system"

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func newTestConfig() tunnel.Config {
	return tunnel.Config{
		FlyOrg:            "personal",
		FlyRegion:         "syd",
		FlyMachineSize:    "shared-cpu-1x",
		FrpsImage:         "snowdreamtech/frps:0.61.1@sha256:f18a0fd489b14d1fdfc68069239722f2ce3ab76b644aeb75219bf1df1b4bcea9",
		FrpcImage:         "snowdreamtech/frpc:0.61.1@sha256:55de10291630ca31e98a07120ad73e25977354a2307731cb28b0dc42f6987c59",
		OperatorNamespace: testNamespace,
	}
}

func newTestFlyClient(server *fakefly.Server) *flyio.Client {
	return flyio.NewClient("test-token").
		WithBaseURL(server.URL).
		WithGraphQLURL(server.URL + "/graphql")
}

func testService(name, namespace string, ports ...corev1.ServicePort) *corev1.Service {
	lbClass := "fly-tunnel-operator.dev/lb"
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: make(map[string]string),
		},
		Spec: corev1.ServiceSpec{
			Type:              corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass: &lbClass,
			Ports:             ports,
		},
	}
}

func TestProvision(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	svc := testService("envoy-gateway", "envoy-gateway-system",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
		corev1.ServicePort{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP},
	)

	result, err := mgr.Provision(context.Background(), svc)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	// Verify tunnel result.
	if result.FlyApp == "" {
		t.Error("expected fly app name")
	}
	if result.MachineID == "" {
		t.Error("expected machine ID")
	}
	if result.PublicIP == "" {
		t.Error("expected public IP")
	}
	if result.IPID == "" {
		t.Error("expected IP ID")
	}
	if result.FrpcDeployment == "" {
		t.Error("expected frpc deployment name")
	}

	// Verify fly.io App was created.
	if server.AppCount() != 1 {
		t.Errorf("expected 1 app, got %d", server.AppCount())
	}
	if !server.HasApp(result.FlyApp) {
		t.Errorf("expected app %q to exist", result.FlyApp)
	}

	// Verify fly.io Machine was created.
	if server.MachineCount() != 1 {
		t.Errorf("expected 1 machine, got %d", server.MachineCount())
	}

	// Verify dedicated IPv4 was allocated.
	if server.IPCount() != 1 {
		t.Errorf("expected 1 IP, got %d", server.IPCount())
	}

	// Verify frpc ConfigMap was created.
	var cm corev1.ConfigMap
	err = kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment + "-config",
		Namespace: testNamespace,
	}, &cm)
	if err != nil {
		t.Fatalf("expected frpc ConfigMap to exist: %v", err)
	}

	config, ok := cm.Data["frpc.toml"]
	if !ok {
		t.Fatal("expected frpc.toml in ConfigMap data")
	}
	if config == "" {
		t.Error("expected non-empty frpc.toml config")
	}

	// Verify frpc Deployment was created.
	var deploy appsv1.Deployment
	err = kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment,
		Namespace: testNamespace,
	}, &deploy)
	if err != nil {
		t.Fatalf("expected frpc Deployment to exist: %v", err)
	}

	if *deploy.Spec.Replicas != 1 {
		t.Errorf("expected 1 replica, got %d", *deploy.Spec.Replicas)
	}

	container := deploy.Spec.Template.Spec.Containers[0]
	if container.Image != "snowdreamtech/frpc:0.61.1@sha256:55de10291630ca31e98a07120ad73e25977354a2307731cb28b0dc42f6987c59" {
		t.Errorf("expected frpc image, got %q", container.Image)
	}
}

func TestProvision_MultipleServices(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	// Provision two independent services.
	svc1 := testService("envoy-gateway", "envoy-gateway-system",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	)
	svc2 := testService("minecraft", "default",
		corev1.ServicePort{Name: "game", Port: 25565, Protocol: corev1.ProtocolTCP},
	)

	result1, err := mgr.Provision(context.Background(), svc1)
	if err != nil {
		t.Fatalf("Provision svc1 failed: %v", err)
	}

	result2, err := mgr.Provision(context.Background(), svc2)
	if err != nil {
		t.Fatalf("Provision svc2 failed: %v", err)
	}

	// Each should get its own App, Machine, and IP.
	if server.AppCount() != 2 {
		t.Errorf("expected 2 apps, got %d", server.AppCount())
	}
	if server.MachineCount() != 2 {
		t.Errorf("expected 2 machines, got %d", server.MachineCount())
	}
	if server.IPCount() != 2 {
		t.Errorf("expected 2 IPs, got %d", server.IPCount())
	}

	// Fly apps should differ.
	if result1.FlyApp == result2.FlyApp {
		t.Errorf("expected different fly apps, both got %s", result1.FlyApp)
	}

	// IPs should differ.
	if result1.PublicIP == result2.PublicIP {
		t.Errorf("expected different public IPs, both got %s", result1.PublicIP)
	}

	// Machine IDs should differ.
	if result1.MachineID == result2.MachineID {
		t.Errorf("expected different machine IDs, both got %s", result1.MachineID)
	}
}

func TestTeardown(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	svc := testService("envoy-gateway", "envoy-gateway-system",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	)

	result, err := mgr.Provision(context.Background(), svc)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	// Store tunnel state in annotations (as the controller would).
	svc.Annotations[tunnel.AnnotationFlyApp] = result.FlyApp
	svc.Annotations[tunnel.AnnotationMachineID] = result.MachineID
	svc.Annotations[tunnel.AnnotationFrpcDeployment] = result.FrpcDeployment
	svc.Annotations[tunnel.AnnotationIPID] = result.IPID
	svc.Annotations[tunnel.AnnotationPublicIP] = result.PublicIP

	// Verify resources exist before teardown.
	if server.AppCount() != 1 {
		t.Fatalf("expected 1 app before teardown, got %d", server.AppCount())
	}
	if server.MachineCount() != 1 {
		t.Fatalf("expected 1 machine before teardown, got %d", server.MachineCount())
	}
	if server.IPCount() != 1 {
		t.Fatalf("expected 1 IP before teardown, got %d", server.IPCount())
	}

	err = mgr.Teardown(context.Background(), svc)
	if err != nil {
		t.Fatalf("Teardown failed: %v", err)
	}

	// Verify App was deleted.
	if server.AppCount() != 0 {
		t.Errorf("expected 0 apps after teardown, got %d", server.AppCount())
	}

	// Verify Machine was deleted.
	if server.MachineCount() != 0 {
		t.Errorf("expected 0 machines after teardown, got %d", server.MachineCount())
	}

	// Verify IP was released.
	if server.IPCount() != 0 {
		t.Errorf("expected 0 IPs after teardown, got %d", server.IPCount())
	}

	// Verify frpc Deployment was deleted.
	var deploy appsv1.Deployment
	err = kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment,
		Namespace: testNamespace,
	}, &deploy)
	if err == nil {
		t.Error("expected frpc Deployment to be deleted")
	}

	// Verify frpc ConfigMap was deleted.
	var cm corev1.ConfigMap
	err = kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment + "-config",
		Namespace: testNamespace,
	}, &cm)
	if err == nil {
		t.Error("expected frpc ConfigMap to be deleted")
	}
}

func TestUpdate(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	svc := testService("envoy-gateway", "envoy-gateway-system",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	)

	result, err := mgr.Provision(context.Background(), svc)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	// Store tunnel state in annotations.
	svc.Annotations[tunnel.AnnotationFlyApp] = result.FlyApp
	svc.Annotations[tunnel.AnnotationMachineID] = result.MachineID
	svc.Annotations[tunnel.AnnotationFrpcDeployment] = result.FrpcDeployment
	svc.Annotations[tunnel.AnnotationIPID] = result.IPID
	svc.Annotations[tunnel.AnnotationPublicIP] = result.PublicIP

	// Add a new port.
	svc.Spec.Ports = append(svc.Spec.Ports,
		corev1.ServicePort{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP},
	)

	err = mgr.Update(context.Background(), svc)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify ConfigMap was updated.
	var cm corev1.ConfigMap
	err = kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment + "-config",
		Namespace: testNamespace,
	}, &cm)
	if err != nil {
		t.Fatalf("expected ConfigMap to exist: %v", err)
	}

	config := cm.Data["frpc.toml"]
	if !containsString(config, "remotePort = 443") {
		t.Error("expected updated config to contain port 443")
	}

	// Verify Deployment has restart annotation.
	var deploy appsv1.Deployment
	err = kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment,
		Namespace: testNamespace,
	}, &deploy)
	if err != nil {
		t.Fatalf("expected Deployment to exist: %v", err)
	}

	if _, ok := deploy.Spec.Template.Annotations["fly-tunnel-operator.dev/restart-at"]; !ok {
		t.Error("expected restart annotation on Deployment pod template")
	}
}

func TestProvision_RegionOverride(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	var capturedRegion string
	server.OnCreateMachine = func(appName string, input flyio.CreateMachineInput) error {
		capturedRegion = input.Region
		return nil
	}

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	svc := testService("test", "default",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	)
	svc.Annotations[tunnel.AnnotationFlyRegion] = "iad"

	_, err := mgr.Provision(context.Background(), svc)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	if capturedRegion != "iad" {
		t.Errorf("expected region 'iad', got %q", capturedRegion)
	}
}

func TestProvision_DefaultFrpcResources(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	svc := testService("test", "default",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	)

	result, err := mgr.Provision(context.Background(), svc)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	var deploy appsv1.Deployment
	if err := kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment,
		Namespace: testNamespace,
	}, &deploy); err != nil {
		t.Fatalf("expected frpc Deployment to exist: %v", err)
	}

	res := deploy.Spec.Template.Spec.Containers[0].Resources

	wantCPUReq := resource.MustParse("10m")
	wantMemReq := resource.MustParse("32Mi")
	wantMemLim := resource.MustParse("128Mi")

	if !res.Requests.Cpu().Equal(wantCPUReq) {
		t.Errorf("cpu request: want %v, got %v", &wantCPUReq, res.Requests.Cpu())
	}
	if !res.Requests.Memory().Equal(wantMemReq) {
		t.Errorf("memory request: want %v, got %v", &wantMemReq, res.Requests.Memory())
	}
	if !res.Limits.Memory().Equal(wantMemLim) {
		t.Errorf("memory limit: want %v, got %v", &wantMemLim, res.Limits.Memory())
	}
	if _, hasCPULimit := res.Limits[corev1.ResourceCPU]; hasCPULimit {
		t.Error("expected no CPU limit")
	}
}

func TestProvision_FrpcResourceAnnotationOverrides(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	svc := testService("test", "default",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	)
	svc.Annotations[tunnel.AnnotationFrpcCPURequest] = "50m"
	svc.Annotations[tunnel.AnnotationFrpcCPULimit] = "200m"
	svc.Annotations[tunnel.AnnotationFrpcMemoryRequest] = "64Mi"
	svc.Annotations[tunnel.AnnotationFrpcMemoryLimit] = "256Mi"

	result, err := mgr.Provision(context.Background(), svc)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}

	var deploy appsv1.Deployment
	if err := kubeClient.Get(context.Background(), types.NamespacedName{
		Name:      result.FrpcDeployment,
		Namespace: testNamespace,
	}, &deploy); err != nil {
		t.Fatalf("expected frpc Deployment to exist: %v", err)
	}

	res := deploy.Spec.Template.Spec.Containers[0].Resources

	wantCPUReq := resource.MustParse("50m")
	wantCPULim := resource.MustParse("200m")
	wantMemReq := resource.MustParse("64Mi")
	wantMemLim := resource.MustParse("256Mi")

	if !res.Requests.Cpu().Equal(wantCPUReq) {
		t.Errorf("cpu request: want %v, got %v", &wantCPUReq, res.Requests.Cpu())
	}
	if !res.Limits.Cpu().Equal(wantCPULim) {
		t.Errorf("cpu limit: want %v, got %v", &wantCPULim, res.Limits.Cpu())
	}
	if !res.Requests.Memory().Equal(wantMemReq) {
		t.Errorf("memory request: want %v, got %v", &wantMemReq, res.Requests.Memory())
	}
	if !res.Limits.Memory().Equal(wantMemLim) {
		t.Errorf("memory limit: want %v, got %v", &wantMemLim, res.Limits.Memory())
	}
}

func TestProvision_InvalidResourceAnnotation(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mgr := tunnel.NewManager(newTestFlyClient(server), kubeClient, newTestConfig())

	svc := testService("test", "default",
		corev1.ServicePort{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
	)
	svc.Annotations[tunnel.AnnotationFrpcMemoryLimit] = "not-a-quantity"

	_, err := mgr.Provision(context.Background(), svc)
	if err == nil {
		t.Fatal("expected Provision to fail with invalid resource annotation")
	}
	if !containsString(err.Error(), tunnel.AnnotationFrpcMemoryLimit) {
		t.Errorf("expected error to mention annotation %q, got: %v", tunnel.AnnotationFrpcMemoryLimit, err)
	}
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
