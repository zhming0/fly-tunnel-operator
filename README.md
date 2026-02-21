# fly-tunnel-operator

A Kubernetes operator that fulfills `Service type: LoadBalancer` requests by provisioning [frp](https://github.com/fatedier/frp) tunnels through [Fly.io](https://fly.io) Machines.

Designed for homelabs and environments without a cloud provider's load balancer integration, fly-tunnel-operator gives every `LoadBalancer` Service a real public IPv4 address routed through Fly.io's global anycast network.

## How it works

```
Internet
   |
   v
┌──────────────────────────┐
│  Fly.io Machine (frps)   │  <-- dedicated IPv4
│  region: ord              │
└──────────┬───────────────┘
           │ frp tunnel (TCP)
           v
┌──────────────────────────┐
│  frpc Deployment         │  <-- runs in-cluster
│  (ConfigMap-driven)      │
└──────────┬───────────────┘
           │ ClusterIP DNS
           v
┌──────────────────────────┐
│  Your Service            │  <-- type: LoadBalancer
│  (e.g. envoy-gateway)    │
└──────────────────────────┘
```

When a `Service` with `type: LoadBalancer` and `spec.loadBalancerClass: fly-tunnel-operator.dev/lb` is created, the operator:

1. Creates a Fly.io Machine running `frps` (frp server)
2. Allocates a dedicated IPv4 address on Fly.io
3. Deploys an `frpc` (frp client) Deployment in-cluster with a generated TOML config
4. Patches the Service's `.status.loadBalancer.ingress` with the public IP

When the Service is deleted, the operator tears down everything in reverse (Machine, IP, frpc Deployment + ConfigMap) using a finalizer.

## Prerequisites

- A Kubernetes cluster (any distro: k3s, kind, EKS, GKE, etc.)
- A [Fly.io](https://fly.io) account with an API token
- A Fly.io app created for hosting the Machines (`fly apps create <name>`)
- Helm 3

## Installation

### Via Helm

```bash
helm install fly-tunnel-operator charts/fly-tunnel-operator \
  --namespace fly-tunnel-operator-system \
  --create-namespace \
  --set flyApiToken=<YOUR_FLY_API_TOKEN> \
  --set flyApp=<YOUR_FLY_APP_NAME> \
  --set flyRegion=ord
```

### Configuration

| Parameter | Default | Description |
|---|---|---|
| `flyApiToken` | (required) | Fly.io API token |
| `flyApp` | (required) | Fly.io app name for Machines |
| `flyRegion` | (required) | Fly.io region (e.g. `ord`, `sjc`, `lhr`) |
| `flyMachineSize` | `shared-cpu-1x` | Machine size preset |
| `loadBalancerClass` | `fly-tunnel-operator.dev/lb` | LoadBalancer class to watch |
| `frpsImage` | `snowdreamtech/frps:latest` | Container image for frps |
| `frpcImage` | `snowdreamtech/frpc:latest` | Container image for frpc |
| `image.repository` | `ghcr.io/zhiming0/fly-tunnel-operator` | Operator image |
| `image.tag` | `appVersion` | Operator image tag |
| `replicaCount` | `1` | Operator replicas (leader election active) |

## Usage

Create a Service with `type: LoadBalancer` and the matching `loadBalancerClass`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-web-app
  namespace: default
spec:
  type: LoadBalancer
  loadBalancerClass: fly-tunnel-operator.dev/lb
  ports:
    - name: http
      port: 80
      protocol: TCP
    - name: https
      port: 443
      protocol: TCP
  selector:
    app: my-web-app
```

After the operator reconciles, the Service will have a public IP:

```bash
$ kubectl get svc my-web-app
NAME         TYPE           CLUSTER-IP     EXTERNAL-IP    PORT(S)
my-web-app   LoadBalancer   10.43.100.50   137.66.x.x    80:31234/TCP,443:31235/TCP
```

### Per-Service overrides

Override the Fly.io region or machine size for individual Services via annotations:

```yaml
metadata:
  annotations:
    fly-tunnel-operator.dev/fly-region: lhr
    fly-tunnel-operator.dev/fly-machine-size: shared-cpu-2x
```

### Supported machine sizes

| Preset | CPUs | Memory |
|---|---|---|
| `shared-cpu-1x` | 1 shared | 256 MB |
| `shared-cpu-2x` | 2 shared | 512 MB |
| `shared-cpu-4x` | 4 shared | 1024 MB |
| `performance-1x` | 1 dedicated | 2048 MB |
| `performance-2x` | 2 dedicated | 4096 MB |

## Architecture

```
.
├── main.go                          # Operator entry point
├── internal/
│   ├── controller/
│   │   └── service_controller.go    # Service reconciler (watch + reconcile loop)
│   ├── tunnel/
│   │   └── manager.go               # Tunnel lifecycle (provision / update / teardown)
│   ├── flyio/
│   │   └── client.go                # Fly.io Machines REST + GraphQL API client
│   ├── frp/
│   │   └── config.go                # frpc/frps TOML config generation
│   └── fakefly/
│       └── server.go                # Fake Fly.io API server (testing)
├── charts/fly-tunnel-operator/           # Helm chart
├── Dockerfile
└── Makefile
```

### Service annotations

The operator tracks tunnel state on the Service via annotations:

| Annotation | Description |
|---|---|
| `fly-tunnel-operator.dev/machine-id` | Fly.io Machine ID |
| `fly-tunnel-operator.dev/frpc-deployment` | Name of the in-cluster frpc Deployment |
| `fly-tunnel-operator.dev/ip-id` | Fly.io IP address allocation ID |
| `fly-tunnel-operator.dev/public-ip` | Allocated public IPv4 address |
| `fly-tunnel-operator.dev/fly-region` | (user-set) Override Fly.io region |
| `fly-tunnel-operator.dev/fly-machine-size` | (user-set) Override machine size |

## License

MIT
