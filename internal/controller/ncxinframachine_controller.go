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
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	capierrors "sigs.k8s.io/cluster-api/errors" //nolint:staticcheck // required for CAPI contract FailureReason types
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
	"github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/pkg/scope"
	nico "github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
)

const (
	// NcxInfraMachineFinalizer allows cleanup of NVIDIA Carbide resources before deletion
	NcxInfraMachineFinalizer = "ncxinframachine.infrastructure.cluster.x-k8s.io"
)

// Condition types
const (
	InstanceProvisionedCondition  clusterv1.ConditionType = "InstanceProvisioned"
	InstanceProvisioningCondition clusterv1.ConditionType = "InstanceProvisioning"
	NetworkConfiguredCondition    clusterv1.ConditionType = "NetworkConfigured"
	BootstrapDataAppliedCondition clusterv1.ConditionType = "BootstrapDataApplied"
)

// NcxInfraMachineReconciler reconciles a NcxInfraMachine object
type NcxInfraMachineReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// NcxInfraClient can be set for testing to inject a mock client
	NcxInfraClient scope.NcxInfraClientInterface
	// OrgName can be set for testing
	OrgName string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=ncxinframachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=ncxinframachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=ncxinframachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles NcxInfraMachine reconciliation
func (r *NcxInfraMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the NcxInfraMachine instance
	nvidiaCarbideMachine := &infrastructurev1.NcxInfraMachine{}
	if err := r.Get(ctx, req.NamespacedName, nvidiaCarbideMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the owner Machine
	machine, err := util.GetOwnerMachine(ctx, r.Client, nvidiaCarbideMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		logger.Info("Waiting for Machine Controller to set OwnerRef on NcxInfraMachine")
		return ctrl.Result{}, nil
	}

	// Fetch the owner Cluster
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		logger.Info("Waiting for Cluster to be set on Machine")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Fetch the NcxInfraCluster
	nvidiaCarbideCluster := &infrastructurev1.NcxInfraCluster{}
	nvidiaCarbideClusterKey := client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Get(ctx, nvidiaCarbideClusterKey, nvidiaCarbideCluster); err != nil {
		return ctrl.Result{}, err
	}

	// Check if cluster is paused
	if annotations.IsPaused(cluster, nvidiaCarbideMachine) {
		logger.Info("NcxInfraMachine or Cluster is marked as paused, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Return early if NcxInfraCluster is not ready
	if !nvidiaCarbideCluster.Status.Ready {
		logger.Info("Waiting for NcxInfraCluster to be ready")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Return early if bootstrap data is not ready
	if machine.Spec.Bootstrap.DataSecretName == nil {
		logger.Info("Waiting for bootstrap data to be available")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Initialize patch helper
	patchHelper, err := patch.NewHelper(nvidiaCarbideMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always attempt to patch the object and status after each reconciliation
	defer func() {
		if err := patchHelper.Patch(ctx, nvidiaCarbideMachine); err != nil {
			logger.Error(err, "failed to patch NcxInfraMachine")
		}
	}()

	// Create cluster scope for credentials
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

	// Create machine scope
	machineScope, err := scope.NewMachineScope(scope.MachineScopeParams{
		Client:               r.Client,
		Cluster:              cluster,
		Machine:              machine,
		NcxInfraCluster: nvidiaCarbideCluster,
		NcxInfraMachine: nvidiaCarbideMachine,
		NcxInfraClient:  clusterScope.NcxInfraClient,
		OrgName:              clusterScope.OrgName,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create machine scope: %w", err)
	}

	// Handle deletion
	if !nvidiaCarbideMachine.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, machineScope)
	}

	// Handle normal reconciliation
	return r.reconcileNormal(ctx, machineScope, clusterScope)
}

func (r *NcxInfraMachineReconciler) reconcileNormal(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NcxInfraMachine")

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(machineScope.NcxInfraMachine, NcxInfraMachineFinalizer) {
		controllerutil.AddFinalizer(machineScope.NcxInfraMachine, NcxInfraMachineFinalizer)
		return ctrl.Result{Requeue: true}, nil
	}

	// If instance already exists, check its status
	if machineScope.InstanceID() != "" {
		return r.reconcileInstance(ctx, machineScope, clusterScope)
	}

	// Check for existing instance with the same name (duplicate prevention)
	if existingInstance, err := r.findExistingInstance(ctx, machineScope, clusterScope); err != nil {
		logger.Error(err, "failed to check for existing instance")
	} else if existingInstance != nil && existingInstance.Id != nil {
		logger.Info("Found existing instance with matching name, reusing",
			"instanceID", *existingInstance.Id, "name", machineScope.Name())
		machineScope.SetInstanceID(*existingInstance.Id)
		if existingInstance.MachineId.Get() != nil {
			machineScope.SetMachineID(*existingInstance.MachineId.Get())
		}
		if existingInstance.Status != nil {
			machineScope.SetInstanceState(string(*existingInstance.Status))
		}
		return r.reconcileInstance(ctx, machineScope, clusterScope)
	}

	// Create new instance.
	// NOTE: BatchCreateInstance is available in the SDK for creating up to 18
	// instances per call, but CAPI's reconcile-per-machine model makes batching
	// impractical — each NcxInfraMachine is reconciled independently with
	// no coordination mechanism. A higher-level batching controller would be
	// needed to detect concurrent pending machines and coordinate batch creation.
	// For now, instances are created individually per reconcile.
	if err := r.createInstance(ctx, machineScope, clusterScope); err != nil {
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:    string(InstanceProvisionedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "InstanceCreationFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}

	conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
		Type:   string(InstanceProvisionedCondition),
		Status: metav1.ConditionTrue,
		Reason: "InstanceCreated",
	})

	// Requeue to check instance status
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *NcxInfraMachineReconciler) createInstance(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) error {
	logger := log.FromContext(ctx)

	// Get bootstrap data
	bootstrapData, err := machineScope.GetBootstrapData(ctx)
	if err != nil {
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:    string(BootstrapDataAppliedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "BootstrapDataFailed",
			Message: err.Error(),
		})
		return fmt.Errorf("failed to get bootstrap data: %w", err)
	}
	conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
		Type:   string(BootstrapDataAppliedCondition),
		Status: metav1.ConditionTrue,
		Reason: "BootstrapDataReady",
	})

	// Validate capabilities before creating
	if err := r.validateCapabilities(ctx, machineScope, clusterScope); err != nil {
		return err
	}

	// Get Site ID (as site name for ProviderID)
	siteName, err := clusterScope.SiteID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get site ID: %w", err)
	}

	// Build network interfaces
	interfaces, err := r.buildInterfaces(machineScope, clusterScope)
	if err != nil {
		return err
	}

	// Build instance create request
	instanceReq := nico.InstanceCreateRequest{
		Name:       machineScope.Name(),
		TenantId:   machineScope.TenantID(),
		VpcId:      machineScope.VPCID(),
		UserData:   *nico.NewNullableString(&bootstrapData),
		Interfaces: interfaces,
	}

	// Apply optional spec fields to the request
	r.applyOptionalInstanceFields(machineScope, &instanceReq)

	logger.Info("Creating NVIDIA Carbide instance",
		"name", machineScope.Name(),
		"vpcID", machineScope.VPCID(),
		"role", machineScope.Role())

	// Create instance via NVIDIA Carbide API
	instance, httpResp, err := machineScope.NcxInfraClient.CreateInstance(ctx, machineScope.OrgName, instanceReq)
	if err != nil {
		return fmt.Errorf("failed to create instance: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to create instance, status %d", httpResp.StatusCode)
	}

	if instance == nil || instance.Id == nil {
		return fmt.Errorf("instance ID missing in response")
	}

	instanceID := *instance.Id
	machineID := ""
	// Set serial console URL annotation if available
	if instance.SerialConsoleUrl.Get() != nil && *instance.SerialConsoleUrl.Get() != "" {
		if machineScope.NcxInfraMachine.Annotations == nil {
			machineScope.NcxInfraMachine.Annotations = map[string]string{}
		}
		machineScope.NcxInfraMachine.Annotations["ncx-infra.io/serial-console-url"] = *instance.SerialConsoleUrl.Get()
	}

	if instance.MachineId.Get() != nil {
		machineID = *instance.MachineId.Get()
	}

	status := ""
	if instance.Status != nil {
		status = string(*instance.Status)
	}

	// Update machine scope with instance details
	machineScope.SetInstanceID(instanceID)
	machineScope.SetMachineID(machineID)
	machineScope.SetInstanceState(status)
	if err := machineScope.SetProviderID(clusterScope.TenantID(), siteName, instanceID); err != nil {
		return fmt.Errorf("failed to set provider ID: %w", err)
	}

	logger.Info("Successfully created NVIDIA Carbide instance",
		"instanceID", instanceID,
		"machineID", machineID,
		"status", status)
	r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeNormal, "InstanceCreated",
		"Successfully created instance %s", instanceID)

	return nil
}

func (r *NcxInfraMachineReconciler) reconcileInstance(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get instance status from NVIDIA Carbide
	instance, httpResp, err := machineScope.NcxInfraClient.GetInstance(
		ctx, machineScope.OrgName, machineScope.InstanceID())
	if err != nil {
		logger.Error(err, "failed to get instance status", "instanceID", machineScope.InstanceID())
		return ctrl.Result{}, err
	}

	if httpResp.StatusCode != http.StatusOK || instance == nil {
		logger.Error(nil, "unexpected response getting instance",
			"instanceID", machineScope.InstanceID(),
			"status", httpResp.StatusCode)
		return ctrl.Result{}, fmt.Errorf("failed to get instance, status %d", httpResp.StatusCode)
	}

	// Update instance state
	if instance.Status != nil {
		machineScope.SetInstanceState(string(*instance.Status))
	}
	// Set serial console URL annotation if available
	if instance.SerialConsoleUrl.Get() != nil && *instance.SerialConsoleUrl.Get() != "" {
		if machineScope.NcxInfraMachine.Annotations == nil {
			machineScope.NcxInfraMachine.Annotations = map[string]string{}
		}
		machineScope.NcxInfraMachine.Annotations["ncx-infra.io/serial-console-url"] = *instance.SerialConsoleUrl.Get()
	}

	if instance.MachineId.Get() != nil {
		machineScope.SetMachineID(*instance.MachineId.Get())
	}

	// Extract IP addresses from interfaces
	addresses := []clusterv1.MachineAddress{}
	for _, iface := range instance.Interfaces {
		for _, ipAddr := range iface.IpAddresses {
			addresses = append(addresses, clusterv1.MachineAddress{
				Type:    clusterv1.MachineInternalIP,
				Address: ipAddr,
			})
		}
	}

	if len(addresses) > 0 {
		machineScope.SetAddresses(addresses)
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:   string(NetworkConfiguredCondition),
			Status: metav1.ConditionTrue,
			Reason: "NetworkReady",
		})
	}

	// Check if instance is ready
	if instance.Status != nil && string(*instance.Status) == "Ready" {
		machineScope.SetReady(true)
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:   string(InstanceProvisioningCondition),
			Status: metav1.ConditionFalse,
			Reason: "ProvisioningComplete",
		})
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:   string(clusterv1.ReadyCondition),
			Status: metav1.ConditionTrue,
			Reason: "NcxInfraMachineReady",
		})

		// Apply post-creation updates if spec has changed
		if updateReq, needsUpdate := r.buildUpdateRequest(machineScope, instance); needsUpdate {
			logger.Info("Applying post-creation updates to instance", "instanceID", machineScope.InstanceID())
			_, _, updateErr := machineScope.NcxInfraClient.UpdateInstance(
				ctx, machineScope.OrgName, machineScope.InstanceID(), updateReq)
			if updateErr != nil {
				logger.Error(updateErr, "failed to update instance", "instanceID", machineScope.InstanceID())
				r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeWarning, "UpdateFailed",
					"Failed to update instance %s: %v", machineScope.InstanceID(), updateErr)
			} else {
				r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeNormal, "InstanceUpdated",
					"Successfully updated instance %s", machineScope.InstanceID())
			}
		}

		// Set control plane endpoint if not already configured.
		// If a load balancer VIP is pre-configured in ControlPlaneEndpoint,
		// it takes precedence over individual machine addresses.
		// For HA control planes, the first ready machine sets the endpoint
		// when no VIP is configured; subsequent machines don't overwrite it.
		cpEndpoint := clusterScope.NcxInfraCluster.Spec.ControlPlaneEndpoint
		if machineScope.IsControlPlane() && (cpEndpoint == nil || cpEndpoint.Host == "") {
			if len(addresses) > 0 {
				port := int32(6443)
				if cpEndpoint != nil && cpEndpoint.Port != 0 {
					port = cpEndpoint.Port
				}
				clusterScope.NcxInfraCluster.Spec.ControlPlaneEndpoint = &clusterv1.APIEndpoint{
					Host: addresses[0].Address,
					Port: port,
				}
				logger.Info("Updated control plane endpoint from first ready control plane machine",
					"host", addresses[0].Address, "port", port)
			}
		}

		instanceIDStr := ""
		if instance.Id != nil {
			instanceIDStr = *instance.Id
		}
		logger.Info("NcxInfraMachine is ready", "instanceID", instanceIDStr, "status", string(*instance.Status))
		r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeNormal, "InstanceReady",
			"Instance %s is ready", instanceIDStr)
		return ctrl.Result{}, nil
	}

	// Instance is still provisioning or in error, requeue
	instanceIDStr := ""
	statusStr := ""
	if instance.Id != nil {
		instanceIDStr = *instance.Id
	}
	if instance.Status != nil {
		statusStr = string(*instance.Status)
	}

	// Fetch and expose status history for debugging when in error or prolonged provisioning
	if statusStr == "Error" || statusStr == "Provisioning" {
		r.exposeStatusHistory(ctx, machineScope)
	}

	// Set failure info for error state
	if statusStr == "Error" {
		errReason := capierrors.MachineStatusError("ProvisioningFailed")
		errMsg := fmt.Sprintf("Instance %s is in Error state", instanceIDStr)
		setMachineFailure(machineScope.NcxInfraMachine, errReason, errMsg)
	}

	conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
		Type:    string(InstanceProvisioningCondition),
		Status:  metav1.ConditionTrue,
		Reason:  "WaitingForReady",
		Message: fmt.Sprintf("Instance %s is in state %s", instanceIDStr, statusStr),
	})

	logger.Info("Waiting for instance to be ready",
		"instanceID", instanceIDStr,
		"status", statusStr)

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

//nolint:unparam // ctrl.Result is part of the reconciler interface contract
func (r *NcxInfraMachineReconciler) reconcileDelete(
	ctx context.Context, machineScope *scope.MachineScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Deleting NcxInfraMachine")

	// Delete instance if it exists
	if machineScope.InstanceID() != "" {
		logger.Info("Deleting NVIDIA Carbide instance", "instanceID", machineScope.InstanceID())

		httpResp, err := machineScope.NcxInfraClient.DeleteInstance(ctx, machineScope.OrgName, machineScope.InstanceID())
		if err != nil {
			if httpResp != nil && httpResp.StatusCode == http.StatusNotFound {
				logger.Info("Instance already deleted", "instanceID", machineScope.InstanceID())
			} else {
				logger.Error(err, "failed to delete instance", "instanceID", machineScope.InstanceID())
				return ctrl.Result{}, err
			}
		} else if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent &&
			httpResp.StatusCode != http.StatusNotFound {
			logger.Error(nil, "failed to delete instance",
				"instanceID", machineScope.InstanceID(),
				"status", httpResp.StatusCode)
			return ctrl.Result{}, fmt.Errorf("failed to delete instance, status %d", httpResp.StatusCode)
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(machineScope.NcxInfraMachine, NcxInfraMachineFinalizer)

	logger.Info("Successfully deleted NcxInfraMachine")
	return ctrl.Result{}, nil
}

// validateCapabilities checks site and tenant capabilities for advanced features.
func (r *NcxInfraMachineReconciler) validateCapabilities(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) error {
	spec := machineScope.NcxInfraMachine.Spec

	if len(spec.NVLinkInterfaces) > 0 || len(spec.InfiniBandInterfaces) > 0 {
		siteID, siteErr := clusterScope.SiteID(ctx)
		if siteErr == nil {
			site, _, siteErr := clusterScope.NcxInfraClient.GetSite(
				ctx, clusterScope.OrgName, siteID)
			if siteErr == nil && site != nil && site.Capabilities != nil {
				if len(spec.NVLinkInterfaces) > 0 &&
					site.Capabilities.NvLinkPartition != nil &&
					!*site.Capabilities.NvLinkPartition {
					return fmt.Errorf("site %s does not support NVLink partitioning", siteID)
				}
			}
		}
	}

	if spec.InstanceType.MachineID != "" {
		tenant, _, tenantErr := clusterScope.NcxInfraClient.GetCurrentTenant(
			ctx, clusterScope.OrgName)
		if tenantErr == nil && tenant != nil && tenant.Capabilities != nil {
			if tenant.Capabilities.TargetedInstanceCreation != nil &&
				!*tenant.Capabilities.TargetedInstanceCreation {
				return fmt.Errorf("tenant does not have targeted instance creation enabled; cannot use machineID")
			}
		}
	}

	return nil
}

// buildInterfaces constructs the network interface list from machine and cluster specs.
func (r *NcxInfraMachineReconciler) buildInterfaces(
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) ([]nico.InterfaceCreateRequest, error) {
	var interfaces []nico.InterfaceCreateRequest

	// Primary interface
	if machineScope.NcxInfraMachine.Spec.Network.VPCPrefixName != "" {
		vpcPrefixID, err := machineScope.GetVPCPrefixID()
		if err != nil {
			return nil, fmt.Errorf("failed to get VPC prefix ID: %w", err)
		}
		ifReq := nico.InterfaceCreateRequest{
			VpcPrefixId: &vpcPrefixID,
		}
		if machineScope.NcxInfraMachine.Spec.Network.IpAddress != "" {
			ip := machineScope.NcxInfraMachine.Spec.Network.IpAddress
			ifReq.IpAddress = *nico.NewNullableString(&ip)
		}
		interfaces = append(interfaces, ifReq)
	} else {
		subnetID, err := machineScope.GetSubnetID()
		if err != nil {
			return nil, fmt.Errorf("failed to get subnet ID: %w", err)
		}
		physicalFalse := false
		interfaces = append(interfaces, nico.InterfaceCreateRequest{
			SubnetId:   &subnetID,
			IsPhysical: &physicalFalse,
		})
	}

	// Additional interfaces
	netStatus := clusterScope.NcxInfraCluster.Status.NetworkStatus
	for _, iface := range machineScope.NcxInfraMachine.Spec.Network.AdditionalInterfaces {
		if iface.VPCPrefixName != "" {
			prefixID, ok := netStatus.VPCPrefixIDs[iface.VPCPrefixName]
			if !ok {
				return nil, fmt.Errorf("VPC prefix %s not found in cluster status", iface.VPCPrefixName)
			}
			ifReq := nico.InterfaceCreateRequest{
				VpcPrefixId: &prefixID,
			}
			if iface.IpAddress != "" {
				ip := iface.IpAddress
				ifReq.IpAddress = *nico.NewNullableString(&ip)
			}
			interfaces = append(interfaces, ifReq)
		} else {
			subnetID, ok := netStatus.SubnetIDs[iface.SubnetName]
			if !ok {
				return nil, fmt.Errorf("subnet %s not found in cluster status", iface.SubnetName)
			}
			interfaces = append(interfaces, nico.InterfaceCreateRequest{
				SubnetId:   &subnetID,
				IsPhysical: &iface.IsPhysical,
			})
		}
	}

	return interfaces, nil
}

// applyOptionalInstanceFields sets optional fields on the InstanceCreateRequest from the machine spec.
//
//nolint:gocyclo // field-mapping function, each branch is simple
func (r *NcxInfraMachineReconciler) applyOptionalInstanceFields(
	machineScope *scope.MachineScope,
	req *nico.InstanceCreateRequest,
) {
	spec := machineScope.NcxInfraMachine.Spec

	if len(spec.SSHKeyGroups) > 0 {
		req.SshKeyGroupIds = spec.SSHKeyGroups
	}
	if len(spec.Labels) > 0 {
		req.Labels = spec.Labels
	}
	if spec.InstanceType.ID != "" {
		req.InstanceTypeId = &spec.InstanceType.ID
	}
	if spec.InstanceType.MachineID != "" {
		req.MachineId = &spec.InstanceType.MachineID
	}
	if spec.InstanceType.AllowUnhealthyMachine {
		req.AllowUnhealthyMachine = &spec.InstanceType.AllowUnhealthyMachine
	}
	if spec.OperatingSystem != nil && spec.OperatingSystem.ID != "" {
		osID := spec.OperatingSystem.ID
		req.OperatingSystemId = *nico.NewNullableString(&osID)
	}

	if len(spec.InfiniBandInterfaces) > 0 {
		ibInterfaces := make([]nico.InfiniBandInterfaceCreateRequest, 0, len(spec.InfiniBandInterfaces))
		for _, ibSpec := range spec.InfiniBandInterfaces {
			ibReq := nico.InfiniBandInterfaceCreateRequest{
				PartitionId: &ibSpec.PartitionID,
			}
			if ibSpec.Device != "" {
				ibReq.Device = &ibSpec.Device
			}
			if ibSpec.DeviceInstance != nil {
				ibReq.DeviceInstance = ibSpec.DeviceInstance
			}
			if ibSpec.IsPhysical {
				ibReq.IsPhysical = &ibSpec.IsPhysical
			}
			ibInterfaces = append(ibInterfaces, ibReq)
		}
		req.InfinibandInterfaces = ibInterfaces
	}

	if len(spec.NVLinkInterfaces) > 0 {
		nvlinkInterfaces := make([]nico.NVLinkInterfaceCreateRequest, 0, len(spec.NVLinkInterfaces))
		for _, nvSpec := range spec.NVLinkInterfaces {
			nvReq := nico.NVLinkInterfaceCreateRequest{
				NvLinklogicalPartitionId: &nvSpec.LogicalPartitionID,
			}
			if nvSpec.DeviceInstance != nil {
				nvReq.DeviceInstance = nvSpec.DeviceInstance
			}
			nvlinkInterfaces = append(nvlinkInterfaces, nvReq)
		}
		req.NvLinkInterfaces = nvlinkInterfaces
	}

	if len(spec.DPUExtensionServices) > 0 {
		dpuDeployments := make([]nico.DpuExtensionServiceDeploymentRequest, 0, len(spec.DPUExtensionServices))
		for _, dpuSpec := range spec.DPUExtensionServices {
			dpuReq := nico.DpuExtensionServiceDeploymentRequest{
				DpuExtensionServiceId: &dpuSpec.ServiceID,
			}
			if dpuSpec.Version != "" {
				dpuReq.Version = &dpuSpec.Version
			}
			dpuDeployments = append(dpuDeployments, dpuReq)
		}
		req.DpuExtensionServiceDeployments = dpuDeployments
	}

	if spec.Description != "" {
		desc := spec.Description
		req.Description = *nico.NewNullableString(&desc)
	}
	if spec.AlwaysBootWithCustomIpxe {
		req.AlwaysBootWithCustomIpxe = &spec.AlwaysBootWithCustomIpxe
	}

	if spec.PhoneHomeEnabled != nil {
		req.PhoneHomeEnabled = spec.PhoneHomeEnabled
	} else {
		phoneHome := true
		req.PhoneHomeEnabled = &phoneHome
	}
}

// exposeStatusHistory fetches the instance status history and emits events.
func (r *NcxInfraMachineReconciler) exposeStatusHistory(
	ctx context.Context, machineScope *scope.MachineScope,
) {
	logger := log.FromContext(ctx)

	history, _, err := machineScope.NcxInfraClient.GetInstanceStatusHistory(
		ctx, machineScope.OrgName, machineScope.InstanceID())
	if err != nil {
		logger.V(1).Info("Failed to fetch status history", "error", err)
		return
	}

	for _, entry := range history {
		status := ""
		if entry.Status != nil {
			status = *entry.Status
		}
		message := ""
		if entry.Message != nil {
			message = *entry.Message
		}
		if message != "" {
			r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeWarning, "StatusHistory",
				"[%s] %s", status, message)
		}
	}
}

// buildUpdateRequest compares the desired spec with the current instance and returns
// an InstanceUpdateRequest if any mutable fields have changed.
func (r *NcxInfraMachineReconciler) buildUpdateRequest(
	machineScope *scope.MachineScope, instance *nico.Instance,
) (nico.InstanceUpdateRequest, bool) {
	updateReq := nico.InstanceUpdateRequest{}
	needsUpdate := false

	// Check SSH key groups
	if len(machineScope.NcxInfraMachine.Spec.SSHKeyGroups) > 0 {
		currentSSHKeys := instance.SshKeyGroupIds
		desiredSSHKeys := machineScope.NcxInfraMachine.Spec.SSHKeyGroups
		if !stringSlicesEqual(currentSSHKeys, desiredSSHKeys) {
			updateReq.SshKeyGroupIds = desiredSSHKeys
			needsUpdate = true
		}
	}

	// Check labels
	if len(machineScope.NcxInfraMachine.Spec.Labels) > 0 {
		if !mapsEqual(instance.Labels, machineScope.NcxInfraMachine.Spec.Labels) {
			updateReq.Labels = machineScope.NcxInfraMachine.Spec.Labels
			needsUpdate = true
		}
	}

	// Check DPU extension service deployments
	if len(machineScope.NcxInfraMachine.Spec.DPUExtensionServices) > 0 {
		dpuDeployments := make([]nico.DpuExtensionServiceDeploymentRequest, 0, len(machineScope.NcxInfraMachine.Spec.DPUExtensionServices))
		for _, dpuSpec := range machineScope.NcxInfraMachine.Spec.DPUExtensionServices {
			dpuReq := nico.DpuExtensionServiceDeploymentRequest{
				DpuExtensionServiceId: &dpuSpec.ServiceID,
			}
			if dpuSpec.Version != "" {
				dpuReq.Version = &dpuSpec.Version
			}
			dpuDeployments = append(dpuDeployments, dpuReq)
		}
		updateReq.DpuExtensionServiceDeployments = dpuDeployments
		needsUpdate = true
	}

	return updateReq, needsUpdate
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// findExistingInstance checks if an instance with the same name already exists.
func (r *NcxInfraMachineReconciler) findExistingInstance(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) (*nico.Instance, error) {
	instances, _, err := clusterScope.NcxInfraClient.GetAllInstance(ctx, machineScope.OrgName)
	if err != nil {
		return nil, err
	}
	for i := range instances {
		if instances[i].Name != nil && *instances[i].Name == machineScope.Name() {
			return &instances[i], nil
		}
	}
	return nil, nil
}

// recordEvent records an event on the given object if a Recorder is set.
func (r *NcxInfraMachineReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// setMachineFailure sets the FailureReason and FailureMessage on the machine status.
func setMachineFailure(machine *infrastructurev1.NcxInfraMachine, reason capierrors.MachineStatusError, message string) {
	machine.Status.FailureReason = &reason
	machine.Status.FailureMessage = &message
}

// SetupWithManager sets up the controller with the Manager.
func (r *NcxInfraMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1.NcxInfraMachine{}).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(
				util.MachineToInfrastructureMapFunc(
					infrastructurev1.GroupVersion.WithKind("NcxInfraMachine"),
				),
			),
		).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(
			mgr.GetScheme(), ctrl.Log.WithName("ncxinframachine"), "")).
		Named("ncxinframachine").
		Complete(r)
}
