# Development Guide

## Requirements

- Go 1.24+
- Docker (for building container images)
- A kubeconfig pointing at a cluster (for manual testing)
- [frp](https://github.com/fatedier/frp) binaries (for integration tests)

## Building

```bash
# Build the operator binary
make build

# Build the Docker image
make docker-build IMG=fly-tunnel-operator:dev
```

The binary is output to `bin/manager`.

## Running locally

The operator can run outside the cluster using your local kubeconfig:

```bash
export FLY_API_TOKEN=<your-token>
export FLY_APP=<your-app>
export FLY_REGION=ord

go run . \
  --namespace fly-tunnel-operator-system \
  --fly-machine-size shared-cpu-1x
```

Or equivalently:

```bash
make run
```

By default the operator watches Services with `loadBalancerClass: fly-tunnel-operator.dev/lb`. Override with `--load-balancer-class`.

## Testing

### Unit tests

Unit tests use a fake Fly.io API server (`internal/fakefly/server.go`) and controller-runtime's fake client. No network or cluster access required.

```bash
make test
```

Or run specific packages:

```bash
go test ./internal/frp/ -v          # frp config generation
go test ./internal/flyio/ -v        # Fly.io API client
go test ./internal/tunnel/ -v       # tunnel lifecycle manager
```

### Controller integration tests

Controller tests use [envtest](https://book.kubebuilder.io/reference/envtest) which runs a real kube-apiserver and etcd locally. The envtest binaries are auto-discovered from `~/.local/share/kubebuilder-envtest/`.

```bash
go test ./internal/controller/ -v
```

If you don't have envtest binaries installed, use setup-envtest:

```bash
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
setup-envtest use 1.31.x
```

Then set the `KUBEBUILDER_ASSETS` environment variable to the path printed by setup-envtest, or let the test suite auto-discover them.

### frp integration tests

Integration tests verify that generated frpc/frps configs work with real frp binaries. They are gated behind a build tag and skipped if the binaries are not found.

```bash
# Download frp binaries
curl -L https://github.com/fatedier/frp/releases/download/v0.61.1/frp_0.61.1_linux_amd64.tar.gz | tar xz -C /tmp

# Run integration tests
go test -tags integration ./internal/frp/ -v
```

The tests look for `frps`/`frpc` in these locations (in order):

1. `FRP_BIN_DIR` environment variable
2. `PATH`
3. `/tmp/frp_0.61.1_linux_amd64/`
4. `/usr/local/bin/`
5. `/usr/bin/`

The integration test suite includes:

| Test | What it verifies |
|---|---|
| `TestIntegration_SinglePortTunnel` | Full TCP tunnel with echo server, data flows end-to-end |
| `TestIntegration_MultiPortTunnel` | Multi-port tunnel (HTTP + HTTPS), independent backends |
| `TestIntegration_ConfigParseValid` | `frpc verify` accepts a 3-port generated config |
| `TestIntegration_ServerConfigParseValid` | `frps verify` accepts the server config |
| `TestIntegration_LargePortRange` | 20-port config generates correctly and parses |
| `TestIntegration_UDPProxy` | UDP protocol type is emitted and parseable |

### Running all tests

```bash
# Unit + controller integration tests
make test

# Including frp integration tests
go test -tags integration ./... -v
```

## Project structure

```
internal/
├── controller/
│   ├── service_controller.go       # Reconciler: watches Services, drives provisioning
│   ├── service_controller_test.go  # envtest integration tests (6 tests)
│   └── suite_test.go               # envtest setup (shared manager, fake fly server)
├── tunnel/
│   ├── manager.go                  # Provision / Update / Teardown orchestration
│   └── manager_test.go             # Unit tests with fakes (5 tests)
├── flyio/
│   ├── client.go                   # Fly.io Machines REST API + GraphQL client
│   └── client_test.go              # Unit tests with httptest server (12 tests)
├── frp/
│   ├── config.go                   # TOML config generation for frpc/frps
│   ├── config_test.go              # Unit tests (3 tests)
│   └── config_integration_test.go  # Integration tests with real frp binaries (6 tests)
└── fakefly/
    └── server.go                   # Fake Fly.io API (REST + GraphQL) for testing
```

## Key design decisions

### No CRDs

The operator works entirely with core `Service` objects. It watches `Service type: LoadBalancer` with a specific `loadBalancerClass` and stores all tunnel state in annotations on the Service itself. This avoids CRD installation and version management.

### Finalizer-based cleanup

A finalizer (`fly-tunnel-operator.dev/finalizer`) is added to every managed Service. On deletion, the operator tears down the Fly.io Machine, releases the IPv4, and deletes the in-cluster frpc Deployment + ConfigMap before removing the finalizer and allowing the Service to be garbage collected.

### One Machine per Service

Each LoadBalancer Service gets its own Fly.io Machine running frps and its own dedicated IPv4. This provides isolation and makes per-service region/size overrides straightforward.

### frpc runs in-cluster

The frpc client runs as a Deployment inside the cluster. Its config is mounted from a ConfigMap that the operator regenerates on port changes. The frpc connects outbound to the Fly.io Machine's public IP, so no inbound firewall rules are needed on the cluster.

## Helm chart

The Helm chart is in `charts/fly-tunnel-operator/`. To render templates without installing:

```bash
make helm-template
```

To install:

```bash
make helm-install
```

To customize values:

```bash
helm install fly-tunnel-operator charts/fly-tunnel-operator \
  --namespace fly-tunnel-operator-system \
  --create-namespace \
  -f my-values.yaml
```

## Makefile targets

| Target | Description |
|---|---|
| `make build` | Build the operator binary to `bin/manager` |
| `make run` | Run the operator locally via `go run` |
| `make test` | Run all unit and envtest integration tests |
| `make lint` | Run golangci-lint |
| `make fmt` | Format Go source files |
| `make vet` | Run go vet |
| `make docker-build` | Build Docker image |
| `make docker-push` | Push Docker image |
| `make helm-install` | Install Helm chart |
| `make helm-uninstall` | Uninstall Helm chart |
| `make helm-template` | Render Helm templates to stdout |
| `make clean` | Remove build artifacts |
