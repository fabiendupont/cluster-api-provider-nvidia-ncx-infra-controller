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

package v1beta1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-nvidiacarbidemachine,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=nvidiacarbidemachines,verbs=create;update,versions=v1beta1,name=vnvidiacarbidemachine.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &NvidiaCarbideMachine{}

func (r *NvidiaCarbideMachine) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(r).
		Complete()
}

func (r *NvidiaCarbideMachine) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	machine, ok := obj.(*NvidiaCarbideMachine)
	if !ok {
		return nil, fmt.Errorf("expected NvidiaCarbideMachine, got %T", obj)
	}
	return nil, machine.validateMachine().ToAggregate()
}

func (r *NvidiaCarbideMachine) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	machine, ok := newObj.(*NvidiaCarbideMachine)
	if !ok {
		return nil, fmt.Errorf("expected NvidiaCarbideMachine, got %T", newObj)
	}
	return nil, machine.validateMachine().ToAggregate()
}

func (r *NvidiaCarbideMachine) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *NvidiaCarbideMachine) validateMachine() field.ErrorList {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	// Validate mutual exclusion of instanceTypeId vs machineId
	instanceType := r.Spec.InstanceType
	if instanceType.ID != "" && instanceType.MachineID != "" {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("instanceType"),
			"id and machineID are mutually exclusive"))
	}

	// At least one of ID or MachineID must be set
	if instanceType.ID == "" && instanceType.MachineID == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("instanceType"),
			"one of id or machineID must be specified"))
	}

	// Validate primary network interface: exactly one of SubnetName or VPCPrefixName
	if r.Spec.Network.SubnetName == "" && r.Spec.Network.VPCPrefixName == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("network"),
			"one of subnetName or vpcPrefixName must be specified"))
	}
	if r.Spec.Network.SubnetName != "" && r.Spec.Network.VPCPrefixName != "" {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("network"),
			"subnetName and vpcPrefixName are mutually exclusive"))
	}

	// Validate additional interfaces: each must have exactly one of SubnetName or VPCPrefixName
	for i, iface := range r.Spec.Network.AdditionalInterfaces {
		ifacePath := specPath.Child("network", "additionalInterfaces").Index(i)
		if iface.SubnetName == "" && iface.VPCPrefixName == "" {
			allErrs = append(allErrs, field.Required(
				ifacePath,
				"one of subnetName or vpcPrefixName must be specified"))
		}
		if iface.SubnetName != "" && iface.VPCPrefixName != "" {
			allErrs = append(allErrs, field.Forbidden(
				ifacePath,
				"subnetName and vpcPrefixName are mutually exclusive"))
		}
	}

	// Validate DPU extension services
	for i, dpuSpec := range r.Spec.DPUExtensionServices {
		dpuPath := specPath.Child("dpuExtensionServices").Index(i)
		if dpuSpec.ServiceID == "" {
			allErrs = append(allErrs, field.Required(
				dpuPath.Child("serviceID"),
				"DPU extension service ID must not be empty"))
		}
	}

	// Validate InfiniBand interfaces
	for i, ibSpec := range r.Spec.InfiniBandInterfaces {
		ibPath := specPath.Child("infiniBandInterfaces").Index(i)
		if ibSpec.PartitionID == "" {
			allErrs = append(allErrs, field.Required(
				ibPath.Child("partitionID"),
				"InfiniBand partition ID must not be empty"))
		}
	}

	// Validate NVLink interfaces
	for i, nvSpec := range r.Spec.NVLinkInterfaces {
		nvPath := specPath.Child("nvlinkInterfaces").Index(i)
		if nvSpec.LogicalPartitionID == "" {
			allErrs = append(allErrs, field.Required(
				nvPath.Child("logicalPartitionID"),
				"NVLink logical partition ID must not be empty"))
		}
	}

	if len(allErrs) > 0 {
		return allErrs
	}
	return nil
}
