// Package fakefly provides a fake Fly.io API server for testing.
package fakefly

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/zhming0/fly-tunnel-operator/internal/flyio"
)

// Server is a fake Fly.io API server for testing.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	apps     map[string]bool             // appName -> exists
	machines map[string]*flyio.Machine   // machineID -> Machine
	ips      map[string]*flyio.IPAddress // ipID -> IPAddress

	nextMachineID int
	nextIPID      int
	nextIPAddr    int

	// Hooks for custom behaviour in tests.
	OnCreateApp     func(appName, orgSlug string) error
	OnDeleteApp     func(appName string) error
	OnCreateMachine func(appName string, input flyio.CreateMachineInput) error
	OnDeleteMachine func(appName, machineID string) error
	OnAllocateIP    func(appName string) error
	OnReleaseIP     func(appName, ipID string) error
}

// NewServer creates and starts a new fake Fly.io API server.
func NewServer() *Server {
	s := &Server{
		apps:        make(map[string]bool),
		machines:    make(map[string]*flyio.Machine),
		ips:         make(map[string]*flyio.IPAddress),
		nextIPAddr:  1,
	}

	mux := http.NewServeMux()

	// Apps REST API routes (exact match for create/list).
	mux.HandleFunc("/v1/apps", s.handleApps)

	// Apps + Machines REST API routes (path prefix for app-specific operations).
	mux.HandleFunc("/v1/apps/", s.handleAppsAndMachines)

	// GraphQL endpoint for IP allocation.
	mux.HandleFunc("/graphql", s.handleGraphQL)

	s.Server = httptest.NewServer(mux)
	return s
}

// AppCount returns the number of apps.
func (s *Server) AppCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.apps)
}

// HasApp returns true if the app exists.
func (s *Server) HasApp(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apps[name]
}

// GetMachines returns a copy of all machines.
func (s *Server) GetMachines() map[string]*flyio.Machine {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make(map[string]*flyio.Machine, len(s.machines))
	for k, v := range s.machines {
		cp := *v
		result[k] = &cp
	}
	return result
}

// GetIPs returns a copy of all allocated IPs.
func (s *Server) GetIPs() map[string]*flyio.IPAddress {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make(map[string]*flyio.IPAddress, len(s.ips))
	for k, v := range s.ips {
		cp := *v
		result[k] = &cp
	}
	return result
}

// MachineCount returns the number of machines.
func (s *Server) MachineCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.machines)
}

// IPCount returns the number of allocated IPs.
func (s *Server) IPCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ips)
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.createApp(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleAppsAndMachines(w http.ResponseWriter, r *http.Request) {
	// Parse path: /v1/apps/{appName}[/machines[/{machineID}[/wait]]]
	path := strings.TrimPrefix(r.URL.Path, "/v1/apps/")
	parts := strings.Split(path, "/")

	if len(parts) < 1 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	appName := parts[0]

	// DELETE /v1/apps/{appName} — delete app
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.deleteApp(w, r, appName)
		return
	}

	if len(parts) < 2 || parts[1] != "machines" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch {
	case len(parts) == 2 && r.Method == http.MethodPost:
		s.createMachine(w, r, appName)
	case len(parts) == 3 && r.Method == http.MethodGet:
		s.getMachine(w, r, parts[2])
	case len(parts) == 3 && r.Method == http.MethodPost:
		s.updateMachine(w, r, parts[2])
	case len(parts) == 3 && r.Method == http.MethodDelete:
		s.deleteMachine(w, r, appName, parts[2])
	case len(parts) == 4 && parts[3] == "wait" && r.Method == http.MethodGet:
		s.waitMachine(w, r, parts[2])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var input flyio.CreateAppInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.OnCreateApp != nil {
		if err := s.OnCreateApp(input.AppName, input.OrgSlug); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.mu.Lock()
	if s.apps[input.AppName] {
		s.mu.Unlock()
		http.Error(w, "app already exists", http.StatusConflict)
		return
	}
	s.apps[input.AppName] = true
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) deleteApp(w http.ResponseWriter, _ *http.Request, appName string) {
	if s.OnDeleteApp != nil {
		if err := s.OnDeleteApp(appName); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.mu.Lock()
	delete(s.apps, appName)
	s.mu.Unlock()

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) createMachine(w http.ResponseWriter, r *http.Request, appName string) {
	var input flyio.CreateMachineInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.OnCreateMachine != nil {
		if err := s.OnCreateMachine(appName, input); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.mu.Lock()
	s.nextMachineID++
	id := fmt.Sprintf("machine-%d", s.nextMachineID)
	instanceID := fmt.Sprintf("instance-%d", s.nextMachineID)

	machine := &flyio.Machine{
		ID:         id,
		Name:       input.Name,
		State:      "started",
		Region:     input.Region,
		InstanceID: instanceID,
		PrivateIP:  fmt.Sprintf("fdaa:0:1::%d", s.nextMachineID),
		Config:     input.Config,
	}
	s.machines[id] = machine
	s.mu.Unlock()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(machine)
}

func (s *Server) getMachine(w http.ResponseWriter, _ *http.Request, machineID string) {
	s.mu.Lock()
	machine, ok := s.machines[machineID]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(machine)
}

func (s *Server) updateMachine(w http.ResponseWriter, r *http.Request, machineID string) {
	s.mu.Lock()
	machine, ok := s.machines[machineID]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var input flyio.CreateMachineInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	machine.Config = input.Config
	if input.Name != "" {
		machine.Name = input.Name
	}
	s.mu.Unlock()

	json.NewEncoder(w).Encode(machine)
}

func (s *Server) deleteMachine(w http.ResponseWriter, _ *http.Request, appName, machineID string) {
	if s.OnDeleteMachine != nil {
		if err := s.OnDeleteMachine(appName, machineID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	s.mu.Lock()
	delete(s.machines, machineID)
	s.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

func (s *Server) waitMachine(w http.ResponseWriter, _ *http.Request, machineID string) {
	s.mu.Lock()
	_, ok := s.machines[machineID]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Fake: always return immediately as if the machine reached the target state.
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	var gqlReq struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&gqlReq); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch {
	case strings.Contains(gqlReq.Query, "allocateIpAddress"):
		s.allocateIP(w, gqlReq.Variables)
	case strings.Contains(gqlReq.Query, "releaseIpAddress"):
		s.releaseIP(w, gqlReq.Variables)
	case strings.Contains(gqlReq.Query, "ipAddresses"):
		s.listIPs(w)
	default:
		http.Error(w, "unknown query", http.StatusBadRequest)
	}
}

func (s *Server) allocateIP(w http.ResponseWriter, variables json.RawMessage) {
	var vars struct {
		Input struct {
			AppID string `json:"appId"`
			Type  string `json:"type"`
		} `json:"input"`
	}
	json.Unmarshal(variables, &vars)

	if s.OnAllocateIP != nil {
		if err := s.OnAllocateIP(vars.Input.AppID); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]string{{"message": err.Error()}},
			})
			return
		}
	}

	s.mu.Lock()
	s.nextIPID++
	s.nextIPAddr++
	ipID := fmt.Sprintf("ip-%d", s.nextIPID)
	ip := &flyio.IPAddress{
		ID:      ipID,
		Address: fmt.Sprintf("137.66.%d.%d", s.nextIPAddr/256, s.nextIPAddr%256),
		Type:    "v4",
		Region:  "global",
	}
	s.ips[ipID] = ip
	s.mu.Unlock()

	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"allocateIpAddress": map[string]interface{}{
				"ipAddress": ip,
			},
		},
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) releaseIP(w http.ResponseWriter, variables json.RawMessage) {
	var vars struct {
		Input struct {
			AppID       string `json:"appId"`
			IPAddressID string `json:"ipAddressId"`
		} `json:"input"`
	}
	json.Unmarshal(variables, &vars)

	if s.OnReleaseIP != nil {
		if err := s.OnReleaseIP(vars.Input.AppID, vars.Input.IPAddressID); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]string{{"message": err.Error()}},
			})
			return
		}
	}

	s.mu.Lock()
	delete(s.ips, vars.Input.IPAddressID)
	s.mu.Unlock()

	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"releaseIpAddress": map[string]interface{}{
				"app": map[string]string{"name": vars.Input.AppID},
			},
		},
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) listIPs(w http.ResponseWriter) {
	s.mu.Lock()
	nodes := make([]*flyio.IPAddress, 0, len(s.ips))
	for _, ip := range s.ips {
		nodes = append(nodes, ip)
	}
	s.mu.Unlock()

	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"app": map[string]interface{}{
				"ipAddresses": map[string]interface{}{
					"nodes": nodes,
				},
			},
		},
	}
	json.NewEncoder(w).Encode(resp)
}
