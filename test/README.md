# Testing Guide

This directory contains comprehensive tests for the NVIDIA Carbide CAPI Provider.

## Test Structure

```
test/
├── integration/     # Integration tests using envtest
├── e2e/            # End-to-end tests on real clusters
└── README.md       # This file
```

## Prerequisites

### For All Tests
- Go 1.25+
- kubectl
- Make

### For Integration Tests
- envtest binaries (installed via `make setup-envtest`)

### For E2E Tests
- Active Kubernetes cluster with CAPI installed
- NVIDIA Carbide API credentials
- Available Site, Instance Types, and SSH Key Groups

## Running Tests

### Unit Tests

Run all unit tests for controllers and API client:

```bash
make test
```

This runs:
- `pkg/cloud/client_test.go` - API client unit tests
- `internal/controller/*_test.go` - Controller unit tests

### Integration Tests

Integration tests use envtest to spin up a real Kubernetes API server and test controller reconciliation:

```bash
# Setup envtest first time
make setup-envtest

# Run integration tests
make test-integration
```

**What integration tests cover:**
- Full controller reconciliation loops
- CRD validation
- Finalizer handling
- Status updates
- Error handling

### E2E Tests

E2E tests require a real NVIDIA Carbide environment and test full cluster lifecycle:

```bash
# Set environment variables
export NCX_INFRA_API_ENDPOINT="https://api.carbide.nvidia.com"
export NCX_INFRA_ORG_NAME="your-org"
export NCX_INFRA_API_TOKEN="your-jwt-token"
export E2E_SITE_ID="site-uuid"
export E2E_TENANT_ID="tenant-uuid"
export E2E_INSTANCE_TYPE_ID="instance-type-uuid"
export E2E_SSH_KEY_GROUP_ID="ssh-key-group-uuid"

# Run E2E tests
make test-e2e
```

**What E2E tests cover:**
- Full cluster creation (VPC → Subnets → NSG → Instances)
- Machine provisioning with bootstrap data
- Control plane and worker node lifecycle
- Cluster deletion and cleanup
- Real NVIDIA Carbide API integration

**⚠️ WARNING:** E2E tests create real resources in NVIDIA Carbide. Ensure you have cleanup automation and understand costs.

## Test Coverage

### Unit Tests (`pkg/cloud/client_test.go`)

Tests all NVIDIA Carbide API operations:
- ✅ VPC create/get/delete
- ✅ Subnet create/get/delete
- ✅ NSG create/get/delete
- ✅ Instance create/get/delete
- ✅ JWT authentication
- ✅ HTTP error handling

### Controller Unit Tests

**NcxInfraCluster Controller:**
- ✅ VPC provisioning
- ✅ Subnet creation
- ✅ NSG configuration
- ✅ Idempotent reconciliation
- ✅ Deletion with finalizers
- ✅ Status conditions

**NcxInfraMachine Controller:**
- ✅ Instance creation with bootstrap data
- ✅ Provider ID generation
- ✅ Status updates (Pending → Provisioning → Ready)
- ✅ Address extraction
- ✅ Waiting for cluster ready
- ✅ Deletion with finalizers

### Integration Tests

- ✅ Full controller reconciliation with real Kubernetes API
- ✅ CRD validation
- ✅ Owner reference handling
- ✅ Secret management
- ✅ Finalizer processing

### E2E Tests

- ✅ HA cluster creation (3 CP + 3 workers)
- ✅ Network setup (VPC, subnets, NSG)
- ✅ Instance provisioning
- ✅ Cluster deletion and cleanup

## Writing New Tests

### Unit Test Example

```go
func TestMyFeature(t *testing.T) {
    mockClient := &mocks.MockClient{}
    mockClient.On("CreateVPC", mock.Anything, mock.Anything).
        Return(&cloud.VPC{ID: "vpc-123"}, nil)

    // Test logic here

    mockClient.AssertExpectations(t)
}
```

### Integration Test Example

```go
var _ = Describe("My Feature", func() {
    It("should do something", func() {
        resource := &MyResource{...}
        Expect(k8sClient.Create(ctx, resource)).To(Succeed())

        Eventually(func() bool {
            // Check condition
            return true
        }, timeout, interval).Should(BeTrue())
    })
})
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Tests
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - name: Unit Tests
        run: make test
      - name: Integration Tests
        run: |
          make setup-envtest
          make test-integration
```

## Debugging Failed Tests

### Unit Tests
```bash
# Run specific test
go test -v ./pkg/cloud -run TestClient_CreateVPC

# Run with coverage
make test-coverage
```

### Integration Tests
```bash
# Run with verbose output
ginkgo -v ./test/integration

# Run specific test
ginkgo -focus="NcxInfraCluster" ./test/integration
```

### E2E Tests
```bash
# Run with verbose output
ginkgo -v ./test/e2e

# Keep resources for debugging
export E2E_SKIP_CLEANUP=true
ginkgo ./test/e2e
```

## Test Maintenance

### Updating Mocks

When the NVIDIA Carbide API client interface changes:

```bash
# Regenerate mocks
mockery --name=Client --dir=pkg/cloud --output=pkg/cloud/mocks
```

### Updating Test Data

Test data should:
- Use realistic but fake IDs (e.g., `vpc-123`, not `test`)
- Include all required fields
- Cover edge cases (empty strings, nil pointers, etc.)

## Known Issues

1. **envtest Flakiness**: Integration tests may occasionally fail due to timing issues. Retry if this happens.
2. **E2E Cleanup**: Always verify E2E tests cleaned up resources to avoid costs.
3. **Mock Limitations**: Mocks don't test actual API contract. Use E2E tests for full validation.

## Contributing

When adding new features:
1. Add unit tests for the feature
2. Add integration tests if it affects reconciliation
3. Update E2E tests if it affects cluster lifecycle
4. Update this README with new test scenarios
