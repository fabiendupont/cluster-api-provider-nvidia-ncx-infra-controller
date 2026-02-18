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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

	infrastructurev1 "github.com/fabiendupont/cluster-api-provider-nvidia-carbide/api/v1beta1"
	"github.com/fabiendupont/cluster-api-provider-nvidia-carbide/pkg/scope"
	bmm "github.com/nvidia/bare-metal-manager-rest/sdk/standard"
)

const (
	// NvidiaCarbideClusterFinalizer allows cleanup of NVIDIA Carbide resources before deletion
	NvidiaCarbideClusterFinalizer = "nvidiacarbidecluster.infrastructure.cluster.x-k8s.io"
)

// Condition types
const (
	VPCReadyCondition     clusterv1.ConditionType = "VPCReady"
	SubnetsReadyCondition clusterv1.ConditionType = "SubnetsReady"
	NSGReadyCondition     clusterv1.ConditionType = "NSGReady"
)

// NvidiaCarbideClusterReconciler reconciles a NvidiaCarbideCluster object
type NvidiaCarbideClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NvidiaCarbideClient can be set for testing to inject a mock client
	NvidiaCarbideClient scope.NvidiaCarbideClientInterface
	// OrgName can be set for testing
	OrgName string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbideclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbideclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbideclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile handles NvidiaCarbideCluster reconciliation
func (r *NvidiaCarbideClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the NvidiaCarbideCluster instance
	nvidiaCarbideCluster := &infrastructurev1.NvidiaCarbideCluster{}
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
		logger.Info("Waiting for Cluster Controller to set OwnerRef on NvidiaCarbideCluster")
		return ctrl.Result{}, nil
	}

	// Check if cluster is paused
	if annotations.IsPaused(cluster, nvidiaCarbideCluster) {
		logger.Info("NvidiaCarbideCluster or Cluster is marked as paused, skipping reconciliation")
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
			logger.Error(err, "failed to patch NvidiaCarbideCluster")
		}
	}()

	// Create cluster scope
	clusterScope, err := scope.NewClusterScope(ctx, scope.ClusterScopeParams{
		Client:               r.Client,
		Cluster:              cluster,
		NvidiaCarbideCluster: nvidiaCarbideCluster,
		NvidiaCarbideClient:  r.NvidiaCarbideClient, // Will be nil in production, set for tests
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

func (r *NvidiaCarbideClusterReconciler) reconcileNormal(
	ctx context.Context, clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NvidiaCarbideCluster")

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(clusterScope.NvidiaCarbideCluster, NvidiaCarbideClusterFinalizer) {
		controllerutil.AddFinalizer(clusterScope.NvidiaCarbideCluster, NvidiaCarbideClusterFinalizer)
		return ctrl.Result{Requeue: true}, nil
	}

	// Get Site ID
	siteID, err := clusterScope.SiteID(ctx)
	if err != nil {
		conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
			Type:    string(VPCReadyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "SiteNotFound",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}

	// Reconcile VPC
	if err := r.reconcileVPC(ctx, clusterScope, siteID); err != nil {
		conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
			Type:    string(VPCReadyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "VPCReconcileFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}
	conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
		Type:   string(VPCReadyCondition),
		Status: metav1.ConditionTrue,
		Reason: "VPCReady",
	})

	// Reconcile Subnets
	if err := r.reconcileSubnets(ctx, clusterScope, siteID); err != nil {
		conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
			Type:    string(SubnetsReadyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "SubnetReconcileFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}
	conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
		Type:   string(SubnetsReadyCondition),
		Status: metav1.ConditionTrue,
		Reason: "SubnetsReady",
	})

	// Reconcile Network Security Group (if specified)
	if clusterScope.NvidiaCarbideCluster.Spec.VPC.NetworkSecurityGroup != nil {
		if err := r.reconcileNSG(ctx, clusterScope, siteID); err != nil {
			conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
				Type:    string(NSGReadyCondition),
				Status:  metav1.ConditionFalse,
				Reason:  "NSGReconcileFailed",
				Message: err.Error(),
			})
			return ctrl.Result{}, err
		}
		conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
			Type:   string(NSGReadyCondition),
			Status: metav1.ConditionTrue,
			Reason: "NSGReady",
		})
	}

	// Mark cluster as ready
	clusterScope.SetReady(true)
	conditions.Set(clusterScope.NvidiaCarbideCluster, metav1.Condition{
		Type:   string(clusterv1.ReadyCondition),
		Status: metav1.ConditionTrue,
		Reason: "NvidiaCarbideClusterReady",
	})

	logger.Info("Successfully reconciled NvidiaCarbideCluster")
	return ctrl.Result{}, nil
}

func (r *NvidiaCarbideClusterReconciler) reconcileVPC(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	// Check if VPC already exists
	if clusterScope.VPCID() != "" {
		// Verify VPC still exists in NVIDIA Carbide
		vpc, _, err := clusterScope.NvidiaCarbideClient.GetVpc(ctx, clusterScope.OrgName, clusterScope.VPCID())
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
	vpcSpec := clusterScope.NvidiaCarbideCluster.Spec.VPC

	vpcReq := bmm.VpcCreateRequest{
		Name:   vpcSpec.Name,
		SiteId: siteID,
	}
	if vpcSpec.NetworkVirtualizationType != "" {
		netVirtType := vpcSpec.NetworkVirtualizationType
		vpcReq.NetworkVirtualizationType = *bmm.NewNullableString(&netVirtType)
	}
	if len(vpcSpec.Labels) > 0 {
		vpcReq.Labels = vpcSpec.Labels
	}

	logger.Info("Creating VPC", "name", vpcSpec.Name, "siteID", siteID)
	vpc, httpResp, err := clusterScope.NvidiaCarbideClient.CreateVpc(ctx, clusterScope.OrgName, vpcReq)
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

	return nil
}

// parseCIDR parses a CIDR string and returns the network address and prefix length
func parseCIDR(cidr string) (network string, prefixLength int, err error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid CIDR %s: %w", cidr, err)
	}

	// Get prefix length
	ones, _ := ipNet.Mask.Size()

	// Return the network address (not the host part)
	networkAddr := ipNet.IP.String()

	return networkAddr, ones, nil
}

// ensureIPBlock ensures an IP block exists for subnet allocation
// This creates a shared IP block for all subnets in the cluster
func (r *NvidiaCarbideClusterReconciler) ensureIPBlock(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) (string, error) {
	logger := log.FromContext(ctx)

	// Check if we already have an IP block
	if clusterScope.IPBlockID() != "" {
		// Verify it still exists
		ipBlock, _, err := clusterScope.NvidiaCarbideClient.GetIpblock(ctx, clusterScope.OrgName, clusterScope.IPBlockID())
		if err == nil && ipBlock != nil {
			logger.V(1).Info("IP block already exists", "ipBlockID", clusterScope.IPBlockID())
			return clusterScope.IPBlockID(), nil
		}
		logger.Info("Existing IP block not found, will create new one", "oldIPBlockID", clusterScope.IPBlockID())
	}

	// Create a new IP block for this cluster's subnets
	ipBlockName := fmt.Sprintf("%s-ipblock", clusterScope.NvidiaCarbideCluster.Name)
	ipBlockReq := bmm.IpBlockCreateRequest{
		Name:            ipBlockName,
		Prefix:          "10.0.0.0",
		PrefixLength:    16,
		ProtocolVersion: "ipv4",
		RoutingType:     "datacenter_only",
		SiteId:          siteID,
	}

	logger.Info("Creating IP block", "name", ipBlockName, "prefix", "10.0.0.0/16", "siteID", siteID)
	ipBlock, httpResp, err := clusterScope.NvidiaCarbideClient.CreateIpblock(ctx, clusterScope.OrgName, ipBlockReq)
	if err != nil {
		return "", fmt.Errorf("failed to create IP block: %w", err)
	}

	if httpResp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("failed to create IP block, status %d", httpResp.StatusCode)
	}

	if ipBlock == nil || ipBlock.Id == nil {
		return "", fmt.Errorf("IP block ID missing in response")
	}

	clusterScope.SetIPBlockID(*ipBlock.Id)
	logger.Info("Successfully created IP block", "ipBlockID", *ipBlock.Id)

	return *ipBlock.Id, nil
}

func (r *NvidiaCarbideClusterReconciler) reconcileSubnets(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	vpcID := clusterScope.VPCID()
	if vpcID == "" {
		return fmt.Errorf("VPC ID is empty")
	}

	// Ensure IP block exists for subnet allocation
	ipBlockID, err := r.ensureIPBlock(ctx, clusterScope, siteID)
	if err != nil {
		return fmt.Errorf("failed to ensure IP block: %w", err)
	}

	subnetIDs := clusterScope.SubnetIDs()

	// Reconcile each subnet
	for _, subnetSpec := range clusterScope.NvidiaCarbideCluster.Spec.Subnets {
		// Check if subnet already exists
		if existingID, exists := subnetIDs[subnetSpec.Name]; exists {
			// Verify subnet still exists in NVIDIA Carbide
			subnet, _, err := clusterScope.NvidiaCarbideClient.GetSubnet(ctx, clusterScope.OrgName, existingID)
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
		_, prefixLength, err := parseCIDR(subnetSpec.CIDR)
		if err != nil {
			return fmt.Errorf("failed to parse CIDR for subnet %s: %w", subnetSpec.Name, err)
		}

		// Create subnet using IP block
		subnetReq := bmm.SubnetCreateRequest{
			Name:         subnetSpec.Name,
			VpcId:        vpcID,
			Ipv4BlockId:  &ipBlockID,
			PrefixLength: int32(prefixLength),
		}

		logger.Info("Creating subnet",
			"name", subnetSpec.Name, "cidr", subnetSpec.CIDR,
			"prefixLength", prefixLength, "vpcID", vpcID,
			"ipBlockID", ipBlockID)
		subnet, httpResp, err := clusterScope.NvidiaCarbideClient.CreateSubnet(ctx, clusterScope.OrgName, subnetReq)
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
	}

	return nil
}

func (r *NvidiaCarbideClusterReconciler) reconcileNSG(
	ctx context.Context, clusterScope *scope.ClusterScope, siteID string,
) error {
	logger := log.FromContext(ctx)

	nsgSpec := clusterScope.NvidiaCarbideCluster.Spec.VPC.NetworkSecurityGroup

	// Check if NSG already exists
	if clusterScope.NSGID() != "" {
		// Verify NSG still exists in NVIDIA Carbide
		nsg, _, err := clusterScope.NvidiaCarbideClient.GetNetworkSecurityGroup(
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
	rules := make([]bmm.NetworkSecurityGroupRule, 0, len(nsgSpec.Rules))
	for _, rule := range nsgSpec.Rules {
		// API requires both source and destination prefixes
		// Use "0.0.0.0/0" as default (any) if not specified
		sourcePrefix := rule.SourceCIDR
		if sourcePrefix == "" {
			sourcePrefix = "0.0.0.0/0"
		}
		destPrefix := "0.0.0.0/0" // Default to any destination

		ruleName := rule.Name
		nsgRule := bmm.NetworkSecurityGroupRule{
			Name:              *bmm.NewNullableString(&ruleName),
			Direction:         strings.ToLower(rule.Direction),
			Protocol:          strings.ToLower(rule.Protocol),
			Action:            strings.ToLower(rule.Action),
			SourcePrefix:      sourcePrefix,
			DestinationPrefix: destPrefix,
		}

		// Map port range to destination port range
		if rule.PortRange != "" {
			portRange := rule.PortRange
			nsgRule.DestinationPortRange = *bmm.NewNullableString(&portRange)
		}

		rules = append(rules, nsgRule)
	}

	// Create NSG
	nsgReq := bmm.NetworkSecurityGroupCreateRequest{
		Name:   nsgSpec.Name,
		SiteId: siteID,
	}
	if len(rules) > 0 {
		nsgReq.Rules = rules
	}

	logger.Info("Creating NSG", "name", nsgSpec.Name, "siteID", siteID)
	nsg, httpResp, err := clusterScope.NvidiaCarbideClient.CreateNetworkSecurityGroup(ctx, clusterScope.OrgName, nsgReq)
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

	return nil
}

//nolint:unparam // ctrl.Result is part of the reconciler interface contract
func (r *NvidiaCarbideClusterReconciler) reconcileDelete(
	ctx context.Context, clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Deleting NvidiaCarbideCluster")

	// Delete NSG if it exists
	if clusterScope.NSGID() != "" {
		logger.Info("Deleting NSG", "nsgID", clusterScope.NSGID())
		httpResp, err := clusterScope.NvidiaCarbideClient.DeleteNetworkSecurityGroup(
			ctx, clusterScope.OrgName, clusterScope.NSGID())
		if err != nil {
			logger.Error(err, "failed to delete NSG", "nsgID", clusterScope.NSGID())
			return ctrl.Result{}, err
		}
		if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent {
			logger.Error(nil, "failed to delete NSG", "nsgID", clusterScope.NSGID(), "status", httpResp.StatusCode)
			return ctrl.Result{}, fmt.Errorf("failed to delete NSG, status %d", httpResp.StatusCode)
		}
	}

	// Delete Subnets
	for subnetName, subnetID := range clusterScope.SubnetIDs() {
		logger.Info("Deleting subnet", "subnetName", subnetName, "subnetID", subnetID)
		httpResp, err := clusterScope.NvidiaCarbideClient.DeleteSubnet(ctx, clusterScope.OrgName, subnetID)
		if err != nil {
			logger.Error(err, "failed to delete subnet", "subnetName", subnetName, "subnetID", subnetID)
			return ctrl.Result{}, err
		}
		if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent {
			logger.Error(nil, "failed to delete subnet",
				"subnetName", subnetName, "subnetID", subnetID,
				"status", httpResp.StatusCode)
			return ctrl.Result{}, fmt.Errorf("failed to delete subnet %s, status %d", subnetName, httpResp.StatusCode)
		}
	}

	// Delete VPC
	if clusterScope.VPCID() != "" {
		logger.Info("Deleting VPC", "vpcID", clusterScope.VPCID())
		httpResp, err := clusterScope.NvidiaCarbideClient.DeleteVpc(ctx, clusterScope.OrgName, clusterScope.VPCID())
		if err != nil {
			logger.Error(err, "failed to delete VPC", "vpcID", clusterScope.VPCID())
			return ctrl.Result{}, err
		}
		if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent {
			logger.Error(nil, "failed to delete VPC", "vpcID", clusterScope.VPCID(), "status", httpResp.StatusCode)
			return ctrl.Result{}, fmt.Errorf("failed to delete VPC, status %d", httpResp.StatusCode)
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(clusterScope.NvidiaCarbideCluster, NvidiaCarbideClusterFinalizer)

	logger.Info("Successfully deleted NvidiaCarbideCluster")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NvidiaCarbideClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1.NvidiaCarbideCluster{}).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(
				util.ClusterToInfrastructureMapFunc(
					ctx,
					infrastructurev1.GroupVersion.WithKind("NvidiaCarbideCluster"),
					mgr.GetClient(),
					&infrastructurev1.NvidiaCarbideCluster{},
				),
			),
		).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(
			mgr.GetScheme(), ctrl.Log.WithName("nvidiacarbidecluster"), "")).
		Named("nvidiacarbidecluster").
		Complete(r)
}
