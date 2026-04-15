/*
Copyright 2026 Fabien Dupont.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package testutil

import (
	"context"
	"net/http"

	nico "github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

// MockNcxInfraClient is a mock implementation of NcxInfraClientInterface for testing
type MockNcxInfraClient struct {
	// VPC methods
	CreateVPCFunc func(
		ctx context.Context, org string, req nico.VpcCreateRequest,
	) (*nico.VPC, *http.Response, error)
	GetVPCFunc func(
		ctx context.Context, org string, vpcId string,
	) (*nico.VPC, *http.Response, error)
	DeleteVPCFunc func(
		ctx context.Context, org string, vpcId string,
	) (*http.Response, error)

	// Subnet methods
	CreateSubnetFunc func(
		ctx context.Context, org string, req nico.SubnetCreateRequest,
	) (*nico.Subnet, *http.Response, error)
	GetSubnetFunc func(
		ctx context.Context, org string, subnetId string,
	) (*nico.Subnet, *http.Response, error)
	DeleteSubnetFunc func(
		ctx context.Context, org string, subnetId string,
	) (*http.Response, error)

	// Instance methods
	CreateInstanceFunc func(
		ctx context.Context, org string, req nico.InstanceCreateRequest,
	) (*nico.Instance, *http.Response, error)
	GetInstanceFunc func(
		ctx context.Context, org string, instanceId string,
	) (*nico.Instance, *http.Response, error)
	DeleteInstanceFunc func(
		ctx context.Context, org string, instanceId string,
	) (*http.Response, error)

	// Network Security Group methods
	CreateNetworkSecurityGroupFunc func(
		ctx context.Context, org string, req nico.NetworkSecurityGroupCreateRequest,
	) (*nico.NetworkSecurityGroup, *http.Response, error)
	GetNetworkSecurityGroupFunc func(
		ctx context.Context, org string, nsgId string,
	) (*nico.NetworkSecurityGroup, *http.Response, error)
	DeleteNetworkSecurityGroupFunc func(
		ctx context.Context, org string, nsgId string,
	) (*http.Response, error)

	// Allocation methods
	CreateAllocationFunc func(
		ctx context.Context, org string, req nico.AllocationCreateRequest,
	) (*nico.Allocation, *http.Response, error)
	GetAllocationFunc func(
		ctx context.Context, org string, allocationId string,
	) (*nico.Allocation, *http.Response, error)
	GetAllAllocationFunc func(
		ctx context.Context, org string,
	) ([]nico.Allocation, *http.Response, error)
	DeleteAllocationFunc func(
		ctx context.Context, org string, allocationId string,
	) (*http.Response, error)

	// IP Block methods
	CreateIpblockFunc func(
		ctx context.Context, org string, req nico.IpBlockCreateRequest,
	) (*nico.IpBlock, *http.Response, error)
	GetIpblockFunc func(
		ctx context.Context, org string, ipBlockId string,
	) (*nico.IpBlock, *http.Response, error)
	DeleteIpblockFunc func(
		ctx context.Context, org string, ipBlockId string,
	) (*http.Response, error)

	// Site methods
	GetAllSiteFunc func(
		ctx context.Context, org string,
	) ([]nico.Site, *http.Response, error)

	// Instance list
	GetAllInstanceFunc func(
		ctx context.Context, org string,
	) ([]nico.Instance, *http.Response, error)

	// Site details
	GetSiteFunc func(
		ctx context.Context, org string, siteId string,
	) (*nico.Site, *http.Response, error)

	// Tenant
	GetCurrentTenantFunc func(
		ctx context.Context, org string,
	) (*nico.Tenant, *http.Response, error)

	// Instance update and history
	UpdateInstanceFunc func(
		ctx context.Context, org string, instanceId string, req nico.InstanceUpdateRequest,
	) (*nico.Instance, *http.Response, error)

	GetInstanceStatusHistoryFunc func(
		ctx context.Context, org string, instanceId string,
	) ([]nico.StatusDetail, *http.Response, error)

	// Batch
	BatchCreateInstanceFunc func(
		ctx context.Context, org string, req nico.BatchInstanceCreateRequest,
	) ([]nico.Instance, *http.Response, error)

	// Machine (physical)
	GetMachineFunc func(
		ctx context.Context, org string, machineId string,
	) (*nico.Machine, *http.Response, error)

	// Health / Fault events
	ListFaultEventsFunc func(
		ctx context.Context, org string, machineId string, state string, severity string,
	) ([]nico.FaultEvent, *http.Response, error)

	// VPC Peering methods
	CreateVpcPeeringFunc func(
		ctx context.Context, org string, req nico.VpcPeeringCreateRequest,
	) (*nico.VpcPeering, *http.Response, error)
	GetVpcPeeringFunc func(
		ctx context.Context, org string, peeringId string,
	) (*nico.VpcPeering, *http.Response, error)
	DeleteVpcPeeringFunc func(
		ctx context.Context, org string, peeringId string,
	) (*http.Response, error)

	// VPC Prefix methods
	CreateVpcPrefixFunc func(
		ctx context.Context, org string, req nico.VpcPrefixCreateRequest,
	) (*nico.VpcPrefix, *http.Response, error)
	GetVpcPrefixFunc func(
		ctx context.Context, org string, vpcPrefixId string,
	) (*nico.VpcPrefix, *http.Response, error)
	DeleteVpcPrefixFunc func(
		ctx context.Context, org string, vpcPrefixId string,
	) (*http.Response, error)
}

// VPC methods
func (m *MockNcxInfraClient) CreateVpc(
	ctx context.Context, org string, req nico.VpcCreateRequest,
) (*nico.VPC, *http.Response, error) {
	if m.CreateVPCFunc != nil {
		return m.CreateVPCFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetVpc(
	ctx context.Context, org string, vpcId string,
) (*nico.VPC, *http.Response, error) {
	if m.GetVPCFunc != nil {
		return m.GetVPCFunc(ctx, org, vpcId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteVpc(
	ctx context.Context, org string, vpcId string,
) (*http.Response, error) {
	if m.DeleteVPCFunc != nil {
		return m.DeleteVPCFunc(ctx, org, vpcId)
	}
	return nil, nil
}

// Subnet methods
func (m *MockNcxInfraClient) CreateSubnet(
	ctx context.Context, org string, req nico.SubnetCreateRequest,
) (*nico.Subnet, *http.Response, error) {
	if m.CreateSubnetFunc != nil {
		return m.CreateSubnetFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetSubnet(
	ctx context.Context, org string, subnetId string,
) (*nico.Subnet, *http.Response, error) {
	if m.GetSubnetFunc != nil {
		return m.GetSubnetFunc(ctx, org, subnetId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteSubnet(
	ctx context.Context, org string, subnetId string,
) (*http.Response, error) {
	if m.DeleteSubnetFunc != nil {
		return m.DeleteSubnetFunc(ctx, org, subnetId)
	}
	return nil, nil
}

// Instance methods
func (m *MockNcxInfraClient) CreateInstance(
	ctx context.Context, org string, req nico.InstanceCreateRequest,
) (*nico.Instance, *http.Response, error) {
	if m.CreateInstanceFunc != nil {
		return m.CreateInstanceFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetInstance(
	ctx context.Context, org string, instanceId string,
) (*nico.Instance, *http.Response, error) {
	if m.GetInstanceFunc != nil {
		return m.GetInstanceFunc(ctx, org, instanceId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteInstance(
	ctx context.Context, org string, instanceId string,
) (*http.Response, error) {
	if m.DeleteInstanceFunc != nil {
		return m.DeleteInstanceFunc(ctx, org, instanceId)
	}
	return nil, nil
}

// NetworkSecurityGroup methods
func (m *MockNcxInfraClient) CreateNetworkSecurityGroup(
	ctx context.Context, org string, req nico.NetworkSecurityGroupCreateRequest,
) (*nico.NetworkSecurityGroup, *http.Response, error) {
	if m.CreateNetworkSecurityGroupFunc != nil {
		return m.CreateNetworkSecurityGroupFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetNetworkSecurityGroup(
	ctx context.Context, org string, nsgId string,
) (*nico.NetworkSecurityGroup, *http.Response, error) {
	if m.GetNetworkSecurityGroupFunc != nil {
		return m.GetNetworkSecurityGroupFunc(ctx, org, nsgId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteNetworkSecurityGroup(
	ctx context.Context, org string, nsgId string,
) (*http.Response, error) {
	if m.DeleteNetworkSecurityGroupFunc != nil {
		return m.DeleteNetworkSecurityGroupFunc(ctx, org, nsgId)
	}
	return nil, nil
}

// Allocation methods
func (m *MockNcxInfraClient) CreateAllocation(
	ctx context.Context, org string, req nico.AllocationCreateRequest,
) (*nico.Allocation, *http.Response, error) {
	if m.CreateAllocationFunc != nil {
		return m.CreateAllocationFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetAllocation(
	ctx context.Context, org string, allocationId string,
) (*nico.Allocation, *http.Response, error) {
	if m.GetAllocationFunc != nil {
		return m.GetAllocationFunc(ctx, org, allocationId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetAllAllocation(
	ctx context.Context, org string,
) ([]nico.Allocation, *http.Response, error) {
	if m.GetAllAllocationFunc != nil {
		return m.GetAllAllocationFunc(ctx, org)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteAllocation(
	ctx context.Context, org string, allocationId string,
) (*http.Response, error) {
	if m.DeleteAllocationFunc != nil {
		return m.DeleteAllocationFunc(ctx, org, allocationId)
	}
	return nil, nil
}

// IPBlock methods
func (m *MockNcxInfraClient) CreateIpblock(
	ctx context.Context, org string, req nico.IpBlockCreateRequest,
) (*nico.IpBlock, *http.Response, error) {
	if m.CreateIpblockFunc != nil {
		return m.CreateIpblockFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetIpblock(
	ctx context.Context, org string, ipBlockId string,
) (*nico.IpBlock, *http.Response, error) {
	if m.GetIpblockFunc != nil {
		return m.GetIpblockFunc(ctx, org, ipBlockId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteIpblock(
	ctx context.Context, org string, ipBlockId string,
) (*http.Response, error) {
	if m.DeleteIpblockFunc != nil {
		return m.DeleteIpblockFunc(ctx, org, ipBlockId)
	}
	return nil, nil
}

// Site methods
func (m *MockNcxInfraClient) GetAllSite(
	ctx context.Context, org string,
) ([]nico.Site, *http.Response, error) {
	if m.GetAllSiteFunc != nil {
		return m.GetAllSiteFunc(ctx, org)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetAllInstance(
	ctx context.Context, org string,
) ([]nico.Instance, *http.Response, error) {
	if m.GetAllInstanceFunc != nil {
		return m.GetAllInstanceFunc(ctx, org)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetSite(
	ctx context.Context, org string, siteId string,
) (*nico.Site, *http.Response, error) {
	if m.GetSiteFunc != nil {
		return m.GetSiteFunc(ctx, org, siteId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetCurrentTenant(
	ctx context.Context, org string,
) (*nico.Tenant, *http.Response, error) {
	if m.GetCurrentTenantFunc != nil {
		return m.GetCurrentTenantFunc(ctx, org)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) UpdateInstance(
	ctx context.Context, org string, instanceId string, req nico.InstanceUpdateRequest,
) (*nico.Instance, *http.Response, error) {
	if m.UpdateInstanceFunc != nil {
		return m.UpdateInstanceFunc(ctx, org, instanceId, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetInstanceStatusHistory(
	ctx context.Context, org string, instanceId string,
) ([]nico.StatusDetail, *http.Response, error) {
	if m.GetInstanceStatusHistoryFunc != nil {
		return m.GetInstanceStatusHistoryFunc(ctx, org, instanceId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) BatchCreateInstance(
	ctx context.Context, org string, req nico.BatchInstanceCreateRequest,
) ([]nico.Instance, *http.Response, error) {
	if m.BatchCreateInstanceFunc != nil {
		return m.BatchCreateInstanceFunc(ctx, org, req)
	}
	return nil, nil, nil
}

// Machine methods
func (m *MockNcxInfraClient) GetMachine(
	ctx context.Context, org string, machineId string,
) (*nico.Machine, *http.Response, error) {
	if m.GetMachineFunc != nil {
		return m.GetMachineFunc(ctx, org, machineId)
	}
	return nil, nil, nil
}

// Health / Fault event methods
func (m *MockNcxInfraClient) ListFaultEvents(
	ctx context.Context, org string, machineId string, state string, severity string,
) ([]nico.FaultEvent, *http.Response, error) {
	if m.ListFaultEventsFunc != nil {
		return m.ListFaultEventsFunc(ctx, org, machineId, state, severity)
	}
	return nil, nil, nil
}

// VPC Prefix methods
func (m *MockNcxInfraClient) CreateVpcPrefix(
	ctx context.Context, org string, req nico.VpcPrefixCreateRequest,
) (*nico.VpcPrefix, *http.Response, error) {
	if m.CreateVpcPrefixFunc != nil {
		return m.CreateVpcPrefixFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetVpcPrefix(
	ctx context.Context, org string, vpcPrefixId string,
) (*nico.VpcPrefix, *http.Response, error) {
	if m.GetVpcPrefixFunc != nil {
		return m.GetVpcPrefixFunc(ctx, org, vpcPrefixId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteVpcPrefix(
	ctx context.Context, org string, vpcPrefixId string,
) (*http.Response, error) {
	if m.DeleteVpcPrefixFunc != nil {
		return m.DeleteVpcPrefixFunc(ctx, org, vpcPrefixId)
	}
	return nil, nil
}

// VPC Peering methods
func (m *MockNcxInfraClient) CreateVpcPeering(
	ctx context.Context, org string, req nico.VpcPeeringCreateRequest,
) (*nico.VpcPeering, *http.Response, error) {
	if m.CreateVpcPeeringFunc != nil {
		return m.CreateVpcPeeringFunc(ctx, org, req)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) GetVpcPeering(
	ctx context.Context, org string, peeringId string,
) (*nico.VpcPeering, *http.Response, error) {
	if m.GetVpcPeeringFunc != nil {
		return m.GetVpcPeeringFunc(ctx, org, peeringId)
	}
	return nil, nil, nil
}

func (m *MockNcxInfraClient) DeleteVpcPeering(
	ctx context.Context, org string, peeringId string,
) (*http.Response, error) {
	if m.DeleteVpcPeeringFunc != nil {
		return m.DeleteVpcPeeringFunc(ctx, org, peeringId)
	}
	return nil, nil
}

// Helper functions to create common response objects

func MockHTTPResponse(statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     make(http.Header),
	}
}

func Ptr[T any](v T) *T {
	return &v
}
