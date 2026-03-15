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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validMachine() *NvidiaCarbideMachine {
	return &NvidiaCarbideMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: NvidiaCarbideMachineSpec{
			InstanceType: InstanceTypeSpec{
				ID: "instance-type-uuid",
			},
			Network: NetworkSpec{
				SubnetName: "control-plane",
			},
		},
	}
}

func TestMachineWebhook_ValidCreate(t *testing.T) {
	m := validMachine()
	_, err := m.ValidateCreate(context.Background(), m)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestMachineWebhook_MutualExclusion(t *testing.T) {
	m := validMachine()
	m.Spec.InstanceType.ID = "type-uuid"
	m.Spec.InstanceType.MachineID = "machine-uuid"
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error for both instanceTypeId and machineId set")
	}
}

func TestMachineWebhook_NeitherIDSet(t *testing.T) {
	m := validMachine()
	m.Spec.InstanceType.ID = ""
	m.Spec.InstanceType.MachineID = ""
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error when neither id nor machineID is set")
	}
}

func TestMachineWebhook_MachineIDOnly(t *testing.T) {
	m := validMachine()
	m.Spec.InstanceType.ID = ""
	m.Spec.InstanceType.MachineID = "machine-uuid"
	_, err := m.ValidateCreate(context.Background(), m)
	if err != nil {
		t.Errorf("expected no error for machineID only, got %v", err)
	}
}

func TestMachineWebhook_EmptyNetworkNames(t *testing.T) {
	m := validMachine()
	m.Spec.Network.SubnetName = ""
	m.Spec.Network.VPCPrefixName = ""
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error when neither subnet nor VPC prefix is set")
	}
}

func TestMachineWebhook_BothNetworkNames(t *testing.T) {
	m := validMachine()
	m.Spec.Network.SubnetName = "subnet"
	m.Spec.Network.VPCPrefixName = "prefix"
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error when both subnet and VPC prefix are set")
	}
}

func TestMachineWebhook_VPCPrefixOnly(t *testing.T) {
	m := validMachine()
	m.Spec.Network.SubnetName = ""
	m.Spec.Network.VPCPrefixName = "my-prefix"
	_, err := m.ValidateCreate(context.Background(), m)
	if err != nil {
		t.Errorf("expected no error for VPC prefix only, got %v", err)
	}
}

func TestMachineWebhook_DPUExtensionEmptyServiceID(t *testing.T) {
	m := validMachine()
	m.Spec.DPUExtensionServices = []DPUExtensionServiceSpec{
		{ServiceID: ""},
	}
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error for empty DPU extension service ID")
	}
}

func TestMachineWebhook_ValidDPUExtension(t *testing.T) {
	m := validMachine()
	m.Spec.DPUExtensionServices = []DPUExtensionServiceSpec{
		{ServiceID: "dpu-service-uuid", Version: "1.0"},
	}
	_, err := m.ValidateCreate(context.Background(), m)
	if err != nil {
		t.Errorf("expected no error for valid DPU extension, got %v", err)
	}
}

func TestMachineWebhook_AdditionalIfaceMutualExclusion(t *testing.T) {
	m := validMachine()
	m.Spec.Network.AdditionalInterfaces = []NetworkInterface{
		{SubnetName: "s1", VPCPrefixName: "p1"},
	}
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error for additional interface with both subnet and VPC prefix")
	}
}

func TestMachineWebhook_EmptyIBPartitionID(t *testing.T) {
	m := validMachine()
	m.Spec.InfiniBandInterfaces = []InfiniBandInterfaceSpec{
		{PartitionID: ""},
	}
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error for empty IB partition ID")
	}
}

func TestMachineWebhook_ValidIBInterface(t *testing.T) {
	m := validMachine()
	m.Spec.InfiniBandInterfaces = []InfiniBandInterfaceSpec{
		{PartitionID: "ib-partition-uuid"},
	}
	_, err := m.ValidateCreate(context.Background(), m)
	if err != nil {
		t.Errorf("expected no error for valid IB interface, got %v", err)
	}
}

func TestMachineWebhook_EmptyNVLinkPartitionID(t *testing.T) {
	m := validMachine()
	m.Spec.NVLinkInterfaces = []NVLinkInterfaceSpec{
		{LogicalPartitionID: ""},
	}
	_, err := m.ValidateCreate(context.Background(), m)
	if err == nil {
		t.Error("expected error for empty NVLink partition ID")
	}
}

func TestMachineWebhook_ValidUpdate(t *testing.T) {
	old := validMachine()
	new := validMachine()
	new.Spec.InstanceType.AllowUnhealthyMachine = true
	_, err := old.ValidateUpdate(context.Background(), old, new)
	if err != nil {
		t.Errorf("expected no error for valid update, got %v", err)
	}
}
