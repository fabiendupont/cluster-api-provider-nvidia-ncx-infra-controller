# Quickstart Guide

This guide walks you through creating your first Kubernetes cluster on NVIDIA NCX Infra Controller using Cluster API.

## Prerequisites

Before you begin, ensure you have:

1. **Management Cluster** with Cluster API installed
2. **NVIDIA NCX Infra Controller API Credentials** (JWT token, org name, endpoint)
3. **Instance Types** available in your NVIDIA NCX Infra Controller site
4. **SSH Key Groups** configured in NVIDIA NCX Infra Controller

## Step 1: Install Cluster API

If you haven't already, install Cluster API on your management cluster:

```bash
# Install clusterctl
curl -L https://github.com/kubernetes-sigs/cluster-api/releases/download/v1.12.1/clusterctl-linux-amd64 -o clusterctl
chmod +x clusterctl
sudo mv clusterctl /usr/local/bin/

# Initialize Cluster API core components
clusterctl init
```

## Step 2: Install NVIDIA NCX Infra Controller Provider

### Option A: clusterctl

Add the provider to `~/.cluster-api/clusterctl.yaml`:

```yaml
providers:
  - name: nvidia-ncx-infra-controller
    url: https://github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/releases/latest/infrastructure-components.yaml
    type: InfrastructureProvider
```

Install the provider:

```bash
clusterctl init --infrastructure nvidia-ncx-infra-controller
```

### Option B: OLM (OpenShift)

Create a CatalogSource to make the operator available in OperatorHub:

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

Then install **Cluster API Provider NVIDIA NCX Infra Controller** from OperatorHub in the OpenShift console.

### Option C: Manual

```bash
git clone https://github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller
cd cluster-api-provider-nvidia-ncx-infra-controller

export IMG=<your-registry>/cluster-api-provider-nvidia-ncx-infra-controller:v0.1.0
make docker-build docker-push IMG=$IMG
make install
make deploy IMG=$IMG
```

Verify the controller is running:

```bash
kubectl get pods -n cluster-api-provider-nvidia-ncx-infra-controller-system
```

## Step 3: Create Credentials Secret

Create a secret with your NVIDIA NCX Infra Controller API credentials:

```bash
kubectl create secret generic nvidia-ncx-infra-controller-credentials \
  --from-literal=endpoint="https://api.carbide.nvidia.com" \
  --from-literal=orgName="your-org-name" \
  --from-literal=token="your-jwt-token" \
  -n default
```

## Step 4: Get Site and Instance Information

Find your Site name or UUID and available instance types through the NVIDIA NCX Infra Controller API or UI. Note:

- **Site name** (e.g., `my-site`)
- **Tenant ID** (UUID)
- **Instance type ID** (UUID for control plane and worker nodes)
- **SSH key group ID** (UUID)

## Step 5: Create a Cluster

### Using clusterctl

Set the required environment variables and generate the cluster manifests:

```bash
export CLUSTER_NAME="my-cluster"
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

### Using static YAML

Edit the sample template and apply it:

```bash
# Review and edit the template
vim config/samples/cluster-template.yaml

# Apply
kubectl apply -f config/samples/cluster-template.yaml
```

## Step 6: Monitor Cluster Creation

Watch the cluster creation progress:

```bash
# Watch cluster status
kubectl get clusters -w

# Watch machines being created
kubectl get machines -w

# View detailed status
kubectl describe ncxinfracluster my-cluster
```

Bare-metal instance provisioning typically takes 5-15 minutes.

## Step 7: Access the Workload Cluster

Once the cluster is ready, get the kubeconfig:

```bash
clusterctl get kubeconfig my-cluster > my-cluster.kubeconfig

kubectl --kubeconfig=my-cluster.kubeconfig get nodes
```

## Step 8: Install Cloud Controller Manager

For node lifecycle management, install the NVIDIA NCX Infra Controller Cloud Controller Manager in the workload cluster. See [cloud-provider-nvidia-ncx-infra-controller](../../cloud-provider-nvidia-ncx-infra-controller/README.md) for instructions.

## Step 9: Install CNI Plugin

Install a CNI plugin in your workload cluster (Calico example):

```bash
kubectl --kubeconfig=my-cluster.kubeconfig \
  apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.26.1/manifests/calico.yaml
```

## Scaling

### Scale Workers

```bash
kubectl scale machinedeployment my-cluster-workers --replicas=5
```

### Scale Control Plane

```bash
kubectl patch kubeadmcontrolplane my-cluster-control-plane \
  --type=merge -p '{"spec":{"replicas":5}}'
```

## Deleting the Cluster

```bash
kubectl delete cluster my-cluster
```

This will deprovision all NVIDIA NCX Infra Controller instances, delete subnets, NSG, VPC, and remove all cluster resources.

## Troubleshooting

### Cluster stuck in Provisioning

```bash
kubectl describe ncxinfracluster my-cluster
kubectl logs -n cluster-api-provider-nvidia-ncx-infra-controller-system \
  deployment/cluster-api-provider-nvidia-ncx-infra-controller-controller-manager -f
```

### Machines not provisioning

```bash
kubectl get machines
kubectl describe machine <machine-name>
```

### Network issues

```bash
kubectl get ncxinfracluster my-cluster -o jsonpath='{.status.vpcID}'
kubectl get ncxinfracluster my-cluster -o jsonpath='{.status.networkStatus}'
```

## Common Configuration Patterns

### Multi-NIC for DPU Connectivity

```yaml
spec:
  template:
    spec:
      network:
        subnetName: "primary"
        additionalInterfaces:
          - subnetName: "dpu-network"
            isPhysical: true
```

### Specific Machine Targeting

```yaml
spec:
  template:
    spec:
      instanceType:
        machineID: "specific-machine-uuid"
        allowUnhealthyMachine: false
```

### Custom Network Security

```yaml
vpc:
  networkSecurityGroup:
    name: "custom-nsg"
    rules:
      - name: "allow-custom-port"
        direction: "ingress"
        protocol: "tcp"
        portRange: "8080"
        sourceCIDR: "10.0.0.0/8"
        action: "allow"
```
