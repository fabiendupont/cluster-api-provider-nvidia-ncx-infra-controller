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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validCluster() *NcxInfraCluster {
	return &NcxInfraCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: NcxInfraClusterSpec{
			SiteRef:  SiteReference{ID: "site-uuid"},
			TenantID: "tenant-uuid",
			VPC: VPCSpec{
				Name:                      "test-vpc",
				NetworkVirtualizationType: "ETHERNET_VIRTUALIZER",
			},
			Subnets: []SubnetSpec{
				{Name: "cp", CIDR: "10.0.1.0/24"},
			},
			Authentication: AuthenticationSpec{
				SecretRef: corev1.SecretReference{Name: "creds"},
			},
		},
	}
}

func TestClusterWebhook_ValidCreate(t *testing.T) {
	c := validCluster()
	_, err := c.ValidateCreate(context.Background(), c)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestClusterWebhook_EmptyVPCName(t *testing.T) {
	c := validCluster()
	c.Spec.VPC.Name = ""
	_, err := c.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Error("expected error for empty VPC name")
	}
}

func TestClusterWebhook_InvalidNVT(t *testing.T) {
	c := validCluster()
	c.Spec.VPC.NetworkVirtualizationType = "INVALID"
	_, err := c.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Error("expected error for invalid network virtualization type")
	}
}

func TestClusterWebhook_InvalidCIDR(t *testing.T) {
	c := validCluster()
	c.Spec.Subnets[0].CIDR = "not-a-cidr"
	_, err := c.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Error("expected error for invalid CIDR")
	}
}

func TestClusterWebhook_EmptySiteRef(t *testing.T) {
	c := validCluster()
	c.Spec.SiteRef = SiteReference{}
	_, err := c.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Error("expected error for empty site reference")
	}
}

func TestClusterWebhook_SiteRefByName(t *testing.T) {
	c := validCluster()
	c.Spec.SiteRef = SiteReference{Name: "my-site"}
	_, err := c.ValidateCreate(context.Background(), c)
	if err != nil {
		t.Errorf("expected no error for site ref by name, got %v", err)
	}
}

func TestClusterWebhook_NoSubnets(t *testing.T) {
	c := validCluster()
	c.Spec.Subnets = nil
	_, err := c.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Error("expected error for no subnets")
	}
}

func TestClusterWebhook_ImmutableSiteRef(t *testing.T) {
	old := validCluster()
	new := validCluster()
	new.Spec.SiteRef.ID = "different-site-uuid"
	_, err := old.ValidateUpdate(context.Background(), old, new)
	if err == nil {
		t.Error("expected error for immutable siteRef change")
	}
}

func TestClusterWebhook_ImmutableTenantID(t *testing.T) {
	old := validCluster()
	new := validCluster()
	new.Spec.TenantID = "different-tenant"
	_, err := old.ValidateUpdate(context.Background(), old, new)
	if err == nil {
		t.Error("expected error for immutable tenantID change")
	}
}

func TestClusterWebhook_ImmutableVPCName(t *testing.T) {
	old := validCluster()
	new := validCluster()
	new.Spec.VPC.Name = "different-vpc"
	_, err := old.ValidateUpdate(context.Background(), old, new)
	if err == nil {
		t.Error("expected error for immutable VPC name change")
	}
}

func TestClusterWebhook_ValidVPCPrefix(t *testing.T) {
	c := validCluster()
	c.Spec.VPCPrefixes = []VPCPrefixSpec{
		{Name: "prefix-1", CIDR: "10.0.3.0/24"},
	}
	_, err := c.ValidateCreate(context.Background(), c)
	if err != nil {
		t.Errorf("expected no error for valid VPC prefix, got %v", err)
	}
}

func TestClusterWebhook_InvalidVPCPrefixCIDR(t *testing.T) {
	c := validCluster()
	c.Spec.VPCPrefixes = []VPCPrefixSpec{
		{Name: "prefix-1", CIDR: "not-a-cidr"},
	}
	_, err := c.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Error("expected error for invalid VPC prefix CIDR")
	}
}

func TestClusterWebhook_EmptyVPCPrefixName(t *testing.T) {
	c := validCluster()
	c.Spec.VPCPrefixes = []VPCPrefixSpec{
		{Name: "", CIDR: "10.0.3.0/24"},
	}
	_, err := c.ValidateCreate(context.Background(), c)
	if err == nil {
		t.Error("expected error for empty VPC prefix name")
	}
}

func TestClusterWebhook_AllowedUpdate(t *testing.T) {
	old := validCluster()
	new := validCluster()
	// Changing subnets is allowed
	new.Spec.Subnets = append(new.Spec.Subnets, SubnetSpec{Name: "workers", CIDR: "10.0.2.0/24"})
	_, err := old.ValidateUpdate(context.Background(), old, new)
	if err != nil {
		t.Errorf("expected no error for allowed update, got %v", err)
	}
}
