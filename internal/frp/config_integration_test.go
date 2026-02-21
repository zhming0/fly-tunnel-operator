//go:build integration

package frp_test

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zhiming0/fly-tunnel-operator/internal/frp"
)

// findFrpBinary searches for frps/frpc in common locations.
func findFrpBinary(name string) string {
	// Check env var override.
	if dir := os.Getenv("FRP_BIN_DIR"); dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Check common locations.
	candidates := []string{
		filepath.Join("/tmp/frp_0.61.1_linux_amd64", name),
		filepath.Join("/usr/local/bin", name),
		filepath.Join("/usr/bin", name),
	}

	// Also check in PATH.
	if p, err := exec.LookPath(name); err == nil {
		candidates = append([]string{p}, candidates...)
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// getFreePort returns an available TCP port.
func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// startEchoServer starts a TCP server that echoes back received data with a prefix.
// Returns the listener so the caller can close it.
func startEchoServer(t *testing.T, port int) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("failed to start echo server on port %d: %v", port, err)
	}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // Listener closed.
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					line := scanner.Text()
					fmt.Fprintf(c, "echo:%s\n", line)
				}
			}(conn)
		}
	}()

	return l
}

// waitForPort waits until a TCP port is accepting connections.
func waitForPort(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("port %d not available after %v", port, timeout)
}

// TestIntegration_SinglePortTunnel verifies that a generated frpc config
// successfully tunnels TCP traffic through frps for a single-port service.
func TestIntegration_SinglePortTunnel(t *testing.T) {
	frpsBin := findFrpBinary("frps")
	frpcBin := findFrpBinary("frpc")
	if frpsBin == "" || frpcBin == "" {
		t.Skip("frps/frpc binaries not found; set FRP_BIN_DIR or install frp")
	}

	// Allocate ports.
	controlPort := getFreePort(t)
	servicePort := getFreePort(t)
	backendPort := getFreePort(t)

	// Start the backend echo server (simulates the Kubernetes service).
	echoListener := startEchoServer(t, backendPort)
	defer echoListener.Close()

	// Create a temp dir for config files.
	tmpDir := t.TempDir()

	// Generate and write frps config.
	frpsConfig := frp.GenerateServerConfig(controlPort)
	frpsConfigPath := filepath.Join(tmpDir, "frps.toml")
	os.WriteFile(frpsConfigPath, []byte(frpsConfig), 0644)

	// Start frps.
	frpsCmd := exec.Command(frpsBin, "-c", frpsConfigPath)
	frpsCmd.Env = noProxyEnv()
	frpsCmd.Stdout = os.Stdout
	frpsCmd.Stderr = os.Stderr
	if err := frpsCmd.Start(); err != nil {
		t.Fatalf("failed to start frps: %v", err)
	}
	defer func() {
		frpsCmd.Process.Kill()
		frpsCmd.Wait()
	}()

	// Wait for frps control port to be ready.
	waitForPort(t, controlPort, 10*time.Second)
	t.Logf("frps is listening on control port %d", controlPort)

	// Build a Service object matching a single-port service.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "echo-service",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:     "echo",
					Port:     int32(servicePort),
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}

	// Generate frpc config. Override localIP to 127.0.0.1 and localPort to backendPort
	// since we're running locally (not in a K8s cluster).
	frpcConfig := frp.GenerateClientConfig(svc, "127.0.0.1", controlPort)
	// Patch localIP and localPort to point to our local echo server.
	frpcConfig = strings.ReplaceAll(frpcConfig,
		"localIP = \"echo-service.default.svc.cluster.local\"",
		"localIP = \"127.0.0.1\"")
	frpcConfig = strings.ReplaceAll(frpcConfig,
		fmt.Sprintf("localPort = %d", servicePort),
		fmt.Sprintf("localPort = %d", backendPort))

	frpcConfigPath := filepath.Join(tmpDir, "frpc.toml")
	os.WriteFile(frpcConfigPath, []byte(frpcConfig), 0644)

	t.Logf("frpc config:\n%s", frpcConfig)

	// Start frpc.
	frpcCmd := exec.Command(frpcBin, "-c", frpcConfigPath)
	frpcCmd.Env = noProxyEnv()
	frpcCmd.Stdout = os.Stdout
	frpcCmd.Stderr = os.Stderr
	if err := frpcCmd.Start(); err != nil {
		t.Fatalf("failed to start frpc: %v", err)
	}
	defer func() {
		frpcCmd.Process.Kill()
		frpcCmd.Wait()
	}()

	// Wait for the tunnel's remote port to be ready.
	waitForPort(t, servicePort, 10*time.Second)
	t.Logf("tunnel is ready on remote port %d", servicePort)

	// Send data through the tunnel and verify it comes back.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", servicePort), 5*time.Second)
	if err != nil {
		t.Fatalf("failed to connect through tunnel: %v", err)
	}
	defer conn.Close()

	// Send a test message.
	testMsg := "hello-from-tunnel-test"
	fmt.Fprintf(conn, "%s\n", testMsg)

	// Read the response.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("failed to read response: %v", scanner.Err())
	}

	response := scanner.Text()
	expected := "echo:" + testMsg
	if response != expected {
		t.Errorf("unexpected response: got %q, want %q", response, expected)
	}

	t.Logf("tunnel traffic verified: sent %q, received %q", testMsg, response)
}

// TestIntegration_MultiPortTunnel verifies that generated frpc config
// correctly tunnels traffic for a service with multiple ports.
func TestIntegration_MultiPortTunnel(t *testing.T) {
	frpsBin := findFrpBinary("frps")
	frpcBin := findFrpBinary("frpc")
	if frpsBin == "" || frpcBin == "" {
		t.Skip("frps/frpc binaries not found; set FRP_BIN_DIR or install frp")
	}

	// Allocate ports.
	controlPort := getFreePort(t)
	httpRemotePort := getFreePort(t)
	httpsRemotePort := getFreePort(t)
	httpBackendPort := getFreePort(t)
	httpsBackendPort := getFreePort(t)

	// Start two backend echo servers (simulating HTTP and HTTPS backends).
	httpListener := startEchoServer(t, httpBackendPort)
	defer httpListener.Close()
	httpsListener := startEchoServer(t, httpsBackendPort)
	defer httpsListener.Close()

	tmpDir := t.TempDir()

	// Generate and write frps config.
	frpsConfig := frp.GenerateServerConfig(controlPort)
	frpsConfigPath := filepath.Join(tmpDir, "frps.toml")
	os.WriteFile(frpsConfigPath, []byte(frpsConfig), 0644)

	// Start frps.
	frpsCmd := exec.Command(frpsBin, "-c", frpsConfigPath)
	frpsCmd.Env = noProxyEnv()
	frpsCmd.Stdout = os.Stdout
	frpsCmd.Stderr = os.Stderr
	if err := frpsCmd.Start(); err != nil {
		t.Fatalf("failed to start frps: %v", err)
	}
	defer func() {
		frpsCmd.Process.Kill()
		frpsCmd.Wait()
	}()

	waitForPort(t, controlPort, 10*time.Second)

	// Build a multi-port Service.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "envoy-gateway",
			Namespace: "envoy-gateway-system",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: int32(httpRemotePort), Protocol: corev1.ProtocolTCP},
				{Name: "https", Port: int32(httpsRemotePort), Protocol: corev1.ProtocolTCP},
			},
		},
	}

	// Generate frpc config and patch for local testing.
	frpcConfig := frp.GenerateClientConfig(svc, "127.0.0.1", controlPort)
	frpcConfig = strings.ReplaceAll(frpcConfig,
		"localIP = \"envoy-gateway.envoy-gateway-system.svc.cluster.local\"",
		"localIP = \"127.0.0.1\"")
	// Patch localPort for HTTP.
	frpcConfig = patchLocalPort(frpcConfig, "envoy-gateway-http", httpBackendPort)
	// Patch localPort for HTTPS.
	frpcConfig = patchLocalPort(frpcConfig, "envoy-gateway-https", httpsBackendPort)

	frpcConfigPath := filepath.Join(tmpDir, "frpc.toml")
	os.WriteFile(frpcConfigPath, []byte(frpcConfig), 0644)

	t.Logf("frpc config:\n%s", frpcConfig)

	// Start frpc.
	frpcCmd := exec.Command(frpcBin, "-c", frpcConfigPath)
	frpcCmd.Env = noProxyEnv()
	frpcCmd.Stdout = os.Stdout
	frpcCmd.Stderr = os.Stderr
	if err := frpcCmd.Start(); err != nil {
		t.Fatalf("failed to start frpc: %v", err)
	}
	defer func() {
		frpcCmd.Process.Kill()
		frpcCmd.Wait()
	}()

	// Wait for both tunneled ports.
	waitForPort(t, httpRemotePort, 10*time.Second)
	waitForPort(t, httpsRemotePort, 10*time.Second)
	t.Logf("both tunnels ready: HTTP on %d, HTTPS on %d", httpRemotePort, httpsRemotePort)

	// Test HTTP tunnel.
	t.Run("HTTP port", func(t *testing.T) {
		verifyTunnel(t, httpRemotePort, "http-test-message")
	})

	// Test HTTPS tunnel.
	t.Run("HTTPS port", func(t *testing.T) {
		verifyTunnel(t, httpsRemotePort, "https-test-message")
	})
}

// TestIntegration_ConfigParseValid verifies that frpc can parse our generated config
// without errors (validates TOML syntax and field names).
func TestIntegration_ConfigParseValid(t *testing.T) {
	frpcBin := findFrpBinary("frpc")
	if frpcBin == "" {
		t.Skip("frpc binary not found; set FRP_BIN_DIR or install frp")
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "test-namespace",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, Protocol: corev1.ProtocolTCP},
				{Name: "https", Port: 443, Protocol: corev1.ProtocolTCP},
				{Name: "grpc", Port: 9090, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	config := frp.GenerateClientConfig(svc, "10.0.0.1", 7000)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "frpc.toml")
	os.WriteFile(configPath, []byte(config), 0644)

	// Use frpc verify to check the config is valid.
	cmd := exec.Command(frpcBin, "verify", "-c", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frpc verify failed: %v\noutput: %s\nconfig:\n%s", err, string(output), config)
	}

	t.Logf("frpc verify output: %s", strings.TrimSpace(string(output)))
}

// TestIntegration_ServerConfigParseValid verifies that frps can parse our generated config.
func TestIntegration_ServerConfigParseValid(t *testing.T) {
	frpsBin := findFrpBinary("frps")
	if frpsBin == "" {
		t.Skip("frps binary not found; set FRP_BIN_DIR or install frp")
	}

	config := frp.GenerateServerConfig(7000)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "frps.toml")
	os.WriteFile(configPath, []byte(config), 0644)

	cmd := exec.Command(frpsBin, "verify", "-c", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frps verify failed: %v\noutput: %s\nconfig:\n%s", err, string(output), config)
	}

	t.Logf("frps verify output: %s", strings.TrimSpace(string(output)))
}

// TestIntegration_LargePortRange verifies config generation and parsing with many ports.
func TestIntegration_LargePortRange(t *testing.T) {
	frpcBin := findFrpBinary("frpc")
	if frpcBin == "" {
		t.Skip("frpc binary not found; set FRP_BIN_DIR or install frp")
	}

	ports := []corev1.ServicePort{}
	for i := 0; i < 20; i++ {
		ports = append(ports, corev1.ServicePort{
			Name:     fmt.Sprintf("port-%d", 8000+i),
			Port:     int32(8000 + i),
			Protocol: corev1.ProtocolTCP,
		})
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "many-ports",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Ports: ports,
		},
	}

	config := frp.GenerateClientConfig(svc, "10.0.0.1", 7000)

	// Verify config has all 20 proxy entries.
	proxyCount := strings.Count(config, "[[proxies]]")
	if proxyCount != 20 {
		t.Errorf("expected 20 proxy entries, got %d", proxyCount)
	}

	// Verify frpc can parse it.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "frpc.toml")
	os.WriteFile(configPath, []byte(config), 0644)

	cmd := exec.Command(frpcBin, "verify", "-c", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frpc verify failed for 20-port config: %v\noutput: %s", err, string(output))
	}
}

// TestIntegration_UDPProxy verifies that UDP protocol annotation is generated correctly.
func TestIntegration_UDPProxy(t *testing.T) {
	frpcBin := findFrpBinary("frpc")
	if frpcBin == "" {
		t.Skip("frpc binary not found; set FRP_BIN_DIR or install frp")
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dns-service",
			Namespace: "kube-system",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "dns-tcp", Port: 53, Protocol: corev1.ProtocolTCP},
				{Name: "dns-udp", Port: 53, Protocol: corev1.ProtocolUDP},
			},
		},
	}

	config := frp.GenerateClientConfig(svc, "10.0.0.1", 7000)

	// Verify both TCP and UDP proxy types are present.
	if !strings.Contains(config, `type = "tcp"`) {
		t.Error("expected tcp proxy type in config")
	}
	if !strings.Contains(config, `type = "udp"`) {
		t.Error("expected udp proxy type in config")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "frpc.toml")
	os.WriteFile(configPath, []byte(config), 0644)

	cmd := exec.Command(frpcBin, "verify", "-c", configPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frpc verify failed for UDP config: %v\noutput: %s", err, string(output))
	}

	t.Logf("UDP proxy config validated:\n%s", config)
}

// noProxyEnv returns a copy of os.Environ() with HTTP proxy variables removed.
// This prevents frpc/frps from trying to connect through a corporate proxy
// when communicating on localhost.
func noProxyEnv() []string {
	skip := map[string]bool{
		"HTTP_PROXY":  true,
		"HTTPS_PROXY": true,
		"http_proxy":  true,
		"https_proxy": true,
		"ALL_PROXY":   true,
		"all_proxy":   true,
	}

	var filtered []string
	for _, env := range os.Environ() {
		key := strings.SplitN(env, "=", 2)[0]
		if !skip[key] {
			filtered = append(filtered, env)
		}
	}
	// Ensure NO_PROXY covers localhost.
	filtered = append(filtered, "NO_PROXY=127.0.0.1,localhost")
	filtered = append(filtered, "no_proxy=127.0.0.1,localhost")
	return filtered
}

// verifyTunnel connects to a tunneled port and verifies the echo response.
func verifyTunnel(t *testing.T, port int, message string) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatalf("failed to connect to tunnel port %d: %v", port, err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "%s\n", message)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("failed to read response from port %d: %v", port, scanner.Err())
	}

	response := scanner.Text()
	expected := "echo:" + message
	if response != expected {
		t.Errorf("port %d: got %q, want %q", port, response, expected)
	}

	t.Logf("port %d verified: sent %q, received %q", port, message, response)
}

// patchLocalPort replaces the localPort for a specific proxy name in the config.
func patchLocalPort(config, proxyName string, newPort int) string {
	lines := strings.Split(config, "\n")
	inProxy := false
	for i, line := range lines {
		if strings.Contains(line, fmt.Sprintf(`name = "%s"`, proxyName)) {
			inProxy = true
		}
		if inProxy && strings.HasPrefix(line, "localPort = ") {
			lines[i] = fmt.Sprintf("localPort = %d", newPort)
			break
		}
	}
	return strings.Join(lines, "\n")
}
