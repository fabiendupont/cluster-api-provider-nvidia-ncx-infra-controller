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
	// NvidiaCarbideMachineFinalizer allows cleanup of NVIDIA Carbide resources before deletion
	NvidiaCarbideMachineFinalizer = "nvidiacarbidemachine.infrastructure.cluster.x-k8s.io"
)

// Condition types
const (
	InstanceProvisionedCondition clusterv1.ConditionType = "InstanceProvisioned"
	NetworkConfiguredCondition   clusterv1.ConditionType = "NetworkConfigured"
)

// NvidiaCarbideMachineReconciler reconciles a NvidiaCarbideMachine object
type NvidiaCarbideMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

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

	// Create new instance
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
		return fmt.Errorf("failed to get bootstrap data: %w", err)
	}

	// Get subnet ID for primary network interface
	subnetID, err := machineScope.GetSubnetID()
	if err != nil {
		return fmt.Errorf("failed to get subnet ID: %w", err)
	}

	// Get Site ID (as site name for ProviderID)
	siteName, err := clusterScope.SiteID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get site ID: %w", err)
	}

	// Build primary network interface
	physicalFalse := false
	interfaces := []bmm.InterfaceCreateRequest{
		{
			SubnetId:   &subnetID,
			IsPhysical: &physicalFalse,
		},
	}

	// Add additional network interfaces if specified
	for _, additionalIf := range machineScope.NvidiaCarbideMachine.Spec.Network.AdditionalInterfaces {
		// Look up subnet ID from cluster status
		additionalSubnetID, ok := clusterScope.NvidiaCarbideCluster.Status.NetworkStatus.SubnetIDs[additionalIf.SubnetName]
		if !ok {
			return fmt.Errorf("subnet %s not found in cluster status", additionalIf.SubnetName)
		}

		interfaces = append(interfaces, bmm.InterfaceCreateRequest{
			SubnetId:   &additionalSubnetID,
			IsPhysical: &additionalIf.IsPhysical,
		})
	}

	// Build instance create request
	instanceReq := bmm.InstanceCreateRequest{
		Name:       machineScope.Name(),
		TenantId:   machineScope.TenantID(),
		VpcId:      machineScope.VPCID(),
		UserData:   *bmm.NewNullableString(&bootstrapData),
		Interfaces: interfaces,
	}

	// Set SSH key groups if specified
	if len(machineScope.NvidiaCarbideMachine.Spec.SSHKeyGroups) > 0 {
		instanceReq.SshKeyGroupIds = machineScope.NvidiaCarbideMachine.Spec.SSHKeyGroups
	}

	// Set labels if specified
	if len(machineScope.NvidiaCarbideMachine.Spec.Labels) > 0 {
		instanceReq.Labels = machineScope.NvidiaCarbideMachine.Spec.Labels
	}

	// Set instance type or specific machine ID
	if machineScope.NvidiaCarbideMachine.Spec.InstanceType.ID != "" {
		instanceReq.InstanceTypeId = &machineScope.NvidiaCarbideMachine.Spec.InstanceType.ID
	}
	if machineScope.NvidiaCarbideMachine.Spec.InstanceType.MachineID != "" {
		instanceReq.MachineId = &machineScope.NvidiaCarbideMachine.Spec.InstanceType.MachineID
	}

	// Enable phone home for bootstrap communication
	phoneHome := true
	instanceReq.PhoneHomeEnabled = &phoneHome

	logger.Info("Creating NVIDIA Carbide instance",
		"name", machineScope.Name(),
		"vpcID", machineScope.VPCID(),
		"subnetID", subnetID,
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
			Type:   string(clusterv1.ReadyCondition),
			Status: metav1.ConditionTrue,
			Reason: "NvidiaCarbideMachineReady",
		})

		// For first control plane machine, update cluster endpoint if not set
		cpEndpoint := clusterScope.NvidiaCarbideCluster.Spec.ControlPlaneEndpoint
		if machineScope.IsControlPlane() && (cpEndpoint == nil || cpEndpoint.Host == "") {
			if len(addresses) > 0 {
				clusterScope.NvidiaCarbideCluster.Spec.ControlPlaneEndpoint = &clusterv1.APIEndpoint{
					Host: addresses[0].Address,
					Port: 6443,
				}
				logger.Info("Updated control plane endpoint", "host", addresses[0].Address)
			}
		}

		instanceIDStr := ""
		if instance.Id != nil {
			instanceIDStr = *instance.Id
		}
		logger.Info("NvidiaCarbideMachine is ready", "instanceID", instanceIDStr, "status", string(*instance.Status))
		return ctrl.Result{}, nil
	}

	// Instance is still provisioning, requeue
	instanceIDStr := ""
	statusStr := ""
	if instance.Id != nil {
		instanceIDStr = *instance.Id
	}
	if instance.Status != nil {
		statusStr = string(*instance.Status)
	}
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
			logger.Error(err, "failed to delete instance", "instanceID", machineScope.InstanceID())
			return ctrl.Result{}, err
		}

		if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusNoContent {
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
