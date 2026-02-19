package flyio_test

import (
	"context"
	"testing"
	"time"

	"github.com/zhiming0/fly-frp-tunnel/internal/fakefly"
	"github.com/zhiming0/fly-frp-tunnel/internal/flyio"
)

func newTestClient(server *fakefly.Server) *flyio.Client {
	return flyio.NewClient("test-token").
		WithBaseURL(server.URL).
		WithGraphQLURL(server.URL + "/graphql")
}

func TestCreateMachine(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	machine, err := client.CreateMachine(context.Background(), "test-app", flyio.CreateMachineInput{
		Name:   "test-machine",
		Region: "syd",
		Config: flyio.MachineConfig{
			Image: "snowdreamtech/frps:latest",
			Services: []flyio.MachineService{
				{Protocol: "tcp", InternalPort: 7000, Ports: []flyio.Port{{Port: 7000}}},
				{Protocol: "tcp", InternalPort: 80, Ports: []flyio.Port{{Port: 80}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMachine failed: %v", err)
	}

	if machine.ID == "" {
		t.Error("expected machine ID to be set")
	}
	if machine.Name != "test-machine" {
		t.Errorf("expected name 'test-machine', got %q", machine.Name)
	}
	if machine.Region != "syd" {
		t.Errorf("expected region 'syd', got %q", machine.Region)
	}
	if machine.State != "started" {
		t.Errorf("expected state 'started', got %q", machine.State)
	}
	if machine.InstanceID == "" {
		t.Error("expected instance ID to be set")
	}

	if server.MachineCount() != 1 {
		t.Errorf("expected 1 machine on server, got %d", server.MachineCount())
	}
}

func TestGetMachine(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	created, err := client.CreateMachine(context.Background(), "test-app", flyio.CreateMachineInput{
		Name:   "get-test",
		Region: "iad",
		Config: flyio.MachineConfig{Image: "test:latest"},
	})
	if err != nil {
		t.Fatalf("CreateMachine failed: %v", err)
	}

	fetched, err := client.GetMachine(context.Background(), "test-app", created.ID)
	if err != nil {
		t.Fatalf("GetMachine failed: %v", err)
	}

	if fetched.ID != created.ID {
		t.Errorf("expected ID %q, got %q", created.ID, fetched.ID)
	}
	if fetched.Name != "get-test" {
		t.Errorf("expected name 'get-test', got %q", fetched.Name)
	}
}

func TestGetMachine_NotFound(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	_, err := client.GetMachine(context.Background(), "test-app", "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent machine")
	}
}

func TestDeleteMachine(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	machine, err := client.CreateMachine(context.Background(), "test-app", flyio.CreateMachineInput{
		Name:   "delete-test",
		Region: "syd",
		Config: flyio.MachineConfig{Image: "test:latest"},
	})
	if err != nil {
		t.Fatalf("CreateMachine failed: %v", err)
	}

	if server.MachineCount() != 1 {
		t.Fatalf("expected 1 machine, got %d", server.MachineCount())
	}

	err = client.DeleteMachine(context.Background(), "test-app", machine.ID)
	if err != nil {
		t.Fatalf("DeleteMachine failed: %v", err)
	}

	if server.MachineCount() != 0 {
		t.Errorf("expected 0 machines after delete, got %d", server.MachineCount())
	}
}

func TestUpdateMachine(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	machine, err := client.CreateMachine(context.Background(), "test-app", flyio.CreateMachineInput{
		Name:   "update-test",
		Region: "syd",
		Config: flyio.MachineConfig{
			Image: "old:latest",
			Services: []flyio.MachineService{
				{Protocol: "tcp", InternalPort: 80},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateMachine failed: %v", err)
	}

	updated, err := client.UpdateMachine(context.Background(), "test-app", machine.ID, flyio.CreateMachineInput{
		Name:   "update-test",
		Region: "syd",
		Config: flyio.MachineConfig{
			Image: "new:latest",
			Services: []flyio.MachineService{
				{Protocol: "tcp", InternalPort: 80},
				{Protocol: "tcp", InternalPort: 443},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateMachine failed: %v", err)
	}

	if updated.Config.Image != "new:latest" {
		t.Errorf("expected image 'new:latest', got %q", updated.Config.Image)
	}
	if len(updated.Config.Services) != 2 {
		t.Errorf("expected 2 services, got %d", len(updated.Config.Services))
	}
}

func TestWaitForMachine(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	machine, err := client.CreateMachine(context.Background(), "test-app", flyio.CreateMachineInput{
		Name:   "wait-test",
		Region: "syd",
		Config: flyio.MachineConfig{Image: "test:latest"},
	})
	if err != nil {
		t.Fatalf("CreateMachine failed: %v", err)
	}

	err = client.WaitForMachine(context.Background(), "test-app", machine.ID, machine.InstanceID, "started", 30*time.Second)
	if err != nil {
		t.Fatalf("WaitForMachine failed: %v", err)
	}
}

func TestAllocateDedicatedIPv4(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	ip, err := client.AllocateDedicatedIPv4(context.Background(), "test-app")
	if err != nil {
		t.Fatalf("AllocateDedicatedIPv4 failed: %v", err)
	}

	if ip.ID == "" {
		t.Error("expected IP ID to be set")
	}
	if ip.Address == "" {
		t.Error("expected IP address to be set")
	}
	if ip.Type != "v4" {
		t.Errorf("expected type 'v4', got %q", ip.Type)
	}

	if server.IPCount() != 1 {
		t.Errorf("expected 1 IP on server, got %d", server.IPCount())
	}
}

func TestReleaseIPAddress(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	ip, err := client.AllocateDedicatedIPv4(context.Background(), "test-app")
	if err != nil {
		t.Fatalf("AllocateDedicatedIPv4 failed: %v", err)
	}

	if server.IPCount() != 1 {
		t.Fatalf("expected 1 IP, got %d", server.IPCount())
	}

	err = client.ReleaseIPAddress(context.Background(), "test-app", ip.ID)
	if err != nil {
		t.Fatalf("ReleaseIPAddress failed: %v", err)
	}

	if server.IPCount() != 0 {
		t.Errorf("expected 0 IPs after release, got %d", server.IPCount())
	}
}

func TestListIPAddresses(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	// Allocate 3 IPs.
	for i := 0; i < 3; i++ {
		_, err := client.AllocateDedicatedIPv4(context.Background(), "test-app")
		if err != nil {
			t.Fatalf("AllocateDedicatedIPv4 failed: %v", err)
		}
	}

	ips, err := client.ListIPAddresses(context.Background(), "test-app")
	if err != nil {
		t.Fatalf("ListIPAddresses failed: %v", err)
	}

	if len(ips) != 3 {
		t.Errorf("expected 3 IPs, got %d", len(ips))
	}
}

func TestCreateMachine_MultipleMachines(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()
	client := newTestClient(server)

	// Create 3 machines and verify each gets a unique ID.
	ids := make(map[string]bool)
	for i := 0; i < 3; i++ {
		m, err := client.CreateMachine(context.Background(), "test-app", flyio.CreateMachineInput{
			Name:   "multi-test",
			Region: "syd",
			Config: flyio.MachineConfig{Image: "test:latest"},
		})
		if err != nil {
			t.Fatalf("CreateMachine[%d] failed: %v", i, err)
		}
		if ids[m.ID] {
			t.Errorf("duplicate machine ID: %s", m.ID)
		}
		ids[m.ID] = true
	}

	if server.MachineCount() != 3 {
		t.Errorf("expected 3 machines, got %d", server.MachineCount())
	}
}

func TestCreateMachine_HookError(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	server.OnCreateMachine = func(appName string, input flyio.CreateMachineInput) error {
		return errFakeFailure
	}

	client := newTestClient(server)
	_, err := client.CreateMachine(context.Background(), "test-app", flyio.CreateMachineInput{
		Name:   "fail-test",
		Region: "syd",
		Config: flyio.MachineConfig{Image: "test:latest"},
	})
	if err == nil {
		t.Error("expected error from hook failure")
	}
}

func TestAllocateIP_HookError(t *testing.T) {
	server := fakefly.NewServer()
	defer server.Close()

	server.OnAllocateIP = func(appName string) error {
		return errFakeFailure
	}

	client := newTestClient(server)
	_, err := client.AllocateDedicatedIPv4(context.Background(), "test-app")
	if err == nil {
		t.Error("expected error from hook failure")
	}
}

var errFakeFailure = &fakeError{msg: "fake failure"}

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }
