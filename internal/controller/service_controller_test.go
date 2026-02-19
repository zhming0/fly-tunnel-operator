package controller_test

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/zhiming0/fly-frp-tunnel/internal/controller"
	"github.com/zhiming0/fly-frp-tunnel/internal/tunnel"
)

const (
	testTimeout  = 30 * time.Second
	testInterval = 250 * time.Millisecond
)

func ensureNamespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	_ = k8sClient.Create(testCtx, ns)
}

func waitForServiceIP(t *testing.T, key types.NamespacedName, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var svc corev1.Service
		if err := k8sClient.Get(testCtx, key, &svc); err == nil {
			if len(svc.Status.LoadBalancer.Ingress) > 0 && svc.Status.LoadBalancer.Ingress[0].IP != "" {
				return svc.Status.LoadBalancer.Ingress[0].IP
			}
		}
		time.Sleep(testInterval)
	}
	t.Fatalf("timed out waiting for Service %s to get an external IP", key)
	return ""
}

func waitForServiceDeletion(t *testing.T, key types.NamespacedName, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var svc corev1.Service
		err := k8sClient.Get(testCtx, key, &svc)
		if err != nil {
			return // Deleted or not found.
		}
		time.Sleep(testInterval)
	}
	t.Fatalf("timed out waiting for Service %s to be deleted", key)
}

func TestReconcile_CreateService_GetsExternalIP(t *testing.T) {
	ensureNamespace(t, "test-create-ns")
	ensureNamespace(t, operatorNamespace)

	machinesBefore := flyServer.MachineCount()
	ipsBefore := flyServer.IPCount()

	lbClass := controller.DefaultLoadBalancerClass
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc-create",
			Namespace: "test-create-ns",
		},
		Spec: corev1.ServiceSpec{
			Type:              corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass: &lbClass,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
				{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app": "test"},
		},
	}

	if err := k8sClient.Create(testCtx, svc); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Wait for the operator to assign an external IP.
	ip := waitForServiceIP(t, types.NamespacedName{Name: "test-svc-create", Namespace: "test-create-ns"}, testTimeout)
	if ip == "" {
		t.Fatal("expected a non-empty external IP")
	}
	t.Logf("Service got external IP: %s", ip)

	// Verify Machine was created.
	if flyServer.MachineCount()-machinesBefore != 1 {
		t.Errorf("expected 1 new machine, got %d", flyServer.MachineCount()-machinesBefore)
	}

	// Verify IP was allocated.
	if flyServer.IPCount()-ipsBefore != 1 {
		t.Errorf("expected 1 new IP, got %d", flyServer.IPCount()-ipsBefore)
	}

	// Verify Service annotations.
	var svcFetched corev1.Service
	if err := k8sClient.Get(testCtx, types.NamespacedName{Name: "test-svc-create", Namespace: "test-create-ns"}, &svcFetched); err != nil {
		t.Fatalf("failed to get service: %v", err)
	}

	frpcDeployName := svcFetched.Annotations[tunnel.AnnotationFrpcDeployment]
	if frpcDeployName == "" {
		t.Fatal("expected frpc deployment annotation")
	}

	// Verify frpc Deployment was created.
	var deploy appsv1.Deployment
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		err := k8sClient.Get(testCtx, types.NamespacedName{Name: frpcDeployName, Namespace: operatorNamespace}, &deploy)
		if err == nil {
			break
		}
		time.Sleep(testInterval)
	}
	if deploy.Name == "" {
		t.Fatal("frpc Deployment was not created")
	}

	if svcFetched.Annotations[tunnel.AnnotationMachineID] == "" {
		t.Error("expected machine-id annotation")
	}
	if svcFetched.Annotations[tunnel.AnnotationPublicIP] == "" {
		t.Error("expected public-ip annotation")
	}
	if svcFetched.Annotations[tunnel.AnnotationIPID] == "" {
		t.Error("expected ip-id annotation")
	}
}

func TestReconcile_IgnoresNonMatchingService(t *testing.T) {
	ensureNamespace(t, "test-ignore-ns")

	machinesBefore := flyServer.MachineCount()

	// Service with no loadBalancerClass — should be ignored.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc-ignore",
			Namespace: "test-ignore-ns",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app": "test"},
		},
	}

	if err := k8sClient.Create(testCtx, svc); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Wait a bit and verify no new Machine was created.
	time.Sleep(2 * time.Second)

	if flyServer.MachineCount() != machinesBefore {
		t.Errorf("expected no new machines for non-matching service, got %d new",
			flyServer.MachineCount()-machinesBefore)
	}
}

func TestReconcile_IgnoresClusterIPService(t *testing.T) {
	ensureNamespace(t, "test-clusterip-ns")

	machinesBefore := flyServer.MachineCount()

	// ClusterIP service — K8s API rejects loadBalancerClass on non-LB services,
	// so we just verify the controller doesn't act on plain ClusterIP services.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc-clusterip",
			Namespace: "test-clusterip-ns",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	if err := k8sClient.Create(testCtx, svc); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	time.Sleep(2 * time.Second)

	if flyServer.MachineCount() != machinesBefore {
		t.Errorf("expected no new machines for ClusterIP service, got %d new",
			flyServer.MachineCount()-machinesBefore)
	}
}

func TestReconcile_DeleteService_CleansUp(t *testing.T) {
	ensureNamespace(t, "test-delete-ns")
	ensureNamespace(t, operatorNamespace)

	lbClass := controller.DefaultLoadBalancerClass
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc-delete",
			Namespace: "test-delete-ns",
		},
		Spec: corev1.ServiceSpec{
			Type:              corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass: &lbClass,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app": "test"},
		},
	}

	if err := k8sClient.Create(testCtx, svc); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Wait for the operator to provision the tunnel.
	waitForServiceIP(t, types.NamespacedName{Name: "test-svc-delete", Namespace: "test-delete-ns"}, testTimeout)

	// Record counts after provisioning.
	machinesAfterProvision := flyServer.MachineCount()
	ipsAfterProvision := flyServer.IPCount()

	// Delete the Service.
	if err := k8sClient.Delete(testCtx, svc); err != nil {
		t.Fatalf("failed to delete service: %v", err)
	}

	// Wait for cleanup.
	waitForServiceDeletion(t, types.NamespacedName{Name: "test-svc-delete", Namespace: "test-delete-ns"}, testTimeout)

	// Give time for async teardown.
	time.Sleep(2 * time.Second)

	// Verify Machine was deleted (count decreased by 1).
	if flyServer.MachineCount() != machinesAfterProvision-1 {
		t.Errorf("expected machine count to decrease by 1, was %d now %d",
			machinesAfterProvision, flyServer.MachineCount())
	}

	// Verify IP was released (count decreased by 1).
	if flyServer.IPCount() != ipsAfterProvision-1 {
		t.Errorf("expected IP count to decrease by 1, was %d now %d",
			ipsAfterProvision, flyServer.IPCount())
	}
}

func TestReconcile_MultipleServices_IndependentTunnels(t *testing.T) {
	ensureNamespace(t, "test-multi-ns")
	ensureNamespace(t, operatorNamespace)

	machinesBefore := flyServer.MachineCount()
	ipsBefore := flyServer.IPCount()

	lbClass := controller.DefaultLoadBalancerClass

	svc1 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-multi-1",
			Namespace: "test-multi-ns",
		},
		Spec: corev1.ServiceSpec{
			Type:              corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass: &lbClass,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app": "test1"},
		},
	}

	svc2 := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-multi-2",
			Namespace: "test-multi-ns",
		},
		Spec: corev1.ServiceSpec{
			Type:              corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass: &lbClass,
			Ports: []corev1.ServicePort{
				{Name: "game", Port: 25565, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app": "test2"},
		},
	}

	if err := k8sClient.Create(testCtx, svc1); err != nil {
		t.Fatalf("failed to create svc1: %v", err)
	}
	if err := k8sClient.Create(testCtx, svc2); err != nil {
		t.Fatalf("failed to create svc2: %v", err)
	}

	ip1 := waitForServiceIP(t, types.NamespacedName{Name: "svc-multi-1", Namespace: "test-multi-ns"}, testTimeout)
	ip2 := waitForServiceIP(t, types.NamespacedName{Name: "svc-multi-2", Namespace: "test-multi-ns"}, testTimeout)

	t.Logf("svc1 IP: %s, svc2 IP: %s", ip1, ip2)

	// IPs should be different.
	if ip1 == ip2 {
		t.Errorf("expected different IPs, both got %s", ip1)
	}

	// 2 new machines.
	if flyServer.MachineCount()-machinesBefore != 2 {
		t.Errorf("expected 2 new machines, got %d", flyServer.MachineCount()-machinesBefore)
	}

	// 2 new IPs.
	if flyServer.IPCount()-ipsBefore != 2 {
		t.Errorf("expected 2 new IPs, got %d", flyServer.IPCount()-ipsBefore)
	}
}

func TestReconcile_UpdateServicePorts_RegeneratesConfig(t *testing.T) {
	ensureNamespace(t, "test-update-ns")
	ensureNamespace(t, operatorNamespace)

	lbClass := controller.DefaultLoadBalancerClass
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc-update",
			Namespace: "test-update-ns",
		},
		Spec: corev1.ServiceSpec{
			Type:              corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass: &lbClass,
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app": "test"},
		},
	}

	if err := k8sClient.Create(testCtx, svc); err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	waitForServiceIP(t, types.NamespacedName{Name: "test-svc-update", Namespace: "test-update-ns"}, testTimeout)

	// Fetch current state.
	var current corev1.Service
	if err := k8sClient.Get(testCtx, types.NamespacedName{Name: "test-svc-update", Namespace: "test-update-ns"}, &current); err != nil {
		t.Fatalf("failed to get service: %v", err)
	}

	frpcDeployName := current.Annotations[tunnel.AnnotationFrpcDeployment]

	// Add an HTTPS port.
	current.Spec.Ports = append(current.Spec.Ports,
		corev1.ServicePort{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP},
	)
	if err := k8sClient.Update(testCtx, &current); err != nil {
		t.Fatalf("failed to update service: %v", err)
	}

	// Wait for ConfigMap to be updated with port 443.
	deadline := time.Now().Add(testTimeout)
	configUpdated := false
	for time.Now().Before(deadline) {
		var cm corev1.ConfigMap
		if err := k8sClient.Get(testCtx, types.NamespacedName{
			Name:      frpcDeployName + "-config",
			Namespace: operatorNamespace,
		}, &cm); err == nil {
			if config, ok := cm.Data["frpc.toml"]; ok {
				if containsSubstring(config, "remotePort = 443") {
					configUpdated = true
					break
				}
			}
		}
		time.Sleep(testInterval)
	}

	if !configUpdated {
		t.Error("expected frpc config to be updated with port 443")
	}
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
