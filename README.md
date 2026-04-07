# Cluster API Provider for NVIDIA NCX Infra Controller

A Kubernetes Cluster API infrastructure provider for managing bare-metal clusters on the NVIDIA NCX Infra Controller platform.

## Overview

This project implements a Kubernetes Cluster API (CAPI) infrastructure provider for NVIDIA NCX Infra Controller, using an auto-generated Go client from the NCX Infra Controller REST API OpenAPI specification.

### Features

- **NcxInfraCluster Controller**: Manages VPC, subnets, network security groups, and VPC peering
- **NcxInfraMachine Controller**: Provisions bare-metal instances with full lifecycle management
- **Multi-tenancy Support**: Tenant-scoped resource isolation
- **Network Virtualization**: Support for ETHERNET_VIRTUALIZER and FNN
- **VPC Peering**: Cross-VPC network connectivity
- **Explicit IP Selection**: Request specific IP addresses for VPC Prefix interfaces
- **Provider ID**: `nico://org/tenant/site/instance-id` format for node correlation
- **Bootstrap Integration**: Works with kubeadm and k3s bootstrap providers
- **IP Block Auto-Management**: Automatic creation and management of IP blocks for subnet allocation (Kubernetes-native CIDR notation)
- **Type-Safe API Client**: Auto-generated from OpenAPI 3.1 specification (zero maintenance)

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│           Management Kubernetes Cluster                     │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  CAPI Core + NCX Infra Controller Provider            │  │
│  │  • NcxInfraCluster controller                    │  │
│  │  • NcxInfraMachine controller                    │  │
│  └───────────────────────────────────────────────────────┘  │
│                        ↓ REST API (JWT)                     │
└─────────────────────────────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│          NVIDIA NCX Infra Controller Platform               │
│  • VPC/Networking    • Instance Lifecycle                   │
│  • Site Management   • Machine Allocation                   │
│  • Multi-tenancy     • Health Monitoring                    │
└─────────────────────────────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│                    Physical Sites                           │
│  • Bare-metal machines with DPUs                            │
│  • Site Agent + Instance provisioning                       │
└─────────────────────────────────────────────────────────────┘
```

## Prerequisites

- Go version v1.25+
- Docker version 17.03+
- kubectl version v1.28+
- Kubernetes management cluster with Cluster API v1.12+ installed
- Access to NCX Infra Controller REST API with JWT authentication

## Installation

There are three ways to install the provider, depending on your platform.

### Option A: clusterctl (Kubernetes)

Configure `~/.cluster-api/clusterctl.yaml` to register the provider:

```yaml
providers:
  - name: nvidia-ncx-infra-controller
    url: https://github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/releases/latest/infrastructure-components.yaml
    type: InfrastructureProvider
```

Then install:

```bash
clusterctl init --infrastructure nvidia-ncx-infra-controller
```

### Option B: OLM (OpenShift)

Apply the File Based Catalog, then install from OperatorHub:

```bash
kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: nvidia-ncx-infra-controller-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: ghcr.io/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller-catalog:v0.1.0
  displayName: NVIDIA NCX Infra Controller
EOF
```

The operator appears in OperatorHub as **Cluster API Provider NVIDIA NCX Infra Controller**.

### Option C: Manual (kustomize)

```bash
# Build and push Docker image
make docker-build docker-push IMG=<your-registry>/cluster-api-provider-nvidia-ncx-infra-controller:latest

# Install CRDs and deploy controller
make install
make deploy IMG=<your-registry>/cluster-api-provider-nvidia-ncx-infra-controller:latest
```

### Create Credentials Secret

Regardless of installation method, create a credentials secret:

```bash
kubectl create secret generic ncx-infra-credentials \
  --from-literal=endpoint="https://api.carbide.nvidia.com" \
  --from-literal=orgName="your-org-name" \
  --from-literal=token="your-jwt-token" \
  -n default
```

## Usage

### Create a Cluster with clusterctl

```bash
export NCX_INFRA_SITE_NAME="my-site"
export NCX_INFRA_TENANT_ID="your-tenant-uuid"
export NCX_INFRA_CONTROL_PLANE_INSTANCE_TYPE_ID="instance-type-uuid"
export NCX_INFRA_WORKER_INSTANCE_TYPE_ID="instance-type-uuid"
export NCX_INFRA_SSH_KEY_GROUP_ID="ssh-key-group-uuid"

clusterctl generate cluster my-cluster \
  --infrastructure nvidia-ncx-infra-controller \
  --kubernetes-version v1.28.0 \
  --control-plane-machine-count 3 \
  --worker-machine-count 3 \
  | kubectl apply -f -
```

### Create a Cluster from YAML

```yaml
apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: my-cluster
spec:
  clusterNetwork:
    pods:
      cidrBlocks: ["10.244.0.0/16"]
    services:
      cidrBlocks: ["10.96.0.0/12"]
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: NcxInfraCluster
    name: my-cluster
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: NcxInfraCluster
metadata:
  name: my-cluster
spec:
  siteRef:
    name: my-site
  tenantID: "tenant-uuid"
  vpc:
    name: "my-cluster-vpc"
    networkVirtualizationType: "ETHERNET_VIRTUALIZER"
  subnets:
    - name: "control-plane"
      cidr: "10.100.1.0/24"
    - name: "worker"
      cidr: "10.100.2.0/24"
  authentication:
    secretRef:
      name: ncx-infra-credentials
```

See `config/samples/cluster-template.yaml` for a complete example with control plane, workers, and bootstrap configuration.

## Configuration

### NcxInfraCluster

| Field | Description |
|-------|-------------|
| `siteRef` | Reference to Site (name or ID) |
| `tenantID` | Tenant ID for multi-tenancy |
| `vpc.networkVirtualizationType` | `ETHERNET_VIRTUALIZER` or `FNN` |
| `subnets` | List of subnets (use Kubernetes-native CIDR notation) |
| `subnets[].cidr` | Subnet CIDR (e.g., `10.0.1.0/24`) - IP blocks are auto-managed |
| `vpc.networkSecurityGroup` | Optional NSG configuration |
| `vpcPeerings` | Optional VPC peering connections to other VPCs |

### NcxInfraMachine

| Field | Description |
|-------|-------------|
| `instanceType.id` | Instance type UUID (or use `machineID` for specific machine) |
| `network.subnetName` | Subnet to attach the machine to |
| `network.ipAddress` | Explicit IP for VPC Prefix interfaces |
| `network.additionalInterfaces` | Additional NICs for multi-network configurations |
| `sshKeyGroups` | SSH key group IDs |

### IP Block Auto-Management

The controller automatically creates and manages IP blocks for subnet allocation:

```yaml
spec:
  subnets:
  - name: control-plane
    cidr: 10.0.1.0/24  # Controller creates IP block automatically
  - name: worker
    cidr: 10.0.2.0/24  # Allocated from same IP block
```

The controller creates one /16 IP block per cluster and allocates subnets from it. The IP block ID is tracked in `status.networkStatus.ipBlockID`.

## Development

### Building

```bash
make manifests generate   # Generate CRDs and deepcopy
make build                # Build binary
make test                 # Run unit tests
make test-integration     # Run integration tests (requires envtest)
make docker-build         # Build Docker image
```

### Release Artifacts

```bash
# clusterctl release artifacts (infrastructure-components.yaml, metadata.yaml, cluster-template.yaml)
make release IMG=ghcr.io/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller:v0.1.0

# OLM bundle image
make bundle-build bundle-push

# FBC catalog image
make catalog-build catalog-push
```

### Project Structure

```
cluster-api-provider-nvidia-ncx-infra-controller/
├── api/v1beta1/              # CRD type definitions
├── internal/controller/      # Cluster and Machine controllers
├── pkg/
│   ├── scope/                # Controller scopes (cluster, machine)
│   └── providerid/           # Provider ID parsing
├── cmd/main.go               # Controller manager entrypoint
├── config/                   # Kustomize deployment manifests
├── templates/                # clusterctl cluster templates
├── bundle/                   # OLM bundle (CSV + CRDs)
├── catalog/                  # File Based Catalog for OLM
├── metadata.yaml             # clusterctl provider metadata
└── clusterctl-settings.json  # clusterctl local dev config
```

## Troubleshooting

### Check Controller Logs

```bash
kubectl logs -n cluster-api-provider-nvidia-ncx-infra-controller-system \
  deployment/cluster-api-provider-nvidia-ncx-infra-controller-controller-manager
```

### Verify Cluster Status

```bash
kubectl describe ncxinfracluster my-cluster
kubectl get machines -w
```

### Common Issues

- **Instances stuck provisioning**: Bare-metal provisioning typically takes 5-15 minutes
- **Authentication errors**: Verify credentials secret contains valid JWT token
- **Network connectivity**: Check VPC and subnet IDs in cluster status

## Related Projects

- **[machine-api-provider-nvidia-ncx-infra-controller](../machine-api-provider-nvidia-ncx-infra-controller)** - OpenShift Machine API provider
- **[cloud-provider-nvidia-ncx-infra-controller](../cloud-provider-nvidia-ncx-infra-controller)** - Kubernetes Cloud Controller Manager

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
