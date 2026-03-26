# Phase 2 Completed: CAPI Provider Refactor

## Overview

Phase 2 of the repository separation plan has been completed. The cluster-api-provider-nvidia-ncx-infra-controller now uses the auto-generated Go client from `github.com/NVIDIA/carbide-rest/client` instead of the custom `pkg/cloud` package.

## Major Changes

### 1. Dependency Updates

**Added:**
- `github.com/NVIDIA/carbide-rest` - Generated Go client from OpenAPI specification

**Removed:**
- `pkg/cloud/` - Custom API client (replaced by generated client)
- `pkg/util/` - ProviderID utilities (moved to carbide-rest/client)

### 2. Controller Updates

#### NcxInfraCluster Controller

**File**: `internal/controller/ncxinfracluster_controller.go`

**Key Changes:**
- Uses `restclient.ClientWithResponses` from generated client
- Implements CIDR to IP block conversion (Option A: automatic management)
- Proper type conversions between CRD types and API types
- Enum handling for VPC, NSG rules, IP blocks

**New Functions:**
- `parseCIDR()` - Converts CIDR notation to network address + prefix length
- `ensureIPBlock()` - Automatically creates and manages IP block for cluster subnets

**Type Conversions:**
- String → Enum types (Direction, Protocol, Action, NetworkVirtualizationType)
- CIDR string → IP block ID + prefix length
- CRD field names → API field names (PortRange → DestinationPortRange, etc.)

#### NcxInfraMachine Controller

**File**: `internal/controller/ncxinframachine_controller.go`

**Key Changes:**
- Uses `restclient.ClientWithResponses` for all API calls
- Converts string IDs to UUIDs for API requests
- Handles response types correctly (JSON201 for creates, JSON200 for reads)
- Proper instance status enum conversions

**Type Conversions:**
- String IDs → `uuid.UUID` for tenant, VPC, subnet, instance type, SSH key groups
- Boolean pointers for optional fields (IsPhysical, PhoneHomeEnabled)
- Instance status enum → string conversions
- Labels map → `restclient.Labels` type

### 3. Scope Updates

#### ClusterScope

**File**: `pkg/scope/cluster.go`

**Changes:**
- Uses `*restclient.ClientWithResponses` instead of custom client
- Added `IPBlockID()` and `SetIPBlockID()` methods for tracking IP blocks
- Fetches credentials from Secret and creates authenticated client

#### MachineScope

**File**: `pkg/scope/machine.go`

**Changes:**
- Uses `*restclient.ClientWithResponses` for API operations
- Updated to use generated client types

### 4. API Types (CRDs)

#### NcxInfraCluster Status

**File**: `api/v1beta1/ncxinfracluster_types.go`

**Added Field:**
```go
type NetworkStatus struct {
    SubnetIDs map[string]string `json:"subnetIDs,omitempty"`
    NSGID string `json:"nsgID,omitempty"`
    IPBlockID string `json:"ipBlockID,omitempty"`  // NEW: Track IP block for subnets
}
```

**Why**: Controllers now automatically create one IP block per cluster and track its ID for subnet allocation.

### 5. IP Block Auto-Management (Option A)

**Approach**: Controller automatically creates and manages IP blocks

**Benefits:**
- Users only specify CIDR notation (Kubernetes-native)
- Controller handles NVIDIA NCX Infra Controller-specific IP block requirements
- Matches AWS/Azure/GCP CAPI provider patterns
- Transparent to users

**Implementation:**
```go
// ensureIPBlock creates a shared /16 IP block for all cluster subnets
func (r *NcxInfraClusterReconciler) ensureIPBlock(ctx context.Context, clusterScope *scope.ClusterScope, siteID string) (uuid.UUID, error) {
    // Check if IP block already exists
    if clusterScope.IPBlockID() != "" {
        // Return existing IP block
    }

    // Create new IP block: 10.0.0.0/16
    ipBlockReq := restclient.CreateIpblockJSONRequestBody{
        Name:            fmt.Sprintf("%s-ipblock", clusterScope.NcxInfraCluster.Name),
        Prefix:          "10.0.0.0",
        PrefixLength:    16,
        ProtocolVersion: restclient.Ipv4,
        RoutingType:     restclient.IpBlockCreateRequestRoutingTypeDatacenterOnly,
        SiteId:          siteUUID,
    }

    // Create and track IP block
}
```

**User Experience:**
```yaml
spec:
  subnets:
  - name: control-plane
    cidr: 10.0.1.0/24  # Just specify CIDR
    role: control-plane
  - name: worker
    cidr: 10.0.2.0/24  # Controller handles IP block allocation
    role: worker
```

### 6. OpenShift Code

**Status**: Documented for Phase 3-4 extraction

**Files:**
- `openshift/cloudprovider/` - ✅ Updated to use generated client (ready for Phase 4)
- `openshift/machine/` - ❌ Still has stubs, will be updated in Phase 3
- `openshift/README.md` - Documents extraction plan

**Build**: OpenShift code excluded from main build (`go build ./api/... ./internal/... ./pkg/... ./cmd/...`)

### 7. Tests

**Status**: Require rewriting for generated client

**Documentation**: See `TESTING.md` for migration strategy

**Test files affected:**
- `internal/controller/ncxinfracluster_controller_test.go`
- `internal/controller/ncxinframachine_controller_test.go`

**Recommended approach**: HTTP test server mocking API endpoints

## Type Conversion Patterns

### String → Enum
```go
// CRD uses strings for user-friendliness
direction := restclient.NetworkSecurityGroupRuleDirection(strings.ToLower(rule.Direction))
protocol := restclient.NetworkSecurityGroupRuleProtocol(strings.ToLower(rule.Protocol))
```

### String → UUID
```go
// API requires UUIDs for identifiers
tenantUUID, err := uuid.Parse(machineScope.TenantID())
if err != nil {
    return fmt.Errorf("invalid tenant ID: %w", err)
}
```

### CIDR → IP Block
```go
// Parse CIDR to get prefix length
_, ipNet, err := net.ParseCIDR(subnetSpec.CIDR)
ones, _ := ipNet.Mask.Size()  // Get /24, /16, etc.

// Use in subnet creation
subnetReq := restclient.CreateSubnetJSONRequestBody{
    Name:         subnetSpec.Name,
    Ipv4BlockId:  &ipBlockID,
    PrefixLength: ones,
}
```

### Response Handling
```go
// Creates return 201
resp, err := client.CreateVpcWithResponse(ctx, orgName, req)
vpc := resp.JSON201  // NOT JSON200

// Reads return 200
resp, err := client.GetVpcWithResponse(ctx, orgName, vpcID, nil)
vpc := resp.JSON200
```

## Build Verification

All core packages build successfully:
```bash
cd cluster-api-provider-nvidia-ncx-infra-controller
go build ./api/... ./internal/... ./pkg/... ./cmd/...
# Success - no errors
```

## Breaking Changes

### For Users

None - CRD API remains unchanged. Existing NcxInfraCluster and NcxInfraMachine resources continue to work.

### For Developers

- `pkg/cloud` package removed - use `github.com/NVIDIA/carbide-rest/client`
- `pkg/util` package removed - use `github.com/NVIDIA/carbide-rest/client`
- Test mocks need rewriting for generated client

## Migration Guide

### For Existing Deployments

No migration needed - CRD API is unchanged.

### For Developers Extending the Provider

**Before:**
```go
import "github.com/NVIDIA/cluster-api-provider-nvidia-ncx-infra-controller/pkg/cloud"

client := cloud.NewClient(endpoint, orgName, token)
vpc, err := client.CreateVPC(ctx, req)
```

**After:**
```go
import restclient "github.com/NVIDIA/carbide-rest/client"

client, err := restclient.NewClientWithAuth(endpoint, token)
vpc, err := client.CreateVpcWithResponse(ctx, orgName, req)
if vpc.JSON201 != nil {
    // Use vpc.JSON201 for created resource
}
```

### For Test Writers

See `TESTING.md` for comprehensive testing guide using HTTP mock servers.

## Next Steps

### Phase 3: Extract Machine API Provider (Week 3)
- Create `machine-api-provider-nvidia-ncx-infra-controller` repository
- Move `openshift/machine/` → new repo
- Update to use generated client
- Complete stub implementations

### Phase 4: Extract Cloud Provider (Week 4)
- Create `cloud-provider-nvidia-ncx-infra-controller` repository
- Move `openshift/cloudprovider/` → new repo (already using generated client)
- Add deployment manifests

### Testing
- Implement HTTP test server approach (see `TESTING.md`)
- Rewrite unit tests for controllers
- Add integration tests with envtest
- Set up E2E tests with Kind

### Documentation
- Update README with new dependency info
- Add API reference documentation
- Create developer guide
- Document IP block auto-management

## References

- **Main Plan**: `/home/fdupont/.claude/plans/velvet-foraging-sparrow.md`
- **Testing Guide**: `TESTING.md`
- **OpenShift Status**: `openshift/README.md`
- **Generated Client**: `github.com/NVIDIA/carbide-rest/client`
- **OpenAPI Spec**: `github.com/NVIDIA/carbide-rest/openapi/spec.yaml`

## Verification Checklist

- ✅ All core packages compile
- ✅ CRDs unchanged (user-facing API stable)
- ✅ Controllers use generated client
- ✅ IP block auto-management implemented
- ✅ Type conversions handled correctly
- ✅ OpenShift cloudprovider updated
- ✅ Documentation created for pending work
- ⏳ Tests require rewriting (documented)
- ⏳ README needs dependency update
- ⏳ Integration/E2E tests needed
