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

	infrastructurev1 "github.com/fabiendupont/cluster-api-provider-nvidia-carbide/api/v1beta1"
	"github.com/fabiendupont/cluster-api-provider-nvidia-carbide/pkg/scope"
	bmm "github.com/nvidia/bare-metal-manager-rest/sdk/standard"
)

const (
	// NvidiaCarbideMachineFinalizer allows cleanup of NVIDIA Carbide resources before deletion
	NvidiaCarbideMachineFinalizer = "nvidiacarbidemachine.infrastructure.cluster.x-k8s.io"
)

// Condition types
const (
	InstanceProvisionedCondition  clusterv1.ConditionType = "InstanceProvisioned"
	InstanceProvisioningCondition clusterv1.ConditionType = "InstanceProvisioning"
	NetworkConfiguredCondition    clusterv1.ConditionType = "NetworkConfigured"
	BootstrapDataAppliedCondition clusterv1.ConditionType = "BootstrapDataApplied"
)

// NvidiaCarbideMachineReconciler reconciles a NvidiaCarbideMachine object
type NvidiaCarbideMachineReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// NvidiaCarbideClient can be set for testing to inject a mock client
	NvidiaCarbideClient scope.NvidiaCarbideClientInterface
	// OrgName can be set for testing
	OrgName string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbidemachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbidemachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbidemachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles NvidiaCarbideMachine reconciliation
func (r *NvidiaCarbideMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the NvidiaCarbideMachine instance
	nvidiaCarbideMachine := &infrastructurev1.NvidiaCarbideMachine{}
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
		logger.Info("Waiting for Machine Controller to set OwnerRef on NvidiaCarbideMachine")
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

	// Fetch the NvidiaCarbideCluster
	nvidiaCarbideCluster := &infrastructurev1.NvidiaCarbideCluster{}
	nvidiaCarbideClusterKey := client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Get(ctx, nvidiaCarbideClusterKey, nvidiaCarbideCluster); err != nil {
		return ctrl.Result{}, err
	}

	// Check if cluster is paused
	if annotations.IsPaused(cluster, nvidiaCarbideMachine) {
		logger.Info("NvidiaCarbideMachine or Cluster is marked as paused, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Return early if NvidiaCarbideCluster is not ready
	if !nvidiaCarbideCluster.Status.Ready {
		logger.Info("Waiting for NvidiaCarbideCluster to be ready")
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
			logger.Error(err, "failed to patch NvidiaCarbideMachine")
		}
	}()

	// Create cluster scope for credentials
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

	// Create machine scope
	machineScope, err := scope.NewMachineScope(scope.MachineScopeParams{
		Client:               r.Client,
		Cluster:              cluster,
		Machine:              machine,
		NvidiaCarbideCluster: nvidiaCarbideCluster,
		NvidiaCarbideMachine: nvidiaCarbideMachine,
		NvidiaCarbideClient:  clusterScope.NvidiaCarbideClient,
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

func (r *NvidiaCarbideMachineReconciler) reconcileNormal(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NvidiaCarbideMachine")

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(machineScope.NvidiaCarbideMachine, NvidiaCarbideMachineFinalizer) {
		controllerutil.AddFinalizer(machineScope.NvidiaCarbideMachine, NvidiaCarbideMachineFinalizer)
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
	// impractical — each NvidiaCarbideMachine is reconciled independently with
	// no coordination mechanism. A higher-level batching controller would be
	// needed to detect concurrent pending machines and coordinate batch creation.
	// For now, instances are created individually per reconcile.
	if err := r.createInstance(ctx, machineScope, clusterScope); err != nil {
		conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
			Type:    string(InstanceProvisionedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "InstanceCreationFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err
	}

	conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
		Type:   string(InstanceProvisionedCondition),
		Status: metav1.ConditionTrue,
		Reason: "InstanceCreated",
	})

	// Requeue to check instance status
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *NvidiaCarbideMachineReconciler) createInstance(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) error {
	logger := log.FromContext(ctx)

	// Get bootstrap data
	bootstrapData, err := machineScope.GetBootstrapData(ctx)
	if err != nil {
		conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
			Type:    string(BootstrapDataAppliedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  "BootstrapDataFailed",
			Message: err.Error(),
		})
		return fmt.Errorf("failed to get bootstrap data: %w", err)
	}
	conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
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
	instanceReq := bmm.InstanceCreateRequest{
		Name:       machineScope.Name(),
		TenantId:   machineScope.TenantID(),
		VpcId:      machineScope.VPCID(),
		UserData:   *bmm.NewNullableString(&bootstrapData),
		Interfaces: interfaces,
	}

	// Apply optional spec fields to the request
	r.applyOptionalInstanceFields(machineScope, &instanceReq)

	logger.Info("Creating NVIDIA Carbide instance",
		"name", machineScope.Name(),
		"vpcID", machineScope.VPCID(),
		"role", machineScope.Role())

	// Create instance via NVIDIA Carbide API
	instance, httpResp, err := machineScope.NvidiaCarbideClient.CreateInstance(ctx, machineScope.OrgName, instanceReq)
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
		if machineScope.NvidiaCarbideMachine.Annotations == nil {
			machineScope.NvidiaCarbideMachine.Annotations = map[string]string{}
		}
		machineScope.NvidiaCarbideMachine.Annotations["nvidia-carbide.io/serial-console-url"] = *instance.SerialConsoleUrl.Get()
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
	r.recordEvent(machineScope.NvidiaCarbideMachine, corev1.EventTypeNormal, "InstanceCreated",
		"Successfully created instance %s", instanceID)

	return nil
}

func (r *NvidiaCarbideMachineReconciler) reconcileInstance(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get instance status from NVIDIA Carbide
	instance, httpResp, err := machineScope.NvidiaCarbideClient.GetInstance(
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
		if machineScope.NvidiaCarbideMachine.Annotations == nil {
			machineScope.NvidiaCarbideMachine.Annotations = map[string]string{}
		}
		machineScope.NvidiaCarbideMachine.Annotations["nvidia-carbide.io/serial-console-url"] = *instance.SerialConsoleUrl.Get()
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
		conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
			Type:   string(NetworkConfiguredCondition),
			Status: metav1.ConditionTrue,
			Reason: "NetworkReady",
		})
	}

	// Check if instance is ready
	if instance.Status != nil && string(*instance.Status) == "Ready" {
		machineScope.SetReady(true)
		conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
			Type:   string(InstanceProvisioningCondition),
			Status: metav1.ConditionFalse,
			Reason: "ProvisioningComplete",
		})
		conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
			Type:   string(clusterv1.ReadyCondition),
			Status: metav1.ConditionTrue,
			Reason: "NvidiaCarbideMachineReady",
		})

		// Apply post-creation updates if spec has changed
		if updateReq, needsUpdate := r.buildUpdateRequest(machineScope, instance); needsUpdate {
			logger.Info("Applying post-creation updates to instance", "instanceID", machineScope.InstanceID())
			_, _, updateErr := machineScope.NvidiaCarbideClient.UpdateInstance(
				ctx, machineScope.OrgName, machineScope.InstanceID(), updateReq)
			if updateErr != nil {
				logger.Error(updateErr, "failed to update instance", "instanceID", machineScope.InstanceID())
				r.recordEvent(machineScope.NvidiaCarbideMachine, corev1.EventTypeWarning, "UpdateFailed",
					"Failed to update instance %s: %v", machineScope.InstanceID(), updateErr)
			} else {
				r.recordEvent(machineScope.NvidiaCarbideMachine, corev1.EventTypeNormal, "InstanceUpdated",
					"Successfully updated instance %s", machineScope.InstanceID())
			}
		}

		// Set control plane endpoint if not already configured.
		// If a load balancer VIP is pre-configured in ControlPlaneEndpoint,
		// it takes precedence over individual machine addresses.
		// For HA control planes, the first ready machine sets the endpoint
		// when no VIP is configured; subsequent machines don't overwrite it.
		cpEndpoint := clusterScope.NvidiaCarbideCluster.Spec.ControlPlaneEndpoint
		if machineScope.IsControlPlane() && (cpEndpoint == nil || cpEndpoint.Host == "") {
			if len(addresses) > 0 {
				port := int32(6443)
				if cpEndpoint != nil && cpEndpoint.Port != 0 {
					port = cpEndpoint.Port
				}
				clusterScope.NvidiaCarbideCluster.Spec.ControlPlaneEndpoint = &clusterv1.APIEndpoint{
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
		logger.Info("NvidiaCarbideMachine is ready", "instanceID", instanceIDStr, "status", string(*instance.Status))
		r.recordEvent(machineScope.NvidiaCarbideMachine, corev1.EventTypeNormal, "InstanceReady",
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
		setMachineFailure(machineScope.NvidiaCarbideMachine, errReason, errMsg)
	}

	conditions.Set(machineScope.NvidiaCarbideMachine, metav1.Condition{
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
func (r *NvidiaCarbideMachineReconciler) reconcileDelete(
	ctx context.Context, machineScope *scope.MachineScope,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Deleting NvidiaCarbideMachine")

	// Delete instance if it exists
	if machineScope.InstanceID() != "" {
		logger.Info("Deleting NVIDIA Carbide instance", "instanceID", machineScope.InstanceID())

		httpResp, err := machineScope.NvidiaCarbideClient.DeleteInstance(ctx, machineScope.OrgName, machineScope.InstanceID())
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
	controllerutil.RemoveFinalizer(machineScope.NvidiaCarbideMachine, NvidiaCarbideMachineFinalizer)

	logger.Info("Successfully deleted NvidiaCarbideMachine")
	return ctrl.Result{}, nil
}

// validateCapabilities checks site and tenant capabilities for advanced features.
func (r *NvidiaCarbideMachineReconciler) validateCapabilities(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) error {
	spec := machineScope.NvidiaCarbideMachine.Spec

	if len(spec.NVLinkInterfaces) > 0 || len(spec.InfiniBandInterfaces) > 0 {
		siteID, siteErr := clusterScope.SiteID(ctx)
		if siteErr == nil {
			site, _, siteErr := clusterScope.NvidiaCarbideClient.GetSite(
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
		tenant, _, tenantErr := clusterScope.NvidiaCarbideClient.GetCurrentTenant(
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
func (r *NvidiaCarbideMachineReconciler) buildInterfaces(
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) ([]bmm.InterfaceCreateRequest, error) {
	var interfaces []bmm.InterfaceCreateRequest

	// Primary interface
	if machineScope.NvidiaCarbideMachine.Spec.Network.VPCPrefixName != "" {
		vpcPrefixID, err := machineScope.GetVPCPrefixID()
		if err != nil {
			return nil, fmt.Errorf("failed to get VPC prefix ID: %w", err)
		}
		interfaces = append(interfaces, bmm.InterfaceCreateRequest{
			VpcPrefixId: &vpcPrefixID,
		})
	} else {
		subnetID, err := machineScope.GetSubnetID()
		if err != nil {
			return nil, fmt.Errorf("failed to get subnet ID: %w", err)
		}
		physicalFalse := false
		interfaces = append(interfaces, bmm.InterfaceCreateRequest{
			SubnetId:   &subnetID,
			IsPhysical: &physicalFalse,
		})
	}

	// Additional interfaces
	netStatus := clusterScope.NvidiaCarbideCluster.Status.NetworkStatus
	for _, iface := range machineScope.NvidiaCarbideMachine.Spec.Network.AdditionalInterfaces {
		if iface.VPCPrefixName != "" {
			prefixID, ok := netStatus.VPCPrefixIDs[iface.VPCPrefixName]
			if !ok {
				return nil, fmt.Errorf("VPC prefix %s not found in cluster status", iface.VPCPrefixName)
			}
			interfaces = append(interfaces, bmm.InterfaceCreateRequest{
				VpcPrefixId: &prefixID,
			})
		} else {
			subnetID, ok := netStatus.SubnetIDs[iface.SubnetName]
			if !ok {
				return nil, fmt.Errorf("subnet %s not found in cluster status", iface.SubnetName)
			}
			interfaces = append(interfaces, bmm.InterfaceCreateRequest{
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
func (r *NvidiaCarbideMachineReconciler) applyOptionalInstanceFields(
	machineScope *scope.MachineScope,
	req *bmm.InstanceCreateRequest,
) {
	spec := machineScope.NvidiaCarbideMachine.Spec

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
		req.OperatingSystemId = *bmm.NewNullableString(&osID)
	}

	if len(spec.InfiniBandInterfaces) > 0 {
		ibInterfaces := make([]bmm.InfiniBandInterfaceCreateRequest, 0, len(spec.InfiniBandInterfaces))
		for _, ibSpec := range spec.InfiniBandInterfaces {
			ibReq := bmm.InfiniBandInterfaceCreateRequest{
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
		nvlinkInterfaces := make([]bmm.NVLinkInterfaceCreateRequest, 0, len(spec.NVLinkInterfaces))
		for _, nvSpec := range spec.NVLinkInterfaces {
			nvReq := bmm.NVLinkInterfaceCreateRequest{
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
		dpuDeployments := make([]bmm.DpuExtensionServiceDeploymentRequest, 0, len(spec.DPUExtensionServices))
		for _, dpuSpec := range spec.DPUExtensionServices {
			dpuReq := bmm.DpuExtensionServiceDeploymentRequest{
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
		req.Description = *bmm.NewNullableString(&desc)
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
func (r *NvidiaCarbideMachineReconciler) exposeStatusHistory(
	ctx context.Context, machineScope *scope.MachineScope,
) {
	logger := log.FromContext(ctx)

	history, _, err := machineScope.NvidiaCarbideClient.GetInstanceStatusHistory(
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
			r.recordEvent(machineScope.NvidiaCarbideMachine, corev1.EventTypeWarning, "StatusHistory",
				"[%s] %s", status, message)
		}
	}
}

// buildUpdateRequest compares the desired spec with the current instance and returns
// an InstanceUpdateRequest if any mutable fields have changed.
func (r *NvidiaCarbideMachineReconciler) buildUpdateRequest(
	machineScope *scope.MachineScope, instance *bmm.Instance,
) (bmm.InstanceUpdateRequest, bool) {
	updateReq := bmm.InstanceUpdateRequest{}
	needsUpdate := false

	// Check SSH key groups
	if len(machineScope.NvidiaCarbideMachine.Spec.SSHKeyGroups) > 0 {
		currentSSHKeys := instance.SshKeyGroupIds
		desiredSSHKeys := machineScope.NvidiaCarbideMachine.Spec.SSHKeyGroups
		if !stringSlicesEqual(currentSSHKeys, desiredSSHKeys) {
			updateReq.SshKeyGroupIds = desiredSSHKeys
			needsUpdate = true
		}
	}

	// Check labels
	if len(machineScope.NvidiaCarbideMachine.Spec.Labels) > 0 {
		if !mapsEqual(instance.Labels, machineScope.NvidiaCarbideMachine.Spec.Labels) {
			updateReq.Labels = machineScope.NvidiaCarbideMachine.Spec.Labels
			needsUpdate = true
		}
	}

	// Check DPU extension service deployments
	if len(machineScope.NvidiaCarbideMachine.Spec.DPUExtensionServices) > 0 {
		dpuDeployments := make([]bmm.DpuExtensionServiceDeploymentRequest, 0, len(machineScope.NvidiaCarbideMachine.Spec.DPUExtensionServices))
		for _, dpuSpec := range machineScope.NvidiaCarbideMachine.Spec.DPUExtensionServices {
			dpuReq := bmm.DpuExtensionServiceDeploymentRequest{
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
func (r *NvidiaCarbideMachineReconciler) findExistingInstance(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
) (*bmm.Instance, error) {
	instances, _, err := clusterScope.NvidiaCarbideClient.GetAllInstance(ctx, machineScope.OrgName)
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
func (r *NvidiaCarbideMachineReconciler) recordEvent(obj runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, eventType, reason, messageFmt, args...)
	}
}

// setMachineFailure sets the FailureReason and FailureMessage on the machine status.
func setMachineFailure(machine *infrastructurev1.NvidiaCarbideMachine, reason capierrors.MachineStatusError, message string) {
	machine.Status.FailureReason = &reason
	machine.Status.FailureMessage = &message
}

// SetupWithManager sets up the controller with the Manager.
func (r *NvidiaCarbideMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1.NvidiaCarbideMachine{}).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(
				util.MachineToInfrastructureMapFunc(
					infrastructurev1.GroupVersion.WithKind("NvidiaCarbideMachine"),
				),
			),
		).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(
			mgr.GetScheme(), ctrl.Log.WithName("nvidiacarbidemachine"), "")).
		Named("nvidiacarbidemachine").
		Complete(r)
}
