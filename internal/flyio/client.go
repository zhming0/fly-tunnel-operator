// Package flyio provides a client for the Fly.io Machines API.
package flyio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultBaseURL    = "https://api.machines.dev"
	defaultGraphQLURL = "https://api.fly.io/graphql"
	apiVersion        = "v1"
)

// Client interacts with the Fly.io Machines API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	graphQLURL string
	token      string
}

// NewClient creates a new Fly.io Machines API client.
func NewClient(token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    defaultBaseURL,
		graphQLURL: defaultGraphQLURL,
		token:      token,
	}
}

// WithBaseURL sets a custom base URL for the Machines REST API.
func (c *Client) WithBaseURL(url string) *Client {
	c.baseURL = url
	return c
}

// WithGraphQLURL sets a custom GraphQL endpoint URL.
func (c *Client) WithGraphQLURL(url string) *Client {
	c.graphQLURL = url
	return c
}

// Machine represents a Fly.io Machine.
type Machine struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	State      string        `json:"state"`
	Region     string        `json:"region"`
	InstanceID string        `json:"instance_id"`
	PrivateIP  string        `json:"private_ip"`
	Config     MachineConfig `json:"config"`
}

// MachineConfig is the configuration for a Fly.io Machine.
type MachineConfig struct {
	Image    string            `json:"image"`
	Env      map[string]string `json:"env,omitempty"`
	Services []MachineService  `json:"services,omitempty"`
	Guest    *GuestConfig      `json:"guest,omitempty"`
	Init     *InitConfig       `json:"init,omitempty"`
}

// InitConfig overrides the container's entrypoint/cmd.
type InitConfig struct {
	Cmd        []string `json:"cmd,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
}

// GuestConfig specifies the Machine's resource allocation.
type GuestConfig struct {
	CPUKind  string `json:"cpu_kind"`
	CPUs     int    `json:"cpus"`
	MemoryMB int    `json:"memory_mb"`
}

// MachineService maps ports on the Machine to the Fly.io proxy.
type MachineService struct {
	Protocol     string `json:"protocol"`
	InternalPort int    `json:"internal_port"`
	Ports        []Port `json:"ports,omitempty"`
}

// Port defines an external port mapping.
type Port struct {
	Port     int      `json:"port"`
	Handlers []string `json:"handlers,omitempty"`
}

// CreateMachineInput is the request body for creating a Machine.
type CreateMachineInput struct {
	Name   string        `json:"name"`
	Region string        `json:"region"`
	Config MachineConfig `json:"config"`
}

// IPAddress represents an allocated IP address on Fly.io.
type IPAddress struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Type      string `json:"type"`
	Region    string `json:"region"`
	CreatedAt string `json:"created_at"`
}

// CreateAppInput is the request body for creating a Fly App.
type CreateAppInput struct {
	AppName string `json:"app_name"`
	OrgSlug string `json:"org_slug"`
}

// AllocateIPAddressInput is the GraphQL mutation input for allocating an IP.
type AllocateIPAddressInput struct {
	AppID   string `json:"appId"`
	Type    string `json:"type"`
	Region  string `json:"region,omitempty"`
	Network string `json:"network,omitempty"`
}

// GraphQL types for IP allocation via the Fly.io platform API.
type graphQLRequest struct {
	Query     string      `json:"query"`
	Variables interface{} `json:"variables,omitempty"`
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

type allocateIPData struct {
	AllocateIPAddress struct {
		IPAddress IPAddress `json:"ipAddress"`
	} `json:"allocateIpAddress"`
}

type releaseIPData struct {
	ReleaseIPAddress struct {
		App struct {
			Name string `json:"name"`
		} `json:"app"`
	} `json:"releaseIpAddress"`
}

// CreateMachine creates a new Machine in the specified app.
func (c *Client) CreateMachine(ctx context.Context, appName string, input CreateMachineInput) (*Machine, error) {
	url := fmt.Sprintf("%s/%s/apps/%s/machines", c.baseURL, apiVersion, appName)

	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshaling create machine input: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("creating machine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("creating machine: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	var machine Machine
	if err := json.NewDecoder(resp.Body).Decode(&machine); err != nil {
		return nil, fmt.Errorf("decoding machine response: %w", err)
	}

	return &machine, nil
}

// GetMachine retrieves a Machine by ID.
func (c *Client) GetMachine(ctx context.Context, appName, machineID string) (*Machine, error) {
	url := fmt.Sprintf("%s/%s/apps/%s/machines/%s", c.baseURL, apiVersion, appName, machineID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getting machine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("machine %s not found", machineID)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getting machine: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	var machine Machine
	if err := json.NewDecoder(resp.Body).Decode(&machine); err != nil {
		return nil, fmt.Errorf("decoding machine response: %w", err)
	}

	return &machine, nil
}

// DeleteMachine destroys a Machine by ID.
func (c *Client) DeleteMachine(ctx context.Context, appName, machineID string) error {
	url := fmt.Sprintf("%s/%s/apps/%s/machines/%s?force=true", c.baseURL, apiVersion, appName, machineID)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deleting machine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deleting machine: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// UpdateMachine updates a Machine's configuration.
func (c *Client) UpdateMachine(ctx context.Context, appName, machineID string, input CreateMachineInput) (*Machine, error) {
	url := fmt.Sprintf("%s/%s/apps/%s/machines/%s", c.baseURL, apiVersion, appName, machineID)

	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshaling update machine input: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("updating machine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("updating machine: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	var machine Machine
	if err := json.NewDecoder(resp.Body).Decode(&machine); err != nil {
		return nil, fmt.Errorf("decoding machine response: %w", err)
	}

	return &machine, nil
}

// WaitForMachine waits for a Machine to reach the specified state.
func (c *Client) WaitForMachine(ctx context.Context, appName, machineID, instanceID, targetState string, timeout time.Duration) error {
	url := fmt.Sprintf("%s/%s/apps/%s/machines/%s/wait?instance_id=%s&state=%s&timeout=%d",
		c.baseURL, apiVersion, appName, machineID, instanceID, targetState, int(timeout.Seconds()))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("waiting for machine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("waiting for machine: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// AllocateDedicatedIPv4 allocates a dedicated IPv4 address for the app using the Fly.io GraphQL API.
func (c *Client) AllocateDedicatedIPv4(ctx context.Context, appName string) (*IPAddress, error) {
	query := `
		mutation($input: AllocateIPAddressInput!) {
			allocateIpAddress(input: $input) {
				ipAddress {
					id
					address
					type
					region
					createdAt
				}
			}
		}
	`

	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"appId": appName,
			"type":  "v4",
		},
	}

	gqlReq := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(gqlReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("allocating IP: %w", err)
	}
	defer resp.Body.Close()

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("decoding graphql response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	var data allocateIPData
	if err := json.Unmarshal(gqlResp.Data, &data); err != nil {
		return nil, fmt.Errorf("decoding allocate IP data: %w", err)
	}

	return &data.AllocateIPAddress.IPAddress, nil
}

// ReleaseIPAddress releases an allocated IP address.
func (c *Client) ReleaseIPAddress(ctx context.Context, appName, ipID string) error {
	query := `
		mutation($input: ReleaseIPAddressInput!) {
			releaseIpAddress(input: $input) {
				app {
					name
				}
			}
		}
	`

	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"appId":       appName,
			"ipAddressId": ipID,
		},
	}

	gqlReq := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(gqlReq)
	if err != nil {
		return fmt.Errorf("marshaling graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("releasing IP: %w", err)
	}
	defer resp.Body.Close()

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return fmt.Errorf("decoding graphql response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	return nil
}

// ListIPAddresses lists all IP addresses for an app.
func (c *Client) ListIPAddresses(ctx context.Context, appName string) ([]IPAddress, error) {
	query := `
		query($appName: String!) {
			app(name: $appName) {
				ipAddresses {
					nodes {
						id
						address
						type
						region
						createdAt
					}
				}
			}
		}
	`

	variables := map[string]interface{}{
		"appName": appName,
	}

	gqlReq := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(gqlReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing IPs: %w", err)
	}
	defer resp.Body.Close()

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("decoding graphql response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
	}

	var data struct {
		App struct {
			IPAddresses struct {
				Nodes []IPAddress `json:"nodes"`
			} `json:"ipAddresses"`
		} `json:"app"`
	}
	if err := json.Unmarshal(gqlResp.Data, &data); err != nil {
		return nil, fmt.Errorf("decoding IP list data: %w", err)
	}

	return data.App.IPAddresses.Nodes, nil
}

// CreateApp creates a new Fly App in the specified organization.
func (c *Client) CreateApp(ctx context.Context, appName, orgSlug string) error {
	url := fmt.Sprintf("%s/%s/apps", c.baseURL, apiVersion)

	body, err := json.Marshal(CreateAppInput{
		AppName: appName,
		OrgSlug: orgSlug,
	})
	if err != nil {
		return fmt.Errorf("marshaling create app input: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("creating app: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("creating app: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// DeleteApp deletes a Fly App by name.
// Uses force=true to stop any running Machines and delete immediately.
func (c *Client) DeleteApp(ctx context.Context, appName string) error {
	url := fmt.Sprintf("%s/%s/apps/%s?force=true", c.baseURL, apiVersion, appName)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deleting app: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deleting app: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
}
