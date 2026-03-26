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

package scope

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrastructurev1 "github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/api/v1beta1"
	nico "github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

// NcxInfraClientInterface defines the methods we need from the NVIDIA Carbide REST client
type NcxInfraClientInterface interface {
	// VPC
	CreateVpc(ctx context.Context, org string, req nico.VpcCreateRequest) (*nico.VPC, *http.Response, error)
	GetVpc(ctx context.Context, org string, vpcId string) (*nico.VPC, *http.Response, error)
	DeleteVpc(ctx context.Context, org string, vpcId string) (*http.Response, error)

	// Subnet
	CreateSubnet(ctx context.Context, org string, req nico.SubnetCreateRequest) (*nico.Subnet, *http.Response, error)
	GetSubnet(ctx context.Context, org string, subnetId string) (*nico.Subnet, *http.Response, error)
	DeleteSubnet(ctx context.Context, org string, subnetId string) (*http.Response, error)

	// IPBlock
	CreateIpblock(ctx context.Context, org string, req nico.IpBlockCreateRequest) (*nico.IpBlock, *http.Response, error)
	GetIpblock(ctx context.Context, org string, ipBlockId string) (*nico.IpBlock, *http.Response, error)
	DeleteIpblock(ctx context.Context, org string, ipBlockId string) (*http.Response, error)

	// NetworkSecurityGroup
	CreateNetworkSecurityGroup(
		ctx context.Context, org string, req nico.NetworkSecurityGroupCreateRequest,
	) (*nico.NetworkSecurityGroup, *http.Response, error)
	GetNetworkSecurityGroup(
		ctx context.Context, org string, nsgId string,
	) (*nico.NetworkSecurityGroup, *http.Response, error)
	DeleteNetworkSecurityGroup(ctx context.Context, org string, nsgId string) (*http.Response, error)

	// Allocation
	CreateAllocation(
		ctx context.Context, org string, req nico.AllocationCreateRequest,
	) (*nico.Allocation, *http.Response, error)
	GetAllocation(
		ctx context.Context, org string, allocationId string,
	) (*nico.Allocation, *http.Response, error)
	GetAllAllocation(ctx context.Context, org string) ([]nico.Allocation, *http.Response, error)
	DeleteAllocation(ctx context.Context, org string, allocationId string) (*http.Response, error)

	// Site
	GetAllSite(ctx context.Context, org string) ([]nico.Site, *http.Response, error)

	// Instance List (for duplicate prevention)
	GetAllInstance(ctx context.Context, org string) ([]nico.Instance, *http.Response, error)

	// Instance
	CreateInstance(ctx context.Context, org string, req nico.InstanceCreateRequest) (*nico.Instance, *http.Response, error)
	GetInstance(ctx context.Context, org string, instanceId string) (*nico.Instance, *http.Response, error)
	DeleteInstance(ctx context.Context, org string, instanceId string) (*http.Response, error)

	// Site details
	GetSite(ctx context.Context, org string, siteId string) (*nico.Site, *http.Response, error)

	// Tenant
	GetCurrentTenant(ctx context.Context, org string) (*nico.Tenant, *http.Response, error)

	// Instance update and history
	UpdateInstance(
		ctx context.Context, org string, instanceId string, req nico.InstanceUpdateRequest,
	) (*nico.Instance, *http.Response, error)
	GetInstanceStatusHistory(
		ctx context.Context, org string, instanceId string,
	) ([]nico.StatusDetail, *http.Response, error)

	// Batch instance creation
	BatchCreateInstance(
		ctx context.Context, org string, req nico.BatchInstanceCreateRequest,
	) ([]nico.Instance, *http.Response, error)

	// VPC Prefix
	CreateVpcPrefix(
		ctx context.Context, org string, req nico.VpcPrefixCreateRequest,
	) (*nico.VpcPrefix, *http.Response, error)
	GetVpcPrefix(
		ctx context.Context, org string, vpcPrefixId string,
	) (*nico.VpcPrefix, *http.Response, error)
	DeleteVpcPrefix(
		ctx context.Context, org string, vpcPrefixId string,
	) (*http.Response, error)

	// VPC Peering
	CreateVpcPeering(
		ctx context.Context, org string, req nico.VpcPeeringCreateRequest,
	) (*nico.VpcPeering, *http.Response, error)
	GetVpcPeering(
		ctx context.Context, org string, peeringId string,
	) (*nico.VpcPeering, *http.Response, error)
	DeleteVpcPeering(
		ctx context.Context, org string, peeringId string,
	) (*http.Response, error)
}

// ncxInfraClient wraps the SDK APIClient and injects auth context
type ncxInfraClient struct {
	client *nico.APIClient
	token  string
}

func (c *ncxInfraClient) authCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, nico.ContextAccessToken, c.token)
}

// VPC methods
func (c *ncxInfraClient) CreateVpc(
	ctx context.Context, org string, req nico.VpcCreateRequest,
) (*nico.VPC, *http.Response, error) {
	return c.client.VPCAPI.CreateVpc(c.authCtx(ctx), org).VpcCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetVpc(ctx context.Context, org, vpcId string) (*nico.VPC, *http.Response, error) {
	return c.client.VPCAPI.GetVpc(c.authCtx(ctx), org, vpcId).Execute()
}
func (c *ncxInfraClient) DeleteVpc(ctx context.Context, org, vpcId string) (*http.Response, error) {
	return c.client.VPCAPI.DeleteVpc(c.authCtx(ctx), org, vpcId).Execute()
}

// Subnet methods
func (c *ncxInfraClient) CreateSubnet(
	ctx context.Context, org string, req nico.SubnetCreateRequest,
) (*nico.Subnet, *http.Response, error) {
	return c.client.SubnetAPI.CreateSubnet(c.authCtx(ctx), org).SubnetCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetSubnet(ctx context.Context, org, subnetId string) (*nico.Subnet, *http.Response, error) {
	return c.client.SubnetAPI.GetSubnet(c.authCtx(ctx), org, subnetId).Execute()
}
func (c *ncxInfraClient) DeleteSubnet(ctx context.Context, org, subnetId string) (*http.Response, error) {
	return c.client.SubnetAPI.DeleteSubnet(c.authCtx(ctx), org, subnetId).Execute()
}

// IPBlock methods
func (c *ncxInfraClient) CreateIpblock(
	ctx context.Context, org string, req nico.IpBlockCreateRequest,
) (*nico.IpBlock, *http.Response, error) {
	return c.client.IPBlockAPI.CreateIpblock(c.authCtx(ctx), org).IpBlockCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetIpblock(ctx context.Context, org, ipBlockId string) (*nico.IpBlock, *http.Response, error) {
	return c.client.IPBlockAPI.GetIpblock(c.authCtx(ctx), org, ipBlockId).Execute()
}
func (c *ncxInfraClient) DeleteIpblock(ctx context.Context, org, ipBlockId string) (*http.Response, error) {
	return c.client.IPBlockAPI.DeleteIpblock(c.authCtx(ctx), org, ipBlockId).Execute()
}

// NetworkSecurityGroup methods
func (c *ncxInfraClient) CreateNetworkSecurityGroup(
	ctx context.Context, org string, req nico.NetworkSecurityGroupCreateRequest,
) (*nico.NetworkSecurityGroup, *http.Response, error) {
	return c.client.NetworkSecurityGroupAPI.CreateNetworkSecurityGroup(
		c.authCtx(ctx), org,
	).NetworkSecurityGroupCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetNetworkSecurityGroup(
	ctx context.Context, org, nsgId string,
) (*nico.NetworkSecurityGroup, *http.Response, error) {
	return c.client.NetworkSecurityGroupAPI.GetNetworkSecurityGroup(c.authCtx(ctx), org, nsgId).Execute()
}
func (c *ncxInfraClient) DeleteNetworkSecurityGroup(ctx context.Context, org, nsgId string) (*http.Response, error) {
	return c.client.NetworkSecurityGroupAPI.DeleteNetworkSecurityGroup(c.authCtx(ctx), org, nsgId).Execute()
}

// Allocation methods

func (c *ncxInfraClient) CreateAllocation(
	ctx context.Context, org string, req nico.AllocationCreateRequest,
) (*nico.Allocation, *http.Response, error) {
	return c.client.AllocationAPI.CreateAllocation(c.authCtx(ctx), org).AllocationCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetAllocation(
	ctx context.Context, org, allocationId string,
) (*nico.Allocation, *http.Response, error) {
	return c.client.AllocationAPI.GetAllocation(c.authCtx(ctx), org, allocationId).Execute()
}
func (c *ncxInfraClient) GetAllAllocation(ctx context.Context, org string) ([]nico.Allocation, *http.Response, error) {
	return c.client.AllocationAPI.GetAllAllocation(c.authCtx(ctx), org).Execute()
}
func (c *ncxInfraClient) DeleteAllocation(ctx context.Context, org, allocationId string) (*http.Response, error) {
	return c.client.AllocationAPI.DeleteAllocation(c.authCtx(ctx), org, allocationId).Execute()
}

// Site methods

func (c *ncxInfraClient) GetAllSite(ctx context.Context, org string) ([]nico.Site, *http.Response, error) {
	return c.client.SiteAPI.GetAllSite(c.authCtx(ctx), org).Execute()
}

// Instance methods

func (c *ncxInfraClient) CreateInstance(
	ctx context.Context, org string, req nico.InstanceCreateRequest,
) (*nico.Instance, *http.Response, error) {
	return c.client.InstanceAPI.CreateInstance(c.authCtx(ctx), org).InstanceCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetInstance(
	ctx context.Context, org, instanceId string,
) (*nico.Instance, *http.Response, error) {
	return c.client.InstanceAPI.GetInstance(c.authCtx(ctx), org, instanceId).Execute()
}
func (c *ncxInfraClient) DeleteInstance(ctx context.Context, org, instanceId string) (*http.Response, error) {
	return c.client.InstanceAPI.DeleteInstance(c.authCtx(ctx), org, instanceId).Execute()
}
func (c *ncxInfraClient) GetAllInstance(ctx context.Context, org string) ([]nico.Instance, *http.Response, error) {
	return c.client.InstanceAPI.GetAllInstance(c.authCtx(ctx), org).Execute()
}

func (c *ncxInfraClient) GetSite(ctx context.Context, org, siteId string) (*nico.Site, *http.Response, error) {
	return c.client.SiteAPI.GetSite(c.authCtx(ctx), org, siteId).Execute()
}

func (c *ncxInfraClient) GetCurrentTenant(ctx context.Context, org string) (*nico.Tenant, *http.Response, error) {
	return c.client.TenantAPI.GetCurrentTenant(c.authCtx(ctx), org).Execute()
}

func (c *ncxInfraClient) UpdateInstance(
	ctx context.Context, org, instanceId string, req nico.InstanceUpdateRequest,
) (*nico.Instance, *http.Response, error) {
	return c.client.InstanceAPI.UpdateInstance(c.authCtx(ctx), org, instanceId).InstanceUpdateRequest(req).Execute()
}

func (c *ncxInfraClient) GetInstanceStatusHistory(
	ctx context.Context, org, instanceId string,
) ([]nico.StatusDetail, *http.Response, error) {
	return c.client.InstanceAPI.GetInstanceStatusHistory(c.authCtx(ctx), org, instanceId).Execute()
}

func (c *ncxInfraClient) BatchCreateInstance(
	ctx context.Context, org string, req nico.BatchInstanceCreateRequest,
) ([]nico.Instance, *http.Response, error) {
	return c.client.InstanceAPI.BatchCreateInstance(c.authCtx(ctx), org).BatchInstanceCreateRequest(req).Execute()
}

// VPC Prefix methods
func (c *ncxInfraClient) CreateVpcPrefix(
	ctx context.Context, org string, req nico.VpcPrefixCreateRequest,
) (*nico.VpcPrefix, *http.Response, error) {
	return c.client.VPCPrefixAPI.CreateVpcPrefix(c.authCtx(ctx), org).VpcPrefixCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetVpcPrefix(
	ctx context.Context, org, vpcPrefixId string,
) (*nico.VpcPrefix, *http.Response, error) {
	return c.client.VPCPrefixAPI.GetVpcPrefix(c.authCtx(ctx), org, vpcPrefixId).Execute()
}
func (c *ncxInfraClient) DeleteVpcPrefix(ctx context.Context, org, vpcPrefixId string) (*http.Response, error) {
	return c.client.VPCPrefixAPI.DeleteVpcPrefix(c.authCtx(ctx), org, vpcPrefixId).Execute()
}

// VPC Peering methods
func (c *ncxInfraClient) CreateVpcPeering(
	ctx context.Context, org string, req nico.VpcPeeringCreateRequest,
) (*nico.VpcPeering, *http.Response, error) {
	return c.client.VPCPeeringAPI.CreateVpcPeering(c.authCtx(ctx), org).VpcPeeringCreateRequest(req).Execute()
}
func (c *ncxInfraClient) GetVpcPeering(
	ctx context.Context, org, peeringId string,
) (*nico.VpcPeering, *http.Response, error) {
	return c.client.VPCPeeringAPI.GetVpcPeering(c.authCtx(ctx), org, peeringId).Execute()
}
func (c *ncxInfraClient) DeleteVpcPeering(ctx context.Context, org, peeringId string) (*http.Response, error) {
	return c.client.VPCPeeringAPI.DeleteVpcPeering(c.authCtx(ctx), org, peeringId).Execute()
}

// ClusterScopeParams defines parameters for creating a cluster scope
type ClusterScopeParams struct {
	Client               client.Client
	Cluster              *clusterv1.Cluster
	NcxInfraCluster *infrastructurev1.NcxInfraCluster
	NcxInfraClient  NcxInfraClientInterface // Optional: skip creating new client
	OrgName              string                       // Optional: org name
}

// ClusterScope defines the scope for cluster operations
type ClusterScope struct {
	client.Client

	Cluster              *clusterv1.Cluster
	NcxInfraCluster *infrastructurev1.NcxInfraCluster
	NcxInfraClient  NcxInfraClientInterface
	OrgName              string // Organization name for API calls
}

// NewClusterScope creates a new cluster scope
func NewClusterScope(ctx context.Context, params ClusterScopeParams) (*ClusterScope, error) {
	if params.Client == nil {
		return nil, fmt.Errorf("client is required")
	}
	if params.Cluster == nil {
		return nil, fmt.Errorf("cluster is required")
	}
	if params.NcxInfraCluster == nil {
		return nil, fmt.Errorf("ncx infra cluster is required")
	}

	var nvidiaCarbideClient NcxInfraClientInterface
	var orgName string

	// Use provided client if available (for testing), otherwise create a new one
	if params.NcxInfraClient != nil {
		nvidiaCarbideClient = params.NcxInfraClient
		orgName = params.OrgName
	} else {
		// Fetch credentials from secret
		secret := &corev1.Secret{}
		secretKey := types.NamespacedName{
			Name:      params.NcxInfraCluster.Spec.Authentication.SecretRef.Name,
			Namespace: params.NcxInfraCluster.Spec.Authentication.SecretRef.Namespace,
		}
		if secretKey.Namespace == "" {
			secretKey.Namespace = params.NcxInfraCluster.Namespace
		}

		if err := params.Client.Get(ctx, secretKey, secret); err != nil {
			return nil, fmt.Errorf("failed to get credentials secret: %w", err)
		}

		// Validate secret contains required fields
		endpoint, ok := secret.Data["endpoint"]
		if !ok {
			return nil, fmt.Errorf("secret %s is missing 'endpoint' field", secretKey.Name)
		}
		orgNameBytes, ok := secret.Data["orgName"]
		if !ok {
			return nil, fmt.Errorf("secret %s is missing 'orgName' field", secretKey.Name)
		}
		token, ok := secret.Data["token"]
		if !ok {
			return nil, fmt.Errorf("secret %s is missing 'token' field", secretKey.Name)
		}

		orgName = string(orgNameBytes)

		endpointStr := string(endpoint)
		if !strings.HasPrefix(endpointStr, "https://") {
			return nil, fmt.Errorf("endpoint must use https:// scheme, got: %s", endpointStr)
		}

		// Create NVIDIA Carbide API client with authentication
		sdkCfg := nico.NewConfiguration()
		sdkCfg.Servers = nico.ServerConfigurations{
			{URL: string(endpoint)},
		}
		nvidiaCarbideClient = &ncxInfraClient{
			client: nico.NewAPIClient(sdkCfg),
			token:  string(token),
		}
	}

	return &ClusterScope{
		Client:               params.Client,
		Cluster:              params.Cluster,
		NcxInfraCluster: params.NcxInfraCluster,
		NcxInfraClient:  nvidiaCarbideClient,
		OrgName:              orgName,
	}, nil
}

// SiteID returns the Site ID from the site reference
func (s *ClusterScope) SiteID(ctx context.Context) (string, error) {
	// If ID is directly specified, use it
	if s.NcxInfraCluster.Spec.SiteRef.ID != "" {
		return s.NcxInfraCluster.Spec.SiteRef.ID, nil
	}

	// Resolve site name to UUID via the Carbide API
	if s.NcxInfraCluster.Spec.SiteRef.Name != "" {
		sites, _, err := s.NcxInfraClient.GetAllSite(ctx, s.OrgName)
		if err != nil {
			return "", fmt.Errorf("failed to list sites: %w", err)
		}
		for _, site := range sites {
			if site.Name != nil && *site.Name == s.NcxInfraCluster.Spec.SiteRef.Name {
				if site.Id == nil {
					return "", fmt.Errorf("site %q found but has no ID", s.NcxInfraCluster.Spec.SiteRef.Name)
				}
				return *site.Id, nil
			}
		}
		return "", fmt.Errorf("site %q not found", s.NcxInfraCluster.Spec.SiteRef.Name)
	}

	return "", fmt.Errorf("site reference is empty")
}

// Name returns the cluster name
func (s *ClusterScope) Name() string {
	return s.Cluster.Name
}

// Namespace returns the cluster namespace
func (s *ClusterScope) Namespace() string {
	return s.Cluster.Namespace
}

// TenantID returns the tenant ID
func (s *ClusterScope) TenantID() string {
	return s.NcxInfraCluster.Spec.TenantID
}

// VPCID returns the VPC ID from status
func (s *ClusterScope) VPCID() string {
	return s.NcxInfraCluster.Status.VPCID
}

// SetVPCID sets the VPC ID in status
func (s *ClusterScope) SetVPCID(vpcID string) {
	s.NcxInfraCluster.Status.VPCID = vpcID
}

// SetReady sets the ready status
func (s *ClusterScope) SetReady(ready bool) {
	s.NcxInfraCluster.Status.Ready = ready
}

// IsReady returns whether the cluster is ready
func (s *ClusterScope) IsReady() bool {
	return s.NcxInfraCluster.Status.Ready
}

// SubnetIDs returns the subnet IDs from status
func (s *ClusterScope) SubnetIDs() map[string]string {
	if s.NcxInfraCluster.Status.NetworkStatus.SubnetIDs == nil {
		s.NcxInfraCluster.Status.NetworkStatus.SubnetIDs = make(map[string]string)
	}
	return s.NcxInfraCluster.Status.NetworkStatus.SubnetIDs
}

// SetSubnetID sets a subnet ID in status
func (s *ClusterScope) SetSubnetID(name, id string) {
	if s.NcxInfraCluster.Status.NetworkStatus.SubnetIDs == nil {
		s.NcxInfraCluster.Status.NetworkStatus.SubnetIDs = make(map[string]string)
	}
	s.NcxInfraCluster.Status.NetworkStatus.SubnetIDs[name] = id
}

// NSGID returns the network security group ID from status
func (s *ClusterScope) NSGID() string {
	return s.NcxInfraCluster.Status.NetworkStatus.NSGID
}

// SetNSGID sets the network security group ID in status
func (s *ClusterScope) SetNSGID(nsgID string) {
	s.NcxInfraCluster.Status.NetworkStatus.NSGID = nsgID
}

// IPBlockID returns the IP block ID from status
func (s *ClusterScope) IPBlockID() string {
	return s.NcxInfraCluster.Status.NetworkStatus.IPBlockID
}

// SetIPBlockID sets the IP block ID in status
func (s *ClusterScope) SetIPBlockID(ipBlockID string) {
	s.NcxInfraCluster.Status.NetworkStatus.IPBlockID = ipBlockID
}

func (s *ClusterScope) AllocationID() string {
	return s.NcxInfraCluster.Status.NetworkStatus.AllocationID
}

func (s *ClusterScope) SetAllocationID(allocationID string) {
	s.NcxInfraCluster.Status.NetworkStatus.AllocationID = allocationID
}

func (s *ClusterScope) ChildIPBlockID() string {
	return s.NcxInfraCluster.Status.NetworkStatus.ChildIPBlockID
}

func (s *ClusterScope) SetChildIPBlockID(childIPBlockID string) {
	s.NcxInfraCluster.Status.NetworkStatus.ChildIPBlockID = childIPBlockID
}

// VPCPrefixIDs returns the VPC Prefix IDs from status
func (s *ClusterScope) VPCPrefixIDs() map[string]string {
	if s.NcxInfraCluster.Status.NetworkStatus.VPCPrefixIDs == nil {
		s.NcxInfraCluster.Status.NetworkStatus.VPCPrefixIDs = make(map[string]string)
	}
	return s.NcxInfraCluster.Status.NetworkStatus.VPCPrefixIDs
}

// SetVPCPrefixID sets a VPC Prefix ID in status
func (s *ClusterScope) SetVPCPrefixID(name, id string) {
	if s.NcxInfraCluster.Status.NetworkStatus.VPCPrefixIDs == nil {
		s.NcxInfraCluster.Status.NetworkStatus.VPCPrefixIDs = make(map[string]string)
	}
	s.NcxInfraCluster.Status.NetworkStatus.VPCPrefixIDs[name] = id
}

// VPCPeeringIDs returns the VPC Peering IDs from status
func (s *ClusterScope) VPCPeeringIDs() map[string]string {
	if s.NcxInfraCluster.Status.NetworkStatus.VPCPeeringIDs == nil {
		s.NcxInfraCluster.Status.NetworkStatus.VPCPeeringIDs = make(map[string]string)
	}
	return s.NcxInfraCluster.Status.NetworkStatus.VPCPeeringIDs
}

// SetVPCPeeringID sets a VPC Peering ID in status
func (s *ClusterScope) SetVPCPeeringID(peerVPCID, peeringID string) {
	if s.NcxInfraCluster.Status.NetworkStatus.VPCPeeringIDs == nil {
		s.NcxInfraCluster.Status.NetworkStatus.VPCPeeringIDs = make(map[string]string)
	}
	s.NcxInfraCluster.Status.NetworkStatus.VPCPeeringIDs[peerVPCID] = peeringID
}

// PatchObject persists the cluster status
func (s *ClusterScope) PatchObject(ctx context.Context) error {
	return s.Client.Status().Update(ctx, s.NcxInfraCluster)
}

// Close closes the scope
func (s *ClusterScope) Close(ctx context.Context) error {
	return s.PatchObject(ctx)
}
