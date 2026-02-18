# Quickstart Guide

This guide walks you through creating your first Kubernetes cluster on NVIDIA Carbide using Cluster API.

## Prerequisites

Before you begin, ensure you have:

1. **Management Cluster** with Cluster API installed
2. **NVIDIA Carbide API Credentials** (JWT token, org name, endpoint)
3. **Instance Types** available in your NVIDIA Carbide site
4. **SSH Key Groups** configured in NVIDIA Carbide

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

## Step 2: Install NVIDIA Carbide Provider

### Option A: clusterctl

Add the provider to `~/.cluster-api/clusterctl.yaml`:

```yaml
providers:
  - name: nvidia-carbide
    url: https://github.com/fabiendupont/cluster-api-provider-nvidia-carbide/releases/latest/infrastructure-components.yaml
    type: InfrastructureProvider
```

Install the provider:

```bash
clusterctl init --infrastructure nvidia-carbide
```

### Option B: OLM (OpenShift)

Create a CatalogSource to make the operator available in OperatorHub:

```bash
kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: nvidia-carbide-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: ghcr.io/fabiendupont/cluster-api-provider-nvidia-carbide-catalog:v0.1.0
  displayName: NVIDIA Carbide
EOF
```

Then install **Cluster API Provider NVIDIA Carbide** from OperatorHub in the OpenShift console.

### Option C: Manual

```bash
git clone https://github.com/fabiendupont/cluster-api-provider-nvidia-carbide
cd cluster-api-provider-nvidia-carbide

export IMG=<your-registry>/cluster-api-provider-nvidia-carbide:v0.1.0
make docker-build docker-push IMG=$IMG
make install
make deploy IMG=$IMG
```

Verify the controller is running:

```bash
kubectl get pods -n cluster-api-provider-nvidia-carbide-system
```

## Step 3: Create Credentials Secret

Create a secret with your NVIDIA Carbide API credentials:

```bash
kubectl create secret generic nvidia-carbide-credentials \
  --from-literal=endpoint="https://api.carbide.nvidia.com" \
  --from-literal=orgName="your-org-name" \
  --from-literal=token="your-jwt-token" \
  -n default
```

## Step 4: Get Site and Instance Information

Find your Site name or UUID and available instance types through the NVIDIA Carbide API or UI. Note:

- **Site name** (e.g., `my-site`)
- **Tenant ID** (UUID)
- **Instance type ID** (UUID for control plane and worker nodes)
- **SSH key group ID** (UUID)

## Step 5: Create a Cluster

### Using clusterctl

Set the required environment variables and generate the cluster manifests:

```bash
export CLUSTER_NAME="my-cluster"
export NVIDIA_CARBIDE_SITE_NAME="my-site"
export NVIDIA_CARBIDE_TENANT_ID="your-tenant-uuid"
export NVIDIA_CARBIDE_CONTROL_PLANE_INSTANCE_TYPE_ID="instance-type-uuid"
export NVIDIA_CARBIDE_WORKER_INSTANCE_TYPE_ID="instance-type-uuid"
export NVIDIA_CARBIDE_SSH_KEY_GROUP_ID="ssh-key-group-uuid"

clusterctl generate cluster my-cluster \
  --infrastructure nvidia-carbide \
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
kubectl describe nvidiacarbidecluster my-cluster
```

Bare-metal instance provisioning typically takes 5-15 minutes.

## Step 7: Access the Workload Cluster

Once the cluster is ready, get the kubeconfig:

```bash
clusterctl get kubeconfig my-cluster > my-cluster.kubeconfig

kubectl --kubeconfig=my-cluster.kubeconfig get nodes
```

## Step 8: Install Cloud Controller Manager

For node lifecycle management, install the NVIDIA Carbide Cloud Controller Manager in the workload cluster. See [cloud-provider-nvidia-carbide](../../cloud-provider-nvidia-carbide/README.md) for instructions.

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

This will deprovision all NVIDIA Carbide instances, delete subnets, NSG, VPC, and remove all cluster resources.

## Troubleshooting

### Cluster stuck in Provisioning

```bash
kubectl describe nvidiacarbidecluster my-cluster
kubectl logs -n cluster-api-provider-nvidia-carbide-system \
  deployment/cluster-api-provider-nvidia-carbide-controller-manager -f
```

### Machines not provisioning

```bash
kubectl get machines
kubectl describe machine <machine-name>
```

### Network issues

```bash
kubectl get nvidiacarbidecluster my-cluster -o jsonpath='{.status.vpcID}'
kubectl get nvidiacarbidecluster my-cluster -o jsonpath='{.status.networkStatus}'
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
