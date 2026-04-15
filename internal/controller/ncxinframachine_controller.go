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
	"strconv"
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

	nico "github.com/NVIDIA/ncx-infra-controller-rest/sdk/standard"
	infrastructurev1 "github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/api/v1beta1"
	ncxinframetrics "github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/internal/metrics"
	"github.com/fabiendupont/cluster-api-provider-nvidia-ncx-infra-controller/pkg/scope"
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
	NicoHealthyCondition          clusterv1.ConditionType = "NicoHealthy"
	NicoFaultRemediationCondition clusterv1.ConditionType = "NicoFaultRemediation"
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
		Client:          r.Client,
		Cluster:         cluster,
		NcxInfraCluster: nvidiaCarbideCluster,
		NcxInfraClient:  r.NcxInfraClient, // Will be nil in production, set for tests
		OrgName:         r.OrgName,        // Will be empty in production (fetched from secret), set for tests
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create cluster scope: %w", err)
	}

	// Create machine scope
	machineScope, err := scope.NewMachineScope(scope.MachineScopeParams{
		Client:          r.Client,
		Cluster:         cluster,
		Machine:         machine,
		NcxInfraCluster: nvidiaCarbideCluster,
		NcxInfraMachine: nvidiaCarbideMachine,
		NcxInfraClient:  clusterScope.NcxInfraClient,
		OrgName:         clusterScope.OrgName,
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

	// Pre-flight health check: if targeting a specific machine and fault management
	// is supported, verify the machine has no open critical faults before creating.
	// Requeue with backoff since the fault may resolve via automated remediation.
	if machineScope.NcxInfraMachine.Spec.InstanceType.MachineID != "" &&
		!machineScope.NcxInfraMachine.Spec.InstanceType.AllowUnhealthyMachine &&
		r.hasFaultManagement(ctx, clusterScope) {
		targetMachineID := machineScope.NcxInfraMachine.Spec.InstanceType.MachineID
		faults := r.listOpenFaultEvents(ctx, machineScope, targetMachineID)
		if len(faults) > 0 {
			msg := fmt.Sprintf("target machine %s has %d open fault(s): %s",
				targetMachineID, len(faults), formatFaultMessage(faults))
			logger.Info("Deferring instance creation due to unhealthy target machine",
				"machineID", targetMachineID, "faults", len(faults))
			r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeWarning, "PreFlightHealthCheckFailed", msg)
			conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
				Type:    string(InstanceProvisionedCondition),
				Status:  metav1.ConditionFalse,
				Reason:  "PreFlightHealthCheckFailed",
				Message: msg,
			})
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
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
		if apiErr, ok := err.(*scope.APIError); ok && apiErr.IsTransient() {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
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
	createStart := time.Now()
	instance, httpResp, err := machineScope.NcxInfraClient.CreateInstance(ctx, machineScope.OrgName, instanceReq)
	createAPIErr := scope.ClassifyAPIError(httpResp, err, "CreateInstance")
	recordAPIMetrics("CreateInstance", createStart, createAPIErr)
	if apiErr := createAPIErr; apiErr != nil {
		if apiErr.IsTerminal() {
			errReason := capierrors.MachineStatusError("CreateInstanceFailed")
			setMachineFailure(machineScope.NcxInfraMachine, errReason, apiErr.Message)
		}
		return apiErr
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
	getStart := time.Now()
	instance, httpResp, err := machineScope.NcxInfraClient.GetInstance(
		ctx, machineScope.OrgName, machineScope.InstanceID())
	apiErr := scope.ClassifyAPIError(httpResp, err, "GetInstance")
	recordAPIMetrics("GetInstance", getStart, apiErr)
	if apiErr != nil {
		if apiErr.IsNotFound() {
			logger.Info("Instance no longer exists", "instanceID", machineScope.InstanceID())
			errReason := capierrors.MachineStatusError("InstanceNotFound")
			errMsg := fmt.Sprintf("Instance %s no longer exists", machineScope.InstanceID())
			setMachineFailure(machineScope.NcxInfraMachine, errReason, errMsg)
			return ctrl.Result{}, nil
		}
		if apiErr.IsTerminal() {
			logger.Error(apiErr, "terminal error getting instance", "instanceID", machineScope.InstanceID())
			return ctrl.Result{}, apiErr
		}
		logger.Info("Transient error getting instance, will retry",
			"instanceID", machineScope.InstanceID(), "error", apiErr.Message)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if instance == nil {
		return ctrl.Result{}, fmt.Errorf("instance response is nil for %s", machineScope.InstanceID())
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

	// Update health conditions from fault events (NEP-0007) if supported
	if r.hasFaultManagement(ctx, clusterScope) {
		r.updateHealthConditions(ctx, machineScope)
	}

	// Check if instance is ready
	if instance.Status != nil && string(*instance.Status) == "Ready" {
		return r.handleInstanceReady(ctx, machineScope, clusterScope, instance, addresses)
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

	// Set failure info for error state, enriched with fault events when available
	if statusStr == "Error" {
		errReason := capierrors.MachineStatusError("ProvisioningFailed")
		errMsg := fmt.Sprintf("Instance %s is in Error state", instanceIDStr)

		// Try to enrich with fault event details if fault management is supported
		if r.hasFaultManagement(ctx, clusterScope) {
			if healthMsg := r.getMachineHealthMessage(ctx, machineScope); healthMsg != "" {
				errMsg = fmt.Sprintf("Instance %s is in Error state: %s", instanceIDStr, healthMsg)
				errReason = capierrors.UpdateMachineError
			}
		}

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

func (r *NcxInfraMachineReconciler) handleInstanceReady(
	ctx context.Context,
	machineScope *scope.MachineScope,
	clusterScope *scope.ClusterScope,
	instance *nico.Instance,
	addresses []clusterv1.MachineAddress,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	machineScope.SetReady(true)
	ncxinframetrics.MachinesManaged.Inc()

	// Record provisioning duration if instance has a creation timestamp
	if instance.Created != nil {
		duration := time.Since(*instance.Created).Seconds()
		siteLabel := ""
		if instance.SiteId != nil {
			siteLabel = *instance.SiteId
		}
		instanceTypeLabel := ""
		if instance.InstanceTypeId != nil {
			instanceTypeLabel = *instance.InstanceTypeId
		}
		ncxinframetrics.InstanceProvisioningDuration.
			WithLabelValues(siteLabel, instanceTypeLabel).Observe(duration)
	}

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
		logger.Info("Applying post-creation updates to instance",
			"instanceID", machineScope.InstanceID())
		_, _, updateErr := machineScope.NcxInfraClient.UpdateInstance(
			ctx, machineScope.OrgName, machineScope.InstanceID(), updateReq)
		if updateErr != nil {
			logger.Error(updateErr, "failed to update instance",
				"instanceID", machineScope.InstanceID())
			r.recordEvent(machineScope.NcxInfraMachine,
				corev1.EventTypeWarning, "UpdateFailed",
				"Failed to update instance %s: %v",
				machineScope.InstanceID(), updateErr)
		} else {
			r.recordEvent(machineScope.NcxInfraMachine,
				corev1.EventTypeNormal, "InstanceUpdated",
				"Successfully updated instance %s", machineScope.InstanceID())
		}
	}

	// Set control plane endpoint if not already configured.
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
			logger.Info("Updated control plane endpoint",
				"host", addresses[0].Address, "port", port)
		}
	}

	instanceIDStr := ""
	if instance.Id != nil {
		instanceIDStr = *instance.Id
	}
	logger.Info("NcxInfraMachine is ready",
		"instanceID", instanceIDStr, "status", string(*instance.Status))
	r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeNormal, "InstanceReady",
		"Instance %s is ready", instanceIDStr)
	return ctrl.Result{}, nil
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

		deleteStart := time.Now()
		httpResp, err := machineScope.NcxInfraClient.DeleteInstance(ctx, machineScope.OrgName, machineScope.InstanceID())
		delAPIErr := scope.ClassifyAPIError(httpResp, err, "DeleteInstance")
		recordAPIMetrics("DeleteInstance", deleteStart, delAPIErr)
		if apiErr := delAPIErr; apiErr != nil {
			if apiErr.IsNotFound() {
				logger.Info("Instance already deleted", "instanceID", machineScope.InstanceID())
			} else if apiErr.IsTransient() {
				logger.Info("Transient error deleting instance, will retry",
					"instanceID", machineScope.InstanceID(), "error", apiErr.Message)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			} else {
				logger.Error(apiErr, "failed to delete instance", "instanceID", machineScope.InstanceID())
				return ctrl.Result{}, apiErr
			}
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(machineScope.NcxInfraMachine, NcxInfraMachineFinalizer)

	if machineScope.IsReady() {
		ncxinframetrics.MachinesManaged.Dec()
	}

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

// hasFaultManagement checks whether the site supports fault management (NEP-0007).
// Returns false if the capability is absent or the API is unreachable.
func (r *NcxInfraMachineReconciler) hasFaultManagement(
	ctx context.Context, clusterScope *scope.ClusterScope,
) bool {
	logger := log.FromContext(ctx)

	siteID, err := clusterScope.SiteID(ctx)
	if err != nil {
		logger.V(1).Info("Cannot resolve site ID for capability check", "error", err)
		return false
	}

	site, _, err := clusterScope.NcxInfraClient.GetSite(ctx, clusterScope.OrgName, siteID)
	if err != nil || site == nil || site.Capabilities == nil {
		logger.V(1).Info("Cannot fetch site capabilities", "siteID", siteID, "error", err)
		return false
	}

	return site.Capabilities.FaultManagement != nil && *site.Capabilities.FaultManagement
}

// updateHealthConditions sets NicoHealthy and NicoFaultRemediation conditions
// based on the physical machine's fault events.
func (r *NcxInfraMachineReconciler) updateHealthConditions(
	ctx context.Context, machineScope *scope.MachineScope,
) {
	physMachineID := machineScope.MachineID()
	if physMachineID == "" {
		return
	}

	faults := r.listOpenFaultEvents(ctx, machineScope, physMachineID)

	if len(faults) == 0 {
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:   string(NicoHealthyCondition),
			Status: metav1.ConditionTrue,
			Reason: "MachineHealthy",
		})
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:   string(NicoFaultRemediationCondition),
			Status: metav1.ConditionFalse,
			Reason: "NoFaultsDetected",
		})
		return
	}

	msg := formatFaultMessage(faults)

	conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
		Type:    string(NicoHealthyCondition),
		Status:  metav1.ConditionFalse,
		Reason:  "MachineUnhealthy",
		Message: msg,
	})

	// Check if any fault has an active remediation workflow
	remediating := false
	for _, fault := range faults {
		if fault.RemediationWorkflowId != nil && *fault.RemediationWorkflowId != "" {
			remediating = true
			break
		}
	}

	if remediating {
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:    string(NicoFaultRemediationCondition),
			Status:  metav1.ConditionTrue,
			Reason:  "RemediationInProgress",
			Message: "Automated fault remediation in progress",
		})
	} else {
		conditions.Set(machineScope.NcxInfraMachine, metav1.Condition{
			Type:   string(NicoFaultRemediationCondition),
			Status: metav1.ConditionFalse,
			Reason: "NoRemediationInProgress",
		})
	}

	ncxinframetrics.MachinesUnhealthy.Inc()
	r.recordEvent(machineScope.NcxInfraMachine, corev1.EventTypeWarning, "MachineUnhealthy",
		"Physical machine %s has %d open fault(s): %s", physMachineID, len(faults), msg)
}

// getMachineHealthMessage queries fault events for the physical machine and returns
// a human-readable message describing any open critical faults. Returns empty string
// if no faults exist or the health API is unavailable.
func (r *NcxInfraMachineReconciler) getMachineHealthMessage(
	ctx context.Context, machineScope *scope.MachineScope,
) string {
	physMachineID := machineScope.MachineID()
	if physMachineID == "" {
		return ""
	}

	faults := r.listOpenFaultEvents(ctx, machineScope, physMachineID)
	if len(faults) == 0 {
		return ""
	}

	return formatFaultMessage(faults)
}

// listOpenFaultEvents queries the HealthAPI for open fault events on a machine.
// Falls back to GetMachine health probes if the fault events API is unavailable.
// Returns nil if no faults exist or the API is unavailable.
func (r *NcxInfraMachineReconciler) listOpenFaultEvents(
	ctx context.Context, machineScope *scope.MachineScope, physMachineID string,
) []nico.FaultEvent {
	logger := log.FromContext(ctx)

	faults, _, err := machineScope.NcxInfraClient.ListFaultEvents(
		ctx, machineScope.OrgName, physMachineID, "open", "critical")
	if err != nil {
		logger.V(1).Info("Failed to list fault events, falling back to machine health",
			"machineID", physMachineID, "error", err)
		return r.fallbackToMachineHealth(ctx, machineScope, physMachineID)
	}

	return faults
}

// fallbackToMachineHealth converts Machine health probe alerts into FaultEvent structs
// for consistent handling when the HealthAPI is unavailable.
func (r *NcxInfraMachineReconciler) fallbackToMachineHealth(
	ctx context.Context, machineScope *scope.MachineScope, physMachineID string,
) []nico.FaultEvent {
	logger := log.FromContext(ctx)

	machine, _, err := machineScope.NcxInfraClient.GetMachine(ctx, machineScope.OrgName, physMachineID)
	if err != nil {
		logger.V(1).Info("Failed to fetch machine health", "machineID", physMachineID, "error", err)
		return nil
	}

	if machine == nil || machine.Health == nil || len(machine.Health.Alerts) == 0 {
		return nil
	}

	// Convert health probe alerts to FaultEvent for consistent handling
	faults := make([]nico.FaultEvent, 0, len(machine.Health.Alerts))
	for _, alert := range machine.Health.Alerts {
		fault := nico.FaultEvent{
			MachineId: &physMachineID,
		}
		if alert.Id != nil {
			fault.Classification = alert.Id
		}
		if alert.Message != nil {
			fault.Message = alert.Message
		}
		state := "open"
		fault.State = &state
		severity := "critical"
		fault.Severity = &severity
		faults = append(faults, fault)
	}
	return faults
}

// formatFaultMessage builds a human-readable summary from fault events.
func formatFaultMessage(faults []nico.FaultEvent) string {
	if len(faults) == 0 {
		return ""
	}

	fault := faults[0]
	msg := ""
	if fault.Classification != nil {
		msg = *fault.Classification
	}
	if fault.Message != nil && *fault.Message != "" {
		if msg != "" {
			msg += " — " + *fault.Message
		} else {
			msg = *fault.Message
		}
	}
	if len(faults) > 1 {
		msg += fmt.Sprintf(" (+%d more faults)", len(faults)-1)
	}
	return msg
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

// recordAPIMetrics records API latency and error metrics for a completed API call.
func recordAPIMetrics(method string, startTime time.Time, apiErr *scope.APIError) {
	ncxinframetrics.APILatency.WithLabelValues(method).Observe(time.Since(startTime).Seconds())
	if apiErr != nil {
		ncxinframetrics.APIErrors.WithLabelValues(method, strconv.Itoa(apiErr.StatusCode)).Inc()
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
