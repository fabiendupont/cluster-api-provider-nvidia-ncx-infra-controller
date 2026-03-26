# Architecture Documentation

This document provides an in-depth explanation of the Cluster API Provider for NVIDIA NCX Infra Controller architecture, design decisions, and implementation details.

## Overview

The NVIDIA NCX Infra Controller CAPI Provider implements a three-layer architecture:

1. **Cluster API Layer** - Standard Kubernetes cluster lifecycle management
2. **OpenShift Integration Layer** - OpenShift-specific machine management
3. **NVIDIA NCX Infra Controller Platform Layer** - Bare-metal infrastructure provisioning

## Component Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                    Management Cluster                                │
│                                                                       │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                  Cluster API Core                            │   │
│  │  • Cluster Controller                                        │   │
│  │  • Machine Controller                                        │   │
│  │  • MachineDeployment/MachineSet Controllers                 │   │
│  │  • KubeadmControlPlane Controller                           │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                              ↓                                        │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │         NVIDIA NCX Infra Controller Infrastructure Provider                      │   │
│  │                                                              │   │
│  │  ┌────────────────────┐  ┌──────────────────────┐         │   │
│  │  │ NcxInfraCluster     │  │ NcxInfraMachine       │         │   │
│  │  │ Controller         │  │ Controller           │         │   │
│  │  │                    │  │                      │         │   │
│  │  │ • VPC Management   │  │ • Instance Provision │         │   │
│  │  │ • Subnet Provision │  │ • Bootstrap Data     │         │   │
│  │  │ • NSG Configuration│  │ • Status Polling     │         │   │
│  │  └────────────────────┘  └──────────────────────┘         │   │
│  │                                                              │   │
│  │  ┌──────────────────────────────────────────────┐         │   │
│  │  │        Cluster/Machine Scopes                 │         │   │
│  │  │  • Credential Management                      │         │   │
│  │  │  • NVIDIA NCX Infra Controller Client Creation                    │         │   │
│  │  │  • Status Helpers                             │         │   │
│  │  └──────────────────────────────────────────────┘         │   │
│  │                                                              │   │
│  │  ┌──────────────────────────────────────────────┐         │   │
│  │  │        NVIDIA NCX Infra Controller API Client                     │         │   │
│  │  │  • JWT Authentication                         │         │   │
│  │  │  • VPC Operations                             │         │   │
│  │  │  • Instance Lifecycle                         │         │   │
│  │  │  • Networking (Subnets, NSG, IP Blocks)      │         │   │
│  │  └──────────────────────────────────────────────┘         │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                              ↓ HTTPS/JWT                              │
└─────────────────────────────────────────────────────────────────────┘
                               ↓
┌─────────────────────────────────────────────────────────────────────┐
│                    NVIDIA NCX Infra Controller Platform                                  │
│  ┌───────────────────┐  ┌───────────────────┐  ┌─────────────────┐ │
│  │   VPC Manager     │  │  Instance Manager │  │  Site Manager   │ │
│  │  • Network Setup  │  │  • Allocation     │  │  • Site CRDs    │ │
│  │  • Subnet Config  │  │  • Provisioning   │  │  • Health       │ │
│  │  • NSG Rules      │  │  • Lifecycle      │  │                 │ │
│  └───────────────────┘  └───────────────────┘  └─────────────────┘ │
│                              ↓                                        │
│                    Temporal Workflows                                │
└─────────────────────────────────────────────────────────────────────┘
                               ↓
┌─────────────────────────────────────────────────────────────────────┐
│                    Physical Sites                                    │
│  ┌────────────────────────────────────────────────────────────┐    │
│  │  Bare-metal Machines with DPUs                             │    │
│  │  • Site Agent (gRPC)                                       │    │
│  │  • OS Provisioning                                         │    │
│  │  • DPU Configuration                                       │    │
│  └────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
```

## Data Flow

### Cluster Creation Flow

1. **User creates Cluster resource**
   ```yaml
   apiVersion: cluster.x-k8s.io/v1beta2
   kind: Cluster
   ```

2. **CAPI Core creates NcxInfraCluster**
   - Sets owner reference
   - Waits for infrastructure ready

3. **NcxInfraCluster Controller reconciles**
   - Fetches credentials from Secret
   - Creates NVIDIA NCX Infra Controller API client
   - Provisions VPC → Subnets → NSG
   - Updates status with resource IDs
   - Sets Ready condition

4. **CAPI Core creates Machines**
   - For control plane: via KubeadmControlPlane
   - For workers: via MachineDeployment

5. **Bootstrap Provider generates cloud-init**
   - Kubeadm init/join configuration
   - Stores in Secret

6. **NcxInfraMachine Controller reconciles**
   - Waits for NcxInfraCluster ready
   - Waits for bootstrap data
   - Creates NVIDIA NCX Infra Controller Instance with user data
   - Polls instance status
   - Updates Machine addresses
   - Sets provider ID
   - Sets Ready condition

7. **Instance provisioning (Temporal workflow)**
   - Machine allocation
   - OS installation
   - DPU configuration
   - Network setup
   - Bootstrap execution
   - Status: Pending → Provisioning → Ready

8. **Cluster becomes ready**
   - All control plane nodes ready
   - API server accessible
   - Cloud controller manager deployed

### Machine Lifecycle

```
Machine Created
    ↓
NcxInfraMachine Created (owner ref)
    ↓
Wait for NcxInfraCluster Ready
    ↓
Wait for Bootstrap Data Secret
    ↓
Create NVIDIA NCX Infra Controller Instance
    ↓
Poll Instance Status (30s interval)
    ↓
Instance Ready → Extract IP Addresses
    ↓
Update Machine Status
    ↓
Set ProviderID
    ↓
Machine Ready
```

## Controllers

### NcxInfraCluster Controller

**Purpose:** Manages cluster-level infrastructure (VPC, subnets, NSG)

**Reconciliation Logic:**

```go
func Reconcile(ctx, req) {
    1. Fetch NcxInfraCluster
    2. Fetch owner Cluster
    3. Check if paused
    4. Add finalizer
    5. Create cluster scope (with NVIDIA NCX Infra Controller client)

    if deletion:
        Delete NSG → Subnets → VPC
        Remove finalizer
        return

    6. Get Site ID (from Site CRD or direct ID)
    7. Reconcile VPC
        - Check if exists
        - Create if needed
        - Store VPC ID in status
    8. Reconcile Subnets
        - For each subnet spec:
            - Check if exists
            - Create if needed
            - Store subnet ID in status
    9. Reconcile NSG (if specified)
        - Check if exists
        - Create with rules
        - Store NSG ID in status
    10. Set Ready condition
    11. Patch status
}
```

**Status Conditions:**
- `VPCReady` - VPC created and accessible
- `SubnetsReady` - All subnets created
- `NSGReady` - Network security group configured
- `Ready` - All infrastructure ready

### NcxInfraMachine Controller

**Purpose:** Manages individual machine instances

**Reconciliation Logic:**

```go
func Reconcile(ctx, req) {
    1. Fetch NcxInfraMachine
    2. Fetch owner Machine
    3. Fetch owner Cluster
    4. Fetch NcxInfraCluster
    5. Check if paused
    6. Wait for NcxInfraCluster ready
    7. Wait for bootstrap data
    8. Add finalizer
    9. Create machine scope

    if deletion:
        Delete Instance
        Remove finalizer
        return

    if instance exists:
        Get instance status
        Update machine status
        Extract addresses
        if instance ready:
            Set Ready condition
            Update control plane endpoint (if first CP node)
        else:
            Requeue after 30s
    else:
        Create instance:
            - Get bootstrap data
            - Get subnet ID
            - Build network interfaces
            - Set instance type or machine ID
            - Call NVIDIA NCX Infra Controller API
            - Store instance ID
            - Set provider ID
        Requeue after 10s
}
```

**Status Conditions:**
- `InstanceProvisioned` - Instance created in NVIDIA NCX Infra Controller
- `NetworkConfigured` - Network interfaces configured
- `Ready` - Instance running and accessible

## Scopes

### ClusterScope

**Purpose:** Provides context and utilities for cluster reconciliation

**Responsibilities:**
- Manages NVIDIA NCX Infra Controller API client lifecycle
- Provides accessor methods for cluster resources
- Handles credential management
- Status update helpers

**Key Methods:**
```go
- SiteID(ctx) - Resolves site ID from Site CRD or direct reference
- VPCID() - Returns VPC ID from status
- SetVPCID(id) - Updates VPC ID in status
- SubnetIDs() - Returns subnet ID map
- SetSubnetID(name, id) - Updates subnet ID
- TenantID() - Returns tenant ID
- PatchObject(ctx) - Persists status changes
```

### MachineScope

**Purpose:** Provides context for machine reconciliation

**Key Methods:**
```go
- GetBootstrapData(ctx) - Fetches cloud-init from Secret
- GetSubnetID() - Resolves subnet ID from cluster status
- SetProviderID(siteID, instanceID) - Sets provider ID on both resources
- SetAddresses(addresses) - Updates Machine addresses
- IsControlPlane() - Checks if machine is control plane
```

## NVIDIA NCX Infra Controller API Client

### Architecture

```go
type Client struct {
    httpClient *http.Client
    baseURL    string
    orgName    string
    token      string  // JWT token
}
```

### Authentication

Uses JWT bearer token authentication:

```
Authorization: Bearer <jwt-token>
```

### API Operations

**VPC Operations:**
```
POST   /v2/org/{org}/carbide/vpc           - Create VPC
GET    /v2/org/{org}/carbide/vpc/{id}      - Get VPC
DELETE /v2/org/{org}/carbide/vpc/{id}      - Delete VPC
```

**Instance Operations:**
```
POST   /v2/org/{org}/carbide/instance      - Create Instance
GET    /v2/org/{org}/carbide/instance/{id} - Get Instance
DELETE /v2/org/{org}/carbide/instance/{id} - Delete Instance
```

**Subnet Operations:**
```
POST   /v2/org/{org}/carbide/subnet        - Create Subnet
GET    /v2/org/{org}/carbide/subnet/{id}   - Get Subnet
DELETE /v2/org/{org}/carbide/subnet/{id}   - Delete Subnet
```

**NSG Operations:**
```
POST   /v2/org/{org}/carbide/nsg           - Create NSG
GET    /v2/org/{org}/carbide/nsg/{id}      - Get NSG
DELETE /v2/org/{org}/carbide/nsg/{id}      - Delete NSG
```

## Provider ID Format

The provider ID uniquely identifies instances:

```
nvidia-ncx-infra-controller://site-uuid/instance-uuid
```

**Purpose:**
- Correlates Kubernetes Nodes with NVIDIA NCX Infra Controller Instances
- Used by Cloud Controller Manager for node lifecycle
- Enables node-instance mapping for operations

**Usage:**
```go
providerID := util.NewProviderID(siteID, instanceID)
node.Spec.ProviderID = providerID.String()
```

## Network Virtualization

### ETHERNET_VIRTUALIZER

Standard virtualized networking using OVS/OVN:

```yaml
vpc:
  networkVirtualizationType: "ETHERNET_VIRTUALIZER"
```

**Characteristics:**
- Software-defined networking
- NAT and routing at hypervisor
- Standard for most workloads

### FNN (Flat Network Namespace)

Direct networking for DPU workloads:

```yaml
vpc:
  networkVirtualizationType: "FNN"
```

**Characteristics:**
- Direct hardware access
- Lower latency
- Required for DPU-accelerated workloads

## Multi-NIC Support

Supports multiple network interfaces per instance:

```yaml
spec:
  network:
    subnetName: "primary"  # Primary interface
    additionalInterfaces:
      - subnetName: "dpu-network"
        isPhysical: true
      - subnetName: "storage-network"
        isPhysical: false
```

**Use Cases:**
- DPU connectivity
- Storage networks
- Management networks
- Service meshes

## OpenShift Integration

### Machine API Actuator

Implements OpenShift Machine actuator interface:

```go
type Actuator interface {
    Create(ctx, machine) error
    Update(ctx, machine) error
    Exists(ctx, machine) (bool, error)
    Delete(ctx, machine) error
}
```

### Cloud Provider Interface

Implements Kubernetes cloud provider:

```go
type Interface interface {
    Initialize(clientBuilder, stop)
    InstancesV2() (InstancesV2, error)
    Zones() (Zones, error)
    LoadBalancer() (LoadBalancer, error)  // NotImplemented
}
```

**Capabilities:**
- Node lifecycle management
- Instance metadata
- Zone/region topology
- (Load balancers: not implemented, use external)

## Error Handling and Retries

### Reconciliation Requeue Strategy

```go
// Cluster not ready
return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

// Instance provisioning
return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

// Errors
return ctrl.Result{}, err  // Exponential backoff
```

### Finalizers

Used for cleanup on deletion:

```go
const NcxInfraClusterFinalizer = "ncxinfracluster.infrastructure.cluster.x-k8s.io"
const NcxInfraMachineFinalizer = "ncxinframachine.infrastructure.cluster.x-k8s.io"
```

**Cleanup order:**
1. Machines: Delete instances
2. Cluster: Delete NSG → Subnets → VPC

## Security Considerations

### Credential Management

- JWT tokens stored in Kubernetes Secrets
- Secrets referenced by name, not embedded
- RBAC limits access to credential secrets
- Tokens can be rotated via secret updates

### Network Security

- Optional NSG configuration
- Default deny unless explicitly allowed
- Support for ingress/egress rules
- CIDR-based source filtering

### Multi-Tenancy

- Tenant ID scopes all resources
- API enforces tenant isolation
- RBAC controls prevent cross-tenant access

## Performance Characteristics

### Provisioning Times

- **VPC creation**: < 30 seconds
- **Subnet creation**: < 10 seconds per subnet
- **NSG creation**: < 15 seconds
- **Instance provisioning**: 5-15 minutes (bare-metal + OS boot)

### Reconciliation Intervals

- **Cluster controller**: Event-driven + watch
- **Machine controller**: Event-driven + 30s polling when provisioning
- **Status updates**: On every reconciliation

### Resource Limits

- Subnets per VPC: Platform-dependent
- Machines per cluster: Platform-dependent
- Concurrent provisioning: Limited by Temporal workflow capacity

## Future Enhancements

### Planned Features

1. **Load Balancer Integration**
   - MetalLB integration
   - Hardware LB support
   - Service type LoadBalancer

2. **Advanced Networking**
   - Network policies
   - Service mesh integration
   - IPv6 support

3. **Monitoring & Observability**
   - Prometheus metrics
   - Controller dashboards
   - Resource utilization tracking

4. **Validation Webhooks**
   - CRD validation
   - Mutation webhooks
   - Conversion webhooks

5. **High Availability**
   - Multi-site clusters
   - Cross-site failover
   - Disaster recovery

6. **GitOps Integration**
   - Flux compatibility
   - ArgoCD integration
   - Declarative management
