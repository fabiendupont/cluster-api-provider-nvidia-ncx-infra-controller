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

package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrastructurev1 "github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/api/v1beta1"
	ncxinframetrics "github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/internal/metrics"
	"github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/pkg/scope"
	nico "github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

const (
	// NcxInfraClusterFinalizer allows cleanup of NVIDIA Carbide resources before deletion
	NcxInfraClusterFinalizer = "ncxinfracluster.infrastructure.cluster.x-k8s.io"
)

// Condition types
const (
	VPCReadyCondition        clusterv1.ConditionType = "VPCReady"
	SubnetsReadyCondition    clusterv1.ConditionType = "SubnetsReady"
	NSGReadyCondition        clusterv1.ConditionType = "NSGReady"
	AllocationReadyCondition clusterv1.ConditionType = "AllocationReady"
	VPCPeeringReadyCondition clusterv1.ConditionType = "VPCPeeringReady"
)

// resourceTypeIPBlock is the Carbide allocation resource type for IP blocks.
const resourceTypeIPBlock = "IPBlock"

// NcxInfraClusterReconciler reconciles a NcxInfraCluster object
type NcxInfraClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// NcxInfraClient can be set for testing to inject a mock client
	NcxInfraClient scope.NcxInfraClientInterface
	// OrgName can be set for testing
	OrgName string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=ncxinfraclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=ncxinfraclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=ncxinfraclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles NcxInfraCluster reconciliation
func (r *NcxInfraClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the NcxInfraCluster instance
	nvidiaCarbideCluster := &infrastructurev1.NcxInfraCluster{}
	if err := r.Get(ctx, req.NamespacedName, nvidiaCarbideCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the owner Cluster
	cluster, err := util.GetOwnerCluster(ctx, r.Client, nvidiaCarbideCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		logger.Info("Waiting for Cluster Controller to set OwnerRef on NcxInfraCluster")
		return ctrl.Result{}, nil
	}

	// Check if cluster is paused
	if annotations.IsPaused(cluster, nvidiaCarbideCluster) {
		logger.Info("NcxInfraCluster or Cluster is marked as paused, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Initialize patch helper
	patchHelper, err := patch.NewHelper(nvidiaCarbideCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always attempt to patch the object and status after each reconciliation
	defer func() {
		if err := patchHelper.Patch(ctx, nvidiaCarbideCluster); err != nil {
			logger.Error(err, "failed to patch NcxInfraCluster")
		}
	}()

	// Create cluster scope
	clusterScope, err := scope.NewClusterScope(ctx, scope.ClusterScopeParams{
		Client:               r.Client,
		Cluster:              cluster,
		NcxInfraCluster: nvidiaCarbideCluster,
		NcxInfraClient:  r.NcxInfraClient, // Will be nil in production, set for tests
		OrgName:              r.OrgName,             // Will be empty in production (fetched from secret), set for tests
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create cluster scope: %w", err)
	}

	// Handle deletion
	if !nvidiaCarbideCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, clusterScope)
	}

	// Handle normal reconciliation
	return r.reconcileNormal(ctx, clusterScope)
}

func (r *NcxInfraClusterReconciler) reconcileNormal(
	ctx context.Context, clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NcxInfraCluster")

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(clusterScope.NcxInfraCluster, NcxInfraClusterFinalizer) {
		controllerutil.AddFinalizer(clusterScope.NcxInfraCluster, NcxInfraClusterFinalizer)
		return ctrl.Result{Requeue: true}, nil
	}

	// Get Site ID
	siteID, err := clusterScope.SiteID(ctx)
	if err != nil {
		conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
			Type:    string(VPCReadyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "SiteNotFound",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}

	// Ensure IP block and allocation exist before VPC creation
	// (the tenant must have an allocation with the site to create VPCs)
	if _, err := r.ensureIPBlockAndAllocation(ctx, clusterScope, siteID); err != nil {
		conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
			Type:    string(AllocationReadyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "AllocationFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}
	conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
		Type:   string(AllocationReadyCondition),
		Status: metav1.ConditionTrue,
		Reason: "AllocationReady",
	})

	// Reconcile VPC
	if err := r.reconcileVPC(ctx, clusterScope, siteID); err != nil {
		conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
			Type:    string(VPCReadyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "VPCReconcileFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}
	conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
		Type:   string(VPCReadyCondition),
		Status: metav1.ConditionTrue,
		Reason: "VPCReady",
	})

	// Reconcile Subnets
	if err := r.reconcileSubnets(ctx, clusterScope, siteID); err != nil {
		conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
			Type:    string(SubnetsReadyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "SubnetReconcileFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}
	conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
		Type:   string(SubnetsReadyCondition),
		Status: metav1.ConditionTrue,
		Reason: "SubnetsReady",
	})

	// Reconcile VPC Prefixes (if specified, for FNN VPCs)
	if len(clusterScope.NcxInfraCluster.Spec.VPCPrefixes) > 0 {
		if err := r.reconcileVPCPrefixes(ctx, clusterScope, siteID); err != nil {
			conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
				Type:    string(SubnetsReadyCondition),
				Status:  metav1.ConditionFalse,
				Reason:  "VPCPrefixReconcileFailed",
				Message: err.Error(),
			})
			return ctrl.Result{}, err
		}
	}

	// Reconcile Network Security Group (if specified)
	if clusterScope.NcxInfraCluster.Spec.VPC.NetworkSecurityGroup != nil {
		if err := r.reconcileNSG(ctx, clusterScope, siteID); err != nil {
			conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
				Type:    string(NSGReadyCondition),
				Status:  metav1.ConditionFalse,
				Reason:  "NSGReconcileFailed",
				Message: err.Error(),
			})
			return ctrl.Result{}, err
		}
		conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
			Type:   string(NSGReadyCondition),
			Status: metav1.ConditionTrue,
			Reason: "NSGReady",
		})
	}

	// Reconcile VPC Peerings (if specified)
	if len(clusterScope.NcxInfraCluster.Spec.VPCPeerings) > 0 {
		if err := r.reconcileVPCPeerings(ctx, clusterScope, siteID); err != nil {
			conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
				Type:    string(VPCPeeringReadyCondition),
				Status:  metav1.ConditionFalse,
				Reason:  "VPCPeeringReconcileFailed",
				Message: err.Error(),
			})
			return ctrl.Result{}, err
		}
		conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
			Type:   string(VPCPeeringReadyCondition),
			Status: metav1.ConditionTrue,
			Reason: "VPCPeeringReady",
		})
	}

	// Mark cluster as ready
	clusterScope.SetReady(true)
	conditions.Set(clusterScope.NcxInfraCluster, metav1.Condition{
		Type:   string(clusterv1.ReadyCondition),
		Status: metav1.ConditionTrue,
		Reason: "NcxInfraClusterReady",
	})

	r.recordEvent(clusterScope.NcxInfraCluster, "ClusterInfrastructureReady",
		"Cluster infrastructure is ready")
	logger.Info("Successfully reconciled NcxInfraCluster")
	return ctrl.Result{}, nil
}

func (r *NcxInfraClusterReconciler) reconcileVPC(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	// Check if VPC already exists
	if clusterScope.VPCID() != "" {
		// Verify VPC still exists in NVIDIA Carbide
		vpc, _, err := clusterScope.NcxInfraClient.GetVpc(ctx, clusterScope.OrgName, clusterScope.VPCID())
		if err != nil {
			logger.Error(err, "VPC not found in NVIDIA Carbide, will recreate", "vpcID", clusterScope.VPCID())
			clusterScope.SetVPCID("")
		} else if vpc != nil {
			logger.V(1).Info("VPC already exists", "vpcID", clusterScope.VPCID())
			return nil
		} else {
			logger.Info("VPC not found, will recreate", "vpcID", clusterScope.VPCID())
			clusterScope.SetVPCID("")
		}
	}

	// Create VPC
	vpcSpec := clusterScope.NcxInfraCluster.Spec.VPC

	vpcReq := nico.VpcCreateRequest{
		Name:   vpcSpec.Name,
		SiteId: siteID,
	}
	if vpcSpec.NetworkVirtualizationType != "" {
		netVirtType := vpcSpec.NetworkVirtualizationType
		vpcReq.NetworkVirtualizationType = *nico.NewNullableString(&netVirtType)
	}
	if vpcSpec.NVLinkLogicalPartitionID != "" {
		vpcReq.NvLinkLogicalPartitionId = *nico.NewNullableString(&vpcSpec.NVLinkLogicalPartitionID)
	}
	if vpcSpec.Vni != nil {
		vpcReq.Vni = *nico.NewNullableInt32(vpcSpec.Vni)
	}
	if vpcSpec.Description != "" {
		vpcReq.Description = &vpcSpec.Description
	}
	if len(vpcSpec.Labels) > 0 {
		vpcReq.Labels = vpcSpec.Labels
	}

	logger.Info("Creating VPC", "name", vpcSpec.Name, "siteID", siteID)
	vpc, httpResp, err := clusterScope.NcxInfraClient.CreateVpc(ctx, clusterScope.OrgName, vpcReq)
	if err != nil {
		return fmt.Errorf("failed to create VPC: %w", err)
	}

	if httpResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to create VPC, status %d", httpResp.StatusCode)
	}

	if vpc == nil || vpc.Id == nil {
		return fmt.Errorf("VPC ID missing in response")
	}

	clusterScope.SetVPCID(*vpc.Id)
	logger.Info("Successfully created VPC", "vpcID", *vpc.Id)
	r.recordEvent(clusterScope.NcxInfraCluster, "VPCCreated",
		"Successfully created VPC %s", *vpc.Id)
	ncxinframetrics.VPCCount.WithLabelValues(siteID).Inc()

	return nil
}

// parseCIDR parses a CIDR string and returns the prefix length.
func parseCIDR(cidr string) (prefixLength int, err error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, fmt.Errorf("invalid CIDR %s: %w", cidr, err)
	}
	ones, _ := ipNet.Mask.Size()
	return ones, nil
}

// ensureIPBlockAndAllocation ensures an IP block and allocation exist for subnet allocation.
// The allocation creates a child IP block owned by the tenant, which must be used for subnets.
// Returns the child IP block ID.
func (r *NcxInfraClusterReconciler) ensureIPBlockAndAllocation(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) (string, error) {
	logger := log.FromContext(ctx)

	// If we already have a child IP block ID, verify it exists
	if clusterScope.ChildIPBlockID() != "" {
		ipBlock, _, err := clusterScope.NcxInfraClient.GetIpblock(ctx, clusterScope.OrgName, clusterScope.ChildIPBlockID())
		if err == nil && ipBlock != nil {
			logger.V(1).Info("Child IP block already exists", "childIPBlockID", clusterScope.ChildIPBlockID())
			return clusterScope.ChildIPBlockID(), nil
		}
		logger.Info("Existing child IP block not found, will recreate", "oldChildIPBlockID", clusterScope.ChildIPBlockID())
		clusterScope.SetChildIPBlockID("")
		clusterScope.SetAllocationID("")
	}

	// Step 1: Create parent IP block if needed
	parentIPBlockID := clusterScope.IPBlockID()
	if parentIPBlockID == "" {
		ipBlockName := fmt.Sprintf("%s-ipblock", clusterScope.NcxInfraCluster.Name)
		ipBlockReq := nico.IpBlockCreateRequest{
			Name:            ipBlockName,
			Prefix:          "10.0.0.0",
			PrefixLength:    16,
			ProtocolVersion: "IPv4",
			RoutingType:     "DatacenterOnly",
			SiteId:          siteID,
		}

		logger.Info("Creating IP block", "name", ipBlockName, "prefix", "10.0.0.0/16", "siteID", siteID)
		ipBlock, httpResp, err := clusterScope.NcxInfraClient.CreateIpblock(ctx, clusterScope.OrgName, ipBlockReq)
		if err != nil {
			return "", fmt.Errorf("failed to create IP block: %w", err)
		}
		if httpResp.StatusCode != http.StatusCreated {
			return "", fmt.Errorf("failed to create IP block, status %d", httpResp.StatusCode)
		}
		if ipBlock == nil || ipBlock.Id == nil {
			return "", fmt.Errorf("IP block ID missing in response")
		}

		parentIPBlockID = *ipBlock.Id
		clusterScope.SetIPBlockID(parentIPBlockID)
		logger.Info("Successfully created IP block", "ipBlockID", parentIPBlockID)
	}

	// Step 2: Create allocation to link tenant to IP block (creates child IP block)
	if clusterScope.AllocationID() == "" {
		allocName := fmt.Sprintf("%s-allocation", clusterScope.NcxInfraCluster.Name)
		resourceType := resourceTypeIPBlock
		allocReq := nico.AllocationCreateRequest{
			Name:     allocName,
			TenantId: clusterScope.TenantID(),
			SiteId:   siteID,
			AllocationConstraints: []nico.AllocationConstraintCreateRequest{
				{
					ResourceType:    &resourceType,
					ResourceTypeId:  parentIPBlockID,
					ConstraintType:  "OnDemand",
					ConstraintValue: 24,
				},
			},
		}

		logger.Info("Creating allocation", "name", allocName, "tenantID", clusterScope.TenantID(), "siteID", siteID)
		alloc, httpResp, err := clusterScope.NcxInfraClient.CreateAllocation(ctx, clusterScope.OrgName, allocReq)

		// The SDK may fail to deserialize the response (e.g., unrecognized status enum)
		// even when the allocation was created successfully. Check the HTTP status first.
		if httpResp != nil && httpResp.StatusCode == http.StatusCreated {
			// Allocation created — extract IDs if available
			if alloc != nil && alloc.Id != nil {
				clusterScope.SetAllocationID(*alloc.Id)
				logger.Info("Successfully created allocation", "allocationID", *alloc.Id)
				r.extractChildIPBlockID(clusterScope, alloc)
			} else if err != nil {
				// SDK deserialization failed but allocation was created.
				// We cannot recover without knowing the allocation ID.
				logger.Info("Allocation created (201) but SDK deserialization failed, will retry to get IDs", "error", err)
				return "", fmt.Errorf("allocation created but response parsing failed, will retry: %w", err)
			}
		} else if httpResp != nil && httpResp.StatusCode == http.StatusConflict {
			// 409 Conflict — allocation already exists from a previous attempt.
			// Query existing allocations to find the matching one.
			logger.Info("Allocation already exists (409 Conflict), querying existing allocations")
			if foundAlloc, err := r.findExistingAllocation(ctx, clusterScope, allocName); err != nil {
				return "", fmt.Errorf("failed to find existing allocation: %w", err)
			} else if foundAlloc != nil && foundAlloc.Id != nil {
				clusterScope.SetAllocationID(*foundAlloc.Id)
				r.extractChildIPBlockID(clusterScope, foundAlloc)
				logger.Info("Found existing allocation", "allocationID", *foundAlloc.Id)
			} else {
				return "", fmt.Errorf("allocation conflict but could not find existing allocation")
			}
		} else if err != nil {
			return "", fmt.Errorf("failed to create allocation: %w", err)
		}
	}

	// If we have an allocation ID but no child IP block ID, query the allocation
	if clusterScope.AllocationID() != "" && clusterScope.ChildIPBlockID() == "" {
		alloc, _, err := clusterScope.NcxInfraClient.GetAllocation(ctx, clusterScope.OrgName, clusterScope.AllocationID())
		if err != nil {
			return "", fmt.Errorf("failed to get allocation %s: %w", clusterScope.AllocationID(), err)
		}
		if alloc != nil {
			r.extractChildIPBlockID(clusterScope, alloc)
		}
	}

	childIPBlockID := clusterScope.ChildIPBlockID()
	if childIPBlockID == "" {
		return "", fmt.Errorf("child IP block ID not found after allocation creation")
	}

	return childIPBlockID, nil
}

// extractChildIPBlockID extracts the child IP block ID from an allocation's constraints.
func (r *NcxInfraClusterReconciler) extractChildIPBlockID(
	clusterScope *scope.ClusterScope, alloc *nico.Allocation,
) {
	for _, ac := range alloc.AllocationConstraints {
		if ac.ResourceType != nil && *ac.ResourceType == resourceTypeIPBlock {
			if derivedID := ac.DerivedResourceId.Get(); derivedID != nil {
				clusterScope.SetChildIPBlockID(*derivedID)
				break
			}
		}
	}
}

// findExistingAllocation queries all allocations and returns the one matching the given name.
func (r *NcxInfraClusterReconciler) findExistingAllocation(
	ctx context.Context, clusterScope *scope.ClusterScope, name string,
) (*nico.Allocation, error) {
	// The GetAllAllocation SDK method doesn't support name filtering directly,
	// so we fetch all and filter client-side.
	allocations, _, err := clusterScope.NcxInfraClient.GetAllAllocation(ctx, clusterScope.OrgName)
	if err != nil {
		return nil, err
	}
	for i := range allocations {
		if allocations[i].Name != nil && *allocations[i].Name == name {
			return &allocations[i], nil
		}
	}
	return nil, nil
}

func (r *NcxInfraClusterReconciler) reconcileSubnets(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	vpcID := clusterScope.VPCID()
	if vpcID == "" {
		return fmt.Errorf("VPC ID is empty")
	}

	// Ensure IP block and allocation exist (creates child IP block for tenant)
	childIPBlockID, err := r.ensureIPBlockAndAllocation(ctx, clusterScope, siteID)
	if err != nil {
		return fmt.Errorf("failed to ensure IP block and allocation: %w", err)
	}

	subnetIDs := clusterScope.SubnetIDs()

	// Reconcile each subnet
	for _, subnetSpec := range clusterScope.NcxInfraCluster.Spec.Subnets {
		// Check if subnet already exists
		if existingID, exists := subnetIDs[subnetSpec.Name]; exists {
			// Verify subnet still exists in NVIDIA Carbide
			subnet, _, err := clusterScope.NcxInfraClient.GetSubnet(ctx, clusterScope.OrgName, existingID)
			if err != nil || subnet == nil {
				logger.Error(err, "Subnet not found in NVIDIA Carbide, will recreate",
					"subnetName", subnetSpec.Name, "subnetID", existingID)
				delete(subnetIDs, subnetSpec.Name)
			} else {
				logger.V(1).Info("Subnet already exists", "subnetName", subnetSpec.Name, "subnetID", existingID)
				continue
			}
		}

		// Parse CIDR to get prefix length
		prefixLength, err := parseCIDR(subnetSpec.CIDR)
		if err != nil {
			return fmt.Errorf("failed to parse CIDR for subnet %s: %w", subnetSpec.Name, err)
		}

		// Create subnet using child IP block (tenant-owned, from allocation)
		subnetReq := nico.SubnetCreateRequest{
			Name:         subnetSpec.Name,
			VpcId:        vpcID,
			Ipv4BlockId:  &childIPBlockID,
			PrefixLength: int32(prefixLength),
		}

		logger.Info("Creating subnet",
			"name", subnetSpec.Name, "cidr", subnetSpec.CIDR,
			"prefixLength", prefixLength, "vpcID", vpcID,
			"childIPBlockID", childIPBlockID)
		subnet, httpResp, err := clusterScope.NcxInfraClient.CreateSubnet(ctx, clusterScope.OrgName, subnetReq)
		if err != nil {
			return fmt.Errorf("failed to create subnet %s: %w", subnetSpec.Name, err)
		}

		if httpResp.StatusCode != http.StatusCreated {
			return fmt.Errorf("failed to create subnet %s, status %d", subnetSpec.Name, httpResp.StatusCode)
		}

		if subnet == nil || subnet.Id == nil {
			return fmt.Errorf("subnet ID missing in response for %s", subnetSpec.Name)
		}

		clusterScope.SetSubnetID(subnetSpec.Name, *subnet.Id)
		logger.Info("Successfully created subnet", "subnetName", subnetSpec.Name, "subnetID", *subnet.Id)
		r.recordEvent(clusterScope.NcxInfraCluster, "SubnetCreated",
			"Successfully created subnet %s (%s)", subnetSpec.Name, *subnet.Id)
	}

	return nil
}

func (r *NcxInfraClusterReconciler) reconcileVPCPrefixes(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	vpcID := clusterScope.VPCID()
	if vpcID == "" {
		return fmt.Errorf("VPC ID is empty")
	}

	// Ensure IP block and allocation exist (creates child IP block for tenant)
	childIPBlockID, err := r.ensureIPBlockAndAllocation(ctx, clusterScope, siteID)
	if err != nil {
		return fmt.Errorf("failed to ensure IP block and allocation: %w", err)
	}

	vpcPrefixIDs := clusterScope.VPCPrefixIDs()

	for _, prefixSpec := range clusterScope.NcxInfraCluster.Spec.VPCPrefixes {
		// Check if VPC Prefix already exists
		if existingID, exists := vpcPrefixIDs[prefixSpec.Name]; exists {
			prefix, _, err := clusterScope.NcxInfraClient.GetVpcPrefix(ctx, clusterScope.OrgName, existingID)
			if err != nil || prefix == nil {
				logger.Error(err, "VPC Prefix not found, will recreate",
					"prefixName", prefixSpec.Name, "prefixID", existingID)
				delete(vpcPrefixIDs, prefixSpec.Name)
			} else {
				logger.V(1).Info("VPC Prefix already exists", "prefixName", prefixSpec.Name, "prefixID", existingID)
				continue
			}
		}

		prefixLength, err := parseCIDR(prefixSpec.CIDR)
		if err != nil {
			return fmt.Errorf("failed to parse CIDR for VPC prefix %s: %w", prefixSpec.Name, err)
		}

		prefixReq := nico.VpcPrefixCreateRequest{
			Name:         prefixSpec.Name,
			VpcId:        vpcID,
			IpBlockId:    &childIPBlockID,
			PrefixLength: int32(prefixLength),
		}

		logger.Info("Creating VPC Prefix",
			"name", prefixSpec.Name, "cidr", prefixSpec.CIDR,
			"prefixLength", prefixLength, "vpcID", vpcID)
		prefix, httpResp, err := clusterScope.NcxInfraClient.CreateVpcPrefix(ctx, clusterScope.OrgName, prefixReq)
		if err != nil {
			return fmt.Errorf("failed to create VPC prefix %s: %w", prefixSpec.Name, err)
		}

		if httpResp.StatusCode != http.StatusCreated {
			return fmt.Errorf("failed to create VPC prefix %s, status %d", prefixSpec.Name, httpResp.StatusCode)
		}

		if prefix == nil || prefix.Id == nil {
			return fmt.Errorf("VPC prefix ID missing in response for %s", prefixSpec.Name)
		}

		clusterScope.SetVPCPrefixID(prefixSpec.Name, *prefix.Id)
		logger.Info("Successfully created VPC Prefix", "prefixName", prefixSpec.Name, "prefixID", *prefix.Id)
		r.recordEvent(clusterScope.NcxInfraCluster, "VPCPrefixCreated",
			"Successfully created VPC Prefix %s (%s)", prefixSpec.Name, *prefix.Id)
	}

	return nil
}

func (r *NcxInfraClusterReconciler) reconcileVPCPeerings(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	vpcID := clusterScope.VPCID()
	if vpcID == "" {
		return fmt.Errorf("VPC ID is empty")
	}

	peeringIDs := clusterScope.VPCPeeringIDs()

	for _, peeringSpec := range clusterScope.NcxInfraCluster.Spec.VPCPeerings {
		// Check if peering already exists
		if existingID, exists := peeringIDs[peeringSpec.PeerVPCID]; exists {
			peering, _, err := clusterScope.NcxInfraClient.GetVpcPeering(ctx, clusterScope.OrgName, existingID)
			if err != nil || peering == nil {
				logger.Error(err, "VPC Peering not found, will recreate",
					"peerVpcId", peeringSpec.PeerVPCID, "peeringID", existingID)
				delete(peeringIDs, peeringSpec.PeerVPCID)
			} else {
				logger.V(1).Info("VPC Peering already exists",
					"peerVpcId", peeringSpec.PeerVPCID, "peeringID", existingID)
				continue
			}
		}

		peeringReq := nico.VpcPeeringCreateRequest{
			Vpc1Id: vpcID,
			Vpc2Id: peeringSpec.PeerVPCID,
			SiteId: siteID,
		}

		logger.Info("Creating VPC Peering",
			"vpc1Id", vpcID, "vpc2Id", peeringSpec.PeerVPCID, "siteID", siteID)
		peering, httpResp, err := clusterScope.NcxInfraClient.CreateVpcPeering(ctx, clusterScope.OrgName, peeringReq)
		if err != nil {
			return fmt.Errorf("failed to create VPC peering with %s: %w", peeringSpec.PeerVPCID, err)
		}

		if httpResp.StatusCode != http.StatusCreated {
			return fmt.Errorf("failed to create VPC peering with %s, status %d", peeringSpec.PeerVPCID, httpResp.StatusCode)
		}

		if peering == nil || peering.Id == nil {
			return fmt.Errorf("VPC peering ID missing in response for peer %s", peeringSpec.PeerVPCID)
		}

		clusterScope.SetVPCPeeringID(peeringSpec.PeerVPCID, *peering.Id)
		logger.Info("Successfully created VPC Peering",
			"peerVpcId", peeringSpec.PeerVPCID, "peeringID", *peering.Id)
		r.recordEvent(clusterScope.NcxInfraCluster, "VPCPeeringCreated",
			"Successfully created VPC Peering %s with peer %s", *peering.Id, peeringSpec.PeerVPCID)
	}

	return nil
}

func (r *NcxInfraClusterReconciler) reconcileNSG(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	nsgSpec := clusterScope.NcxInfraCluster.Spec.VPC.NetworkSecurityGroup

	// Check if NSG already exists
	if clusterScope.NSGID() != "" {
		// Verify NSG still exists in NVIDIA Carbide
		nsg, _, err := clusterScope.NcxInfraClient.GetNetworkSecurityGroup(
			ctx, clusterScope.OrgName, clusterScope.NSGID())
		if err != nil || nsg == nil {
			logger.Error(err, "NSG not found in NVIDIA Carbide, will recreate", "nsgID", clusterScope.NSGID())
			clusterScope.SetNSGID("")
		} else {
			logger.V(1).Info("NSG already exists", "nsgID", clusterScope.NSGID())
			return nil
		}
	}

	// Convert NSG rules from CRD types to API types
	rules := make([]nico.NetworkSecurityGroupRule, 0, len(nsgSpec.Rules))
	for _, rule := range nsgSpec.Rules {
		// API requires both source and destination prefixes
		// Use "0.0.0.0/0" as default (any) if not specified
		sourcePrefix := rule.SourceCIDR
		if sourcePrefix == "" {
			sourcePrefix = "0.0.0.0/0"
		}
		destPrefix := "0.0.0.0/0" // Default to any destination

		ruleName := rule.Name
		nsgRule := nico.NetworkSecurityGroupRule{
			Name:              *nico.NewNullableString(&ruleName),
			Direction:         strings.ToLower(rule.Direction),
			Protocol:          strings.ToLower(rule.Protocol),
			Action:            strings.ToLower(rule.Action),
			SourcePrefix:      sourcePrefix,
			DestinationPrefix: destPrefix,
		}

		// Map port range to destination port range
		if rule.PortRange != "" {
			portRange := rule.PortRange
			nsgRule.DestinationPortRange = *nico.NewNullableString(&portRange)
		}

		rules = append(rules, nsgRule)
	}

	// Create NSG
	nsgReq := nico.NetworkSecurityGroupCreateRequest{
		Name:   nsgSpec.Name,
		SiteId: siteID,
	}
	if len(rules) > 0 {
		nsgReq.Rules = rules
	}

	logger.Info("Creating NSG", "name", nsgSpec.Name, "siteID", siteID)
	nsg, httpResp, err := clusterScope.NcxInfraClient.CreateNetworkSecurityGroup(ctx, clusterScope.OrgName, nsgReq)
	if err != nil {
		return fmt.Errorf("failed to create NSG: %w", err)
	}

	if httpResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to create NSG, status %d", httpResp.StatusCode)
	}

	if nsg == nil || nsg.Id == nil {
		return fmt.Errorf("NSG ID missing in response")
	}

	clusterScope.SetNSGID(*nsg.Id)
	logger.Info("Successfully created NSG", "nsgID", *nsg.Id)
	r.recordEvent(clusterScope.NcxInfraCluster, "NSGCreated",
		"Successfully created NSG %s", *nsg.Id)

	return nil
}

//nolint:unparam // ctrl.Result is part of the reconciler interface contract
func (r *NcxInfraClusterReconciler) reconcileDelete(
	ctx context.Context, clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Deleting NcxInfraCluster")

	// Delete NSG if it exists
	if clusterScope.NSGID() != "" {
		logger.Info("Deleting NSG", "nsgID", clusterScope.NSGID())
		if err := r.deleteResource(ctx, clusterScope, "NSG", clusterScope.NSGID(),
			clusterScope.NcxInfraClient.DeleteNetworkSecurityGroup); err != nil {
			return ctrl.Result{}, err
		}
		clusterScope.SetNSGID("")
	}

	// Delete VPC Peerings
	for peerVPCID, peeringID := range clusterScope.VPCPeeringIDs() {
		logger.Info("Deleting VPC Peering", "peerVpcId", peerVPCID, "peeringID", peeringID)
		if err := r.deleteResource(ctx, clusterScope, "VPC peering", peeringID,
			clusterScope.NcxInfraClient.DeleteVpcPeering); err != nil {
			return ctrl.Result{}, err
		}
		delete(clusterScope.VPCPeeringIDs(), peerVPCID)
	}

	// Delete VPC Prefixes
	for prefixName, prefixID := range clusterScope.VPCPrefixIDs() {
		logger.Info("Deleting VPC Prefix", "prefixName", prefixName, "prefixID", prefixID)
		if err := r.deleteResource(ctx, clusterScope, "VPC prefix", prefixID,
			clusterScope.NcxInfraClient.DeleteVpcPrefix); err != nil {
			return ctrl.Result{}, err
		}
		delete(clusterScope.VPCPrefixIDs(), prefixName)
	}

	// Delete Subnets
	for subnetName, subnetID := range clusterScope.SubnetIDs() {
		logger.Info("Deleting subnet", "subnetName", subnetName, "subnetID", subnetID)
		if err := r.deleteResource(ctx, clusterScope, "subnet", subnetID,
			clusterScope.NcxInfraClient.DeleteSubnet); err != nil {
			return ctrl.Result{}, err
		}
		delete(clusterScope.SubnetIDs(), subnetName)
	}

	// Delete Allocation if it exists
	if clusterScope.AllocationID() != "" {
		logger.Info("Deleting allocation", "allocationID", clusterScope.AllocationID())
		if err := r.deleteResource(ctx, clusterScope, "allocation", clusterScope.AllocationID(),
			clusterScope.NcxInfraClient.DeleteAllocation); err != nil {
			return ctrl.Result{}, err
		}
		clusterScope.SetAllocationID("")
	}

	// Delete child IP block if it exists
	if clusterScope.ChildIPBlockID() != "" {
		logger.Info("Deleting child IP block", "childIPBlockID", clusterScope.ChildIPBlockID())
		if err := r.deleteResource(ctx, clusterScope, "child IP block", clusterScope.ChildIPBlockID(),
			clusterScope.NcxInfraClient.DeleteIpblock); err != nil {
			return ctrl.Result{}, err
		}
		clusterScope.SetChildIPBlockID("")
	}

	// Delete parent IP block if it exists
	if clusterScope.IPBlockID() != "" {
		logger.Info("Deleting parent IP block", "ipBlockID", clusterScope.IPBlockID())
		if err := r.deleteResource(ctx, clusterScope, "parent IP block", clusterScope.IPBlockID(),
			clusterScope.NcxInfraClient.DeleteIpblock); err != nil {
			return ctrl.Result{}, err
		}
		clusterScope.SetIPBlockID("")
	}

	// Delete VPC
	if clusterScope.VPCID() != "" {
		logger.Info("Deleting VPC", "vpcID", clusterScope.VPCID())
		if err := r.deleteResource(ctx, clusterScope, "VPC", clusterScope.VPCID(),
			clusterScope.NcxInfraClient.DeleteVpc); err != nil {
			return ctrl.Result{}, err
		}
		clusterScope.SetVPCID("")
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(clusterScope.NcxInfraCluster, NcxInfraClusterFinalizer)

	logger.Info("Successfully deleted NcxInfraCluster")
	return ctrl.Result{}, nil
}

// deleteResource calls a delete API method and handles 404 (already deleted) gracefully.
func (r *NcxInfraClusterReconciler) deleteResource(
	ctx context.Context, clusterScope *scope.ClusterScope,
	resourceType, resourceID string,
	deleteFn func(ctx context.Context, org string, id string) (*http.Response, error),
) error {
	logger := log.FromContext(ctx)

	httpResp, err := deleteFn(ctx, clusterScope.OrgName, resourceID)
	if err != nil {
		if httpResp != nil && httpResp.StatusCode == http.StatusNotFound {
			logger.Info("Resource already deleted", "type", resourceType, "id", resourceID)
			return nil
		}
		return fmt.Errorf("failed to delete %s %s: %w", resourceType, resourceID, err)
	}
	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent &&
		httpResp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to delete %s %s, status %d", resourceType, resourceID, httpResp.StatusCode)
	}
	return nil
}

// recordEvent records a Normal event on the given object if a Recorder is set.
func (r *NcxInfraClusterReconciler) recordEvent(obj runtime.Object, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, corev1.EventTypeNormal, reason, messageFmt, args...)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NcxInfraClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1.NcxInfraCluster{}).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(
				util.ClusterToInfrastructureMapFunc(
					ctx,
					infrastructurev1.GroupVersion.WithKind("NcxInfraCluster"),
					mgr.GetClient(),
					&infrastructurev1.NcxInfraCluster{},
				),
			),
		).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(
			mgr.GetScheme(), ctrl.Log.WithName("ncxinfracluster"), "")).
		Named("ncxinfracluster").
		Complete(r)
}
